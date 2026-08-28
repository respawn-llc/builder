package worktree

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
)

func TestEnterWorktreeDefersSelectorValidationUntilExecution(t *testing.T) {
	env := newServiceTestEnv(t)
	validRoot := createExternalWorktree(t, env, "feature/valid-after-invalid")
	ambiguousRoot := filepath.Join(t.TempDir(), filepath.Base(validRoot))
	runGit(t, env.workspaceRoot, "worktree", "add", "-b", "feature/ambiguous-enter", ambiguousRoot, "HEAD")
	t.Cleanup(func() { runGit(t, env.workspaceRoot, "worktree", "remove", "--force", ambiguousRoot) })

	for _, testCase := range []struct {
		selector string
	}{
		{selector: "missing-worktree"},
		{selector: filepath.Base(validRoot)},
	} {
		operationID := clientui.NewWorktreeTransitionID()
		ack, err := env.service.EnterWorktree(env.ctx, &worktreepb.EnterRequest{
			OperationId: operationID.String(),
			SessionId:   env.session.Meta().SessionID,
			Selector:    testCase.selector,
		})
		if err != nil || ack.GetOperationId() != operationID.String() {
			t.Fatalf("selector %q acknowledgement = %+v, %v", testCase.selector, ack, err)
		}
		select {
		case <-env.publisher.ready:
		case <-time.After(time.Second):
			t.Fatalf("selector %q did not publish terminal outcome", testCase.selector)
		}
		env.publisher.mu.Lock()
		outcome := env.publisher.outcomes[len(env.publisher.outcomes)-1]
		env.publisher.mu.Unlock()
		if outcome.OperationID != operationID || outcome.State != clientui.WorktreeTransitionFailed {
			t.Fatalf("selector %q outcome = %+v", testCase.selector, outcome)
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
