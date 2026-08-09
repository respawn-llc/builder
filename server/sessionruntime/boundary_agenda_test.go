package sessionruntime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"core/server/runtime"
	"core/server/runtimecommand"
	"core/server/session"
	"core/shared/runtimeids"

	"github.com/google/uuid"
)

func TestRuntimeBoundExecutionAbortsBeforeWorkHandoff(t *testing.T) {
	for _, test := range []struct {
		name      string
		interrupt func(*testing.T, *agentResource) <-chan error
		want      error
	}{
		{
			name: "execution cancellation",
			interrupt: func(t *testing.T, resource *agentResource) <-chan error {
				t.Helper()
				resource.mu.Lock()
				execution := resource.current
				execution.cancel()
				resource.mu.Unlock()
				return nil
			},
			want: context.Canceled,
		},
		{
			name: "Engine launch failure",
			interrupt: func(t *testing.T, resource *agentResource) <-chan error {
				t.Helper()
				closed := make(chan error, 1)
				go func() {
					closed <- resource.engine.Close()
				}()
				for {
					finished := make(chan struct{})
					err := resource.engine.LaunchAgentExecution(
						func(context.Context) error { return nil },
						func(error) { close(finished) },
					)
					if errors.Is(err, runtime.ErrEngineClosed) {
						return closed
					}
					if err != nil {
						t.Fatalf("probe Engine launch: %v", err)
					}
					<-finished
				}
			},
			want: runtime.ErrEngineClosed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionRuntimeFixture(t)
			sessionID := lifecycleSessionID(t, fixture)
			plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
			attachment := openLifecycleRuntime(
				t,
				fixture.authority,
				sessionID,
				"runtime-bound-handoff",
				&plan,
			)
			fixture.authority.mu.Lock()
			resource := fixture.authority.resources[sessionID]
			fixture.authority.mu.Unlock()
			grant, acquired, err := resource.TryAcquireIdleBoundary(context.Background())
			if err != nil || !acquired {
				t.Fatalf("acquire idle Boundary = (%T, %t, %v)", grant, acquired, err)
			}
			registered := make(chan struct{})
			releaseAdmission := make(chan struct{})
			aborted := make(chan error, 1)
			var runs atomic.Int32
			deferred, err := runtimecommand.Submit(
				context.Background(),
				resource.events,
				struct{}{},
				func(
					admission runtimecommand.Admission,
					_ struct{},
					complete func(struct{}, error),
				) error {
					launchErr := resource.LaunchRuntimeBoundExecution(
						admission,
						func(context.Context, *runtime.Engine) error {
							runs.Add(1)
							return nil
						},
						func(err error) {
							aborted <- err
						},
					)
					if launchErr != nil {
						complete(struct{}{}, launchErr)
						return launchErr
					}
					close(registered)
					<-releaseAdmission
					complete(struct{}{}, nil)
					return nil
				},
			)
			if err != nil {
				t.Fatalf("submit runtime-bound launch: %v", err)
			}
			<-registered
			closeDone := test.interrupt(t, resource)
			close(releaseAdmission)
			if _, err := deferred.Await(context.Background()); err != nil {
				t.Fatalf("await runtime-bound launch admission: %v", err)
			}
			select {
			case abortErr := <-aborted:
				if !errors.Is(abortErr, test.want) {
					t.Fatalf("abort error = %v, want %v", abortErr, test.want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("runtime-bound work was not aborted")
			}
			if runs.Load() != 0 {
				t.Fatalf("runtime-bound work runs = %d, want 0", runs.Load())
			}
			if retry, err := grant.Release(); err != nil || retry {
				t.Fatalf("release transferred idle Boundary = (%t, %v)", retry, err)
			}
			if closeDone != nil {
				if err := <-closeDone; err != nil && !errors.Is(err, runtime.ErrEngineClosed) {
					t.Fatalf("close Engine: %v", err)
				}
			} else if _, err := attachment.Release(
				context.Background(),
				RuntimeReleaseClose,
			); err != nil {
				t.Fatalf("close runtime: %v", err)
			}
		})
	}
}

func TestCloseIfIdleTreatsIdleReducerBoundaryAsInFlightAndRetriesRetirement(t *testing.T) {
	fixture := newSessionRuntimeFixture(t)
	sessionID := lifecycleSessionID(t, fixture)
	plan := authorityTestRuntimePlan(t, fixture, &sessionRuntimeTestLLMClient{})
	attachment := openLifecycleRuntime(
		t,
		fixture.authority,
		sessionID,
		"owner-a",
		&plan,
	)
	fixture.authority.mu.Lock()
	resource := fixture.authority.resources[sessionID]
	fixture.authority.mu.Unlock()
	grant, acquired, err := resource.TryAcquireIdleBoundary(context.Background())
	if err != nil || !acquired {
		t.Fatalf("acquire idle reducer Boundary = (%T, %t, %v)", grant, acquired, err)
	}
	type releaseResult struct {
		result RuntimeReleaseResult
		err    error
	}
	released := make(chan releaseResult, 1)
	go func() {
		result, releaseErr := attachment.Release(
			context.Background(),
			RuntimeReleaseCloseIfIdle,
		)
		released <- releaseResult{result: result, err: releaseErr}
	}()
	select {
	case outcome := <-released:
		t.Fatalf(
			"close-if-idle passed the active reducer Boundary: %+v, %v",
			outcome.result,
			outcome.err,
		)
	case <-time.After(20 * time.Millisecond):
	}
	if retry, err := grant.Release(); err != nil || retry {
		t.Fatalf("release idle reducer Boundary = (%t, %v)", retry, err)
	}
	outcome := <-released
	if outcome.err != nil {
		t.Fatalf("close-if-idle after Boundary release: %v", outcome.err)
	}
	if !outcome.result.Released || outcome.result.Active {
		t.Fatalf("close-if-idle result = %+v, want retired runtime", outcome.result)
	}
	waitRuntimeUnavailable(
		t,
		fixture.authority,
		attachment.Resource(),
		"idle reducer Boundary release",
	)
}

func TestRuntimeBoundIdleWorkRespectsSessionStartMaintenanceBlock(t *testing.T) {
	for _, test := range []struct {
		name          string
		accept        func(*runtime.Engine) error
		wantAcceptErr error
	}{
		{
			name:          "human",
			wantAcceptErr: ErrSessionStartsBlocked,
			accept: func(engine *runtime.Engine) error {
				_, err := engine.QueueUserMessage("blocked human input")
				return err
			},
		},
		{
			name: "long",
			accept: func(engine *runtime.Engine) error {
				engine.HandleBackgroundShellUpdate(runtime.BackgroundShellEvent{
					Type:       runtime.BackgroundShellEventCompleted,
					ID:         "blocked-background",
					ActivityID: uuid.New(),
					State:      "completed",
				}, true)
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionRuntimeFixture(t)
			sessionID := lifecycleSessionID(t, fixture)
			releaseModel := make(chan struct{})
			close(releaseModel)
			client := &ownerlessRetirementLLMClient{
				firstStarted: make(chan struct{}),
				releaseFirst: releaseModel,
			}
			plan := authorityTestRuntimePlan(t, fixture, client)
			attachment := openLifecycleRuntime(
				t,
				fixture.authority,
				sessionID,
				"owner-a",
				&plan,
			)
			block, err := fixture.authority.BlockSessionStarts(
				context.Background(),
				[]runtimeids.SessionID{sessionID},
				SessionStartBlockMaintenance,
			)
			if err != nil {
				t.Fatalf("block Session starts: %v", err)
			}
			acceptErr := fixture.authority.WithRuntime(
				context.Background(),
				attachment.Resource(),
				func(_ context.Context, engine *runtime.Engine) error {
					return test.accept(engine)
				},
			)
			if !errors.Is(acceptErr, test.wantAcceptErr) ||
				(test.wantAcceptErr == nil && acceptErr != nil) {
				t.Fatalf(
					"accept blocked runtime-bound work = %v, want %v",
					acceptErr,
					test.wantAcceptErr,
				)
			}
			maintenanceRan := false
			if err := fixture.authority.RunSessionMaintenance(
				block.AuthorizeMaintenance(context.Background()),
				sessionID.String(),
				func(context.Context, *session.Store, *ActiveRuntimeMaintenance) error {
					maintenanceRan = true
					if calls := client.callCount(); calls != 0 {
						t.Fatalf("model calls during maintenance = %d, want 0", calls)
					}
					return nil
				},
			); err != nil {
				t.Fatalf("run authorized maintenance: %v", err)
			}
			if !maintenanceRan {
				t.Fatal("authorized maintenance did not run")
			}
			if err := block.Close(context.Background()); err != nil {
				t.Fatalf("release Session start block: %v", err)
			}
			if err := fixture.authority.WithRuntime(
				context.Background(),
				attachment.Resource(),
				func(_ context.Context, engine *runtime.Engine) error {
					_, queueErr := engine.QueueUserMessage("later human input")
					return queueErr
				},
			); err != nil {
				t.Fatalf("queue later work: %v", err)
			}
			select {
			case <-client.firstStarted:
			case <-time.After(3 * time.Second):
				t.Fatal("later Agenda item did not run after maintenance")
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				fixture.authority.mu.Lock()
				resource := fixture.authority.resources[sessionID]
				fixture.authority.mu.Unlock()
				if resource == nil {
					break
				}
				resource.mu.Lock()
				idle := resource.current == nil
				resource.mu.Unlock()
				if idle {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("later Agenda execution did not finish")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
