package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"core/prompts"
	"core/server/workflow"
	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

func TestTaskCompleteAgentSessionBuildsCompletionRequest(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-agent")
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &taskCompleteCaptureRemote{
		response: serverapi.WorkflowTaskCompleteResponse{
			TaskID:       "task-1",
			RunID:        "run-1",
			TransitionID: "transition-1",
			State:        "applied",
		},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand(
		"task", "complete",
		"--transition", "done",
		"--summary", "draft",
		"--param", "risk=low",
		"--summary", "final",
		"--commentary", "finished",
	)
	if code != 0 {
		t.Fatalf("task complete exit=%d stderr=%q", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("task complete stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "Completed task task-1") || !strings.Contains(stdout, "transition-1") {
		t.Fatalf("task complete stdout = %q, want readable completion summary", stdout)
	}
	req := remote.requireSingleRequest(t)
	if req.ActorKind != serverapi.WorkflowTaskCompleteActorAgent || req.AgentSessionID != "session-agent" {
		t.Fatalf("actor request = %+v, want agent session", req)
	}
	if req.RunID != "" || req.SessionID != "" || req.TaskID != "" || req.ShortID != "" {
		t.Fatalf("agent default selector request = %+v, want no explicit selector", req)
	}
	if req.TransitionID != "done" || req.Commentary != "finished" {
		t.Fatalf("completion request = %+v, want transition and commentary", req)
	}
	if req.OutputValues["summary"] != "final" || req.OutputValues["risk"] != "low" {
		t.Fatalf("output values = %+v, want dynamic flag last-value-wins and --param value", req.OutputValues)
	}
}

func TestTaskCompleteSafetyGates(t *testing.T) {
	t.Run("human without force fails locally", func(t *testing.T) {
		t.Setenv(sessionenv.SessionIDEnv, "")
		restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, taskCompleteFatalRemote{t: t})
		defer restore()

		stdout, stderr, code := runWorkflowRootCommand("task", "complete", "--run", "run-1")
		if code != 1 {
			t.Fatalf("task complete exit=%d stderr=%q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		if stderr != prompts.WorkflowTaskCompleteHumanSafetyWarningPrompt+"\n" {
			t.Fatalf("stderr = %q, want human safety warning", stderr)
		}
	})

	t.Run("force is banned inside Kent session", func(t *testing.T) {
		t.Setenv(sessionenv.SessionIDEnv, "session-agent")
		restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, taskCompleteFatalRemote{t: t})
		defer restore()

		stdout, stderr, code := runWorkflowRootCommand("task", "complete", "--force", "--run", "run-1")
		if code != 1 {
			t.Fatalf("task complete exit=%d stderr=%q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		if !strings.Contains(stderr, "intended for humans only") {
			t.Fatalf("stderr = %q, want human-only command denial", stderr)
		}
	})

	t.Run("human force requires one explicit selector", func(t *testing.T) {
		t.Setenv(sessionenv.SessionIDEnv, "")
		restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, taskCompleteFatalRemote{t: t})
		defer restore()

		stdout, stderr, code := runWorkflowRootCommand("task", "complete", "--force", "--transition", "done")
		if code != 2 {
			t.Fatalf("task complete exit=%d stderr=%q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		if !strings.Contains(stderr, "requires exactly one explicit selector") {
			t.Fatalf("stderr = %q, want selector requirement", stderr)
		}
	})
}

func TestTaskCompleteSelectorForms(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantRequest serverapi.WorkflowTaskCompleteRequest
	}{
		{
			name: "run",
			args: []string{"task", "complete", "--force", "--run", "run-1"},
			wantRequest: serverapi.WorkflowTaskCompleteRequest{
				ActorKind: serverapi.WorkflowTaskCompleteActorUser,
				Force:     true,
				RunID:     "run-1",
			},
		},
		{
			name: "session",
			args: []string{"task", "complete", "--force", "--session", "session-1"},
			wantRequest: serverapi.WorkflowTaskCompleteRequest{
				ActorKind: serverapi.WorkflowTaskCompleteActorUser,
				Force:     true,
				SessionID: "session-1",
			},
		},
		{
			name: "durable task id",
			args: []string{"task", "complete", "--force", "--task", "task-1"},
			wantRequest: serverapi.WorkflowTaskCompleteRequest{
				ActorKind: serverapi.WorkflowTaskCompleteActorUser,
				Force:     true,
				TaskID:    "task-1",
			},
		},
		{
			name: "task short id",
			args: []string{"task", "complete", "--force", "--project", ".", "--task", "BLD-1"},
			wantRequest: serverapi.WorkflowTaskCompleteRequest{
				ActorKind: serverapi.WorkflowTaskCompleteActorUser,
				Force:     true,
				ProjectID: "project-1",
				ShortID:   "BLD-1",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(sessionenv.SessionIDEnv, "")
			cfg := config.App{WorkspaceRoot: "/workspace"}
			remote := &taskCompleteCaptureRemote{
				projectID: "project-1",
				response: serverapi.WorkflowTaskCompleteResponse{
					TaskID:       "task-1",
					RunID:        "run-1",
					TransitionID: "transition-1",
					State:        "applied",
				},
			}
			restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
			defer restore()

			if _, stderr, code := runWorkflowRootCommand(tt.args...); code != 0 {
				t.Fatalf("%v exit=%d stderr=%q", tt.args, code, stderr)
			}
			req := remote.requireSingleRequest(t)
			if req.ActorKind != tt.wantRequest.ActorKind ||
				req.Force != tt.wantRequest.Force ||
				req.RunID != tt.wantRequest.RunID ||
				req.SessionID != tt.wantRequest.SessionID ||
				req.TaskID != tt.wantRequest.TaskID ||
				req.ProjectID != tt.wantRequest.ProjectID ||
				req.ShortID != tt.wantRequest.ShortID {
				t.Fatalf("request = %+v, want selector fields %+v", req, tt.wantRequest)
			}
		})
	}
}

func TestTaskCompleteRejectsMultipleSelectors(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-agent")
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, taskCompleteFatalRemote{t: t})
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "complete", "--run", "run-1", "--session", "session-1")
	if code != 2 {
		t.Fatalf("task complete exit=%d stderr=%q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "at most one completion target selector is allowed") {
		t.Fatalf("stderr = %q, want selector exclusivity", stderr)
	}
}

func TestTaskCompleteRejectsMissingFlagValueBeforeNextFlag(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-agent")
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, taskCompleteFatalRemote{t: t})
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "complete", "--run", "--session", "session-1")
	if code != 2 {
		t.Fatalf("task complete missing value exit=%d stderr=%q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--run requires a value") {
		t.Fatalf("stderr = %q, want missing run value", stderr)
	}
}

func TestTaskCompleteAmbiguousSelectorErrorShowsRetryGuidance(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-agent")
	remote := &taskCompleteCaptureRemote{
		err: serverapi.WorkflowTaskCompleteSelectorAmbiguousError{Message: "ambiguous completion target"},
	}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "complete", "--run", "run-1")
	if code != 1 {
		t.Fatalf("task complete ambiguous selector exit=%d stderr=%q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "matched multiple active workflow runs") || !strings.Contains(stderr, "--run <run-id>") {
		t.Fatalf("stderr = %q, want ambiguous selector retry guidance", stderr)
	}
}

func TestTaskCompleteMissingTargetErrorShowsRetryGuidance(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-agent")
	remote := &taskCompleteCaptureRemote{
		err: serverapi.ErrWorkflowTaskCompleteTargetNotFound,
	}
	restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
	defer restore()

	stdout, stderr, code := runWorkflowRootCommand("task", "complete")
	if code != 1 {
		t.Fatalf("task complete missing target exit=%d stderr=%q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "no active unfinished agent run matched") || !strings.Contains(stderr, "--session <session-id>") {
		t.Fatalf("stderr = %q, want missing target retry guidance", stderr)
	}
}

func TestTaskCompleteJSONInputModes(t *testing.T) {
	t.Run("inline json produces json output", func(t *testing.T) {
		t.Setenv(sessionenv.SessionIDEnv, "")
		cfg := config.App{WorkspaceRoot: t.TempDir()}
		remote := &taskCompleteCaptureRemote{
			response: serverapi.WorkflowTaskCompleteResponse{
				TaskID:       "task-1",
				RunID:        "run-1",
				TransitionID: "transition-1",
				State:        "applied",
			},
		}
		restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
		defer restore()

		stdout, stderr, code := runWorkflowRootCommand(
			"task", "complete",
			"--force",
			"--run", "run-1",
			"--json", `{"transition":"done","commentary":"finished","summary":123,"empty":null,"output_values":{"risk":false,"details":{"ok":true}}}`,
		)
		if code != 0 {
			t.Fatalf("task complete --json exit=%d stderr=%q", code, stderr)
		}
		if stderr != "" {
			t.Fatalf("stderr = %q, want empty", stderr)
		}
		var decoded serverapi.WorkflowTaskCompleteResponse
		if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
			t.Fatalf("stdout = %q, want JSON response: %v", stdout, err)
		}
		if decoded.RunID != "run-1" || decoded.TransitionID != "transition-1" {
			t.Fatalf("json response = %+v, want complete response", decoded)
		}
		req := remote.requireSingleRequest(t)
		if req.TransitionID != "done" || req.Commentary != "finished" || req.OutputValues["summary"] != "123" || req.OutputValues["risk"] != "false" || req.OutputValues["details"] != `{"ok":true}` || req.OutputValues["empty"] != "null" {
			t.Fatalf("request from json = %+v", req)
		}
	})

	t.Run("json file input", func(t *testing.T) {
		t.Setenv(sessionenv.SessionIDEnv, "")
		path := t.TempDir() + "/complete.json"
		if err := os.WriteFile(path, []byte(`{"transition_id":"done","output_values":{"summary":"from file"}}`), 0o600); err != nil {
			t.Fatalf("write json file: %v", err)
		}
		remote := &taskCompleteCaptureRemote{
			response: serverapi.WorkflowTaskCompleteResponse{TaskID: "task-1", RunID: "run-1", TransitionID: "transition-1", State: "applied"},
		}
		restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, remote)
		defer restore()

		if _, stderr, code := runWorkflowRootCommand("task", "complete", "--force", "--run", "run-1", "--json-file", path); code != 0 {
			t.Fatalf("task complete --json-file exit=%d stderr=%q", code, stderr)
		}
		req := remote.requireSingleRequest(t)
		if req.TransitionID != "done" || req.OutputValues["summary"] != "from file" {
			t.Fatalf("request from json file = %+v", req)
		}
	})

	t.Run("json input is exclusive with field flags", func(t *testing.T) {
		t.Setenv(sessionenv.SessionIDEnv, "")
		restore := replaceWorkflowCommandRemoteOpener(t, config.App{WorkspaceRoot: t.TempDir()}, taskCompleteFatalRemote{t: t})
		defer restore()

		stdout, stderr, code := runWorkflowRootCommand("task", "complete", "--force", "--run", "run-1", "--json", `{}`, "--transition", "done")
		if code != 2 {
			t.Fatalf("task complete mixed json exit=%d stderr=%q", code, stderr)
		}
		if stdout != "" {
			t.Fatalf("stdout = %q, want empty", stdout)
		}
		if !strings.Contains(stderr, "--json cannot be combined with completion field flags") {
			t.Fatalf("stderr = %q, want json exclusivity error", stderr)
		}
	})
}

func TestTaskCompleteHelpSmoke(t *testing.T) {
	_, stderr, code := runWorkflowRootCommand("task", "complete", "--help")
	if code != 0 {
		t.Fatalf("task complete --help exit=%d stderr=%q", code, stderr)
	}
	if strings.TrimSpace(stderr) == "" {
		t.Fatal("task complete help output is empty")
	}
}

func TestTaskCompleteAgentCrossSessionSelectorUsesServiceOwnershipError(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-other")
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := workflowCreateForTest(t, "Completion Workflow").ID
	if _, nodeErr, code := runWorkflowRootCommand("workflow", "node", "add", workflowID, "--key", "implement", "--kind", "agent", "--agent", "workflow-test", "--prompt", "Do work"); code != 0 {
		t.Fatalf("workflow node add exit=%d stderr=%q", code, nodeErr)
	}
	if _, edgeErr, code := runWorkflowRootCommand("workflow", "edge", "add", workflowID, "--from", "backlog", "--transition", "start", "--edge-key", "start", "--to", "implement", "--context", "new_session", "--prompt", "Do work"); code != 0 {
		t.Fatalf("workflow edge add start exit=%d stderr=%q", code, edgeErr)
	}
	if _, edgeErr, code := runWorkflowRootCommand("workflow", "edge", "add", workflowID, "--from", "implement", "--transition", "done", "--edge-key", "done", "--to", "done", "--context", "new_session"); code != 0 {
		t.Fatalf("workflow edge add done exit=%d stderr=%q", code, edgeErr)
	}
	if _, linkErr, code := runWorkflowRootCommand("workflow", "link", binding.ProjectID, workflowID, "--default"); code != 0 {
		t.Fatalf("workflow link exit=%d stderr=%q", code, linkErr)
	}
	created, err := remote.CreateWorkflowTask(context.Background(), serverapi.WorkflowTaskCreateRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Title: "Task", Body: "Body"})
	if err != nil {
		t.Fatalf("CreateWorkflowTask: %v", err)
	}
	started, err := remote.StartWorkflowTask(context.Background(), serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           created.Task.ID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if started.Applied == nil {
		t.Fatalf("StartWorkflowTask response = %+v, want applied payload", started)
	}
	claimed, err := remote.store.ClaimRun(context.Background(), workflow.RunID(started.Applied.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	ownerSessionID := createWorkflowCommandTestSession(t, cfg, binding, remote.metadataStore)
	if err := remote.store.AttachRunSession(context.Background(), workflow.RunID(started.Applied.RunID), claimed.Generation, ownerSessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}

	stdout, stderr, code := runWorkflowRootCommand("task", "complete", "--run", started.Applied.RunID)
	if code != 1 {
		t.Fatalf("task complete cross-session exit=%d stderr=%q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if strings.TrimSpace(stderr) != prompts.WorkflowTaskCompleteAgentOwnershipErrorPrompt {
		t.Fatalf("stderr = %q, want ownership error", stderr)
	}
}

type taskCompleteCaptureRemote struct {
	apicontract.WorkflowService
	projectID string
	response  serverapi.WorkflowTaskCompleteResponse
	err       error
	requests  []serverapi.WorkflowTaskCompleteRequest
}

func (r *taskCompleteCaptureRemote) Close() error { return nil }

func (r *taskCompleteCaptureRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	if strings.TrimSpace(r.projectID) == "" {
		return serverapi.ProjectResolvePathResponse{}, nil
	}
	return serverapi.ProjectResolvePathResponse{Binding: &serverapi.ProjectBinding{ProjectID: r.projectID}}, nil
}

func (r *taskCompleteCaptureRemote) CompleteWorkflowTask(_ context.Context, req serverapi.WorkflowTaskCompleteRequest) (serverapi.WorkflowTaskCompleteResponse, error) {
	r.requests = append(r.requests, req)
	if r.err != nil {
		return serverapi.WorkflowTaskCompleteResponse{}, r.err
	}
	return r.response, nil
}

func (r *taskCompleteCaptureRemote) requireSingleRequest(t *testing.T) serverapi.WorkflowTaskCompleteRequest {
	t.Helper()
	if len(r.requests) != 1 {
		t.Fatalf("completion requests = %+v, want exactly one", r.requests)
	}
	return r.requests[0]
}

type taskCompleteFatalRemote struct {
	apicontract.WorkflowService
	t *testing.T
}

func (r taskCompleteFatalRemote) Close() error { return nil }

func (r taskCompleteFatalRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	r.t.Fatal("task complete unexpectedly resolved project path")
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r taskCompleteFatalRemote) CompleteWorkflowTask(context.Context, serverapi.WorkflowTaskCompleteRequest) (serverapi.WorkflowTaskCompleteResponse, error) {
	r.t.Fatal("task complete unexpectedly called CompleteWorkflowTask")
	return serverapi.WorkflowTaskCompleteResponse{}, errors.New("unexpected CompleteWorkflowTask")
}

var _ workflowCommandRemote = (*taskCompleteCaptureRemote)(nil)
var _ workflowCommandRemote = taskCompleteFatalRemote{}
