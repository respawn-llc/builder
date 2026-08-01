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

	startResponse serverapi.WorkflowTaskStartResponse
	moveResponse  serverapi.WorkflowTaskMoveResponse
	startRequests []serverapi.WorkflowTaskStartRequest
	moveRequests  []serverapi.WorkflowTaskMoveRequest
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
	return r.startResponse, nil
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

func (r *taskDependencyLifecycleRemote) SubscribeWorktreeSetup(context.Context, serverapi.WorktreeSetupSubscribeRequest) (serverapi.WorktreeSetupSubscription, error) {
	return canceledWorktreeSetupSubscription{}, nil
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
	t.Setenv(sessionenv.SessionIDEnv, "")
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

func TestTaskMoveJSONWritesAppliedTypedOutcome(t *testing.T) {
	t.Setenv(sessionenv.SessionIDEnv, "")
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
