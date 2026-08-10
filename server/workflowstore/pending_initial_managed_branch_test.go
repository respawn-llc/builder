package workflowstore

import (
	"database/sql"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
)

func TestCreateTaskInitializesPendingManagedBranchFromShortID(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)

	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	if task.PendingInitialManagedBranchName == nil {
		t.Fatal("created task pending initial managed branch is absent")
	}
	if got := *task.PendingInitialManagedBranchName; got != task.ShortID {
		t.Fatalf("created task pending initial managed branch = %q, want short ID %q", got, task.ShortID)
	}
}

func TestTaskRecordProjectionRejectsPresentBlankManagedWorktreeID(t *testing.T) {
	_, err := taskRecordFromTask(sqlitegen.TaskRecord{
		ManagedWorktreeID: sql.NullString{Valid: true},
	})
	if err == nil {
		t.Fatal("taskRecordFromTask accepted a present blank managed Worktree ID")
	}
}

func TestBindInitialTaskManagedWorktreeClearsCurrentPendingBranch(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	if err := store.ReplacePendingInitialManagedBranchName(ctx, task.ID, "feature/snapshot"); err != nil {
		t.Fatalf("replace snapshot branch: %v", err)
	}
	snapshot, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext snapshot: %v", err)
	}
	if snapshot.Task.PendingInitialManagedBranchName == nil || *snapshot.Task.PendingInitialManagedBranchName != "feature/snapshot" {
		t.Fatalf("snapshot pending initial managed branch = %+v", snapshot.Task.PendingInitialManagedBranchName)
	}
	if err := store.ReplacePendingInitialManagedBranchName(ctx, task.ID, "feature/later"); err != nil {
		t.Fatalf("replace later branch: %v", err)
	}

	worktreeID := "worktree-pending-branch-bind"
	if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID: worktreeID, WorkspaceID: binding.WorkspaceID, CanonicalRoot: t.TempDir(),
		Managed: true, CreatedBranch: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	updated, err := store.queries.BindInitialTaskManagedWorktree(ctx, sqlitegen.BindInitialTaskManagedWorktreeParams{
		ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
		TaskID:            string(task.ID),
	})
	if err != nil {
		t.Fatalf("BindInitialTaskManagedWorktree: %v", err)
	}
	if updated != 1 {
		t.Fatalf("BindInitialTaskManagedWorktree updated %d rows, want 1", updated)
	}

	bound, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext bound: %v", err)
	}
	if bound.Task.ManagedWorktreeID == nil || *bound.Task.ManagedWorktreeID != worktreeID {
		t.Fatalf("bound managed Worktree = %v, want %q", bound.Task.ManagedWorktreeID, worktreeID)
	}
	if bound.Task.PendingInitialManagedBranchName != nil {
		t.Fatalf("bound pending initial managed branch = %q, want absent", *bound.Task.PendingInitialManagedBranchName)
	}
}
