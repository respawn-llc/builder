package workflowstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/server/metadata"
	"core/server/workflow"
	"core/shared/config"
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
	worktreeRoot := filepath.Join(binding.CanonicalRoot, "managed-worktree")
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll worktree root: %v", err)
	}
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
	newRoot := t.TempDir()
	newRootCanonical, err := config.CanonicalWorkspaceRoot(newRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if _, err := store.metadata.RebindWorkspace(ctx, binding.CanonicalRoot, newRoot); err != nil {
		t.Fatalf("RebindWorkspace after managed lock: %v", err)
	}
	targetContext, err = store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext after locked rebind: %v", err)
	}
	if targetContext.SourceWorkspaceID != binding.WorkspaceID || targetContext.SourceWorkspaceRoot != newRootCanonical {
		t.Fatalf("locked source workspace = %q at %q, want rebound workspace %q at %q",
			targetContext.SourceWorkspaceID, targetContext.SourceWorkspaceRoot, binding.WorkspaceID, newRootCanonical)
	}
	row, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask after locked rebind: %v", err)
	}
	root, err := executionRootForTask(ctx, store.queries, row)
	if err != nil {
		t.Fatalf("executionRootForTask after locked rebind: %v", err)
	}
	if root.SourceWorkspaceRoot != newRootCanonical {
		t.Fatalf("execution root source workspace root = %q, want %q", root.SourceWorkspaceRoot, newRootCanonical)
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

func TestManagedTargetLockUsesCapturedWorkspaceAfterSourceRebind(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createChainedContextModeWorkflow(t, ctx, store, workflow.ContextModeNewSession, "coder")
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	oldRoot := binding.CanonicalRoot
	newBinding, err := store.metadata.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	worktreeID := "registered-rebind-" + string(task.ID)
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
	worktreeRecord, err := store.queries.GetWorktreeByID(ctx, worktreeID)
	if err != nil {
		t.Fatalf("GetWorktreeByID: %v", err)
	}
	commitOID := "0123456789abcdef"
	requestedRef := "HEAD"
	candidate := &ExecutionTargetCandidate{
		Snapshot: ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			CommitOID:    &commitOID,
			Provenance:   ExecutionTargetProvenanceResolved,
		},
		Root: ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: oldRoot,
			Managed:             &ManagedExecutionRoot{WorktreeID: worktreeID, Root: worktreeRecord.CanonicalRootPath},
		},
	}
	title := "edited after preparation"
	body := "body edited after preparation"
	if _, err := store.UpdateTask(ctx, UpdateTaskRequest{
		TaskID:            task.ID,
		Title:             &title,
		Body:              &body,
		SourceWorkspaceID: newBinding.WorkspaceID,
	}); err != nil {
		t.Fatalf("UpdateTask after preparation: %v", err)
	}
	if _, err := store.LockTaskExecutionTarget(ctx, task.ID, candidate); err != nil {
		t.Fatalf("LockTaskExecutionTarget after source workspace rebind: %v", err)
	}
	row, err := store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask after target lock: %v", err)
	}
	if row.Title != title || row.Body != body || row.SourceWorkspaceID.String != newBinding.WorkspaceID {
		t.Fatalf("task after target lock = title %q, body %q, workspace %q; want edited fields and workspace %q",
			row.Title, row.Body, row.SourceWorkspaceID.String, newBinding.WorkspaceID)
	}
	var metadataSnapshot struct {
		SourceWorkspaceSnapshot struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"source_workspace_snapshot"`
	}
	if err := json.Unmarshal([]byte(row.MetadataJson), &metadataSnapshot); err != nil {
		t.Fatalf("unmarshal task metadata after target lock: %v", err)
	}
	if metadataSnapshot.SourceWorkspaceSnapshot.WorkspaceID != newBinding.WorkspaceID {
		t.Fatalf("task metadata workspace snapshot = %q, want %q",
			metadataSnapshot.SourceWorkspaceSnapshot.WorkspaceID, newBinding.WorkspaceID)
	}
	targetContext, err := store.GetTaskExecutionTargetContext(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext after rebind: %v", err)
	}
	if targetContext.SourceWorkspaceID != binding.WorkspaceID {
		t.Fatalf("locked source workspace = %q, want captured workspace %q", targetContext.SourceWorkspaceID, binding.WorkspaceID)
	}
	editedAfterLockTitle := "edited after lock"
	editedAfterLockBody := "body edited after lock"
	if _, err := store.UpdateTask(ctx, UpdateTaskRequest{
		TaskID: task.ID,
		Title:  &editedAfterLockTitle,
		Body:   &editedAfterLockBody,
	}); err != nil {
		t.Fatalf("UpdateTask after target lock: %v", err)
	}
	row, err = store.queries.GetTask(ctx, string(task.ID))
	if err != nil {
		t.Fatalf("GetTask after ordinary edit: %v", err)
	}
	if row.Title != editedAfterLockTitle || row.Body != editedAfterLockBody ||
		row.SourceWorkspaceID.String != newBinding.WorkspaceID {
		t.Fatalf("task after ordinary edit = title %q, body %q, workspace %q; want edited fields and workspace %q",
			row.Title, row.Body, row.SourceWorkspaceID.String, newBinding.WorkspaceID)
	}
	laterTask := createDefaultTask(t, ctx, store, binding.ProjectID)
	if _, err := store.UpdateTask(ctx, UpdateTaskRequest{
		TaskID:            laterTask.ID,
		SourceWorkspaceID: newBinding.WorkspaceID,
	}); err != nil {
		t.Fatalf("UpdateTask later attempt source workspace: %v", err)
	}
	laterContext, err := store.GetTaskExecutionTargetContext(ctx, laterTask.ID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext later attempt: %v", err)
	}
	if laterContext.SourceWorkspaceID != newBinding.WorkspaceID {
		t.Fatalf("later unlocked source workspace = %q, want rebound workspace %q", laterContext.SourceWorkspaceID, newBinding.WorkspaceID)
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
