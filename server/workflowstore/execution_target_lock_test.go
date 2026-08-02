package workflowstore

import (
	"context"
	"errors"
	"testing"

	"core/server/metadata"
	"core/server/workflow"
)

func TestDurablePlacementLeavesExecutionTargetUnlockedUntilExplicitLock(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)

	started, err := store.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if len(started.Mutation.Created) != 1 || started.Mutation.Created[0].Scheduling == nil ||
		started.Mutation.Created[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("StartTask mutation = %+v, want ready Current Node", started.Mutation)
	}
	assertExecutionTargetUnlocked(t, ctx, store, task.ID)

	noneCandidate := &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}
	if _, err := store.LockTaskExecutionTarget(ctx, task.ID, noneCandidate); err != nil {
		t.Fatalf("LockTaskExecutionTarget none: %v", err)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext after none lock: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil || targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone {
		t.Fatalf("locked target = %+v, want none target", targetContext.Task.ExecutionTarget)
	}
	if _, err := store.LockTaskExecutionTarget(ctx, task.ID, noneCandidate); !errors.Is(err, ErrExecutionTargetAlreadyLocked) {
		t.Fatalf("replacement lock error = %v, want %v", err, ErrExecutionTargetAlreadyLocked)
	}
}

func TestExecutableManualMoveCanPlaceScriptBeforeExecutionRootValidation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createScriptStartWorkflow(t, ctx, store, "scripts/complete")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	script := nodeByKind(t, definition, workflow.NodeKindScript)
	prepared, err := store.PrepareManualMove(ctx, ManualMoveRequest{
		TaskID:       task.ID,
		TargetNodeID: workflow.NodeIDOf(script),
	})
	if err != nil {
		t.Fatalf("PrepareManualMove: %v", err)
	}
	moved, err := store.ApplyManualMove(ctx, prepared, nil)
	if err != nil {
		t.Fatalf("ApplyManualMove: %v", err)
	}
	if len(moved.Created) != 1 || moved.Created[0].Reference.NodeID != workflow.NodeIDOf(script) {
		t.Fatalf("manual move mutation = %+v, want script Current Node", moved)
	}
	assertExecutionTargetUnlocked(t, ctx, store, task.ID)
}

func TestManagedExecutionTargetLockBindsRegisteredWorktree(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	worktreeID := "registered-" + string(task.ID)
	worktreeRoot := t.TempDir()
	if err := store.metadata.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:            worktreeID,
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: worktreeRoot,
		Managed:       true,
		CreatedBranch: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}

	commitOID := "0123456789abcdef"
	requestedRef := "HEAD"
	registered, err := store.queries.GetWorktreeByID(ctx, worktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeByID: %v", err)
	}
	candidate := &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			CommitOID:    &commitOID,
			Provenance:   ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
			Managed:             &ManagedExecutionRoot{WorktreeID: worktreeID, Root: registered.CanonicalRootPath},
		},
	}
	if _, err := store.LockTaskExecutionTarget(ctx, task.ID, candidate); err != nil {
		t.Fatalf("LockTaskExecutionTarget managed: %v", err)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext after managed lock: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil ||
		targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeHead ||
		targetContext.Task.ManagedWorktreeID != worktreeID {
		t.Fatalf("managed locked target = %+v, want worktree %q", targetContext.Task, worktreeID)
	}
}

func TestExecutionTargetLockUsesCapturedWorkspaceAfterRebind(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	oldRoot := binding.CanonicalRoot
	newRoot := t.TempDir()
	if _, err := store.metadata.RebindWorkspace(ctx, oldRoot, newRoot); err != nil {
		t.Fatalf("RebindWorkspace: %v", err)
	}

	candidate := &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: oldRoot,
		},
	}
	root, err := store.LockTaskExecutionTarget(ctx, task.ID, candidate)
	if err != nil {
		t.Fatalf("LockTaskExecutionTarget after rebind: %v", err)
	}
	if root.SourceWorkspaceID != binding.WorkspaceID || root.SourceWorkspaceRoot != oldRoot {
		t.Fatalf("locked execution root = %+v, want captured workspace %q at %q", root, binding.WorkspaceID, oldRoot)
	}
}

func assertExecutionTargetUnlocked(t *testing.T, ctx context.Context, store *Store, taskID workflow.TaskID) {
	t.Helper()
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil || targetContext.Task.ManagedWorktreeID != "" {
		t.Fatalf("unlocked target facts = %+v, want absent", targetContext.Task)
	}
}
