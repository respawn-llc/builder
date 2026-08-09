package workflowview

import (
	"testing"

	"core/server/workflow"
	"core/server/workflowexecution"
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

func TestTaskStatusProjectionDurableSnapshotRetainsOneCurrentNodeGeneration(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	started := fixture.startTask(t, "durable snapshot")
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{},
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}

	err = projection.WithDurableSnapshot(t.Context(), func(snapshot *TaskStatusDurableSnapshot) error {
		before, err := snapshot.CurrentNodesByTask(t.Context(), []workflow.TaskID{started.task.ID})
		if err != nil {
			return err
		}
		if len(before[started.task.ID]) != 1 || before[started.task.ID][0].Scheduling == nil {
			t.Fatalf("before Current Nodes = %+v", before)
		}
		beforeState := before[started.task.ID][0].Scheduling.State

		if err := fixture.store.InterruptCurrentNode(
			t.Context(),
			started.currentNode,
			"projection_test",
			workflow.CurrentNodeInterruptionDetail{Code: "projection_test"},
		); err != nil {
			return err
		}
		after, err := snapshot.CurrentNodesByTask(t.Context(), []workflow.TaskID{started.task.ID})
		if err != nil {
			return err
		}
		if len(after[started.task.ID]) != 1 || after[started.task.ID][0].Scheduling == nil {
			t.Fatalf("after Current Nodes = %+v", after)
		}
		if got := after[started.task.ID][0].Scheduling.State; got != beforeState {
			t.Fatalf("snapshot escaped transaction: state = %q, want pre-mutation state %q", got, beforeState)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithDurableSnapshot: %v", err)
	}

	var observedState workflow.CurrentNodeSchedulingState
	err = projection.WithDurableSnapshot(t.Context(), func(snapshot *TaskStatusDurableSnapshot) error {
		nodes, err := snapshot.CurrentNodesByTask(t.Context(), []workflow.TaskID{started.task.ID})
		if err != nil {
			return err
		}
		if len(nodes[started.task.ID]) != 1 || nodes[started.task.ID][0].Scheduling == nil {
			t.Fatalf("post-mutation Current Nodes = %+v", nodes)
		}
		observedState = nodes[started.task.ID][0].Scheduling.State
		return nil
	})
	if err != nil {
		t.Fatalf("WithDurableSnapshot post-mutation: %v", err)
	}
	if observedState != workflow.CurrentNodeSchedulingInterrupted {
		t.Fatalf("new snapshot state = %q, want interrupted", observedState)
	}
}

func TestTaskStatusDurableSnapshotCannotEscapeProjectionCallback(t *testing.T) {
	fixture := newCurrentNodeViewFixture(t, false)
	projection, err := NewTaskStatusProjection(
		fixture.metadata,
		fixture.store,
		NewTaskProjector(),
		staticTaskStatusLiveObservationSource{},
	)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}

	var retained *TaskStatusDurableSnapshot
	if err := projection.WithDurableSnapshot(t.Context(), func(snapshot *TaskStatusDurableSnapshot) error {
		retained = snapshot
		return nil
	}); err != nil {
		t.Fatalf("WithDurableSnapshot: %v", err)
	}
	if retained == nil {
		t.Fatal("projection did not provide a durable snapshot")
	}
	if _, err := retained.CurrentNodesByTask(t.Context(), []workflow.TaskID{"missing"}); err == nil {
		t.Fatal("retained durable snapshot remained usable after callback")
	}
}
