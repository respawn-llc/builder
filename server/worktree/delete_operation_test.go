package worktree

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/server/metadata"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
)

func TestDeleteWorktreeRequiresExplicitForceForDirtyTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-precondition")
	if err := os.WriteFile(filepath.Join(created.CanonicalRoot, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, created.WorktreeID))
	var precondition *worktreecontract.DeletePreconditionError
	if !errors.As(err, &precondition) {
		t.Fatalf("delete error = %v, want dirty precondition", err)
	}
}

func TestDeleteWorktreeRechecksDirtyStateAfterCleanPreview(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-preview-race-dirty")
	preview, err := env.service.PreviewWorktreeDelete(env.ctx, &worktreepb.DeletePreviewRequest{
		SessionId: env.session.Meta().SessionID,
		Selector:  created.WorktreeID,
	})
	if err != nil {
		t.Fatalf("PreviewWorktreeDelete: %v", err)
	}
	if preview.Cleanliness.Kind != worktreepb.DirtyStateKind_DIRTY_STATE_CLEAN {
		t.Fatalf("preview cleanliness = %+v, want clean", preview.Cleanliness)
	}
	if err := os.WriteFile(filepath.Join(created.CanonicalRoot, "dirty-after-preview.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err = env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, preview.DeletionSelector))
	var precondition *worktreecontract.DeletePreconditionError
	if !errors.As(err, &precondition) ||
		precondition.Details.DirtyState.Kind != worktreepb.DirtyStateKind_DIRTY_STATE_DIRTY ||
		precondition.Details.DirtyState.DirtyFileCount == nil ||
		*precondition.Details.DirtyState.DirtyFileCount != 1 {
		t.Fatalf("delete error = %v, want typed dirty precondition", err)
	}
}

func TestDeleteWorktreeRechecksUnknownStateAfterCleanPreview(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-preview-race-unknown")
	preview, err := env.service.PreviewWorktreeDelete(env.ctx, &worktreepb.DeletePreviewRequest{
		SessionId: env.session.Meta().SessionID,
		Selector:  created.WorktreeID,
	})
	if err != nil {
		t.Fatalf("PreviewWorktreeDelete: %v", err)
	}
	listOutput := []byte(runGit(t, env.workspaceRoot, "worktree", "list", "--porcelain"))
	env.service.git = NewGitInspector(&previewStatusRunner{
		listOutput: listOutput,
		outputErr:  errors.New("status inspection failed after preview"),
	})

	_, err = env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, preview.DeletionSelector))
	var precondition *worktreecontract.DeletePreconditionError
	if !errors.As(err, &precondition) ||
		precondition.Details.DirtyState.Kind != worktreepb.DirtyStateKind_DIRTY_STATE_UNKNOWN ||
		precondition.Details.DirtyState.UnknownCause == nil {
		t.Fatalf("delete error = %v, want typed unknown precondition", err)
	}
}

func TestDeleteWorktreeCompletesNonCurrentDeletionAndRetainsBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-completed")
	request := worktreeDeleteRequest(env, created.WorktreeID)
	result, err := env.service.DeleteWorktree(env.ctx, request)
	if err != nil {
		t.Fatalf("DeleteWorktree = %+v, %v", result, err)
	}
	if _, err := os.Stat(created.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree root still exists: %v", err)
	}
	if exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, created.BranchName); err != nil || !exists {
		t.Fatalf("retained branch exists=%v err=%v", exists, err)
	}
}

func TestDeleteWorktreeForceDeletesUnmergedBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-force-branch")
	if err := os.WriteFile(filepath.Join(created.CanonicalRoot, "unmerged.txt"), []byte("unmerged"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	runGit(t, created.CanonicalRoot, "add", "unmerged.txt")
	runGit(t, created.CanonicalRoot, "commit", "-m", "unmerged branch change")

	request := worktreeDeleteRequest(env, created.WorktreeID)
	request.BranchCleanupPolicy = worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_DELETE_FORCE
	result, err := env.service.DeleteWorktree(env.ctx, request)
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if result.Cleanup.Kind != worktreepb.BranchCleanupOutcomeKind_WORKTREE_BRANCH_CLEANUP_OUTCOME_DELETED {
		t.Fatalf("DeleteWorktree result = %+v, want deleted branch", result)
	}
	if exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, created.BranchName); err != nil || exists {
		t.Fatalf("force-deleted branch exists=%v err=%v", exists, err)
	}
}

func TestDeleteWorktreeRemovesMetadataForPrunableRegistration(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-prunable-registration")
	if err := os.Remove(filepath.Join(created.CanonicalRoot, ".git")); err != nil {
		t.Fatal(err)
	}
	request := worktreeDeleteRequest(env, created.WorktreeID)
	request.ForceFolderRemoval = true
	if _, err := env.service.DeleteWorktree(env.ctx, request); err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, created.WorktreeID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("worktree record = %v, want sql.ErrNoRows", err)
	}
}

func TestMissingWorktreeDeletePreviewIsCleanAndPreservesLeftoverRoot(t *testing.T) {
	env := newServiceTestEnv(t)
	missingRoot := filepath.Join(t.TempDir(), "missing-root")
	if err := os.MkdirAll(missingRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll missing root: %v", err)
	}
	leftoverFile := filepath.Join(missingRoot, "leftover.txt")
	if err := os.WriteFile(leftoverFile, []byte("preserve"), 0o644); err != nil {
		t.Fatalf("WriteFile leftover: %v", err)
	}
	record := metadata.WorktreeRecord{
		ID:            "missing-preview-delete",
		WorkspaceID:   env.binding.WorkspaceID,
		CanonicalRoot: missingRoot,
		DisplayName:   "missing-preview-delete",
	}
	if err := env.store.UpsertWorktreeRecord(env.ctx, record); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}

	preview, err := env.service.PreviewWorktreeDelete(env.ctx, &worktreepb.DeletePreviewRequest{
		SessionId: env.session.Meta().SessionID,
		Selector:  record.ID,
	})
	if err != nil {
		t.Fatalf("PreviewWorktreeDelete: %v", err)
	}
	if preview.Worktree.GetMissing() == nil ||
		preview.DeletionSelector != record.ID ||
		preview.Cleanliness.Kind != worktreepb.DirtyStateKind_DIRTY_STATE_CLEAN {
		t.Fatalf("missing preview = %+v, want clean ID-bound preview", preview)
	}

	result, err := env.service.DeleteWorktree(env.ctx, &worktreepb.DeleteRequest{
		SessionId:           env.session.Meta().SessionID,
		Selector:            preview.DeletionSelector,
		BranchCleanupPolicy: worktreepb.BranchCleanupMode_WORKTREE_BRANCH_CLEANUP_MODE_RETAIN,
	})
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if result.LeftoverRoot == nil ||
		*result.LeftoverRoot != canonicalTestPath(t, missingRoot) {
		t.Fatalf("delete result = %+v, want completed leftover root", result)
	}
	if content, err := os.ReadFile(leftoverFile); err != nil || string(content) != "preserve" {
		t.Fatalf("leftover file content=%q err=%v, want preserved", content, err)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, record.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing record = %v, want sql.ErrNoRows", err)
	}
}
