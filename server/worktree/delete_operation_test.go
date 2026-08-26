package worktree

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/server/metadata"
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
	preview, err := env.service.PreviewWorktreeDelete(env.ctx, worktreecontract.DeletePreviewRequest{
		SessionID: env.session.Meta().SessionID,
		Selector:  created.WorktreeID,
	})
	if err != nil {
		t.Fatalf("PreviewWorktreeDelete: %v", err)
	}
	if preview.Cleanliness.Kind != worktreecontract.DirtyStateClean {
		t.Fatalf("preview cleanliness = %+v, want clean", preview.Cleanliness)
	}
	if err := os.WriteFile(filepath.Join(created.CanonicalRoot, "dirty-after-preview.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err = env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, preview.DeletionSelector))
	var precondition *worktreecontract.DeletePreconditionError
	if !errors.As(err, &precondition) ||
		precondition.DirtyState.Kind != worktreecontract.DirtyStateDirty ||
		precondition.DirtyState.DirtyFileCount == nil ||
		*precondition.DirtyState.DirtyFileCount != 1 {
		t.Fatalf("delete error = %v, want typed dirty precondition", err)
	}
}

func TestDeleteWorktreeRechecksUnknownStateAfterCleanPreview(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-preview-race-unknown")
	preview, err := env.service.PreviewWorktreeDelete(env.ctx, worktreecontract.DeletePreviewRequest{
		SessionID: env.session.Meta().SessionID,
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
		precondition.DirtyState.Kind != worktreecontract.DirtyStateUnknown ||
		precondition.DirtyState.UnknownCause == nil {
		t.Fatalf("delete error = %v, want typed unknown precondition", err)
	}
}

func TestDeleteWorktreeCompletesNonCurrentDeletionAndRetainsBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-completed")
	request := worktreeDeleteRequest(env, created.WorktreeID)
	request.Origin = &worktreecontract.RuntimeStepOrigin{
		RunID:  "018fdd67-89ab-4cde-8123-456789abc001",
		StepID: "018fdd67-89ab-4cde-8123-456789abc002",
	}
	result, err := env.service.DeleteWorktree(env.ctx, request)
	if err != nil || result.Kind != worktreecontract.DeleteResultKindCompleted {
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
	request.BranchCleanupPolicy = worktreecontract.BranchCleanupModeDeleteForce
	result, err := env.service.DeleteWorktree(env.ctx, request)
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if result.Kind != worktreecontract.DeleteResultKindCompleted ||
		result.Completed == nil ||
		result.Completed.Cleanup.Kind != worktreecontract.BranchCleanupOutcomeDeleted {
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

	preview, err := env.service.PreviewWorktreeDelete(env.ctx, worktreecontract.DeletePreviewRequest{
		SessionID: env.session.Meta().SessionID,
		Selector:  record.ID,
	})
	if err != nil {
		t.Fatalf("PreviewWorktreeDelete: %v", err)
	}
	if preview.Worktree.Variant != worktreecontract.TopologyVariantMissing ||
		preview.DeletionSelector != record.ID ||
		preview.Cleanliness.Kind != worktreecontract.DirtyStateClean {
		t.Fatalf("missing preview = %+v, want clean ID-bound preview", preview)
	}

	result, err := env.service.DeleteWorktree(env.ctx, worktreecontract.DeleteRequest{
		TransitionHeader: worktreecontract.TransitionHeader{
			OperationID: worktreecontract.NewOperationID(),
			SessionID:   env.session.Meta().SessionID,
		},
		Selector:            preview.DeletionSelector,
		BranchCleanupPolicy: worktreecontract.BranchCleanupModeRetain,
	})
	if err != nil {
		t.Fatalf("DeleteWorktree: %v", err)
	}
	if result.Kind != worktreecontract.DeleteResultKindCompleted ||
		result.Completed == nil ||
		result.Completed.LeftoverRoot == nil ||
		*result.Completed.LeftoverRoot != canonicalTestPath(t, missingRoot) {
		t.Fatalf("delete result = %+v, want completed leftover root", result)
	}
	if content, err := os.ReadFile(leftoverFile); err != nil || string(content) != "preserve" {
		t.Fatalf("leftover file content=%q err=%v, want preserved", content, err)
	}
	if _, err := env.store.GetWorktreeRecordByID(env.ctx, record.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing record = %v, want sql.ErrNoRows", err)
	}
}
