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

func TestModelStepEnterRejectsInactiveExactExecution(t *testing.T) {
	env := newServiceTestEnv(t)
	createExternalWorktree(t, env, "feature/model-step-enter")
	ack, err := env.service.EnterWorktree(env.ctx, serverapi.WorktreeEnterRequest{
		WorktreeTransitionHeader: serverapi.WorktreeTransitionHeader{
			OperationID: serverapi.NewWorktreeOperationID(),
			SessionID:   env.session.Meta().SessionID,
			Origin: &serverapi.RuntimeStepOrigin{
				RunID:  "018fdd67-89ab-4cde-8123-456789abc001",
				StepID: "018fdd67-89ab-4cde-8123-456789abc002",
			},
		},
		Selector: "feature/model-step-enter",
	})
	var immediate *serverapi.WorktreeImmediateTransitionError
	if !errors.As(err, &immediate) || immediate.Kind != serverapi.WorktreeImmediateTransitionOriginInactive ||
		ack != (serverapi.WorktreeScheduledAcknowledgement{}) {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
}

func TestModelStepLeaveAndCurrentDeleteRejectInactiveExactExecution(t *testing.T) {
	origin := &serverapi.RuntimeStepOrigin{
		RunID:  "018fdd67-89ab-4cde-8123-456789abc001",
		StepID: "018fdd67-89ab-4cde-8123-456789abc002",
	}
	for _, operation := range []struct {
		name string
		run  func(*serviceTestEnv) error
	}{
		{
			name: "leave",
			run: func(env *serviceTestEnv) error {
				_, err := env.service.LeaveWorktree(env.ctx, serverapi.WorktreeLeaveRequest{
					WorktreeTransitionHeader: serverapi.WorktreeTransitionHeader{
						OperationID: serverapi.NewWorktreeOperationID(),
						SessionID:   env.session.Meta().SessionID,
						Origin:      origin,
					},
				})
				return err
			},
		},
		{
			name: "delete_current",
			run: func(env *serviceTestEnv) error {
				created := mustCreateWorktree(t, env, "feature/model-step-delete")
				updateServiceTestSessionTarget(t, env, env.session.Meta().SessionID, env.binding.WorkspaceID, created.WorktreeID, ".")
				_, err := env.service.DeleteWorktree(env.ctx, serverapi.WorktreeDeleteRequest{
					WorktreeTransitionHeader: serverapi.WorktreeTransitionHeader{
						OperationID: serverapi.NewWorktreeOperationID(),
						SessionID:   env.session.Meta().SessionID,
						Origin:      origin,
					},
					Selector:            created.WorktreeID,
					BranchCleanupPolicy: serverapi.WorktreeBranchCleanupModeRetain,
				})
				return err
			},
		},
	} {
		t.Run(operation.name, func(t *testing.T) {
			err := operation.run(newServiceTestEnv(t))
			var immediate *serverapi.WorktreeImmediateTransitionError
			if !errors.As(err, &immediate) || immediate.Kind != serverapi.WorktreeImmediateTransitionOriginInactive {
				t.Fatalf("error=%v, want inactive model-step origin", err)
			}
		})
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
