package worktree

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/shared/serverapi"
)

func TestDeleteWorktreeRequiresExplicitForceForDirtyTarget(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-precondition")
	if err := os.WriteFile(filepath.Join(created.CanonicalRoot, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := env.service.DeleteWorktree(env.ctx, worktreeDeleteRequest(env, created.WorktreeID))
	var precondition *serverapi.WorktreeDeletePreconditionError
	if !errors.As(err, &precondition) {
		t.Fatalf("delete error = %v, want dirty precondition", err)
	}
}

func TestDeleteWorktreeCompletesNonCurrentDeletionAndRetainsBranch(t *testing.T) {
	env := newServiceTestEnv(t)
	created := mustCreateWorktree(t, env, "feature/delete-completed")
	request := worktreeDeleteRequest(env, created.WorktreeID)
	request.Origin = &serverapi.RuntimeStepOrigin{
		RunID:  "018fdd67-89ab-4cde-8123-456789abc001",
		StepID: "018fdd67-89ab-4cde-8123-456789abc002",
	}
	result, err := env.service.DeleteWorktree(env.ctx, request)
	if err != nil || result.Kind != serverapi.WorktreeDeleteResultKindCompleted {
		t.Fatalf("DeleteWorktree = %+v, %v", result, err)
	}
	if _, err := os.Stat(created.CanonicalRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree root still exists: %v", err)
	}
	if exists, err := env.service.git.BranchExists(env.ctx, env.workspaceRoot, created.BranchName); err != nil || !exists {
		t.Fatalf("retained branch exists=%v err=%v", exists, err)
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
