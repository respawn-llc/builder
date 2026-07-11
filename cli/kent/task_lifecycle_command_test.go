package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"core/prompts"
	"core/shared/client"
	"core/shared/config"
	"core/shared/rpcwire"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

func TestTaskCreateAcceptsSourceWorkspace(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Source Workflow")
	createOut, _ := runWorkflowRootCommandOK(t, "task", "create", "--title", "Sourced", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID, "--source-workspace", binding.WorkspaceID)
	shortID := taskDetailHeadingShortID(t, createOut)
	resp, err := remote.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{ProjectID: binding.ProjectID, ShortID: shortID})
	if err != nil {
		t.Fatalf("GetWorkflowTask after create: %v", err)
	}
	if resp.Task.Summary.SourceWorkspaceID != binding.WorkspaceID {
		t.Fatalf("created task source workspace = %q, want %q", resp.Task.Summary.SourceWorkspaceID, binding.WorkspaceID)
	}
}

func TestTaskStartReportsOperatorActionableConnectionFailure(t *testing.T) {
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, nil, &client.RemoteConnectionError{
			Endpoint: rpcwire.Endpoint{Transport: rpcwire.TransportTCP, Address: "127.0.0.1:54338"},
			Cause:    errors.New("connection refused"),
		}
	}
	defer func() { workflowCommandRemoteOpener = previous }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := taskStartSubcommand([]string{"TASK-1"}, &stdout, &stderr); code != 1 {
		t.Fatalf("task start exit=%d stderr=%q, want connection failure", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Kent server is unavailable at 127.0.0.1:54338") ||
		!strings.Contains(stderr.String(), "start or reconnect the server and retry") ||
		!strings.Contains(stderr.String(), "connection refused") {
		t.Fatalf("task start stderr=%q, want actionable connection failure with cause", stderr.String())
	}
}

func TestTaskEditUpdatesFields(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Edit Workflow")
	createOut, _ := runWorkflowRootCommandOK(t, "task", "create", "--title", "Original", "--body", "Original body", "--workflow", workflowID, "--project", binding.ProjectID)
	shortID := taskDetailHeadingShortID(t, createOut)

	editOut, _ := runWorkflowRootCommandOK(t, "task", "edit", "--project", binding.ProjectID, "--title", "Retitled", shortID)
	if editOut != "Edited task "+shortID+".\n" {
		t.Fatalf("task edit output = %q, want confirmation line", editOut)
	}
	resp, err := remote.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{ProjectID: binding.ProjectID, ShortID: shortID})
	if err != nil {
		t.Fatalf("GetWorkflowTask after title edit: %v", err)
	}
	if resp.Task.Summary.Title != "Retitled" || resp.Task.Body != "Original body" {
		t.Fatalf("after title edit title=%q body=%q, want retitled with unchanged body", resp.Task.Summary.Title, resp.Task.Body)
	}

	runWorkflowRootCommandOK(t, "task", "edit", "--project", binding.ProjectID, "--body", "Edited body", shortID)
	resp, err = remote.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{ProjectID: binding.ProjectID, ShortID: shortID})
	if err != nil {
		t.Fatalf("GetWorkflowTask after body edit: %v", err)
	}
	if resp.Task.Summary.Title != "Retitled" || resp.Task.Body != "Edited body" {
		t.Fatalf("after body edit title=%q body=%q, want unchanged title with edited body", resp.Task.Summary.Title, resp.Task.Body)
	}

	runWorkflowRootCommandOK(t, "task", "edit", "--project", binding.ProjectID, "--source-workspace", binding.WorkspaceID, shortID)
	resp, err = remote.GetWorkflowTask(context.Background(), serverapi.WorkflowTaskGetRequest{ProjectID: binding.ProjectID, ShortID: shortID})
	if err != nil {
		t.Fatalf("GetWorkflowTask after source workspace edit: %v", err)
	}
	if resp.Task.Summary.SourceWorkspaceID != binding.WorkspaceID {
		t.Fatalf("after source workspace edit source=%q, want %q", resp.Task.Summary.SourceWorkspaceID, binding.WorkspaceID)
	}

	jsonOut, _ := runWorkflowRootCommandOK(t, "task", "edit", "--project", binding.ProjectID, "--json", "--title", "JSON title", shortID)
	var updateResp serverapi.WorkflowTaskUpdateResponse
	if err := json.Unmarshal([]byte(jsonOut), &updateResp); err != nil {
		t.Fatalf("task edit --json output = %q, want JSON: %v", jsonOut, err)
	}
	if updateResp.Task.Title != "JSON title" || updateResp.Task.ShortID != shortID {
		t.Fatalf("task edit --json task = %+v, want updated summary", updateResp.Task)
	}
}

func TestTaskEditValidation(t *testing.T) {
	cfg, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restore()

	workflowID := setupLinkedWorkflow(t, binding.ProjectID, "Edit Validation Workflow")
	createOut, _ := runWorkflowRootCommandOK(t, "task", "create", "--title", "Original", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID)
	shortID := taskDetailHeadingShortID(t, createOut)

	if _, stderr, code := runWorkflowRootCommand("task", "edit", "--project", binding.ProjectID); code != 2 || !strings.Contains(stderr, "requires <short-id-or-task-id>") {
		t.Fatalf("task edit without id code=%d stderr=%q, want positional requirement", code, stderr)
	}
	if _, stderr, code := runWorkflowRootCommand("task", "edit", "--project", binding.ProjectID, shortID); code != 2 || !strings.Contains(stderr, "at least one of") {
		t.Fatalf("task edit without fields code=%d stderr=%q, want field requirement", code, stderr)
	}
	if _, stderr, code := runWorkflowRootCommand("task", "edit", "--project", binding.ProjectID, "--body", "x", "--body-file", "/tmp/x", shortID); code != 2 || !strings.Contains(stderr, "--body cannot be combined with --body-file") {
		t.Fatalf("task edit body conflict code=%d stderr=%q, want mutual exclusion error", code, stderr)
	}
}

func TestTaskHumanOnlyActionsAreDeniedInsideKentSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-agent")
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		t.Fatal("human-only task command opened workflow remote")
		return config.App{}, nil, nil
	}
	defer func() {
		workflowCommandRemoteOpener = previous
	}()

	for _, args := range [][]string{
		{"task", "cancel", "TASK-1"},
		{"task", "resume", "TASK-1"},
		{"task", "approve", "transition-1"},
		{"task", "move", "TASK-1", "node-1"},
		{"task", "comment", "delete", "comment-1"},
	} {
		stdout, stderr, code := runWorkflowRootCommand(args...)
		if code != 1 {
			t.Fatalf("%v exit = %d stderr=%q", args, code, stderr)
		}
		if stdout != "" {
			t.Fatalf("%v stdout = %q, want empty", args, stdout)
		}
		if stderr != prompts.WorkflowHumanOnlyTaskActionDeniedPrompt+"\n" {
			t.Fatalf("%v stderr = %q, want denied prompt", args, stderr)
		}
	}
}

func TestTaskExecutionTargetSelectionFlagsRequireConcreteValidSelection(t *testing.T) {
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		t.Fatal("invalid execution-target flags opened workflow remote")
		return config.App{}, nil, nil
	}
	defer func() {
		workflowCommandRemoteOpener = previous
	}()

	for _, args := range [][]string{
		{"task", "start", "--execution-target", "ask", "TASK-1"},
		{"task", "start", "--execution-target", "custom_ref", "TASK-1"},
		{"task", "start", "--execution-target", "head", "--custom-ref", "refs/heads/release", "TASK-1"},
		{"task", "start", "--custom-ref", "refs/heads/release", "TASK-1"},
		{"task", "move", "--execution-target", "custom_ref", "TASK-1", "node-1"},
		{"task", "approve", "--custom-ref", "refs/heads/release", "transition-1"},
	} {
		stdout, stderr, code := runWorkflowRootCommand(args...)
		if code != 2 || stdout != "" || strings.TrimSpace(stderr) == "" {
			t.Fatalf("%v exit=%d stdout=%q stderr=%q, want validation failure", args, code, stdout, stderr)
		}
	}
}

func TestTaskSafeActionsRemainAvailableInsideKentSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-agent")
	_, binding, remote := newWorkflowCommandLoopback(t)
	restore := replaceWorkflowCommandRemoteOpener(t, remote.cfg, remote)
	defer restore()

	workflowID := workflowCreateForTest(t, "Safe Task Workflow").ID
	if workflowID == "" {
		t.Fatal("workflow create did not return a workflow id")
	}
	if _, nodeErr, code := runWorkflowRootCommand("workflow", "node", "add", workflowID, "--key", "implement", "--kind", "agent", "--agent", "workflow-test", "--prompt", "Do work"); code != 0 {
		t.Fatalf("workflow node add exit=%d stderr=%q", code, nodeErr)
	}
	if _, edgeErr, code := runWorkflowRootCommand("workflow", "edge", "add", workflowID, "--from", "backlog", "--transition", "start", "--edge-key", "start", "--to", "implement", "--context", "new_session", "--prompt", "Do work"); code != 0 {
		t.Fatalf("workflow start edge add exit=%d stderr=%q", code, edgeErr)
	}
	if _, edgeErr, code := runWorkflowRootCommand("workflow", "edge", "add", workflowID, "--from", "implement", "--transition", "done", "--edge-key", "done", "--to", "done", "--context", "new_session"); code != 0 {
		t.Fatalf("workflow done edge add exit=%d stderr=%q", code, edgeErr)
	}
	if _, linkErr, code := runWorkflowRootCommand("workflow", "link", binding.ProjectID, workflowID, "--default"); code != 0 {
		t.Fatalf("workflow link exit=%d stderr=%q", code, linkErr)
	}

	taskOut, taskErr, code := runWorkflowRootCommand("task", "create", "--title", "Safe Task", "--body", "Body", "--workflow", workflowID, "--project", binding.ProjectID, "--source-url", "https://github.com/respawn-llc/kent/issues/123")
	if code != 0 {
		t.Fatalf("task create exit=%d stderr=%q", code, taskErr)
	}
	if !strings.Contains(taskOut, "Imported from: https://github.com/respawn-llc/kent/issues/123\n") {
		t.Fatalf("task create output = %q, want source URL", taskOut)
	}
	shortID := taskDetailHeadingShortID(t, taskOut)
	if _, listErr, code := runWorkflowRootCommand("task", "list", "--project", binding.ProjectID); code != 0 {
		t.Fatalf("task list exit=%d stderr=%q", code, listErr)
	}
	if _, showErr, code := runWorkflowRootCommand("task", "show", "--project", binding.ProjectID, shortID); code != 0 {
		t.Fatalf("task show exit=%d stderr=%q", code, showErr)
	}
	commentOut, commentErr, code := runWorkflowRootCommand("task", "comment", "add", "--project", binding.ProjectID, "--author", "user", "--author-id", "octocat", "--body", "note", shortID)
	if code != 0 {
		t.Fatalf("task comment add exit=%d stderr=%q", code, commentErr)
	}
	commentID := labeledOutputValue(t, commentOut, "comment_id")
	if commentID == "" {
		t.Fatalf("task comment add output = %q", commentOut)
	}
	commentListOut, commentListErr, code := runWorkflowRootCommand("task", "comment", "list", "--project", binding.ProjectID, shortID)
	if code != 0 {
		t.Fatalf("task comment list exit=%d stderr=%q", code, commentListErr)
	}
	if !strings.Contains(commentListOut, "octocat at ") {
		t.Fatalf("task comment list output = %q, want author id", commentListOut)
	}
	if _, replaceErr, code := runWorkflowRootCommand("task", "comment", "replace", "--body", "edited", commentID); code != 0 {
		t.Fatalf("task comment replace exit=%d stderr=%q", code, replaceErr)
	}
}

func TestTaskMutationOutputRenderers(t *testing.T) {
	task := serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{ID: "task-1", ShortID: "BLD-1", Title: "Task"},
		Workflow: serverapi.WorkflowPickerItem{WorkflowID: "workflow-1", DisplayName: "Workflow"},
		Placements: []serverapi.WorkflowPlacement{
			{ID: "placement-1", NodeID: "node-1", NodeKey: "implement"},
			{ID: "placement-2", NodeID: "node-2", NodeKey: "review"},
		},
		Runs: []serverapi.WorkflowRun{
			{ID: "run-1", PlacementID: "placement-1", NodeID: "node-1", SessionID: "session-1"},
			{ID: "run-2", PlacementID: "placement-2", NodeID: "node-2", SessionID: "session-2"},
		},
		Transitions: []serverapi.WorkflowTaskTransition{
			{
				ID:            "transition-1",
				SourceNodeKey: "implement",
				TransitionID:  "done",
				Edges: []serverapi.WorkflowTransitionEdge{
					{EdgeKey: "done", TargetNodeKey: "review", State: "applied"},
				},
			},
		},
	}

	var start bytes.Buffer
	writeTaskStartResult(&start, task, serverapi.WorkflowTaskStartResponse{RunID: "run-1", PlacementID: "placement-1", TransitionID: "transition-start"})
	if got, want := start.String(), "Started task BLD-1 in session session-1 using workflow \"Workflow\" (workflow-1).\nFirst node: implement\n"; got != want {
		t.Fatalf("start output = %q, want %q", got, want)
	}

	var approve bytes.Buffer
	writeTaskTransitionResult(&approve, "Approved transition of", task, "transition-1", []string{"run-2"})
	if got, want := approve.String(), "Approved transition of BLD-1 from `implement` to `done`.\nBecause of this, started node review in session session-2.\n"; got != want {
		t.Fatalf("approve output = %q, want %q", got, want)
	}

	var move bytes.Buffer
	writeTaskTransitionResult(&move, "Moved task", task, "transition-1", nil)
	if got, want := move.String(), "Moved task BLD-1 from `implement` to `done`.\n"; got != want {
		t.Fatalf("move output = %q, want %q", got, want)
	}
}

func TestTaskStartSessionPollingTimeoutReportsStartedTask(t *testing.T) {
	remote := &taskSessionPollingRemote{task: serverapi.WorkflowTaskDetail{
		Summary: serverapi.WorkflowTaskSummary{ID: "task-1", ShortID: "BLD-1", Title: "Task"},
		Runs:    []serverapi.WorkflowRun{{ID: "run-1"}},
	}}
	_, err := waitForWorkflowTaskRunSession(context.Background(), remote, "task-1", "run-1", 10*time.Millisecond, time.Millisecond)
	if err == nil {
		t.Fatalf("waitForWorkflowTaskRunSession succeeded, want timeout")
	}
	if got := err.Error(); !strings.Contains(got, "started task BLD-1 with run run-1") || !strings.Contains(got, "session id was not assigned within") {
		t.Fatalf("timeout error = %q, want started task context and timeout detail", got)
	}
}

func TestTaskStartSessionPollingDoesNotWaitForScriptRunSession(t *testing.T) {
	remote := &taskSessionPollingRemote{task: serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{ID: "task-1", ShortID: "BLD-1", Title: "Task"},
		Workflow: serverapi.WorkflowPickerItem{WorkflowID: "workflow-1", DisplayName: "Workflow"},
		Placements: []serverapi.WorkflowPlacement{
			{ID: "placement-1", TaskID: "task-1", NodeID: "node-script", NodeKey: "script"},
		},
		Runs: []serverapi.WorkflowRun{
			{ID: "run-1", TaskID: "task-1", PlacementID: "placement-1", NodeID: "node-script", NodeKind: "script"},
		},
	}}
	detail, err := waitForWorkflowTaskRunSession(context.Background(), remote, "task-1", "run-1", time.Second, time.Millisecond)
	if err != nil {
		t.Fatalf("waitForWorkflowTaskRunSession: %v", err)
	}
	var stdout bytes.Buffer
	writeTaskStartResult(&stdout, detail, serverapi.WorkflowTaskStartResponse{RunID: "run-1", PlacementID: "placement-1", TransitionID: "transition-start"})
	if got, want := stdout.String(), "Started task BLD-1 using workflow \"Workflow\" (workflow-1).\nFirst node: script\n"; got != want {
		t.Fatalf("start output = %q, want %q", got, want)
	}
}

func TestTaskStartCommandIsAvailableInsideKentSessionAndPollsForSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "session-agent")
	restorePolling := replaceTaskStartSessionPolling(t, 50*time.Millisecond, time.Millisecond)
	defer restorePolling()
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := newTaskStartPollingRemote()
	restoreRemote := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restoreRemote()

	stdout, stderr, code := runWorkflowRootCommand("task", "start", "--project", "project-1", "BLD-1")
	if code != 0 {
		t.Fatalf("task start exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	want := "Started task BLD-1 in session session-1 using workflow \"Workflow\" (workflow-1).\nFirst node: implement\n"
	if stdout != want {
		t.Fatalf("task start stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("task start stderr = %q, want empty", stderr)
	}
	if remote.taskIDDetailCalls < 2 {
		t.Fatalf("task detail calls = %d, want polling before session assignment", remote.taskIDDetailCalls)
	}
}

func TestTaskStartJSONWritesStartedOutcomeWithoutPollingTaskDetail(t *testing.T) {
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := newTaskStartPollingRemote()
	restoreRemote := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restoreRemote()

	stdout, stderr, code := runWorkflowRootCommand("task", "start", "--json", "--project", "project-1", "BLD-1")
	if code != 0 || stderr != "" {
		t.Fatalf("task start --json exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var result serverapi.WorkflowTaskInitiatingActionResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("task start --json stdout=%q, want initiating-action JSON: %v", stdout, err)
	}
	if result.Outcome != serverapi.WorkflowTaskInitiatingActionOutcomeStarted || result.Started == nil || result.Started.RunID != "run-1" {
		t.Fatalf("task start --json result=%+v, want started run", result)
	}
	if remote.taskIDDetailCalls != 0 {
		t.Fatalf("task detail calls after JSON start=%d, want none", remote.taskIDDetailCalls)
	}
}

func TestTaskStartNonStartedOutcomesDoNotPollTaskDetail(t *testing.T) {
	for _, tc := range []struct {
		name    string
		json    bool
		outcome serverapi.WorkflowTaskInitiatingActionResult
		want    serverapi.WorkflowTaskInitiatingActionOutcome
		exit    int
	}{
		{
			name:    "selection-required-human",
			outcome: taskSelectionRequiredOutcome("task-1", serverapi.WorkflowTaskExecutionTargetSelectionNone),
			want:    serverapi.WorkflowTaskInitiatingActionOutcomeSelectionRequired,
			exit:    3,
		},
		{
			name:    "selection-required-json",
			json:    true,
			outcome: taskSelectionRequiredOutcome("task-1", serverapi.WorkflowTaskExecutionTargetSelectionNone),
			want:    serverapi.WorkflowTaskInitiatingActionOutcomeSelectionRequired,
			exit:    3,
		},
		{
			name: "in-progress-human",
			outcome: serverapi.WorkflowTaskInitiatingActionResult{
				Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeInProgress,
				InProgress: &serverapi.WorkflowTaskExecutionTargetMaterializationProgress{
					TaskID: "task-1",
					Phase:  serverapi.WorkflowTaskExecutionTargetMaterializationPhaseMaterializing,
				},
			},
			want: serverapi.WorkflowTaskInitiatingActionOutcomeInProgress,
			exit: 4,
		},
		{
			name: "in-progress-json",
			json: true,
			outcome: serverapi.WorkflowTaskInitiatingActionResult{
				Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeInProgress,
				InProgress: &serverapi.WorkflowTaskExecutionTargetMaterializationProgress{
					TaskID: "task-1",
					Phase:  serverapi.WorkflowTaskExecutionTargetMaterializationPhaseMaterializing,
				},
			},
			want: serverapi.WorkflowTaskInitiatingActionOutcomeInProgress,
			exit: 4,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.App{WorkspaceRoot: t.TempDir()}
			remote := &taskStartOutcomeRemote{
				taskStartPollingRemote: newTaskStartPollingRemote(),
				outcomes:               []serverapi.WorkflowTaskInitiatingActionResult{tc.outcome},
			}
			restoreRemote := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
			defer restoreRemote()

			args := []string{"task", "start", "--project", "project-1"}
			if tc.json {
				args = append(args, "--json")
			}
			args = append(args, "BLD-1")
			stdout, stderr, code := runWorkflowRootCommand(args...)
			if code != tc.exit {
				t.Fatalf("task start exit=%d stdout=%q stderr=%q, want %d", code, stdout, stderr, tc.exit)
			}
			if tc.json {
				if stderr != "" {
					t.Fatalf("task start --json stderr=%q, want empty", stderr)
				}
				var result serverapi.WorkflowTaskInitiatingActionResult
				if err := json.Unmarshal([]byte(stdout), &result); err != nil {
					t.Fatalf("task start --json stdout=%q, want initiating-action JSON: %v", stdout, err)
				}
				if result.Outcome != tc.want {
					t.Fatalf("task start --json result=%+v, want outcome %q", result, tc.want)
				}
			} else {
				if stdout != "" {
					t.Fatalf("task start stdout=%q, want empty for non-start outcome", stdout)
				}
				if strings.Count(stderr, "\n") != 1 || strings.TrimSpace(stderr) == "" {
					t.Fatalf("task start stderr=%q, want one concise actionable line", stderr)
				}
			}
			if remote.taskIDDetailCalls != 0 {
				t.Fatalf("task detail calls after %s=%d, want none", tc.want, remote.taskIDDetailCalls)
			}
			if len(remote.startRequests) != 1 || remote.startRequests[0].Selection != nil || remote.startRequests[0].SelectionGeneration != nil {
				t.Fatalf("start requests=%+v, want one unselected request", remote.startRequests)
			}
		})
	}
}

func TestTaskStartExecutionTargetOverrideRetriesSelectionRequirement(t *testing.T) {
	restorePolling := replaceTaskStartSessionPolling(t, 50*time.Millisecond, time.Millisecond)
	defer restorePolling()
	cfg := config.App{WorkspaceRoot: t.TempDir()}
	remote := &taskStartOutcomeRemote{
		taskStartPollingRemote: newTaskStartPollingRemote(),
		outcomes: []serverapi.WorkflowTaskInitiatingActionResult{
			taskSelectionRequiredOutcome("task-1", serverapi.WorkflowTaskExecutionTargetSelectionCustomRef),
			{
				Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeStarted,
				Started: &serverapi.WorkflowTaskStartResponse{
					TransitionID: "transition-1",
					PlacementID:  "placement-1",
					RunID:        "run-1",
				},
			},
		},
	}
	restoreRemote := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
	defer restoreRemote()

	_, stderr, code := runWorkflowRootCommand(
		"task", "start",
		"--project", "project-1",
		"--execution-target", "custom_ref",
		"--custom-ref", "refs/heads/release",
		"BLD-1",
	)
	if code != 0 || stderr != "" {
		t.Fatalf("task start override exit=%d stderr=%q", code, stderr)
	}
	if len(remote.startRequests) != 2 {
		t.Fatalf("start requests=%+v, want selection negotiation and retry", remote.startRequests)
	}
	first, second := remote.startRequests[0], remote.startRequests[1]
	if first.SelectionGeneration != nil ||
		first.Selection == nil || first.Selection.Mode != serverapi.WorkflowTaskExecutionTargetSelectionCustomRef ||
		first.Selection.CustomRef == nil || *first.Selection.CustomRef != "refs/heads/release" {
		t.Fatalf("initial start request=%+v, want explicit custom-ref override", first)
	}
	if second.SelectionGeneration == nil || *second.SelectionGeneration != "selection-1" ||
		second.Selection == nil || second.Selection.Mode != serverapi.WorkflowTaskExecutionTargetSelectionCustomRef ||
		second.Selection.CustomRef == nil || *second.Selection.CustomRef != "refs/heads/release" {
		t.Fatalf("selection retry request=%+v, want negotiated custom-ref selection", second)
	}
}

func TestTaskMoveAndApproveExecutionTargetOverrideRetryOriginalAction(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			name: "move",
			args: []string{
				"task", "move",
				"--project", "project-1",
				"--execution-target", "head",
				"--output", "result=done",
				"--commentary", "manual move",
				"BLD-1", "node-2",
			},
		},
		{
			name: "approve",
			args: []string{
				"task", "approve",
				"--execution-target", "head",
				"transition-1",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.App{WorkspaceRoot: t.TempDir()}
			remote := &taskTransitionOutcomeRemote{
				taskStartPollingRemote: newTaskStartPollingRemote(),
				moveOutcomes: []serverapi.WorkflowTaskInitiatingActionResult{
					taskSelectionRequiredOutcome("task-1", serverapi.WorkflowTaskExecutionTargetSelectionHead),
					{
						Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeMoved,
						Moved:   &serverapi.WorkflowTaskMoveResponse{TransitionID: "transition-1"},
					},
				},
				approveOutcomes: []serverapi.WorkflowTaskInitiatingActionResult{
					taskSelectionRequiredOutcome("task-1", serverapi.WorkflowTaskExecutionTargetSelectionHead),
					{
						Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeApproved,
						Approved: &serverapi.WorkflowTaskApproveResponse{
							TaskID:       "task-1",
							TransitionID: "transition-1",
						},
					},
				},
			}
			restoreRemote := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
			defer restoreRemote()

			_, stderr, code := runWorkflowRootCommand(tc.args...)
			if code != 0 || stderr != "" {
				t.Fatalf("task %s override exit=%d stderr=%q", tc.name, code, stderr)
			}
			switch tc.name {
			case "move":
				if len(remote.moveRequests) != 2 {
					t.Fatalf("move requests=%+v, want selection negotiation and retry", remote.moveRequests)
				}
				first, second := remote.moveRequests[0], remote.moveRequests[1]
				if first.Selection != nil || first.SelectionGeneration != nil {
					t.Fatalf("initial move request=%+v, want no client-side override", first)
				}
				if first.TaskID != "task-1" || first.TargetNodeID != "node-2" || first.Commentary != "manual move" || first.OutputValues["result"] != "done" {
					t.Fatalf("initial move request=%+v, want original move inputs", first)
				}
				if second.SelectionGeneration == nil || *second.SelectionGeneration != "selection-1" ||
					second.Selection == nil || second.Selection.Mode != serverapi.WorkflowTaskExecutionTargetSelectionHead ||
					second.TaskID != first.TaskID || second.TargetNodeID != first.TargetNodeID ||
					second.Commentary != first.Commentary || second.OutputValues["result"] != first.OutputValues["result"] {
					t.Fatalf("move retry request=%+v, want original action plus negotiated head selection", second)
				}
			case "approve":
				if len(remote.approveRequests) != 2 {
					t.Fatalf("approve requests=%+v, want selection negotiation and retry", remote.approveRequests)
				}
				first, second := remote.approveRequests[0], remote.approveRequests[1]
				if first.Selection != nil || first.SelectionGeneration != nil {
					t.Fatalf("initial approve request=%+v, want no client-side override", first)
				}
				if first.TransitionID != "transition-1" {
					t.Fatalf("initial approve request=%+v, want original transition", first)
				}
				if second.SelectionGeneration == nil || *second.SelectionGeneration != "selection-1" ||
					second.Selection == nil || second.Selection.Mode != serverapi.WorkflowTaskExecutionTargetSelectionHead ||
					second.TransitionID != first.TransitionID {
					t.Fatalf("approve retry request=%+v, want original action plus negotiated head selection", second)
				}
			}
		})
	}
}

func TestTaskLifecycleCommandsConsumeWorktreeSetupProgressWithoutMutationDeadline(t *testing.T) {
	restorePolling := replaceTaskStartSessionPolling(t, 50*time.Millisecond, time.Millisecond)
	defer restorePolling()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "start", args: []string{"task", "start", "--project", "project-1", "BLD-1"}, want: "Started task BLD-1"},
		{name: "approve", args: []string{"task", "approve", "transition-1"}, want: "Approved transition of BLD-1"},
		{name: "move", args: []string{"task", "move", "--project", "project-1", "BLD-1", "node-1"}, want: "Moved task BLD-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.App{WorkspaceRoot: t.TempDir()}
			remote := newSetupProgressLifecycleRemote()
			restoreRemote := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
			defer restoreRemote()

			stdout, stderr, code := runWorkflowRootCommand(tc.args...)
			if code != 0 {
				t.Fatalf("command exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !strings.HasPrefix(stdout, tc.want) {
				t.Fatalf("stdout = %q, want lifecycle result prefix %q", stdout, tc.want)
			}
			assertSetupProgressOutputHasPaths(t, stderr, remote.scriptPath, remote.worktreeRoot)
			if !remote.mutationCalled {
				t.Fatal("workflow mutation was not called")
			}
		})
	}
}

func TestTaskLifecycleCommandsWarnAndContinueWhenSetupProgressUnavailable(t *testing.T) {
	restorePolling := replaceTaskStartSessionPolling(t, 50*time.Millisecond, time.Millisecond)
	defer restorePolling()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "start", args: []string{"task", "start", "--project", "project-1", "BLD-1"}, want: "Started task BLD-1"},
		{name: "approve", args: []string{"task", "approve", "transition-1"}, want: "Approved transition of BLD-1"},
		{name: "move", args: []string{"task", "move", "--project", "project-1", "BLD-1", "node-1"}, want: "Moved task BLD-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.App{WorkspaceRoot: t.TempDir()}
			remote := newSetupProgressLifecycleRemote()
			remote.subscribeErr = errors.New("setup subscription unavailable")
			restoreRemote := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
			defer restoreRemote()

			stdout, stderr, code := runWorkflowRootCommand(tc.args...)
			if code != 0 {
				t.Fatalf("command exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !strings.HasPrefix(stdout, tc.want) {
				t.Fatalf("stdout = %q, want lifecycle result prefix %q", stdout, tc.want)
			}
			if !strings.Contains(stderr, "warning: worktree setup progress subscription unavailable") {
				t.Fatalf("stderr = %q, want setup subscription warning", stderr)
			}
			if !remote.mutationCalled {
				t.Fatal("workflow mutation was not called")
			}
		})
	}
}

func TestTaskLifecycleCommandsWarnAndContinueWhenSetupProgressStreamFailsAfterMutation(t *testing.T) {
	restorePolling := replaceTaskStartSessionPolling(t, 50*time.Millisecond, time.Millisecond)
	defer restorePolling()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "start", args: []string{"task", "start", "--project", "project-1", "BLD-1"}, want: "Started task BLD-1"},
		{name: "approve", args: []string{"task", "approve", "transition-1"}, want: "Approved transition of BLD-1"},
		{name: "move", args: []string{"task", "move", "--project", "project-1", "BLD-1", "node-1"}, want: "Moved task BLD-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.App{WorkspaceRoot: t.TempDir()}
			remote := newSetupProgressLifecycleRemote()
			remote.streamErrAfterEvent = errors.New("setup stream disconnected")
			restoreRemote := replaceWorkflowCommandRemoteOpener(t, cfg, remote)
			defer restoreRemote()

			stdout, stderr, code := runWorkflowRootCommand(tc.args...)
			if code != 0 {
				t.Fatalf("command exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if !strings.HasPrefix(stdout, tc.want) {
				t.Fatalf("stdout = %q, want lifecycle result prefix %q", stdout, tc.want)
			}
			assertSetupProgressOutputHasPaths(t, stderr, remote.scriptPath, remote.worktreeRoot)
			if !strings.Contains(stderr, "warning: worktree setup progress stream ended unexpectedly") {
				t.Fatalf("stderr = %q, want setup stream warning", stderr)
			}
		})
	}
}

func replaceTaskStartSessionPolling(t *testing.T, timeout time.Duration, interval time.Duration) func() {
	t.Helper()
	originalTimeout := taskStartSessionPollTimeout
	originalInterval := taskStartSessionPollInterval
	taskStartSessionPollTimeout = timeout
	taskStartSessionPollInterval = interval
	return func() {
		taskStartSessionPollTimeout = originalTimeout
		taskStartSessionPollInterval = originalInterval
	}
}

type taskSessionPollingRemote struct {
	client.WorkflowClient
	task serverapi.WorkflowTaskDetail
}

func (r *taskSessionPollingRemote) Close() error { return nil }

func (r *taskSessionPollingRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *taskSessionPollingRemote) GetWorkflowTask(context.Context, serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{Task: r.task}, nil
}

type taskStartPollingRemote struct {
	client.WorkflowClient
	projectID         string
	taskID            string
	shortID           string
	workflowID        string
	workflow          string
	placementID       string
	runID             string
	sessionID         string
	nodeID            string
	nodeKey           string
	taskIDDetailCalls int
}

func newTaskStartPollingRemote() *taskStartPollingRemote {
	return &taskStartPollingRemote{
		projectID:   "project-1",
		taskID:      "task-1",
		shortID:     "BLD-1",
		workflowID:  "workflow-1",
		workflow:    "Workflow",
		placementID: "placement-1",
		runID:       "run-1",
		sessionID:   "session-1",
		nodeID:      "node-1",
		nodeKey:     "implement",
	}
}

func (r *taskStartPollingRemote) Close() error { return nil }

func (r *taskStartPollingRemote) SubscribeWorktreeSetup(ctx context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	return noopWorktreeSetupSubscription{}, nil
}

func (r *taskStartPollingRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{Binding: &serverapi.ProjectBinding{ProjectID: r.projectID}}, nil
}

func (r *taskStartPollingRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	if req.ProjectID == r.projectID && req.ShortID == r.shortID {
		return serverapi.WorkflowTaskGetResponse{Task: r.taskDetail("")}, nil
	}
	if req.TaskID == r.taskID {
		r.taskIDDetailCalls++
		if r.taskIDDetailCalls == 1 {
			return serverapi.WorkflowTaskGetResponse{Task: r.taskDetail("")}, nil
		}
		return serverapi.WorkflowTaskGetResponse{Task: r.taskDetail(r.sessionID)}, nil
	}
	return serverapi.WorkflowTaskGetResponse{}, sql.ErrNoRows
}

func (r *taskStartPollingRemote) StartWorkflowTask(context.Context, serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	return serverapi.WorkflowTaskInitiatingActionResult{
		Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeStarted,
		Started: &serverapi.WorkflowTaskStartResponse{TransitionID: "transition-1", PlacementID: r.placementID, RunID: r.runID},
	}, nil
}

func (r *taskStartPollingRemote) taskDetail(sessionID string) serverapi.WorkflowTaskDetail {
	return serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{ID: r.taskID, ShortID: r.shortID, WorkflowID: r.workflowID, ProjectID: r.projectID, Title: "Task"},
		Workflow: serverapi.WorkflowPickerItem{WorkflowID: r.workflowID, DisplayName: r.workflow},
		Placements: []serverapi.WorkflowPlacement{
			{ID: r.placementID, TaskID: r.taskID, NodeID: r.nodeID, NodeKey: r.nodeKey},
		},
		Runs: []serverapi.WorkflowRun{
			{ID: r.runID, TaskID: r.taskID, PlacementID: r.placementID, NodeID: r.nodeID, SessionID: sessionID},
		},
	}
}

type taskStartOutcomeRemote struct {
	*taskStartPollingRemote
	outcomes      []serverapi.WorkflowTaskInitiatingActionResult
	startRequests []serverapi.WorkflowTaskStartRequest
}

func (r *taskStartOutcomeRemote) StartWorkflowTask(_ context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	r.startRequests = append(r.startRequests, req)
	if len(r.outcomes) == 0 {
		return serverapi.WorkflowTaskInitiatingActionResult{}, errors.New("no task-start outcome configured")
	}
	outcome := r.outcomes[0]
	r.outcomes = r.outcomes[1:]
	return outcome, nil
}

type taskTransitionOutcomeRemote struct {
	*taskStartPollingRemote
	moveOutcomes    []serverapi.WorkflowTaskInitiatingActionResult
	approveOutcomes []serverapi.WorkflowTaskInitiatingActionResult
	moveRequests    []serverapi.WorkflowTaskMoveRequest
	approveRequests []serverapi.WorkflowTaskApproveRequest
}

func (r *taskTransitionOutcomeRemote) MoveWorkflowTask(_ context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	r.moveRequests = append(r.moveRequests, req)
	if len(r.moveOutcomes) == 0 {
		return serverapi.WorkflowTaskInitiatingActionResult{}, errors.New("no task-move outcome configured")
	}
	outcome := r.moveOutcomes[0]
	r.moveOutcomes = r.moveOutcomes[1:]
	return outcome, nil
}

func (r *taskTransitionOutcomeRemote) ApproveWorkflowTask(_ context.Context, req serverapi.WorkflowTaskApproveRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	r.approveRequests = append(r.approveRequests, req)
	if len(r.approveOutcomes) == 0 {
		return serverapi.WorkflowTaskInitiatingActionResult{}, errors.New("no task-approve outcome configured")
	}
	outcome := r.approveOutcomes[0]
	r.approveOutcomes = r.approveOutcomes[1:]
	return outcome, nil
}

func taskSelectionRequiredOutcome(taskID string, selection serverapi.WorkflowTaskExecutionTargetSelectionMode) serverapi.WorkflowTaskInitiatingActionResult {
	return serverapi.WorkflowTaskInitiatingActionResult{
		Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeSelectionRequired,
		SelectionRequired: &serverapi.WorkflowTaskExecutionTargetSelectionRequired{
			TaskID:            taskID,
			Generation:        "selection-1",
			SourceWorkspaceID: "workspace-1",
			Source:            serverapi.WorkflowTaskExecutionTargetSource{Kind: serverapi.WorkflowTaskExecutionTargetSourceNonGit},
			SupportedSelections: []serverapi.WorkflowTaskExecutionTargetSelectionMode{
				selection,
			},
			ConfiguredPolicy: serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyAsk},
		},
	}
}

type setupProgressLifecycleRemote struct {
	client.WorkflowClient
	projectID           string
	taskID              string
	shortID             string
	workflowID          string
	workflow            string
	placementID         string
	runID               string
	sessionID           string
	nodeID              string
	nodeKey             string
	transitionID        string
	scriptPath          string
	worktreeRoot        string
	events              chan serverapi.WorktreeSetupEvent
	eventConsumed       chan struct{}
	subscribedID        serverapi.WorktreeSetupOperationID
	mutationCalled      bool
	subscribeErr        error
	streamErrAfterEvent error
}

func newSetupProgressLifecycleRemote() *setupProgressLifecycleRemote {
	return &setupProgressLifecycleRemote{
		projectID:     "project-1",
		taskID:        "task-1",
		shortID:       "BLD-1",
		workflowID:    "workflow-1",
		workflow:      "Workflow",
		placementID:   "placement-1",
		runID:         "run-1",
		sessionID:     "session-1",
		nodeID:        "node-1",
		nodeKey:       "implement",
		transitionID:  "transition-1",
		scriptPath:    "/tmp/kent-setup.sh",
		worktreeRoot:  "/tmp/kent-worktree",
		events:        make(chan serverapi.WorktreeSetupEvent, 1),
		eventConsumed: make(chan struct{}, 1),
	}
}

func (r *setupProgressLifecycleRemote) Close() error { return nil }

func (r *setupProgressLifecycleRemote) SubscribeWorktreeSetup(_ context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if r.subscribeErr != nil {
		return nil, r.subscribeErr
	}
	r.subscribedID = req.SetupOperationID
	return &setupProgressSubscription{events: r.events, consumed: r.eventConsumed, errAfterEvent: r.streamErrAfterEvent}, nil
}

func (r *setupProgressLifecycleRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{Binding: &serverapi.ProjectBinding{ProjectID: r.projectID}}, nil
}

func (r *setupProgressLifecycleRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	if req.ProjectID == r.projectID && req.ShortID == r.shortID {
		return serverapi.WorkflowTaskGetResponse{Task: r.taskDetail("")}, nil
	}
	if req.TaskID == r.taskID {
		return serverapi.WorkflowTaskGetResponse{Task: r.taskDetail(r.sessionID)}, nil
	}
	return serverapi.WorkflowTaskGetResponse{}, sql.ErrNoRows
}

func (r *setupProgressLifecycleRemote) StartWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	if err := r.validateMutationContextAndSetupID(ctx, req.SetupOperationID); err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	return serverapi.WorkflowTaskInitiatingActionResult{
		Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeStarted,
		Started: &serverapi.WorkflowTaskStartResponse{TransitionID: r.transitionID, PlacementID: r.placementID, RunID: r.runID},
	}, nil
}

func (r *setupProgressLifecycleRemote) ApproveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskApproveRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	if err := r.validateMutationContextAndSetupID(ctx, req.SetupOperationID); err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	return serverapi.WorkflowTaskInitiatingActionResult{
		Outcome:  serverapi.WorkflowTaskInitiatingActionOutcomeApproved,
		Approved: &serverapi.WorkflowTaskApproveResponse{TaskID: r.taskID, TransitionID: r.transitionID, RunIDs: []string{r.runID}},
	}, nil
}

func (r *setupProgressLifecycleRemote) MoveWorkflowTask(ctx context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskInitiatingActionResult, error) {
	if err := r.validateMutationContextAndSetupID(ctx, req.SetupOperationID); err != nil {
		return serverapi.WorkflowTaskInitiatingActionResult{}, err
	}
	return serverapi.WorkflowTaskInitiatingActionResult{
		Outcome: serverapi.WorkflowTaskInitiatingActionOutcomeMoved,
		Moved:   &serverapi.WorkflowTaskMoveResponse{TransitionID: r.transitionID, RunIDs: []string{r.runID}},
	}, nil
}

func (r *setupProgressLifecycleRemote) validateMutationContextAndSetupID(ctx context.Context, setupOperationID serverapi.WorktreeSetupOperationID) error {
	r.mutationCalled = true
	if _, ok := ctx.Deadline(); ok {
		return errors.New("workflow lifecycle mutation context has a deadline")
	}
	if err := setupOperationID.Validate(); err != nil {
		return err
	}
	if r.subscribedID.Validate() != nil {
		return nil
	}
	if setupOperationID != r.subscribedID {
		return errors.New("workflow lifecycle mutation used a different setup operation id than the subscription")
	}
	r.events <- serverapi.WorktreeSetupEvent{
		SetupOperationID:    setupOperationID,
		SourceWorkspaceRoot: "/tmp/source",
		WorktreeRoot:        r.worktreeRoot,
		ScriptPath:          r.scriptPath,
		Phase:               serverapi.WorktreeSetupPhaseStarted,
	}
	select {
	case <-r.eventConsumed:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("setup progress event was not consumed while mutation was in flight")
	}
}

func (r *setupProgressLifecycleRemote) taskDetail(sessionID string) serverapi.WorkflowTaskDetail {
	return serverapi.WorkflowTaskDetail{
		Summary:  serverapi.WorkflowTaskSummary{ID: r.taskID, ShortID: r.shortID, WorkflowID: r.workflowID, ProjectID: r.projectID, Title: "Task"},
		Workflow: serverapi.WorkflowPickerItem{WorkflowID: r.workflowID, DisplayName: r.workflow},
		Placements: []serverapi.WorkflowPlacement{
			{ID: r.placementID, TaskID: r.taskID, NodeID: r.nodeID, NodeKey: r.nodeKey},
		},
		Runs: []serverapi.WorkflowRun{
			{ID: r.runID, TaskID: r.taskID, PlacementID: r.placementID, NodeID: r.nodeID, SessionID: sessionID},
		},
		Transitions: []serverapi.WorkflowTaskTransition{
			{
				ID:            r.transitionID,
				SourceNodeKey: r.nodeKey,
				TransitionID:  "done",
				Edges: []serverapi.WorkflowTransitionEdge{
					{EdgeKey: "done", TargetNodeKey: "done", State: "applied"},
				},
			},
		},
	}
}

type setupProgressSubscription struct {
	events        <-chan serverapi.WorktreeSetupEvent
	consumed      chan<- struct{}
	errAfterEvent error
	delivered     bool
}

func (s *setupProgressSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	if s.delivered && s.errAfterEvent != nil {
		return serverapi.WorktreeSetupEvent{}, s.errAfterEvent
	}
	select {
	case event := <-s.events:
		s.delivered = true
		select {
		case s.consumed <- struct{}{}:
		default:
		}
		return event, nil
	case <-ctx.Done():
		return serverapi.WorktreeSetupEvent{}, ctx.Err()
	}
}

func (s *setupProgressSubscription) Close() error { return nil }

type noopWorktreeSetupSubscription struct{}

func (noopWorktreeSetupSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	<-ctx.Done()
	return serverapi.WorktreeSetupEvent{}, ctx.Err()
}

func (noopWorktreeSetupSubscription) Close() error { return nil }

func assertSetupProgressOutputHasPaths(t *testing.T, output string, scriptPath string, worktreeRoot string) {
	t.Helper()
	fields := strings.Fields(output)
	if len(fields) < 8 {
		t.Fatalf("setup progress output fields = %v", fields)
	}
	scriptFound := false
	worktreeFound := false
	for _, field := range fields {
		if field == scriptPath {
			scriptFound = true
		}
		if strings.TrimSuffix(field, ".") == worktreeRoot {
			worktreeFound = true
		}
	}
	if !scriptFound || !worktreeFound {
		t.Fatalf("setup progress output = %q, want script %q and worktree %q", output, scriptPath, worktreeRoot)
	}
}
