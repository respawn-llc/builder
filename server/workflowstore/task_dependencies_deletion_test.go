package workflowstore

import (
	"testing"
	"time"

	"core/server/workflow"
)

func TestDeleteTaskRemovesDependenciesAndTouchesDistinctSurvivorsOnce(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	incomingNeighbor := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Incoming", Body: "Body"})
	victim := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Victim", Body: "Body"})
	outgoingNeighbor := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Outgoing", Body: "Body"})
	if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: incomingNeighbor.ID, BlockedTaskID: victim.ID}); err != nil {
		t.Fatalf("add incoming dependency: %v", err)
	}
	if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: victim.ID, BlockedTaskID: outgoingNeighbor.ID}); err != nil {
		t.Fatalf("add outgoing dependency: %v", err)
	}
	beforeIncoming := taskUpdatedAt(t, store, incomingNeighbor.ID)
	beforeOutgoing := taskUpdatedAt(t, store, outgoingNeighbor.ID)
	updatedAt := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	store.now = func() time.Time { return updatedAt }

	if _, err := store.DeleteTask(ctx, victim.ID); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	var dependencyCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM task_dependencies
		WHERE blocker_task_id = ? OR blocked_task_id = ?
	`, victim.ID, victim.ID).Scan(&dependencyCount); err != nil {
		t.Fatalf("count dependencies touching deleted task: %v", err)
	}
	if dependencyCount != 0 {
		t.Fatalf("dependencies touching deleted task = %d, want 0", dependencyCount)
	}
	if got := taskUpdatedAt(t, store, incomingNeighbor.ID); got != updatedAt.UnixMilli() || got == beforeIncoming {
		t.Fatalf("incoming survivor timestamp = %d, want one update at %d after %d", got, updatedAt.UnixMilli(), beforeIncoming)
	}
	if got := taskUpdatedAt(t, store, outgoingNeighbor.ID); got != updatedAt.UnixMilli() || got == beforeOutgoing {
		t.Fatalf("outgoing survivor timestamp = %d, want one update at %d after %d", got, updatedAt.UnixMilli(), beforeOutgoing)
	}
}

var _ workflow.TaskID
