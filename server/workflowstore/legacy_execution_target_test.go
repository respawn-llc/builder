package workflowstore

import (
	"errors"
	"testing"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

func TestTaskExecutionTargetStateClassifiesTargetlessTasks(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)

	t.Run("legacy managed attachment", func(t *testing.T) {
		task := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowID,
			Title:      "Legacy managed",
			Body:       "Body",
		})
		const worktreeID = "legacy-managed-worktree"
		if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
			ID: worktreeID, WorkspaceID: binding.WorkspaceID, CanonicalRoot: t.TempDir(), DisplayName: "legacy", Availability: "available", Managed: true,
		}); err != nil {
			t.Fatalf("UpsertWorktreeRecord: %v", err)
		}
		if _, err := store.queries.UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
			ID: string(task.ID), ManagedWorktreeID: nullableString(worktreeID), UpdatedAtUnixMs: store.now().UnixMilli(),
		}); err != nil {
			t.Fatalf("UpdateTaskManagedWorktree: %v", err)
		}

		state, err := store.TaskExecutionTargetState(ctx, task.ID)
		if err != nil {
			t.Fatalf("TaskExecutionTargetState: %v", err)
		}
		if state.Kind != TaskExecutionTargetStateLegacyManaged {
			t.Fatalf("state = %+v, want legacy managed", state)
		}
	})

	t.Run("historical task with no attachment", func(t *testing.T) {
		task := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowID,
			Title:      "Legacy missing",
			Body:       "Body",
		})
		if _, err := store.StartTask(ctx, task.ID); err != nil {
			t.Fatalf("StartTask: %v", err)
		}

		state, err := store.TaskExecutionTargetState(ctx, task.ID)
		if err != nil {
			t.Fatalf("TaskExecutionTargetState: %v", err)
		}
		if state.Kind != TaskExecutionTargetStateLegacyMissing {
			t.Fatalf("state = %+v, want legacy missing", state)
		}
		if _, err := store.PrepareTaskStartExecutionTargetNegotiation(ctx, task.ID); !errors.Is(err, ErrLegacyTaskExecutionTargetMissing) {
			t.Fatalf("PrepareTaskStartExecutionTargetNegotiation error = %v, want legacy missing", err)
		}
	})

	t.Run("missing historical attachment", func(t *testing.T) {
		task := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowID,
			Title:      "Missing legacy metadata",
			Body:       "Body",
		})
		const worktreeID = "missing-legacy-worktree"
		if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
			ID: worktreeID, WorkspaceID: binding.WorkspaceID, CanonicalRoot: t.TempDir(), DisplayName: "missing", Availability: "available", Managed: true,
		}); err != nil {
			t.Fatalf("UpsertWorktreeRecord: %v", err)
		}
		if _, err := store.queries.UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
			ID: string(task.ID), ManagedWorktreeID: nullableString(worktreeID), UpdatedAtUnixMs: store.now().UnixMilli(),
		}); err != nil {
			t.Fatalf("UpdateTaskManagedWorktree: %v", err)
		}
		if _, err := store.StartTask(ctx, task.ID); err != nil {
			t.Fatalf("StartTask: %v", err)
		}
		if err := store.metadata.DeleteWorktreeRecordByID(ctx, worktreeID); err != nil {
			t.Fatalf("DeleteWorktreeRecordByID: %v", err)
		}

		state, err := store.TaskExecutionTargetState(ctx, task.ID)
		if err != nil {
			t.Fatalf("TaskExecutionTargetState: %v", err)
		}
		if state.Kind != TaskExecutionTargetStateLegacyMissing {
			t.Fatalf("state = %+v, want legacy missing", state)
		}
	})

	t.Run("unstarted task", func(t *testing.T) {
		task := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowID,
			Title:      "Unstarted",
			Body:       "Body",
		})

		state, err := store.TaskExecutionTargetState(ctx, task.ID)
		if err != nil {
			t.Fatalf("TaskExecutionTargetState: %v", err)
		}
		if state.Kind != TaskExecutionTargetStateUnstarted {
			t.Fatalf("state = %+v, want unstarted", state)
		}
	})

	t.Run("zero-run pending approval", func(t *testing.T) {
		task := createTask(t, ctx, store, CreateTaskRequest{
			ProjectID:  binding.ProjectID,
			WorkflowID: workflowID,
			Title:      "Unstarted approval",
			Body:       "Body",
		})
		definition, _, err := store.GetDefinition(ctx, workflowID)
		if err != nil {
			t.Fatalf("GetDefinition: %v", err)
		}
		agent := nodeByKey(t, definition, "agent")
		if _, err := store.ManualMoveTask(ctx, ManualMoveRequest{
			TaskID: task.ID, TargetNodeID: workflow.NodeIDOf(agent), AllowMissingEdge: true,
		}); err != nil {
			t.Fatalf("ManualMoveTask: %v", err)
		}

		state, err := store.TaskExecutionTargetState(ctx, task.ID)
		if err != nil {
			t.Fatalf("TaskExecutionTargetState: %v", err)
		}
		if state.Kind != TaskExecutionTargetStateUnstarted {
			t.Fatalf("state = %+v, want unstarted zero-run pending approval", state)
		}
	})
}
