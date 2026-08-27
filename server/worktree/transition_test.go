package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/worktreecontract"
)

func TestEnterWorktreeRejectsInvalidSelectorsBeforeScheduling(t *testing.T) {
	env := newServiceTestEnv(t)
	validRoot := createExternalWorktree(t, env, "feature/valid-after-invalid")
	ambiguousRoot := filepath.Join(t.TempDir(), filepath.Base(validRoot))
	runGit(t, env.workspaceRoot, "worktree", "add", "-b", "feature/ambiguous-enter", ambiguousRoot, "HEAD")
	t.Cleanup(func() { runGit(t, env.workspaceRoot, "worktree", "remove", "--force", ambiguousRoot) })

	for _, testCase := range []struct {
		selector string
		kind     worktreepb.SelectorErrorKind
	}{
		{selector: "missing-worktree", kind: worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_NOT_FOUND},
		{selector: filepath.Base(validRoot), kind: worktreepb.SelectorErrorKind_WORKTREE_SELECTOR_ERROR_KIND_AMBIGUOUS},
	} {
		_, err := env.service.EnterWorktree(env.ctx, &worktreepb.EnterRequest{
			OperationId: clientui.NewWorktreeTransitionID().String(),
			SessionId:   env.session.Meta().SessionID,
			Selector:    testCase.selector,
		})
		var selectorErr *worktreecontract.SelectorError
		if !errors.As(err, &selectorErr) || selectorErr.Details.Kind != testCase.kind {
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
