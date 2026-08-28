package worktree

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"core/server/runtime"
	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type transitionRuntimeLifecycle struct {
	ready chan *runtime.Engine
}

func (l transitionRuntimeLifecycle) ResourceReady(
	_ context.Context,
	_ sessionruntime.AgentResourceDescriptor,
	engine *runtime.Engine,
	_ sessionruntime.AgentResourceRetainer,
) error {
	l.ready <- engine
	return nil
}

func (transitionRuntimeLifecycle) ResourceDraining(context.Context, sessionruntime.AgentResourceDescriptor) error {
	return nil
}

func TestWorktreeTransitionUsesActiveRuntimeReservationAndDormantDirectExecution(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		active bool
	}{
		{name: "active Runtime defers selector resolution", active: true},
		{name: "dormant Session executes directly", active: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var lifecycle *transitionRuntimeLifecycle
			var env *serviceTestEnv
			if testCase.active {
				lifecycle = &transitionRuntimeLifecycle{ready: make(chan *runtime.Engine, 1)}
				env = newServiceTestEnvWithResourceLifecycle(t, lifecycle)
			} else {
				env = newServiceTestEnv(t)
			}
			operationID := serverapi.NewWorktreeOperationID()
			request := serverapi.WorktreeEnterRequest{
				WorktreeTransitionHeader: serverapi.WorktreeTransitionHeader{
					OperationID: operationID,
					SessionID:   env.session.Meta().SessionID,
				},
				Selector: "missing-worktree",
			}

			var releaseRuntime func()
			if testCase.active {
				plan := deleteActivityTestRuntimePlan(t, env, env.workspaceRoot)
				attachment, err := env.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
					SessionID: openDeleteActivitySessionDescriptor(t, request.SessionID).SessionID(),
					OwnerID:   "worktree-transition-test",
					Runtime:   &plan,
				})
				if err != nil {
					t.Fatalf("OpenRuntime: %v", err)
				}
				releaseRuntime = func() {
					if _, err := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose); err != nil {
						t.Errorf("release Runtime: %v", err)
					}
				}
				defer releaseRuntime()

				engine := <-lifecycle.ready
				started := make(chan struct{})
				release := make(chan struct{})
				done := make(chan error, 1)
				go func() {
					done <- engine.RunWhenIdle(context.Background(), runtime.ActiveKindRuntimeMaintenance, func() error {
						close(started)
						<-release
						return nil
					})
				}()
				<-started

				ack, err := env.service.EnterWorktree(env.ctx, request)
				if err != nil {
					t.Fatalf("EnterWorktree: %v", err)
				}
				if ack.OperationID != operationID {
					t.Fatalf("acknowledgement ID = %s, want %s", ack.OperationID, operationID)
				}
				pending, err := engine.PendingWorkSnapshot()
				if err != nil {
					t.Fatalf("PendingWorkSnapshot: %v", err)
				}
				if len(pending.Items) != 1 || pending.Items[0].ID.String() != operationID.String() ||
					pending.Items[0].Kind != runtimeinput.PendingWorkItemKindWorktreeTransition {
					t.Fatalf("active Pending Work = %+v", pending.Items)
				}
				env.publisher.mu.Lock()
				outcomeCount := len(env.publisher.outcomes)
				env.publisher.mu.Unlock()
				if outcomeCount != 0 {
					t.Fatalf("active Worktree outcomes before boundary = %d", outcomeCount)
				}

				close(release)
				if err := <-done; err != nil {
					t.Fatalf("release Runtime maintenance: %v", err)
				}
			} else {
				ack, err := env.service.EnterWorktree(env.ctx, request)
				if err != nil {
					t.Fatalf("EnterWorktree: %v", err)
				}
				if ack.OperationID != operationID {
					t.Fatalf("acknowledgement ID = %s, want %s", ack.OperationID, operationID)
				}
				env.publisher.mu.Lock()
				outcomeCount := len(env.publisher.outcomes)
				env.publisher.mu.Unlock()
				if outcomeCount != 1 {
					t.Fatalf("dormant Worktree outcomes before response = %d, want 1", outcomeCount)
				}
			}

			deadline := time.Now().Add(3 * time.Second)
			for {
				env.publisher.mu.Lock()
				outcomes := append([]clientui.WorktreeTransitionOutcome(nil), env.publisher.outcomes...)
				env.publisher.mu.Unlock()
				if len(outcomes) != 0 {
					if len(outcomes) != 1 || outcomes[0].OperationID != operationID ||
						outcomes[0].State != clientui.WorktreeTransitionFailed {
						t.Fatalf("Worktree outcomes = %+v", outcomes)
					}
					if outcomes[0].Failure == nil {
						t.Fatal("Worktree selector failure is absent")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("timed out waiting for Worktree transition outcome")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestApplyWorktreeTargetMutationCertainty(t *testing.T) {
	writeErr := errors.New("write failed")
	finishErr := errors.New("finish failed")
	rollbackErr := errors.New("rollback failed")

	for _, testCase := range []struct {
		name     string
		write    error
		finish   runtime.WorktreeApplicationResult
		rollback error
		want     runtime.WorktreeApplicationCertainty
		wantErrs []error
	}{
		{
			name:     "pre-write failure is unapplied",
			write:    writeErr,
			want:     runtime.WorktreeApplicationUnapplied,
			wantErrs: []error{writeErr},
		},
		{
			name:   "completed finish is committed",
			finish: runtime.CommittedWorktreeApplication(nil),
			want:   runtime.WorktreeApplicationCommitted,
		},
		{
			name:     "successful rollback is unapplied",
			finish:   runtime.UnappliedWorktreeApplication(finishErr),
			want:     runtime.WorktreeApplicationUnapplied,
			wantErrs: []error{finishErr},
		},
		{
			name:     "failed rollback is indeterminate",
			finish:   runtime.UnappliedWorktreeApplication(finishErr),
			rollback: rollbackErr,
			want:     runtime.WorktreeApplicationIndeterminate,
			wantErrs: []error{finishErr, rollbackErr},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			finishCalled := false
			rollbackCalled := false
			_, result := applyWorktreeTargetMutation(
				func() error { return testCase.write },
				func() (struct{}, runtime.WorktreeApplicationResult) {
					finishCalled = true
					return struct{}{}, testCase.finish
				},
				func() runtime.WorktreeApplicationResult {
					rollbackCalled = true
					if testCase.rollback != nil {
						return runtime.UnappliedWorktreeApplication(testCase.rollback)
					}
					return runtime.CommittedWorktreeApplication(nil)
				},
			)
			if result.Certainty != testCase.want {
				t.Fatalf("certainty = %v, want %v", result.Certainty, testCase.want)
			}
			for _, wantErr := range testCase.wantErrs {
				if !errors.Is(result.Err, wantErr) {
					t.Fatalf("result error = %v, want %v", result.Err, wantErr)
				}
			}
			if testCase.write != nil && (finishCalled || rollbackCalled) {
				t.Fatalf("pre-write failure called finish=%t rollback=%t", finishCalled, rollbackCalled)
			}
			if testCase.finish.Certainty == runtime.WorktreeApplicationCommitted && rollbackCalled {
				t.Fatal("committed target mutation rolled back")
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
