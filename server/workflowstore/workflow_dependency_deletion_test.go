package workflowstore

import (
	"testing"
	"time"

	"core/server/workflow"
)

func TestDeleteWorkflowCleansCrossWorkflowDependenciesAndTouchesSurvivors(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	deletedWorkflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, deletedWorkflowID, false)
	survivingWorkflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, survivingWorkflowID, true)
	deletedTask := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &deletedWorkflowID, Title: "Deleted", Body: "Body"})
	survivingTask := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &survivingWorkflowID, Title: "Survivor", Body: "Body"})
	deletedDefinition, _, err := store.GetDefinition(ctx, deletedWorkflowID)
	if err != nil {
		t.Fatalf("GetDefinition deleted workflow: %v", err)
	}
	if _, err := manualMoveTaskForStoreTest(store, ctx, ManualMoveRequest{
		TaskID:       deletedTask.ID,
		TargetNodeID: workflow.NodeIDOf(nodeByKind(t, deletedDefinition, workflow.NodeKindTerminal)),
	}); err != nil {
		t.Fatalf("move deleted workflow task to terminal: %v", err)
	}
	if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: deletedTask.ID, BlockedTaskID: survivingTask.ID}); err != nil {
		t.Fatalf("add cross-workflow dependency: %v", err)
	}
	beforeSurvivor := taskUpdatedAt(t, store, survivingTask.ID)
	updatedAt := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	store.now = func() time.Time { return updatedAt }

	impact, err := store.PreviewWorkflowDelete(ctx, deletedWorkflowID)
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}
	deleted := confirmedWorkflowDeleteRequest(impact, false)
	if result, err := deleteWorkflowThroughLifecyclePublication(store, ctx, deleted); err != nil {
		t.Fatalf("DeleteWorkflow: %v", err)
	} else if !result.Deleted {
		t.Fatalf("DeleteWorkflow result = %+v, want deleted", result)
	}
	assertTaskDependencyCount(t, store, deletedTask.ID, survivingTask.ID, 0)
	if got := taskUpdatedAt(t, store, survivingTask.ID); got != updatedAt.UnixMilli() || got == beforeSurvivor {
		t.Fatalf("survivor timestamp = %d, want one update at %d after %d", got, updatedAt.UnixMilli(), beforeSurvivor)
	}
	if _, err := store.queries.GetTask(ctx, string(deletedTask.ID)); err == nil {
		t.Fatal("deleted workflow task still exists")
	}
	if _, err := store.queries.GetTask(ctx, string(survivingTask.ID)); err != nil {
		t.Fatalf("surviving task missing: %v", err)
	}
}

func TestDeleteProjectCascadesTaskDependenciesWithoutSurvivorTouch(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createValidWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	blocker := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocker", Body: "Body"})
	blocked := createTask(t, ctx, store, CreateTaskRequest{ProjectID: binding.ProjectID, WorkflowID: &workflowID, Title: "Blocked", Body: "Body"})
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	for _, task := range []TaskRecord{blocker, blocked} {
		if _, err := manualMoveTaskForStoreTest(store, ctx, ManualMoveRequest{
			TaskID:       task.ID,
			TargetNodeID: workflow.NodeIDOf(nodeByKind(t, definition, workflow.NodeKindTerminal)),
		}); err != nil {
			t.Fatalf("move task %q to terminal: %v", task.ID, err)
		}
	}
	if _, err := store.AddTaskDependency(ctx, TaskDependencyAddRequest{BlockerTaskID: blocker.ID, BlockedTaskID: blocked.ID}); err != nil {
		t.Fatalf("add project dependency: %v", err)
	}
	artifacts := projectDeleteArtifactsNoop{}
	if blockers, err := deleteProjectThroughLifecyclePublication(store, ctx, ProjectDeleteRequest{ProjectID: binding.ProjectID, Artifacts: artifacts}); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	} else if len(blockers) != 0 {
		t.Fatalf("DeleteProject blockers = %+v, want none", blockers)
	}
	var dependencyCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_dependencies`).Scan(&dependencyCount); err != nil {
		t.Fatalf("count dependencies after project delete: %v", err)
	}
	if dependencyCount != 0 {
		t.Fatalf("dependencies after project delete = %d, want 0", dependencyCount)
	}
}
