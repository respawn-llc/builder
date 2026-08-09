package worktree

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/filemode"
	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestClaimedWorktreeOutcomePublishesBeforeBoundaryRelease(t *testing.T) {
	publicationErr := errors.New("publish Worktree outcome")
	tests := []struct {
		name           string
		operationErr   error
		publicationErr error
		blockNotice    bool
		wantState      clientui.WorktreeTransitionState
		wantErr        error
		wantAnyErr     bool
	}{
		{
			name:      "completed success",
			wantState: clientui.WorktreeTransitionCompleted,
		},
		{
			name:         "completed operational failure",
			operationErr: errors.New("switch target"),
			wantState:    clientui.WorktreeTransitionFailed,
		},
		{
			name:           "publication failure still releases",
			publicationErr: publicationErr,
			wantState:      clientui.WorktreeTransitionCompleted,
			wantErr:        publicationErr,
		},
		{
			name:         "failure notice application failure still releases",
			operationErr: errors.New("switch target"),
			blockNotice:  true,
			wantState:    clientui.WorktreeTransitionFailed,
			wantAnyErr:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, attachment := openIdleWorktreeBoundaryRuntime(t)
			env.publisher.err = test.publicationErr
			publishedBeforeRelease := false
			env.publisher.onOutcome = func(clientui.WorktreeTransitionOutcome) {
				duplicate, claimErr := env.authority.ClaimCurrentWorktreeBoundary(
					env.session.Meta().SessionID,
					serverapi.NewWorktreeOperationID(),
				)
				publishedBeforeRelease = duplicate == nil &&
					errors.Is(claimErr, sessionruntime.ErrWorktreeBoundaryClaimed)
			}
			operationID := serverapi.NewWorktreeOperationID()
			claim, err := env.authority.ClaimCurrentWorktreeBoundary(
				env.session.Meta().SessionID,
				operationID,
			)
			if err != nil {
				t.Fatal(err)
			}
			if claim == nil {
				t.Fatal("active runtime did not expose a Worktree boundary claim")
			}
			if err := claim.AwaitGrant(context.Background()); err != nil {
				t.Fatal(err)
			}
			request := worktreeTransitionRequest{
				operationID: operationID,
				sessionID:   env.session.Meta().SessionID,
				kind:        clientui.WorktreeTransitionEnter,
			}
			outcome := worktreeTransitionOutcome(request, test.operationErr)
			var blocker *filemode.EventLogAppendBlocker
			if test.blockNotice {
				blocker = filemode.MustBlockEventLogAppends(
					t,
					filepath.Join(env.session.Dir(), "events.jsonl"),
				)
			}
			err = env.service.submitClaimedWorktreeTransitionOutcome(
				claim,
				request.sessionID,
				outcome,
			)
			if !test.wantAnyErr && !errors.Is(err, test.wantErr) {
				t.Fatalf("outcome error = %v, want %v", err, test.wantErr)
			}
			if test.wantAnyErr && err == nil {
				t.Fatal("outcome application unexpectedly succeeded")
			}
			if !publishedBeforeRelease {
				t.Fatal("Worktree outcome publication did not observe the claimed boundary")
			}
			if blocker != nil {
				if restoreErr := blocker.Restore(); restoreErr != nil {
					t.Fatalf("restore event log: %v", restoreErr)
				}
			}
			env.publisher.mu.Lock()
			published := append(
				[]clientui.WorktreeTransitionOutcome(nil),
				env.publisher.outcomes...,
			)
			env.publisher.mu.Unlock()
			if len(published) != 1 || published[0].State != test.wantState {
				t.Fatalf("published outcomes = %+v, want one %s", published, test.wantState)
			}

			next, err := env.authority.ClaimCurrentWorktreeBoundary(
				request.sessionID,
				serverapi.NewWorktreeOperationID(),
			)
			if err != nil {
				t.Fatalf("claim after outcome: %v", err)
			}
			if err := next.AwaitGrant(context.Background()); err != nil {
				t.Fatalf("await claim after outcome: %v", err)
			}
			grant, err := next.Release()
			if err != nil {
				t.Fatalf("release claim after outcome: %v", err)
			}
			if grant == nil {
				t.Fatal("idle claim release did not transfer reducer ownership")
			}
			if err := grant.Release(); err != nil {
				t.Fatalf("release reducer ownership: %v", err)
			}
			if _, err := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose); err != nil {
				t.Fatalf("close runtime: %v", err)
			}
		})
	}
}

func TestScheduledWorktreeRequestCancellationAfterAcknowledgementDoesNotSuppressOutcome(
	t *testing.T,
) {
	for _, test := range []struct {
		name         string
		operationErr error
		wantState    clientui.WorktreeTransitionState
	}{
		{
			name:      "completed success",
			wantState: clientui.WorktreeTransitionCompleted,
		},
		{
			name:         "completed operational failure",
			operationErr: errors.New("switch target"),
			wantState:    clientui.WorktreeTransitionFailed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			env, attachment := openIdleWorktreeBoundaryRuntime(t)
			requestCtx, cancelRequest := context.WithCancel(context.Background())
			releaseOperation := make(chan struct{})
			env.publisher.mu.Lock()
			env.publisher.ready = make(chan struct{}, 1)
			env.publisher.mu.Unlock()
			request := worktreeTransitionRequest{
				operationID: serverapi.NewWorktreeOperationID(),
				sessionID:   env.session.Meta().SessionID,
				kind:        clientui.WorktreeTransitionEnter,
			}
			ack, err := env.service.scheduleWorktreeTransition(
				requestCtx,
				request,
				func(
					runCtx context.Context,
					_ transitionAuthority,
					_ transitionTargetSync,
				) error {
					select {
					case <-releaseOperation:
						return test.operationErr
					case <-runCtx.Done():
						return context.Cause(runCtx)
					}
				},
				nil,
			)
			if err != nil {
				t.Fatalf("schedule Worktree transition: %v", err)
			}
			if ack.OperationID != request.operationID {
				t.Fatalf(
					"acknowledgement = %+v, want operation %s",
					ack,
					request.operationID,
				)
			}
			cancelRequest()
			close(releaseOperation)

			select {
			case <-env.publisher.ready:
			case <-time.After(3 * time.Second):
				t.Fatal("request cancellation suppressed the Worktree outcome")
			}
			env.publisher.mu.Lock()
			outcomes := append(
				[]clientui.WorktreeTransitionOutcome(nil),
				env.publisher.outcomes...,
			)
			env.publisher.mu.Unlock()
			if len(outcomes) != 1 ||
				outcomes[0].OperationID != request.operationID ||
				outcomes[0].State != test.wantState {
				t.Fatalf(
					"Worktree outcomes = %+v, want one %s outcome",
					outcomes,
					test.wantState,
				)
			}
			next := awaitNextIdleWorktreeClaim(
				t,
				env.authority,
				request.sessionID,
			)
			if err := next.AwaitGrant(context.Background()); err != nil {
				t.Fatalf("await claim after canceled request outcome: %v", err)
			}
			grant, err := next.Release()
			if err != nil {
				t.Fatalf("release claim after canceled request outcome: %v", err)
			}
			if grant == nil {
				t.Fatal("idle claim release did not transfer reducer ownership")
			}
			if err := grant.Release(); err != nil {
				t.Fatalf("release reducer ownership: %v", err)
			}
			if _, err := attachment.Release(
				context.Background(),
				sessionruntime.RuntimeReleaseClose,
			); err != nil {
				t.Fatalf("close runtime: %v", err)
			}
		})
	}
}

func TestScheduledWorktreeAcknowledgementOwnsBoundaryAndMatchingRetry(t *testing.T) {
	env, attachment := openIdleWorktreeBoundaryRuntime(t)
	env.publisher.mu.Lock()
	env.publisher.ready = make(chan struct{}, 1)
	env.publisher.mu.Unlock()
	releaseOperation := make(chan struct{})
	request := worktreeTransitionRequest{
		operationID: serverapi.NewWorktreeOperationID(),
		sessionID:   env.session.Meta().SessionID,
		kind:        clientui.WorktreeTransitionEnter,
		selector:    "feature/claimed-before-ack",
	}
	execute := func(
		runCtx context.Context,
		_ transitionAuthority,
		_ transitionTargetSync,
	) error {
		select {
		case <-releaseOperation:
			return nil
		case <-runCtx.Done():
			return context.Cause(runCtx)
		}
	}
	ack, err := env.service.scheduleWorktreeTransition(
		context.Background(),
		request,
		execute,
		nil,
	)
	if err != nil {
		t.Fatalf("schedule Worktree transition: %v", err)
	}
	if ack.OperationID != request.operationID {
		t.Fatalf("acknowledgement = %+v, want operation %s", ack, request.operationID)
	}
	duplicateClaim, claimErr := env.authority.ClaimCurrentWorktreeBoundary(
		request.sessionID,
		serverapi.NewWorktreeOperationID(),
	)
	if duplicateClaim != nil ||
		!errors.Is(claimErr, sessionruntime.ErrWorktreeBoundaryClaimed) {
		t.Fatalf(
			"boundary after acknowledgement = claim:%v error:%v, want claimed",
			duplicateClaim,
			claimErr,
		)
	}

	retryAck, err := env.service.scheduleWorktreeTransition(
		context.Background(),
		request,
		func(context.Context, transitionAuthority, transitionTargetSync) error {
			return errors.New("matching retry executor must not replace accepted work")
		},
		nil,
	)
	if err != nil || retryAck != ack {
		t.Fatalf("matching retry = %+v, %v; want %+v", retryAck, err, ack)
	}
	different := request
	different.operationID = serverapi.NewWorktreeOperationID()
	_, err = env.service.scheduleWorktreeTransition(
		context.Background(),
		different,
		execute,
		nil,
	)
	var pending *serverapi.WorktreeTransitionPendingError
	if !errors.As(err, &pending) ||
		pending.PendingOperationID != request.operationID {
		t.Fatalf(
			"different pending transition error = %v, want operation %s",
			err,
			request.operationID,
		)
	}

	close(releaseOperation)
	select {
	case <-env.publisher.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("accepted Worktree transition did not publish its outcome")
	}
	next := awaitNextIdleWorktreeClaim(t, env.authority, request.sessionID)
	if err := next.AwaitGrant(context.Background()); err != nil {
		t.Fatalf("await claim after accepted transition: %v", err)
	}
	grant, err := next.Release()
	if err != nil {
		t.Fatalf("release claim after accepted transition: %v", err)
	}
	if grant == nil {
		t.Fatal("idle claim release did not transfer reducer ownership")
	}
	if err := grant.Release(); err != nil {
		t.Fatalf("release reducer ownership: %v", err)
	}
	if _, err := attachment.Release(
		context.Background(),
		sessionruntime.RuntimeReleaseClose,
	); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
}

func awaitNextIdleWorktreeClaim(
	t *testing.T,
	authority *sessionruntime.Authority,
	sessionID string,
) *sessionruntime.WorktreeBoundaryClaim {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		claim, err := authority.ClaimCurrentWorktreeBoundary(
			sessionID,
			serverapi.NewWorktreeOperationID(),
		)
		if err == nil {
			if claim == nil {
				t.Fatal("active runtime returned no Worktree boundary claim")
			}
			return claim
		}
		if !errors.Is(err, sessionruntime.ErrWorktreeBoundaryClaimed) {
			t.Fatalf("claim after Worktree outcome: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("Worktree outcome retained its boundary claim: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestClaimedWorktreeOutcomeRejectsClosedRuntimeWithoutPublication(t *testing.T) {
	env, attachment := openIdleWorktreeBoundaryRuntime(t)
	claim, err := env.authority.ClaimCurrentWorktreeBoundary(
		env.session.Meta().SessionID,
		serverapi.NewWorktreeOperationID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := claim.AwaitGrant(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := attachment.Release(context.Background(), sessionruntime.RuntimeReleaseClose); err != nil {
		t.Fatalf("close runtime: %v", err)
	}
	outcome := clientui.WorktreeTransitionOutcome{
		OperationID: serverapi.NewWorktreeOperationID(),
		Transition:  clientui.WorktreeTransitionLeave,
		State:       clientui.WorktreeTransitionCompleted,
	}
	if err := env.service.submitClaimedWorktreeTransitionOutcome(
		claim,
		env.session.Meta().SessionID,
		outcome,
	); !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("closed-runtime outcome = %v, want runtime unavailable", err)
	}
	env.publisher.mu.Lock()
	published := append(
		[]clientui.WorktreeTransitionOutcome(nil),
		env.publisher.outcomes...,
	)
	env.publisher.mu.Unlock()
	if len(published) != 0 {
		t.Fatalf("closed runtime published outcomes: %+v", published)
	}

	plan := deleteActivityTestRuntimePlan(t, env, env.workspaceRoot)
	descriptor := openDeleteActivitySessionDescriptor(
		t,
		env.session.Meta().SessionID,
	)
	replacement, err := env.authority.OpenRuntime(
		context.Background(),
		sessionruntime.RuntimeOpenRequest{
			SessionID: descriptor.SessionID(),
			OwnerID:   "worktree-boundary-outcome-replacement",
			Runtime:   &plan,
		},
	)
	if err != nil {
		t.Fatalf("open replacement runtime: %v", err)
	}
	replacementClaim, err := env.authority.ClaimCurrentWorktreeBoundary(
		env.session.Meta().SessionID,
		serverapi.NewWorktreeOperationID(),
	)
	if err != nil {
		t.Fatalf("claim replacement runtime: %v", err)
	}
	if err := replacementClaim.AwaitGrant(context.Background()); err != nil {
		t.Fatalf("await replacement claim: %v", err)
	}
	grant, err := replacementClaim.Release()
	if err != nil {
		t.Fatalf("release replacement claim: %v", err)
	}
	if grant == nil {
		t.Fatal("replacement idle claim returned no reducer grant")
	}
	if err := grant.Release(); err != nil {
		t.Fatalf("release replacement reducer grant: %v", err)
	}
	if _, err := replacement.Release(
		context.Background(),
		sessionruntime.RuntimeReleaseClose,
	); err != nil {
		t.Fatalf("close replacement runtime: %v", err)
	}
}

func openIdleWorktreeBoundaryRuntime(
	t *testing.T,
) (*serviceTestEnv, sessionruntime.RuntimeAttachment) {
	t.Helper()
	env := newServiceTestEnv(t)
	plan := deleteActivityTestRuntimePlan(t, env, env.workspaceRoot)
	descriptor := openDeleteActivitySessionDescriptor(
		t,
		env.session.Meta().SessionID,
	)
	attachment, err := env.authority.OpenRuntime(
		context.Background(),
		sessionruntime.RuntimeOpenRequest{
			SessionID: descriptor.SessionID(),
			OwnerID:   "worktree-boundary-outcome",
			Runtime:   &plan,
		},
	)
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	return env, attachment
}
