package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/shared/serverapi"
)

func TestEnterWorktreeRejectsInvalidSelectorsBeforeScheduling(t *testing.T) {
	env := newServiceTestEnv(t)
	validRoot := createExternalWorktree(t, env, "feature/valid-after-invalid")
	ambiguousRoot := filepath.Join(t.TempDir(), filepath.Base(validRoot))
	runGit(t, env.workspaceRoot, "worktree", "add", "-b", "feature/ambiguous-enter", ambiguousRoot, "HEAD")
	t.Cleanup(func() { runGit(t, env.workspaceRoot, "worktree", "remove", "--force", ambiguousRoot) })

	for _, testCase := range []struct {
		selector string
		kind     serverapi.WorktreeSelectorErrorKind
	}{
		{selector: "missing-worktree", kind: serverapi.WorktreeSelectorErrorKindNotFound},
		{selector: filepath.Base(validRoot), kind: serverapi.WorktreeSelectorErrorKindAmbiguous},
	} {
		_, err := env.service.EnterWorktree(env.ctx, serverapi.WorktreeEnterRequest{
			WorktreeTransitionHeader: serverapi.WorktreeTransitionHeader{
				OperationID: serverapi.NewWorktreeOperationID(),
				SessionID:   env.session.Meta().SessionID,
			},
			Selector: testCase.selector,
		})
		var selectorErr *serverapi.WorktreeSelectorError
		if !errors.As(err, &selectorErr) || selectorErr.Kind != testCase.kind {
			t.Fatalf("selector %q error = %v, want %s", testCase.selector, err, testCase.kind)
		}
	}
}

func createExternalWorktree(t *testing.T, env *serviceTestEnv, branch string) string {
	t.Helper()
	root := env.baseDir + "-external"
	runGit(t, env.workspaceRoot, "worktree", "add", "-b", branch, root, "HEAD")
	t.Cleanup(func() {
		if _, err := os.Stat(root); err == nil {
			runGit(t, env.workspaceRoot, "worktree", "remove", "--force", root)
		}
	})
	return root
}
