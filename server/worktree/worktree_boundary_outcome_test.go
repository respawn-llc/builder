package worktree

import (
	"context"
	"errors"
	"testing"

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
		wantState      clientui.WorktreeTransitionState
		wantErr        error
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
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env, attachment := openIdleWorktreeBoundaryRuntime(t)
			env.publisher.err = test.publicationErr
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
			err = env.service.submitClaimedWorktreeTransitionOutcome(
				claim,
				request.sessionID,
				outcome,
			)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("outcome error = %v, want %v", err, test.wantErr)
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
	defer env.publisher.mu.Unlock()
	if len(env.publisher.outcomes) != 0 {
		t.Fatalf("closed runtime published outcomes: %+v", env.publisher.outcomes)
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
