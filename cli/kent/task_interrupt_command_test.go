package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

type taskInterruptCommandRemote struct {
	apicontract.WorkflowService
	interruptRequests []serverapi.WorkflowTaskInterruptRequest
	moveRequests      []serverapi.WorkflowTaskMoveRequest
	previewResponse   *serverapi.WorkflowTaskMovePreviewResponse
	moveResponse      *serverapi.WorkflowTaskMoveResponse
}

func allowHumanTaskActionForTest(t *testing.T) {
	t.Helper()
	t.Setenv(sessionenv.SessionIDEnv, "")
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

func TestTaskMoveCarriesExecutionTargetAndSetupOperation(t *testing.T) {
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
	if request.TaskID != "task-1" || request.TargetNodeID != "done" || request.SetupOperationID.Validate() != nil ||
		request.ExecutionTarget == nil || request.ExecutionTarget.Mode != serverapi.WorkflowExecutionTargetModeHead {
		t.Fatalf("move request = %+v", request)
	}
}
