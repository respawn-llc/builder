package sessionruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	runtimepkg "core/server/runtime"
	"core/shared/serverapi"

	"github.com/google/uuid"
)

func TestWorktreeBoundaryArbitratesClaimAndReducerOrder(t *testing.T) {
	tests := []struct {
		name string
		run  func(
			*testing.T,
			*sessionRuntimeFixture,
			*agentResource,
			serverapi.RuntimeStepOrigin,
		) error
	}{
		{
			name: "claim before Boundary transfers to Worktree",
			run: func(
				t *testing.T,
				fixture *sessionRuntimeFixture,
				resource *agentResource,
				origin serverapi.RuntimeStepOrigin,
			) error {
				claim, err := fixture.authority.ClaimWorktreeBoundary(
					resource.ref,
					serverapi.NewWorktreeOperationID(),
				)
				if err != nil {
					return err
				}
				transfer, err := resource.AgentStepBoundary(context.Background(), origin)
				if err != nil {
					return err
				}
				worktree, ok := transfer.(runtimepkg.AgentStepWorktreeBoundary)
				if !ok {
					t.Fatalf("transfer = %T, want Worktree", transfer)
				}
				if err := claim.AwaitGrant(context.Background()); err != nil {
					return err
				}
				if grant, err := claim.Release(); err != nil {
					return err
				} else if grant != nil {
					t.Fatal("Step-wait Worktree release returned reducer grant to Worktree owner")
				}
				grant, err := worktree.Wait.Await(context.Background())
				if err != nil {
					return err
				}
				return grant.Release()
			},
		},
		{
			name: "reducer grant before claim defers Worktree to next Boundary",
			run: func(
				t *testing.T,
				fixture *sessionRuntimeFixture,
				resource *agentResource,
				origin serverapi.RuntimeStepOrigin,
			) error {
				transfer, err := resource.AgentStepBoundary(context.Background(), origin)
				if err != nil {
					return err
				}
				reducer, ok := transfer.(runtimepkg.AgentStepReducerBoundary)
				if !ok {
					t.Fatalf("transfer = %T, want reducer", transfer)
				}
				claim, err := fixture.authority.ClaimWorktreeBoundary(
					resource.ref,
					serverapi.NewWorktreeOperationID(),
				)
				if err != nil {
					return err
				}
				waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()
				if err := claim.AwaitGrant(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("claim grant before next Boundary = %v, want deadline", err)
				}
				next := serverapi.RuntimeStepOrigin{
					RunID:  origin.RunID,
					StepID: uuid.NewString(),
				}
				if _, err := reducer.Grant.RegisterNext(context.Background(), next); err != nil {
					return err
				}
				nextTransfer, err := resource.AgentStepBoundary(context.Background(), next)
				if err != nil {
					return err
				}
				worktree, ok := nextTransfer.(runtimepkg.AgentStepWorktreeBoundary)
				if !ok {
					t.Fatalf("next transfer = %T, want Worktree", nextTransfer)
				}
				if err := claim.AwaitGrant(context.Background()); err != nil {
					return err
				}
				if grant, err := claim.Release(); err != nil {
					return err
				} else if grant != nil {
					t.Fatal("Step-wait Worktree release returned reducer grant to Worktree owner")
				}
				grant, err := worktree.Wait.Await(context.Background())
				if err != nil {
					return err
				}
				return grant.Release()
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, resource, release := worktreeBoundaryArbitrationFixture(t)
			defer release()
			origin := serverapi.RuntimeStepOrigin{
				RunID:  uuid.NewString(),
				StepID: uuid.NewString(),
			}
			if _, err := resource.AgentStepBegan(context.Background(), origin); err != nil {
				t.Fatalf("AgentStepBegan: %v", err)
			}
			if err := test.run(t, fixture, resource, origin); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func worktreeBoundaryArbitrationFixture(
	t *testing.T,
) (*sessionRuntimeFixture, *agentResource, func()) {
	t.Helper()
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(
		t,
		fixture.authority,
		sessionID,
		"worktree-boundary-arbitration",
		&plan,
	)
	releaseRunner := make(chan struct{})
	handle, err := fixture.authority.StartAgentExecution(
		context.Background(),
		AgentExecutionRequest{
			Descriptor: mustOpenSessionDescriptor(t, sessionID),
			Resource:   CurrentAgentResource{},
			Runner: func(context.Context, ExecutionScope, AgentRuntimeBridge) error {
				<-releaseRunner
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	fixture.authority.mu.Lock()
	resource := fixture.authority.resources[sessionID]
	fixture.authority.mu.Unlock()
	if resource == nil {
		t.Fatal("active resource is unavailable")
	}
	release := func() {
		close(releaseRunner)
		if _, err := handle.Wait(context.Background()); err != nil {
			t.Errorf("wait execution: %v", err)
		}
		if _, err := attachment.Release(context.Background(), RuntimeReleaseClose); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	}
	return &fixture, resource, release
}
