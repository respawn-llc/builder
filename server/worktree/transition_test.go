package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/runtime"
	"core/server/sessionruntime"
	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
)

type terminalFailureKind uint8

const (
	terminalFailurePreWrite terminalFailureKind = iota + 1
	terminalFailureSuccessfulRollback
	terminalFailureSelector
	terminalFailurePublication
	terminalFailureRollback
)

func TestEnterWorktreeDefersSelectorValidationUntilExecution(t *testing.T) {
	env := newServiceTestEnv(t)
	validRoot := createExternalWorktree(t, env, "feature/valid-after-invalid")
	ambiguousRoot := filepath.Join(t.TempDir(), filepath.Base(validRoot))
	runGit(t, env.workspaceRoot, "worktree", "add", "-b", "feature/ambiguous-enter", ambiguousRoot, "HEAD")
	t.Cleanup(func() { runGit(t, env.workspaceRoot, "worktree", "remove", "--force", ambiguousRoot) })
	for _, selector := range []string{"missing-worktree", filepath.Base(validRoot)} {
		operationID := clientui.NewWorktreeTransitionID()
		ack, err := env.service.EnterWorktree(env.ctx, &worktreepb.EnterRequest{
			OperationId: operationID.String(),
			SessionId:   env.session.Meta().SessionID,
			Selector:    selector,
		})
		if err != nil || ack.GetOperationId() != operationID.String() {
			t.Fatalf("selector %q acknowledgement = %+v, %v", selector, ack, err)
		}
		env.publisher.mu.Lock()
		outcome := env.publisher.outcomes[len(env.publisher.outcomes)-1]
		env.publisher.mu.Unlock()
		if outcome.OperationID != operationID || outcome.State != clientui.WorktreeTransitionFailed {
			t.Fatalf("selector %q outcome = %+v", selector, outcome)
		}
	}
}
func TestWorktreeTransitionTerminalCases(t *testing.T) {
	rollbackFailure := errors.New("rollback target")
	failed, completed := clientui.WorktreeTransitionFailed, clientui.WorktreeTransitionCompleted
	tests := []struct {
		name              string
		kind              terminalFailureKind
		active            bool
		outcome           *clientui.WorktreeTransitionState
		restore           bool
		applied           bool
		runtimeAvailable  bool
		runtimeDiagnostic bool
	}{
		{"pre-write technical failure", terminalFailurePreWrite, true, &failed, true, false, true, false},
		{"successful rollback", terminalFailureSuccessfulRollback, true, &failed, true, false, true, false},
		{"selector failure", terminalFailureSelector, true, &failed, false, false, true, false},
		{"post-apply publication failure", terminalFailurePublication, true, &completed, false, true, true, true},
		{"active rollback failure", terminalFailureRollback, true, nil, false, true, false, true},
		{"dormant rollback failure", terminalFailureRollback, false, nil, false, true, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newServiceTestEnv(t)
			env.publisher.ready = make(chan struct{}, 1)
			operationID := clientui.NewWorktreeTransitionID()
			next := mustCreateWorktree(t, env, "feature/terminal-"+operationID.String())
			previous := mustResolveServiceTestTarget(t, env)
			events := make(chan runtime.Event, 16)
			observe := func(event runtime.Event) {
				if event.PendingWorkRestoration != nil || event.QueuedUserMessageStatus != nil || event.Kind == runtime.EventStreamingErrorUpdated {
					events <- event
				}
			}
			var attachment sessionruntime.RuntimeAttachment
			var engine *runtime.Engine
			if test.active {
				plan := deleteActivityRuntimePlan(t, env, env.workspaceRoot, deleteActivityTestLLMClient{}, "off", nil, observe)
				var err error
				attachment, err = env.authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{
					SessionID: openDeleteActivitySessionDescriptor(t, env.session.Meta().SessionID).SessionID(), OwnerID: "worktree-terminal-table", Runtime: &plan,
				})
				if err != nil {
					t.Fatalf("open Runtime: %v", err)
				}
				if err := env.authority.WithRuntime(t.Context(), attachment.Resource(), func(_ context.Context, current *runtime.Engine) error { engine = current; return nil }); err != nil {
					t.Fatalf("capture Runtime: %v", err)
				}
			}
			var releaseMaintenance func()
			if test.active && test.kind == terminalFailureRollback {
				releaseMaintenance = holdTransitionRuntime(t, engine)
			}
			if test.kind == terminalFailurePublication {
				env.publisher.identityErr = errors.New("publish Session identity")
			}
			request := worktreeTransitionRequest{operationID: operationID, sessionID: env.session.Meta().SessionID, kind: clientui.WorktreeTransitionEnter, selector: next.WorktreeID}
			ack, runErr := env.service.runWorktreeTransition(
				t.Context(),
				request,
				runtimeinput.PendingWorkWorktreeTransition{Transition: runtimeinput.PendingWorkWorktreeTransitionEnter, Selector: &request.selector},
				func(ctx context.Context, authority transitionAuthority, sync transitionTargetSync) error {
					if test.kind == terminalFailureSelector {
						return worktreeUnappliedUserCorrectable(errors.New("selector unavailable"))
					}
					apply := func(applyCtx context.Context) error {
						return runTerminalMutationCase(applyCtx, env, next.WorktreeID, previous, test.kind, sync, rollbackFailure)
					}
					if authority != nil {
						return worktreeUnappliedTechnicalUnlessClassified(authority(apply))
					}
					return apply(ctx)
				},
			)
			var queued runtime.QueuedUserMessage
			if test.active {
				if runErr != nil || ack == nil || ack.GetOperationId() != operationID.String() {
					t.Fatalf("active acknowledgement = %+v, %v", ack, runErr)
				}
				if test.kind == terminalFailureRollback {
					var err error
					queued, err = engine.QueueUserMessage(t.Context(), "queued behind indeterminate transition")
					if err != nil {
						t.Fatalf("queue human work: %v", err)
					}
					releaseMaintenance()
				} else {
					select {
					case <-env.publisher.ready:
					case <-time.After(5 * time.Second):
						t.Fatal("timed out waiting for Worktree outcome")
					}
					if err := engine.RunWhenIdle(t.Context(), runtime.ActiveKindRuntimeMaintenance, func() error { return nil }); err != nil {
						t.Fatalf("wait for terminal transition: %v", err)
					}
				}
			} else if ack != nil || !errors.Is(runErr, rollbackFailure) {
				t.Fatalf("dormant result = %+v, %v", ack, runErr)
			}
			env.publisher.mu.Lock()
			outcomes := append([]clientui.WorktreeTransitionOutcome(nil), env.publisher.outcomes...)
			env.publisher.mu.Unlock()
			if test.outcome == nil && len(outcomes) != 0 || test.outcome != nil &&
				(len(outcomes) != 1 || outcomes[0].OperationID != operationID || outcomes[0].State != *test.outcome) {
				t.Fatalf("Worktree outcomes = %+v, want state=%v", outcomes, test.outcome)
			}
			restored, diagnosed, unavailable := waitForTransitionEvents(
				t, events, queued.ID, test.restore, test.runtimeDiagnostic, test.active && test.kind == terminalFailureRollback,
			)
			if restored != test.restore {
				t.Fatalf("technical restoration present=%v, want %v", restored, test.restore)
			}
			if diagnosed != test.runtimeDiagnostic {
				t.Fatalf("Runtime diagnostic present=%v, want %v", diagnosed, test.runtimeDiagnostic)
			}
			if test.outcome != nil && *test.outcome == failed && outcomes[0].Failure == nil {
				t.Fatalf("Worktree outcome diagnostic = %+v", outcomes)
			}
			if test.outcome != nil && *test.outcome == completed && outcomes[0].Failure != nil {
				t.Fatalf("completed Worktree diagnostic = %+v", outcomes[0].Failure)
			}
			if test.active && test.kind == terminalFailureRollback && !unavailable {
				t.Fatalf("queued human work was not failed as Runtime unavailable")
			}
			target := mustResolveServiceTestTarget(t, env)
			if got := sessionTargetWorktreeID(target) == next.WorktreeID; got != test.applied {
				t.Fatalf("persisted target = %+v, want applied=%v", target, test.applied)
			}
			if test.active {
				laterErr := env.authority.WithRuntime(t.Context(), attachment.Resource(), func(ctx context.Context, current *runtime.Engine) error {
					_, err := current.QueueUserMessage(ctx, "later human work")
					return err
				})
				if test.runtimeAvailable && laterErr != nil ||
					!test.runtimeAvailable && !errors.Is(laterErr, serverapi.ErrRuntimeUnavailable) {
					t.Fatalf("later human work error = %v, want Runtime available=%v", laterErr, test.runtimeAvailable)
				}
			} else {
				plan := deleteActivityTestRuntimePlan(t, env, next.CanonicalRoot)
				if _, err := env.authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{
					SessionID: openDeleteActivitySessionDescriptor(t, env.session.Meta().SessionID).SessionID(), OwnerID: "dormant-worktree-follow-up", Runtime: &plan,
				}); err != nil {
					t.Fatalf("later Runtime open: %v", err)
				}
			}
		})
	}
}
func runTerminalMutationCase(ctx context.Context, env *serviceTestEnv, nextWorktreeID string, previous clientui.SessionExecutionTarget, kind terminalFailureKind, sync transitionTargetSync, rollbackFailure error) error {
	_, err := applyWorktreeTargetMutation(
		func() error {
			if kind == terminalFailurePreWrite {
				return errors.New("write target")
			}
			return env.store.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdate{
				SessionID: env.session.Meta().SessionID, Workspace: &metadata.SessionExecutionTargetUpdateWorkspace{ID: env.binding.WorkspaceID},
				Worktree: &metadata.SessionExecutionTargetUpdateWorktree{ID: nextWorktreeID}, CwdRelpath: ".",
			})
		},
		func() (clientui.SessionExecutionTarget, error) {
			target, err := env.store.ResolveSessionExecutionTarget(ctx, env.session.Meta().SessionID)
			if err != nil {
				return clientui.SessionExecutionTarget{}, worktreeUnappliedTechnical(err)
			}
			if kind == terminalFailureSuccessfulRollback || kind == terminalFailureRollback {
				return target, worktreeUnappliedTechnical(errors.New("synchronize target"))
			}
			if kind == terminalFailurePublication {
				return target, worktreeUnappliedTechnicalUnlessClassified(sync(ctx, target, nil))
			}
			return target, nil
		},
		func() error {
			if kind == terminalFailureRollback {
				return rollbackFailure
			}
			return env.store.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdateFromReadModel(env.session.Meta().SessionID, previous))
		},
	)
	return err
}
func holdTransitionRuntime(t *testing.T, engine *runtime.Engine) func() {
	t.Helper()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- engine.RunWhenIdle(t.Context(), runtime.ActiveKindRuntimeMaintenance, func() error { close(started); <-release; return nil })
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out holding Runtime maintenance")
	}
	return func() {
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("release Runtime maintenance: %v", err)
		}
	}
}
func waitForTransitionEvents(
	t *testing.T,
	events <-chan runtime.Event,
	queuedID string,
	wantRestored bool,
	wantDiagnosed bool,
	wantUnavailable bool,
) (restored bool, diagnosed bool, unavailable bool) {
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	record := func(event runtime.Event) {
		restored = restored || event.PendingWorkRestoration != nil
		diagnosed = diagnosed || event.Kind == runtime.EventStreamingErrorUpdated
		status := event.QueuedUserMessageStatus
		unavailable = unavailable || status != nil && status.QueueItemID == queuedID &&
			status.Status == runtime.QueuedUserMessageFailed &&
			status.FailureReason == runtime.QueuedUserMessageFailureRuntimeUnavailable
	}
drain:
	for {
		select {
		case event := <-events:
			record(event)
		default:
			break drain
		}
	}
	for restored != wantRestored || diagnosed != wantDiagnosed || unavailable != wantUnavailable {
		select {
		case event := <-events:
			record(event)
		case <-timeout.C:
			t.Fatal("timed out waiting for transition events")
		}
	}
	return
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
