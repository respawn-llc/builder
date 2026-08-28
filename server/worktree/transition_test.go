package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/runtime"
	"core/server/sessionruntime"
	"core/shared/clientui"
	worktreepb "core/shared/protoapi/gen/kent/api/worktree"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
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

func TestWorktreeTransitionTerminalOutcomesAndRuntimeRetirement(t *testing.T) {
	preWriteFailure := errors.New("write target")
	syncFailure := errors.New("synchronize target")
	selectorFailure := errors.New("selector unavailable")
	publicationFailure := errors.New("publish Session identity")
	rollbackFailure := errors.New("rollback target")

	tests := []struct {
		name              string
		kind              terminalFailureKind
		active            bool
		wantOutcome       *clientui.WorktreeTransitionState
		wantRestoration   bool
		wantAppliedTarget bool
		wantLaterAccepted bool
		wantDiagnostic    bool
	}{
		{
			name:              "pre-write technical failure",
			kind:              terminalFailurePreWrite,
			active:            true,
			wantOutcome:       worktreeTransitionStatePointer(clientui.WorktreeTransitionFailed),
			wantRestoration:   true,
			wantLaterAccepted: true,
		},
		{
			name:              "successful rollback",
			kind:              terminalFailureSuccessfulRollback,
			active:            true,
			wantOutcome:       worktreeTransitionStatePointer(clientui.WorktreeTransitionFailed),
			wantRestoration:   true,
			wantLaterAccepted: true,
		},
		{
			name:              "selector failure",
			kind:              terminalFailureSelector,
			active:            true,
			wantOutcome:       worktreeTransitionStatePointer(clientui.WorktreeTransitionFailed),
			wantLaterAccepted: true,
		},
		{
			name:              "post-commit publication failure",
			kind:              terminalFailurePublication,
			active:            true,
			wantOutcome:       worktreeTransitionStatePointer(clientui.WorktreeTransitionCompleted),
			wantAppliedTarget: true,
			wantLaterAccepted: true,
			wantDiagnostic:    true,
		},
		{
			name:              "rollback failure retires active Runtime",
			kind:              terminalFailureRollback,
			active:            true,
			wantAppliedTarget: true,
			wantDiagnostic:    true,
		},
		{
			name:              "rollback failure stays diagnostic for dormant Session",
			kind:              terminalFailureRollback,
			wantAppliedTarget: true,
			wantLaterAccepted: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newServiceTestEnv(t)
			next := mustCreateWorktree(t, env, "feature/terminal-"+strings.ReplaceAll(test.name, " ", "-"))
			previousTarget := mustResolveServiceTestTarget(t, env)
			operationID := clientui.NewWorktreeTransitionID()
			var eventMu sync.Mutex
			var restorations []runtimeinput.PendingWorkTechnicalRestoration
			var queuedStatuses []runtime.QueuedUserMessageStatusEvent
			streamingErrors := 0
			observe := func(event runtime.Event) {
				eventMu.Lock()
				defer eventMu.Unlock()
				if event.PendingWorkRestoration != nil {
					restorations = append(restorations, *event.PendingWorkRestoration)
				}
				if event.QueuedUserMessageStatus != nil {
					queuedStatuses = append(queuedStatuses, *event.QueuedUserMessageStatus)
				}
				if event.Kind == runtime.EventStreamingErrorUpdated {
					streamingErrors++
				}
			}

			var attachment sessionruntime.RuntimeAttachment
			var engine *runtime.Engine
			var releaseMaintenance func()
			if test.active {
				sessionID, err := runtimeids.ParseSessionID(env.session.Meta().SessionID)
				if err != nil {
					t.Fatalf("parse Session ID: %v", err)
				}
				plan := deleteActivityRuntimePlan(
					t,
					env,
					env.workspaceRoot,
					deleteActivityTestLLMClient{},
					"off",
					nil,
					observe,
				)
				attachment, err = env.authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{
					SessionID: sessionID,
					OwnerID:   "worktree-terminal-table",
					Runtime:   &plan,
				})
				if err != nil {
					t.Fatalf("open Runtime: %v", err)
				}
				if err := env.authority.WithRuntime(t.Context(), attachment.Resource(), func(_ context.Context, current *runtime.Engine) error {
					engine = current
					return nil
				}); err != nil {
					t.Fatalf("capture Runtime: %v", err)
				}
				if test.kind == terminalFailureRollback {
					releaseMaintenance = holdWorktreeTransitionRuntime(t, engine)
				}
			}

			env.publisher.mu.Lock()
			if test.kind == terminalFailurePublication {
				env.publisher.identityErr = publicationFailure
			}
			env.publisher.mu.Unlock()
			request := worktreeTransitionRequest{
				operationID: operationID,
				sessionID:   env.session.Meta().SessionID,
				kind:        clientui.WorktreeTransitionEnter,
				selector:    next.WorktreeID,
			}
			ack, runErr := env.service.runWorktreeTransition(
				t.Context(),
				request,
				runtimeinput.PendingWorkWorktreeTransition{
					Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
					Selector:   &request.selector,
				},
				func(
					ctx context.Context,
					authority transitionAuthority,
					sync transitionTargetSync,
				) error {
					if test.kind == terminalFailureSelector {
						return worktreeUnappliedUserCorrectable(selectorFailure)
					}
					apply := func(applyCtx context.Context) error {
						return runTerminalMutationCase(
							applyCtx,
							env,
							next.WorktreeID,
							previousTarget,
							test.kind,
							sync,
							preWriteFailure,
							syncFailure,
							rollbackFailure,
						)
					}
					if authority != nil {
						return worktreeUnappliedTechnicalUnlessClassified(authority(apply))
					}
					return apply(ctx)
				},
			)
			if test.active {
				if runErr != nil || ack == nil || ack.GetOperationId() != operationID.String() {
					t.Fatalf("active acknowledgement = %+v, %v", ack, runErr)
				}
				var queued runtime.QueuedUserMessage
				if test.kind == terminalFailureRollback {
					var err error
					queued, err = engine.QueueUserMessage(t.Context(), "queued behind indeterminate Worktree transition")
					if err != nil {
						t.Fatalf("queue human work: %v", err)
					}
					releaseMaintenance()
				}
				waitForWorktreeTerminalCase(t, env, attachment, operationID, test.wantOutcome)
				if test.wantDiagnostic {
					waitForWorktreeRuntimeDiagnostic(t, &eventMu, &streamingErrors)
				}
				if test.wantRestoration {
					waitForWorktreeRestoration(t, &eventMu, &restorations)
				}
				if test.kind == terminalFailureRollback {
					eventMu.Lock()
					statuses := append([]runtime.QueuedUserMessageStatusEvent(nil), queuedStatuses...)
					eventMu.Unlock()
					if !hasRuntimeUnavailableFailure(statuses, queued.ID) {
						t.Fatalf("queued human work statuses = %+v, want Runtime unavailable for %s", statuses, queued.ID)
					}
				}
			} else {
				if !errors.Is(runErr, rollbackFailure) || ack != nil {
					t.Fatalf("dormant rollback result = %+v, %v; want diagnostic without acknowledgement", ack, runErr)
				}
			}

			outcome := worktreeOutcomeForOperation(env.publisher, operationID)
			if test.wantOutcome == nil {
				if outcome != nil {
					t.Fatalf("Worktree outcome = %+v, want none", outcome)
				}
			} else if outcome == nil || outcome.State != *test.wantOutcome {
				t.Fatalf("Worktree outcome = %+v, want %s", outcome, *test.wantOutcome)
			}
			eventMu.Lock()
			gotRestorations := append([]runtimeinput.PendingWorkTechnicalRestoration(nil), restorations...)
			eventMu.Unlock()
			if (len(gotRestorations) == 1) != test.wantRestoration {
				t.Fatalf("technical restorations = %+v, want present=%v", gotRestorations, test.wantRestoration)
			}
			target := mustResolveServiceTestTarget(t, env)
			if gotApplied := sessionTargetWorktreeID(target) == next.WorktreeID; gotApplied != test.wantAppliedTarget {
				t.Fatalf("persisted target = %+v, want applied=%v", target, test.wantAppliedTarget)
			}

			if test.active {
				laterErr := env.authority.WithRuntime(t.Context(), attachment.Resource(), func(ctx context.Context, current *runtime.Engine) error {
					_, err := current.QueueUserMessage(ctx, "later human work")
					return err
				})
				if (laterErr == nil) != test.wantLaterAccepted {
					t.Fatalf("later human work error = %v, want accepted=%v", laterErr, test.wantLaterAccepted)
				}
			} else {
				sessionID, err := runtimeids.ParseSessionID(env.session.Meta().SessionID)
				if err != nil {
					t.Fatalf("parse dormant Session ID: %v", err)
				}
				plan := deleteActivityTestRuntimePlan(t, env, next.CanonicalRoot)
				replacement, err := env.authority.OpenRuntime(t.Context(), sessionruntime.RuntimeOpenRequest{
					SessionID: sessionID,
					OwnerID:   "dormant-worktree-follow-up",
					Runtime:   &plan,
				})
				if err != nil {
					t.Fatalf("open Runtime after dormant diagnostic: %v", err)
				}
				if replacement.Resource().SessionID() != sessionID {
					t.Fatalf("replacement Runtime = %v, want Session %s", replacement.Resource(), sessionID)
				}
			}
		})
	}
}

func runTerminalMutationCase(
	ctx context.Context,
	env *serviceTestEnv,
	nextWorktreeID string,
	previousTarget clientui.SessionExecutionTarget,
	kind terminalFailureKind,
	sync transitionTargetSync,
	preWriteFailure error,
	syncFailure error,
	rollbackFailure error,
) error {
	_, err := applyWorktreeTargetMutation(
		func() error {
			if kind == terminalFailurePreWrite {
				return preWriteFailure
			}
			return env.store.UpdateSessionExecutionTarget(ctx, metadata.SessionExecutionTargetUpdate{
				SessionID:  env.session.Meta().SessionID,
				Workspace:  &metadata.SessionExecutionTargetUpdateWorkspace{ID: env.binding.WorkspaceID},
				Worktree:   &metadata.SessionExecutionTargetUpdateWorktree{ID: nextWorktreeID},
				CwdRelpath: ".",
			})
		},
		func() (clientui.SessionExecutionTarget, error) {
			target, err := env.store.ResolveSessionExecutionTarget(ctx, env.session.Meta().SessionID)
			if err != nil {
				return clientui.SessionExecutionTarget{}, worktreeUnappliedTechnical(err)
			}
			switch kind {
			case terminalFailureSuccessfulRollback, terminalFailureRollback:
				return target, worktreeUnappliedTechnical(syncFailure)
			case terminalFailurePublication:
				return target, worktreeUnappliedTechnicalUnlessClassified(sync(ctx, target, nil))
			default:
				return target, nil
			}
		},
		func() error {
			if kind == terminalFailureRollback {
				return rollbackFailure
			}
			return env.store.UpdateSessionExecutionTarget(
				ctx,
				metadata.SessionExecutionTargetUpdateFromReadModel(env.session.Meta().SessionID, previousTarget),
			)
		},
	)
	return err
}

func holdWorktreeTransitionRuntime(t *testing.T, engine *runtime.Engine) func() {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- engine.RunWhenIdle(t.Context(), runtime.ActiveKindRuntimeMaintenance, func() error {
			close(started)
			<-release
			return nil
		})
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

func waitForWorktreeTerminalCase(
	t *testing.T,
	env *serviceTestEnv,
	attachment sessionruntime.RuntimeAttachment,
	operationID clientui.WorktreeTransitionID,
	wantOutcome *clientui.WorktreeTransitionState,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if wantOutcome != nil && worktreeOutcomeForOperation(env.publisher, operationID) != nil {
			return
		}
		if wantOutcome == nil {
			err := env.authority.WithRuntime(t.Context(), attachment.Resource(), func(context.Context, *runtime.Engine) error {
				return nil
			})
			if err != nil {
				return
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for Worktree terminal behavior")
}

func waitForWorktreeRestoration(
	t *testing.T,
	mu *sync.Mutex,
	restorations *[]runtimeinput.PendingWorkTechnicalRestoration,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		published := len(*restorations) != 0
		mu.Unlock()
		if published {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for Worktree technical restoration")
}

func waitForWorktreeRuntimeDiagnostic(t *testing.T, mu *sync.Mutex, streamingErrors *int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		published := *streamingErrors != 0
		mu.Unlock()
		if published {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for Runtime diagnostic")
}

func worktreeOutcomeForOperation(
	publisher *serviceTestPublisher,
	operationID clientui.WorktreeTransitionID,
) *clientui.WorktreeTransitionOutcome {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	for index := range publisher.outcomes {
		if publisher.outcomes[index].OperationID == operationID {
			outcome := publisher.outcomes[index]
			return &outcome
		}
	}
	return nil
}

func hasRuntimeUnavailableFailure(statuses []runtime.QueuedUserMessageStatusEvent, queueItemID string) bool {
	for _, status := range statuses {
		if status.QueueItemID == queueItemID &&
			status.Status == runtime.QueuedUserMessageFailed &&
			status.FailureReason == runtime.QueuedUserMessageFailureRuntimeUnavailable {
			return true
		}
	}
	return false
}

func worktreeTransitionStatePointer(state clientui.WorktreeTransitionState) *clientui.WorktreeTransitionState {
	return &state
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
