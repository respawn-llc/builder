package workflowstore

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
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
	if bound.Task.ManagedWorktreeID != worktreeID {
		t.Fatalf("bound managed Worktree = %q, want %q", bound.Task.ManagedWorktreeID, worktreeID)
	}
	if bound.Task.PendingInitialManagedBranchName != nil {
		t.Fatalf("bound pending initial managed branch = %q, want absent", *bound.Task.PendingInitialManagedBranchName)
	}
}

func TestPendingInitialManagedBranchReplacementRejectsIneligibleTasks(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)

	managedTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeID := "worktree-pending-branch-ineligible"
	if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID: worktreeID, WorkspaceID: binding.WorkspaceID, CanonicalRoot: t.TempDir(),
		Managed: true, CreatedBranch: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if updated, err := store.queries.BindInitialTaskManagedWorktree(ctx, sqlitegen.BindInitialTaskManagedWorktreeParams{
		ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
		TaskID:            string(managedTask.ID),
	}); err != nil || updated != 1 {
		t.Fatalf("BindInitialTaskManagedWorktree = %d, %v", updated, err)
	}
	if err := store.ReplacePendingInitialManagedBranchName(ctx, managedTask.ID, "feature/renamed"); !errors.Is(err, ErrPendingInitialManagedBranchUnavailable) {
		t.Fatalf("replacement after managed bind error = %v, want unavailable", err)
	}

	lockedTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	if err := store.LockTaskExecutionTarget(ctx, lockedTask.ID, &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}); err != nil {
		t.Fatalf("LockTaskExecutionTarget none: %v", err)
	}
	if err := store.ReplacePendingInitialManagedBranchName(ctx, lockedTask.ID, "feature/locked"); !errors.Is(err, ErrPendingInitialManagedBranchUnavailable) {
		t.Fatalf("replacement after target lock error = %v, want unavailable", err)
	}
	locked, err := store.GetTaskExecutionTargetContext(ctx, lockedTask.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext locked: %v", err)
	}
	if locked.Task.PendingInitialManagedBranchName != nil {
		t.Fatalf("locked-none pending initial managed branch = %q, want absent", *locked.Task.PendingInitialManagedBranchName)
	}
}

func TestReplacePendingInitialManagedBranchUsesLatestEligibleWrite(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	createLinkedValidWorkflow(t, ctx, store, binding.ProjectID)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	for _, branchName := range []string{"feature/first", "feature/latest"} {
		if err := store.ReplacePendingInitialManagedBranchName(ctx, task.ID, branchName); err != nil {
			t.Fatalf("ReplacePendingInitialManagedBranchName(%q): %v", branchName, err)
		}
	}

	context, err := store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if context.Task.PendingInitialManagedBranchName == nil {
		t.Fatal("replaced pending initial managed branch is absent")
	}
	if got := *context.Task.PendingInitialManagedBranchName; got != "feature/latest" {
		t.Fatalf("pending initial managed branch = %q, want latest replacement", got)
	}
}
