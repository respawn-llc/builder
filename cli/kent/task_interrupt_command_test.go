package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

type taskInterruptCommandRemote struct {
	apicontract.WorkflowService
	interruptRequests []serverapi.WorkflowTaskInterruptRequest
	resumeRequests    []serverapi.WorkflowTaskResumeRequest
	moveRequests      []serverapi.WorkflowTaskMoveRequest
	previewResponse   *serverapi.WorkflowTaskMovePreviewResponse
	moveResponse      *serverapi.WorkflowTaskMoveResponse
}

type eofWorktreeSetupSubscription struct {
	returned chan<- struct{}
}

type testWorktreeSetupSubscription func(context.Context) (serverapi.WorktreeSetupEvent, error)

func (s testWorktreeSetupSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	return s(ctx)
}
func (testWorktreeSetupSubscription) Close() error { return nil }

func (s eofWorktreeSetupSubscription) Next(context.Context) (serverapi.WorktreeSetupEvent, error) {
	close(s.returned)
	return serverapi.WorktreeSetupEvent{}, io.EOF
}

func (eofWorktreeSetupSubscription) Close() error { return nil }

type eofWorktreeSetupRemote struct {
	*taskInterruptCommandRemote
	returned chan<- struct{}
}

func (r eofWorktreeSetupRemote) SubscribeWorktreeSetup(context.Context, serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	return eofWorktreeSetupSubscription{returned: r.returned}, nil
}

func allowHumanTaskActionForTest(t *testing.T) {
	t.Helper()
	unsetSessionIDEnvironmentForTest(t)
}

func unsetSessionIDEnvironmentForTest(t *testing.T) {
	t.Helper()
	value, present := os.LookupEnv(sessionenv.SessionIDEnv)
	if err := os.Unsetenv(sessionenv.SessionIDEnv); err != nil {
		t.Fatalf("unset %s: %v", sessionenv.SessionIDEnv, err)
	}
	t.Cleanup(func() {
		if present {
			if err := os.Setenv(sessionenv.SessionIDEnv, value); err != nil {
				t.Errorf("restore %s: %v", sessionenv.SessionIDEnv, err)
			}
			return
		}
		if err := os.Unsetenv(sessionenv.SessionIDEnv); err != nil {
			t.Errorf("restore unset %s: %v", sessionenv.SessionIDEnv, err)
		}
	})
}

func (r *taskInterruptCommandRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{
		Task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: req.TaskID, ShortID: "KENT-1"},
		},
	}, nil
}

func (r *taskInterruptCommandRemote) InterruptWorkflowTask(_ context.Context, req serverapi.WorkflowTaskInterruptRequest) (serverapi.WorkflowTaskInterruptResponse, error) {
	r.interruptRequests = append(r.interruptRequests, req)
	return serverapi.WorkflowTaskInterruptResponse{}, nil
}

func (r *taskInterruptCommandRemote) ResumeWorkflowTask(_ context.Context, req serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
	r.resumeRequests = append(r.resumeRequests, req)
	return serverapi.WorkflowTaskResumeResponse{
		Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskResumeApplied{
			CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-1"}},
		},
	}, nil
}

func (r *taskInterruptCommandRemote) SubscribeWorktreeSetup(_ context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	event := serverapi.WorktreeSetupEvent{SetupOperationID: req.SetupOperationID, Phase: serverapi.WorktreeSetupPhaseNotRequired, NotRequired: &serverapi.WorktreeSetupNotRequired{Reason: serverapi.WorktreeSetupNotRequiredNoTargetPreparation}}
	return testWorktreeSetupSubscription(func(context.Context) (serverapi.WorktreeSetupEvent, error) { return event, nil }), nil
}

func (r *taskInterruptCommandRemote) MoveWorkflowTask(_ context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error) {
	r.moveRequests = append(r.moveRequests, req)
	if r.moveResponse != nil {
		return *r.moveResponse, nil
	}
	return serverapi.WorkflowTaskMoveResponse{
		Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
		Applied: &serverapi.WorkflowTaskMoveApplied{
			CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: req.TargetNodeID}},
		},
	}, nil
}

func TestTaskMoveJSONWritesPreviewNoOpTypedOutcome(t *testing.T) {
	allowHumanTaskActionForTest(t)
	remote := &taskInterruptCommandRemote{
		previewResponse: &serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeNoOp,
			NoOp: &serverapi.WorkflowTaskMovePreviewNoOp{
				CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-2"}},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskSubcommand([]string{"move", "task-1", "node-2", "--json"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 || len(remote.moveRequests) != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q requests=%+v", exitCode, stdout.String(), stderr.String(), remote.moveRequests)
	}
	var output serverapi.WorkflowTaskMoveResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if output.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeNoOp ||
		output.NoOp == nil || len(output.NoOp.CurrentNodes) != 1 {
		t.Fatalf("JSON output=%+v", output)
	}
}

func (r *taskInterruptCommandRemote) PreviewWorkflowTaskMove(_ context.Context, req serverapi.WorkflowTaskMovePreviewRequest) (serverapi.WorkflowTaskMovePreviewResponse, error) {
	if r.previewResponse != nil {
		return *r.previewResponse, nil
	}
	return serverapi.WorkflowTaskMovePreviewResponse{
		Outcome: serverapi.WorkflowTaskMovePreviewOutcomeDirect,
		Direct:  &serverapi.WorkflowTaskMovePreviewDirect{},
	}, nil
}

func TestTaskMoveRejectsTransitionFlagsForDirectDestination(t *testing.T) {
	allowHumanTaskActionForTest(t)
	remote := &taskInterruptCommandRemote{}
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	t.Cleanup(func() { workflowCommandRemoteOpener = previous })

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{"move", "task-1", "done", "--transition", "next"}, &stdout, &stderr)
	if exitCode != 2 || len(remote.moveRequests) != 0 {
		t.Fatalf("exit code = %d, move requests = %+v, stderr=%q", exitCode, remote.moveRequests, stderr.String())
	}
}

func TestTaskMoveValidatesStructuredValuesAgainstPreview(t *testing.T) {
	allowHumanTaskActionForTest(t)
	remote := &taskInterruptCommandRemote{
		previewResponse: &serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeTransition,
			Transition: &serverapi.WorkflowTaskMovePreviewTransition{
				Choices: []serverapi.WorkflowTaskMovePreviewTransitionChoice{{
					TransitionKey:         "next",
					Label:                 "Next",
					SourceNodeDisplayName: "Plan",
					RequiredValues: []serverapi.WorkflowTaskMoveRequiredValue{{
						NodeKey:     "plan",
						OutputName:  "summary",
						Description: "Summary",
					}},
				}},
			},
		},
	}
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	t.Cleanup(func() { workflowCommandRemoteOpener = previous })

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{
		"move", "task-1", "implement",
		"--transition", "next",
		"--values-json", `{"plan":{"summary":"done"}}`,
	}, &stdout, &stderr)
	if exitCode != 0 || len(remote.moveRequests) != 1 {
		t.Fatalf("exit code = %d, move requests = %+v, stderr=%q", exitCode, remote.moveRequests, stderr.String())
	}
	if got := remote.moveRequests[0].Values["plan"]["summary"]; got != "done" {
		t.Fatalf("structured values = %+v, want plan.summary=done", remote.moveRequests[0].Values)
	}
}

func TestTaskMoveAutoSelectsSoleTransition(t *testing.T) {
	allowHumanTaskActionForTest(t)
	remote := &taskInterruptCommandRemote{
		previewResponse: &serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeTransition,
			Transition: &serverapi.WorkflowTaskMovePreviewTransition{
				Choices: []serverapi.WorkflowTaskMovePreviewTransitionChoice{{
					TransitionKey:         "next",
					Label:                 "Next",
					SourceNodeDisplayName: "Plan",
					RequiredValues:        []serverapi.WorkflowTaskMoveRequiredValue{},
				}},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{"move", "task-1", "implement"}, &stdout, &stderr)
	if exitCode != 0 || len(remote.moveRequests) != 1 {
		t.Fatalf("exit code = %d, move requests = %+v, stderr=%q", exitCode, remote.moveRequests, stderr.String())
	}
	if remote.moveRequests[0].TransitionKey == nil || *remote.moveRequests[0].TransitionKey != "next" {
		t.Fatalf("move request = %+v, want automatically selected next Transition", remote.moveRequests[0])
	}
}

func TestTaskMoveRejectsExplicitBlankTransition(t *testing.T) {
	allowHumanTaskActionForTest(t)
	remote := &taskInterruptCommandRemote{
		previewResponse: &serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeTransition,
			Transition: &serverapi.WorkflowTaskMovePreviewTransition{
				Choices: []serverapi.WorkflowTaskMovePreviewTransitionChoice{{
					TransitionKey:         "next",
					Label:                 "Next",
					SourceNodeDisplayName: "Plan",
					RequiredValues:        []serverapi.WorkflowTaskMoveRequiredValue{},
				}},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskSubcommand([]string{
		"move", "task-1", "implement", "--transition", " \t",
	}, &stdout, &stderr)
	if exitCode != 2 || stdout.Len() != 0 || len(remote.moveRequests) != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q requests=%+v", exitCode, stdout.String(), stderr.String(), remote.moveRequests)
	}
}

func TestTaskMoveForwardsExtraValueNodeToServerValidation(t *testing.T) {
	allowHumanTaskActionForTest(t)
	remote := &taskInterruptCommandRemote{
		previewResponse: &serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeTransition,
			Transition: &serverapi.WorkflowTaskMovePreviewTransition{
				Choices: []serverapi.WorkflowTaskMovePreviewTransitionChoice{{
					TransitionKey:         "next",
					Label:                 "Next",
					SourceNodeDisplayName: "Plan",
					RequiredValues:        []serverapi.WorkflowTaskMoveRequiredValue{},
				}},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{
		"move", "task-1", "implement", "--values-json", `{"extra":{}}`,
	}, &stdout, &stderr)
	if exitCode != 0 || len(remote.moveRequests) != 1 {
		t.Fatalf("exit code = %d, move requests = %+v, stderr=%q", exitCode, remote.moveRequests, stderr.String())
	}
	if _, ok := remote.moveRequests[0].Values["extra"]; !ok {
		t.Fatalf("structured values = %+v, want extra node forwarded to server", remote.moveRequests[0].Values)
	}
}

func TestTaskMoveForwardsNullValueNodeToServerValidation(t *testing.T) {
	allowHumanTaskActionForTest(t)
	resolved := "already resolved"
	remote := &taskInterruptCommandRemote{
		previewResponse: &serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeTransition,
			Transition: &serverapi.WorkflowTaskMovePreviewTransition{
				Choices: []serverapi.WorkflowTaskMovePreviewTransitionChoice{{
					TransitionKey:         "next",
					Label:                 "Next",
					SourceNodeDisplayName: "Plan",
					RequiredValues: []serverapi.WorkflowTaskMoveRequiredValue{{
						NodeKey:       "plan",
						OutputName:    "summary",
						Description:   "Summary",
						ResolvedValue: &resolved,
					}},
				}},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{
		"move", "task-1", "implement", "--values-json", `{"plan":null}`,
	}, &stdout, &stderr)
	if exitCode != 0 || len(remote.moveRequests) != 1 {
		t.Fatalf("exit code = %d, move requests = %+v, stderr=%q", exitCode, remote.moveRequests, stderr.String())
	}
	if remote.moveRequests[0].Values["plan"] != nil {
		t.Fatalf("structured values = %+v, want null plan node forwarded to server", remote.moveRequests[0].Values)
	}
}

func TestTaskMoveRejectsNullValuesDocument(t *testing.T) {
	allowHumanTaskActionForTest(t)
	remote := &taskInterruptCommandRemote{}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskSubcommand([]string{
		"move", "task-1", "implement", "--values-json", "null",
	}, &stdout, &stderr)

	if exitCode != 2 || stdout.Len() != 0 || len(remote.moveRequests) != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q requests=%+v", exitCode, stdout.String(), stderr.String(), remote.moveRequests)
	}
}

func TestReadManualMoveValuesRejectsExplicitEmptyDocuments(t *testing.T) {
	if _, err := readManualMoveValues("", "", true, false); err == nil {
		t.Fatal("explicit empty inline values document was accepted")
	}
	path := filepath.Join(t.TempDir(), "values.json")
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatalf("write values file: %v", err)
	}
	if _, err := readManualMoveValues("", path, false, true); err == nil {
		t.Fatal("explicit whitespace-only values file was accepted")
	}
}

func TestTaskMoveRequiresExplicitTransitionForMultipleChoices(t *testing.T) {
	allowHumanTaskActionForTest(t)
	remote := &taskInterruptCommandRemote{
		previewResponse: &serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeTransition,
			Transition: &serverapi.WorkflowTaskMovePreviewTransition{
				Choices: []serverapi.WorkflowTaskMovePreviewTransitionChoice{
					{TransitionKey: "next", Label: "Next", SourceNodeDisplayName: "Plan", RequiredValues: []serverapi.WorkflowTaskMoveRequiredValue{}},
					{TransitionKey: "alternate", Label: "Alternate", SourceNodeDisplayName: "Review", RequiredValues: []serverapi.WorkflowTaskMoveRequiredValue{}},
				},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := taskSubcommand([]string{"move", "task-1", "implement"}, &stdout, &stderr); exitCode != 2 {
		t.Fatalf("exit code = %d, want selection-required exit; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.moveRequests) != 0 {
		t.Fatalf("move requests = %+v, want none before selection", remote.moveRequests)
	}

	stdout.Reset()
	stderr.Reset()
	if exitCode := taskSubcommand([]string{"move", "task-1", "implement", "--transition", "alternate"}, &stdout, &stderr); exitCode != 0 {
		t.Fatalf("explicit selection exit code = %d, stderr=%q", exitCode, stderr.String())
	}
	if len(remote.moveRequests) != 1 ||
		remote.moveRequests[0].TransitionKey == nil ||
		*remote.moveRequests[0].TransitionKey != "alternate" {
		t.Fatalf("move requests = %+v, want alternate Transition selection", remote.moveRequests)
	}
}

func TestTaskMoveBlockedPreviewUsesCLIBlockerMapping(t *testing.T) {
	allowHumanTaskActionForTest(t)
	reason := serverapi.WorkflowTaskMovePreviewBlockerWaitingQuestion
	remote := &taskInterruptCommandRemote{
		previewResponse: &serverapi.WorkflowTaskMovePreviewResponse{
			Outcome: serverapi.WorkflowTaskMovePreviewOutcomeBlocked,
			Blocked: &serverapi.WorkflowTaskMovePreviewBlocked{Reason: reason},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{"move", "task-1", "implement"}, &stdout, &stderr)
	if exitCode != 1 || len(remote.moveRequests) != 0 {
		t.Fatalf("exit code = %d, move requests = %+v, stderr=%q", exitCode, remote.moveRequests, stderr.String())
	}
	if manualMoveBlockerMessage(reason) == string(reason) {
		t.Fatalf("blocker mapping returned raw protocol reason %q", reason)
	}
	want := fmt.Sprintf("task move blocked: %s\n", manualMoveBlockerMessage(reason))
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want mapped blocker output", stderr.String())
	}
}

func (r *taskInterruptCommandRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *taskInterruptCommandRemote) Close() error {
	return nil
}

func TestTaskInterruptTargetsTheResolvedTask(t *testing.T) {
	allowHumanTaskActionForTest(t)
	remote := &taskInterruptCommandRemote{}
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{"interrupt", "task-1", "--session", "session-1", "--reason", "operator request"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	want := serverapi.WorkflowTaskInterruptRequest{TaskID: "task-1", SessionID: "session-1", Reason: "operator request"}
	if len(remote.interruptRequests) != 1 || remote.interruptRequests[0] != want {
		t.Fatalf("interrupt requests = %+v, want %+v", remote.interruptRequests, want)
	}
	if got := stdout.String(); got != "Interrupted KENT-1.\n" {
		t.Fatalf("stdout = %q", got)
	}
}

func TestTaskInterruptFromWorkflowSessionCarriesInvokingSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "agent-session")
	remote := &taskInterruptCommandRemote{}
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{"interrupt", "task-1"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.interruptRequests) != 1 ||
		remote.interruptRequests[0].InvokingSessionID == nil ||
		remote.interruptRequests[0].InvokingSessionID.String() != "agent-session" {
		t.Fatalf("interrupt requests = %+v, want invoking Session agent-session", remote.interruptRequests)
	}
}

func TestTaskResumeFromWorkflowSessionCarriesInvokingSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "agent-session")
	remote := &taskInterruptCommandRemote{}
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{"resume", "task-1"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.resumeRequests) != 1 ||
		remote.resumeRequests[0].InvokingSessionID == nil ||
		remote.resumeRequests[0].InvokingSessionID.String() != "agent-session" ||
		remote.resumeRequests[0].SetupOperationID.Validate() != nil {
		t.Fatalf("resume requests = %+v, want invoking Session agent-session", remote.resumeRequests)
	}
}

func TestWorktreeSetupProgressReportsEOFBeforeTerminalEvent(t *testing.T) {
	returned := make(chan struct{})
	remote := eofWorktreeSetupRemote{
		taskInterruptCommandRemote: &taskInterruptCommandRemote{},
		returned:                   returned,
	}
	observation, err := subscribeWorktreeSetupProgress(
		context.Background(),
		remote,
		serverapi.NewWorktreeSetupOperationID(),
		io.Discard,
	)
	if err != nil {
		t.Fatalf("subscribeWorktreeSetupProgress: %v", err)
	}
	<-returned
	if result := <-observation.done; !errors.Is(result.err, io.ErrUnexpectedEOF) {
		t.Fatalf("setup progress result error = %v, want unexpected EOF", result.err)
	}
}

func TestTaskSetupGuidanceProjectsStructuredRecovery(t *testing.T) {
	target := serverapi.WorkflowExecutionTargetSelection{Mode: serverapi.WorkflowExecutionTargetModeHead}
	script := "/repo/setup.sh"
	tests := []struct {
		name     string
		terminal *serverapi.WorktreeSetupEvent
		err      error
		success  bool
		kinds    []taskSetupActionKind
	}{
		{name: "completed", terminal: &serverapi.WorktreeSetupEvent{Phase: serverapi.WorktreeSetupPhaseCompleted, Completed: &serverapi.WorktreeSetupCompleted{}}, success: true},
		{name: "not required with orphan", terminal: &serverapi.WorktreeSetupEvent{Phase: serverapi.WorktreeSetupPhaseNotRequired, NotRequired: &serverapi.WorktreeSetupNotRequired{Reason: serverapi.WorktreeSetupNotRequiredNoConfiguredScript, RetainedPreviousWorktree: &serverapi.RetainedPreviousWorktree{Worktree: setupGuidanceWorktree("/tmp/orphan")}}}, success: true, kinds: []taskSetupActionKind{taskSetupActionListWorktrees}},
		{name: "retained setup failure", terminal: &serverapi.WorktreeSetupEvent{Phase: serverapi.WorktreeSetupPhaseFailed, Failed: &serverapi.WorktreeSetupFailed{RetryReadiness: serverapi.WorktreeSetupRetryReady, Diagnostic: "failed twice", ScriptPath: &script, ExecutionTarget: &target}}, kinds: []taskSetupActionKind{taskSetupActionRetry, taskSetupActionChooseNone, taskSetupActionChooseHead, taskSetupActionChooseDefault, taskSetupActionChooseRef}},
		{name: "topology-free target failure", terminal: &serverapi.WorktreeSetupEvent{Phase: serverapi.WorktreeSetupPhaseFailed, Failed: &serverapi.WorktreeSetupFailed{RetryReadiness: serverapi.WorktreeSetupRetryReady, Diagnostic: "target failed", ExecutionTarget: &target}}, kinds: []taskSetupActionKind{taskSetupActionRetry, taskSetupActionChooseNone, taskSetupActionChooseHead, taskSetupActionChooseDefault, taskSetupActionChooseRef}},
		{name: "timeout", err: context.DeadlineExceeded, kinds: []taskSetupActionKind{taskSetupActionInspect, taskSetupActionListWorktrees}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := projectTaskSetupGuidance("task-1", nil, test.terminal, test.err)
			if err != nil {
				t.Fatalf("project guidance: %v", err)
			}
			if got.Success != test.success || len(got.Actions) != len(test.kinds) {
				t.Fatalf("guidance = %+v", got)
			}
			if (got.Diagnostic == nil) != test.success {
				t.Fatalf("diagnostic = %v for success %t", got.Diagnostic, test.success)
			}
			if test.name == "not required with orphan" && (got.RetainedRoot == nil || *got.RetainedRoot != "/tmp/orphan") {
				t.Fatalf("retained guidance = %+v", got)
			}
			for index, kind := range test.kinds {
				if got.Actions[index].Kind != kind {
					t.Fatalf("action %d = %+v, want %s", index, got.Actions[index], kind)
				}
			}
			if len(got.Actions) == 5 && got.Actions[0].Args[len(got.Actions[0].Args)-1] != "head" {
				t.Fatalf("retry action = %+v", got.Actions[0])
			}
		})
	}
	if workflowTaskSetupObservationTimeout != 2*time.Minute {
		t.Fatalf("observation timeout = %s", workflowTaskSetupObservationTimeout)
	}
	if _, err := projectTaskSetupGuidance("task-1", nil, nil, errors.New(" ")); err == nil {
		t.Fatal("blank diagnostic was accepted")
	}
}

func TestMoveSetupGuidancePreservesStructuredInput(t *testing.T) {
	project, commentary, transition := "project-1", "note", "next"
	base, err := taskMoveRecoveryArgs("task-1", "done", &project, &commentary, &transition, map[string]map[string]string{"plan": {"summary": "done"}}, true, true)
	if err != nil {
		t.Fatalf("recovery args: %v", err)
	}
	target := serverapi.WorkflowExecutionTargetSelection{Mode: serverapi.WorkflowExecutionTargetModeHead}
	guidance, err := projectMoveSetupGuidance(base, &target, &serverapi.WorktreeSetupRetainedError{Worktree: setupGuidanceWorktree("/tmp/retained"), Diagnostic: "failed twice", ScriptPath: "/repo/setup.sh"})
	if err != nil {
		t.Fatalf("project guidance: %v", err)
	}
	if guidance.Success || guidance.Diagnostic == nil || guidance.ScriptPath == nil || len(guidance.Actions) != 5 {
		t.Fatalf("guidance = %+v", guidance)
	}
	wantPrefix := []string{"kent", "task", "move", "task-1", "done", "--project", "project-1", "--commentary", "note", "--transition", "next", "--values-json", `{"plan":{"summary":"done"}}`, "--ignore-dependencies", "--json"}
	for index, action := range guidance.Actions {
		if len(action.Args) != len(wantPrefix)+2 {
			t.Fatalf("action %d = %+v", index, action)
		}
		for argIndex, want := range wantPrefix {
			if action.Args[argIndex] != want {
				t.Fatalf("action %d arg %d = %q, want %q", index, argIndex, action.Args[argIndex], want)
			}
		}
	}
}

func TestAlreadyStartedGuidanceUsesResumeAndMoveActions(t *testing.T) {
	project := "project-1"
	got := taskAlreadyStartedGuidance("task-1", &project)
	if len(got.Actions) != 2 || got.Actions[0].Kind != taskSetupActionRetry || got.Actions[1].Kind != taskSetupActionMove {
		t.Fatalf("guidance = %+v", got)
	}
	if got.Actions[0].Args[len(got.Actions[0].Args)-1] != project {
		t.Fatalf("Resume action = %+v", got.Actions[0])
	}
}

func setupGuidanceWorktree(root string) serverapi.WorktreeTopologyEntry {
	return serverapi.WorktreeTopologyEntry{
		Variant: serverapi.WorktreeTopologyVariantRegistered,
		Registered: &serverapi.WorktreeRegisteredFacts{
			Git:  serverapi.WorktreeGitFacts{CanonicalRoot: root, HeadObject: "0123456789abcdef"},
			Kent: serverapi.WorktreeKentFacts{WorktreeID: "worktree-1", CanonicalRoot: root, DisplayName: "KENT-453", Managed: true},
		},
	}
}

func TestTaskMoveCarriesExecutionTargetWithoutSetupOperation(t *testing.T) {
	allowHumanTaskActionForTest(t)
	remote := &taskInterruptCommandRemote{}
	previous := workflowCommandRemoteOpener
	workflowCommandRemoteOpener = func(context.Context, string) (config.App, workflowCommandRemote, error) {
		return config.App{}, remote, nil
	}
	t.Cleanup(func() {
		workflowCommandRemoteOpener = previous
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := taskSubcommand([]string{"move", "task-1", "done", "--execution-target", "head"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", exitCode, stderr.String())
	}
	if len(remote.moveRequests) != 1 {
		t.Fatalf("move requests = %+v, want one", remote.moveRequests)
	}
	request := remote.moveRequests[0]
	if request.TaskID != "task-1" || request.TargetNodeID != "done" ||
		request.ExecutionTarget == nil || request.ExecutionTarget.Mode != serverapi.WorkflowExecutionTargetModeHead {
		t.Fatalf("move request = %+v", request)
	}
}
