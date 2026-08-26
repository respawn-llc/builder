package workflowview

import (
	"testing"

	"core/server/workflow"
	"core/server/workflowexecution"
	"core/shared/serverapi"
)

type staticTaskStatusLiveObservationSource struct {
	observation workflowexecution.WorkflowTaskExecutionObservation
}

func (s staticTaskStatusLiveObservationSource) ObserveWorkflowTaskExecutions([]workflow.TaskID) (workflowexecution.WorkflowTaskExecutionObservation, error) {
	return s.observation, nil
}

type countingTaskStatusLiveObservationSource struct {
	source TaskStatusLiveObservationSource
	calls  *int
}

func (s countingTaskStatusLiveObservationSource) ObserveWorkflowTaskExecutions(taskIDs []workflow.TaskID) (workflowexecution.WorkflowTaskExecutionObservation, error) {
	*s.calls++
	return s.source.ObserveWorkflowTaskExecutions(taskIDs)
}

func TestTaskDetailProjectsConcurrencyQueuedCurrentNodeAsResumable(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "concurrency queued")
	detail := taskDetailWithObservation(t, fixture, workflowexecution.WorkflowTaskExecutionObservation{
		ConcurrencyQueued: map[workflow.TaskID][]workflow.CurrentNodeReference{
			started.task.ID: {started.currentNode},
		},
		Quiescence: map[workflow.TaskID]bool{started.task.ID: false},
	})

	projected, err := detail.GetTask(t.Context(), string(started.task.ID))
	if err != nil {
		t.Fatalf("TaskDetail.GetTask: %v", err)
	}
	if projected.Status.Kind != serverapi.WorkflowTaskStatusKindQueued ||
		!projected.Actions.CanResume ||
		projected.Actions.CanInterrupt {
		t.Fatalf("concurrency-queued Task detail = %+v", projected)
	}
}
