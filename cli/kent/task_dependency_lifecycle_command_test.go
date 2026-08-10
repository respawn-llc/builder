package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"core/shared/apicontract"
	"core/shared/serverapi"
	"core/shared/sessionenv"
)

type taskDependencyLifecycleRemote struct {
	apicontract.WorkflowService

	startResponse   serverapi.WorkflowTaskStartResponse
	startError      error
	moveResponse    serverapi.WorkflowTaskMoveResponse
	approveResponse serverapi.WorkflowTaskApproveResponse
	startRequests   []serverapi.WorkflowTaskStartRequest
	moveRequests    []serverapi.WorkflowTaskMoveRequest
	approveRequests []serverapi.WorkflowTaskApproveRequest
	setupEvent      *serverapi.WorktreeSetupEvent
}

func (r *taskDependencyLifecycleRemote) GetWorkflowTask(_ context.Context, req serverapi.WorkflowTaskGetRequest) (serverapi.WorkflowTaskGetResponse, error) {
	return serverapi.WorkflowTaskGetResponse{
		Task: serverapi.WorkflowTaskDetail{
			Summary: serverapi.WorkflowTaskSummary{ID: req.TaskID, ShortID: "KENT-1", ProjectID: "project-1"},
		},
	}, nil
}

func (r *taskDependencyLifecycleRemote) StartWorkflowTask(_ context.Context, req serverapi.WorkflowTaskStartRequest) (serverapi.WorkflowTaskStartResponse, error) {
	r.startRequests = append(r.startRequests, req)
	return r.startResponse, r.startError
}

func (r *taskDependencyLifecycleRemote) MoveWorkflowTask(_ context.Context, req serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error) {
	r.moveRequests = append(r.moveRequests, req)
	return r.moveResponse, nil
}

func (r *taskDependencyLifecycleRemote) PreviewWorkflowTaskMove(_ context.Context, req serverapi.WorkflowTaskMovePreviewRequest) (serverapi.WorkflowTaskMovePreviewResponse, error) {
	return serverapi.WorkflowTaskMovePreviewResponse{
		Outcome: serverapi.WorkflowTaskMovePreviewOutcomeDirect,
		Direct:  &serverapi.WorkflowTaskMovePreviewDirect{},
	}, nil
}

func (r *taskDependencyLifecycleRemote) ApproveWorkflowTask(_ context.Context, req serverapi.WorkflowTaskApproveRequest) (serverapi.WorkflowTaskApproveResponse, error) {
	r.approveRequests = append(r.approveRequests, req)
	return r.approveResponse, nil
}

func (r *taskDependencyLifecycleRemote) SubscribeWorktreeSetup(_ context.Context, req serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	if r.setupEvent != nil {
		event := *r.setupEvent
		event.SetupOperationID = req.SetupOperationID
		return testWorktreeSetupSubscription(func(context.Context) (serverapi.WorktreeSetupEvent, error) { return event, nil }), nil
	}
	return eofWorktreeSetupSubscription{returned: make(chan struct{})}, nil
}

func (r *taskDependencyLifecycleRemote) ResolveProjectPath(context.Context, serverapi.ProjectResolvePathRequest) (serverapi.ProjectResolvePathResponse, error) {
	return serverapi.ProjectResolvePathResponse{}, nil
}

func (r *taskDependencyLifecycleRemote) Close() error { return nil }

type canceledWorktreeSetupSubscription struct{}

func (canceledWorktreeSetupSubscription) Next(ctx context.Context) (serverapi.WorktreeSetupEvent, error) {
	<-ctx.Done()
	return serverapi.WorktreeSetupEvent{}, ctx.Err()
}

func (canceledWorktreeSetupSubscription) Close() error { return nil }

func TestTaskStartDependencyConfirmationIsNoninteractiveAndMapsIgnoreFlag(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	count := 2
	for _, tt := range []struct {
		name       string
		args       []string
		json       bool
		wantIgnore bool
	}{
		{name: "human", args: []string{"task-1"}},
		{name: "json with explicit proceed", args: []string{"task-1", "--ignore-dependencies", "--json"}, json: true, wantIgnore: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			remote := &taskDependencyLifecycleRemote{
				startResponse: serverapi.WorkflowTaskStartResponse{
					Outcome:                    serverapi.WorkflowTaskActionOutcomeDependencyConfirmationRequired,
					UnsatisfiedDependencyCount: &count,
				},
			}
			installWorkflowCommandRemote(t, remote)

			var stdout, stderr bytes.Buffer
			exitCode := taskStartSubcommand(tt.args, &stdout, &stderr)

			if exitCode != 1 || len(remote.startRequests) != 1 {
				t.Fatalf("exit=%d stdout=%q stderr=%q requests=%+v", exitCode, stdout.String(), stderr.String(), remote.startRequests)
			}
			if remote.startRequests[0].ProceedDespiteDependencies != tt.wantIgnore {
				t.Fatalf("proceed_despite_dependencies=%t", remote.startRequests[0].ProceedDespiteDependencies)
			}
			if remote.startRequests[0].BranchName != nil {
				t.Fatalf("branch_name=%q, want omission", *remote.startRequests[0].BranchName)
			}
			if tt.json {
				if stderr.Len() != 0 {
					t.Fatalf("JSON stderr=%q", stderr.String())
				}
				var output map[string]json.RawMessage
				if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
					t.Fatalf("decode JSON: %v", err)
				}
				if len(output) != 2 || output["outcome"] == nil || output["unsatisfied_dependency_count"] == nil {
					t.Fatalf("JSON=%s", stdout.String())
				}
				return
			}
			const want = "Task task-1 has 2 unsatisfied dependencies.\nReview them with `kent task show task-1`.\nRerun with `--ignore-dependencies` to proceed.\n"
			if stdout.Len() != 0 || stderr.String() != want {
				t.Fatalf("stdout=%q stderr=%q want=%q", stdout.String(), stderr.String(), want)
			}
		})
	}
}

func TestTaskStartObservationFailureWritesAppliedJSONAndCarriesInvokingSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "agent-session")
	remote := &taskDependencyLifecycleRemote{
		startResponse: serverapi.WorkflowTaskStartResponse{
			Outcome: serverapi.WorkflowTaskActionOutcomeApplied,
			Applied: &serverapi.WorkflowTaskStartApplied{
				CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-1"}},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskStartSubcommand([]string{"task-1", "--json"}, &stdout, &stderr)

	if exitCode != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	var output serverapi.WorkflowTaskStartResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output.Applied == nil {
		t.Fatalf("JSON output=%+v, error=%v", output, err)
	}
	if len(remote.startRequests) != 1 ||
		remote.startRequests[0].InvokingSessionID == nil ||
		remote.startRequests[0].InvokingSessionID.String() != "agent-session" {
		t.Fatalf("start requests=%+v, want invoking Session agent-session", remote.startRequests)
	}
}

func TestTaskStartForwardsExplicitBranchName(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	branchName := " feature/KENT-1 "
	remote := &taskDependencyLifecycleRemote{
		startResponse: serverapi.WorkflowTaskStartResponse{
			Outcome: serverapi.WorkflowTaskActionOutcomeApplied,
			Applied: &serverapi.WorkflowTaskStartApplied{
				CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-1"}},
			},
		},
		setupEvent: &serverapi.WorktreeSetupEvent{Phase: serverapi.WorktreeSetupPhaseNotRequired, NotRequired: &serverapi.WorktreeSetupNotRequired{Reason: serverapi.WorktreeSetupNotRequiredNoConfiguredScript}},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskStartSubcommand([]string{"task-1", "--branch-name", branchName, "--json"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.startRequests) != 1 ||
		remote.startRequests[0].BranchName == nil ||
		*remote.startRequests[0].BranchName != branchName {
		t.Fatalf("start requests=%+v, want explicit branch name", remote.startRequests)
	}
}

func TestTaskStartRendersTypedInitialBranchError(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	remote := &taskDependencyLifecycleRemote{
		startError: &serverapi.WorkflowTaskInitialBranchError{
			Reason:     serverapi.WorkflowTaskInitialBranchErrorReasonInvalidName,
			BranchName: "feature bad",
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskStartSubcommand([]string{"task-1", "--branch-name", "feature bad"}, &stdout, &stderr)

	if exitCode != 1 || stdout.Len() != 0 || len(remote.startRequests) != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q requests=%+v", exitCode, stdout.String(), stderr.String(), remote.startRequests)
	}
	if stderr.Len() == 0 || stderr.String() == remote.startError.Error()+"\n" {
		t.Fatalf("stderr=%q, want typed branch rendering instead of the generic error", stderr.String())
	}
}

func TestTaskMoveDependencyConfirmationSupportsJSONAndMapsIgnoreFlag(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	count := 1
	remote := &taskDependencyLifecycleRemote{
		moveResponse: serverapi.WorkflowTaskMoveResponse{
			Outcome:                    serverapi.WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired,
			UnsatisfiedDependencyCount: &count,
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskMoveSubcommand(
		[]string{"task-1", "node-2", "--ignore-dependencies", "--json"},
		&stdout,
		&stderr,
	)

	if exitCode != 1 || stderr.Len() != 0 || len(remote.moveRequests) != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q requests=%+v", exitCode, stdout.String(), stderr.String(), remote.moveRequests)
	}
	if !remote.moveRequests[0].ProceedDespiteDependencies {
		t.Fatalf("move request=%+v", remote.moveRequests[0])
	}
	if remote.moveRequests[0].BranchName != nil {
		t.Fatalf("branch_name=%q, want omission", *remote.moveRequests[0].BranchName)
	}
	var output serverapi.WorkflowTaskMoveResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if output.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired ||
		output.UnsatisfiedDependencyCount == nil || *output.UnsatisfiedDependencyCount != 1 {
		t.Fatalf("JSON output=%+v", output)
	}
}

func TestTaskMoveFromWorkflowSessionCarriesInvokingSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "agent-session")
	remote := &taskDependencyLifecycleRemote{
		moveResponse: serverapi.WorkflowTaskMoveResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
			Applied: &serverapi.WorkflowTaskMoveApplied{
				CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-2"}},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskMoveSubcommand([]string{"task-1", "node-2", "--json"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.moveRequests) != 1 ||
		remote.moveRequests[0].InvokingSessionID == nil ||
		remote.moveRequests[0].InvokingSessionID.String() != "agent-session" {
		t.Fatalf("move requests=%+v, want invoking Session agent-session", remote.moveRequests)
	}
}

func TestTaskMoveJSONWritesAppliedTypedOutcome(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	remote := &taskDependencyLifecycleRemote{
		moveResponse: serverapi.WorkflowTaskMoveResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
			Applied: &serverapi.WorkflowTaskMoveApplied{
				CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-2"}},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskMoveSubcommand([]string{"task-1", "node-2", "--json"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	var output serverapi.WorkflowTaskMoveResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if output.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied ||
		output.Applied == nil || len(output.Applied.CurrentNodes) != 1 {
		t.Fatalf("JSON output=%+v", output)
	}
}
func TestTaskMoveDependencyConfirmationMapsIgnoreFlag(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	count := 2
	remote := &taskDependencyLifecycleRemote{
		moveResponse: serverapi.WorkflowTaskMoveResponse{
			Outcome:                    serverapi.WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired,
			UnsatisfiedDependencyCount: &count,
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskMoveSubcommand([]string{"task-1", "node-2", "--ignore-dependencies", "--json"}, &stdout, &stderr)

	if exitCode != 1 || stderr.Len() != 0 || len(remote.moveRequests) != 1 {
		t.Fatalf("exit=%d stdout=%q stderr=%q requests=%+v", exitCode, stdout.String(), stderr.String(), remote.moveRequests)
	}
	if !remote.moveRequests[0].ProceedDespiteDependencies {
		t.Fatalf("move request = %+v, want proceed despite dependencies", remote.moveRequests[0])
	}
	var output serverapi.WorkflowTaskMoveResponse
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if output.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired ||
		output.UnsatisfiedDependencyCount == nil || *output.UnsatisfiedDependencyCount != count {
		t.Fatalf("JSON output=%+v", output)
	}
}

func TestTaskMoveJSONWritesSubmittedNoOpTypedOutcome(t *testing.T) {
	unsetSessionIDEnvironmentForTest(t)
	remote := &taskDependencyLifecycleRemote{
		moveResponse: serverapi.WorkflowTaskMoveResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeNoOp,
			NoOp: &serverapi.WorkflowTaskMoveNoOp{
				CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-2"}},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskMoveSubcommand([]string{"task-1", "node-2", "--json"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 || len(remote.moveRequests) != 1 {
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
func TestTaskApproveFromWorkflowSessionCarriesInvokingSession(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "agent-session")
	remote := &taskDependencyLifecycleRemote{
		approveResponse: serverapi.WorkflowTaskApproveResponse{
			Outcome: serverapi.WorkflowExecutionTargetActionOutcomeApplied,
			Applied: &serverapi.WorkflowTaskApproveApplied{
				TaskID:       "task-1",
				CurrentNodes: []serverapi.WorkflowTaskCurrentNode{{NodeID: "node-2"}},
			},
		},
	}
	installWorkflowCommandRemote(t, remote)

	var stdout, stderr bytes.Buffer
	exitCode := taskApproveSubcommand([]string{"approval-1"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if len(remote.approveRequests) != 1 ||
		remote.approveRequests[0].InvokingSessionID == nil ||
		remote.approveRequests[0].InvokingSessionID.String() != "agent-session" {
		t.Fatalf("approve requests=%+v, want invoking Session agent-session", remote.approveRequests)
	}
}
