package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/tools"
)

func TestPreLiveGroupStartFailuresEmitNoBatch(t *testing.T) {
	t.Run("StepBegan sink failure", func(t *testing.T) {
		stepBeganErr := errors.New("step-began sink failed")
		var events []Event
		sink := &callbackStepLifecycleSink{onTransition: func(transition StepLifecycleTransition) error {
			if transition == StepLifecycleTransitionBegan {
				return stepBeganErr
			}
			return nil
		}}
		eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{
			Model:         "gpt-5",
			StepLifecycle: sink,
			OnEvent: func(event Event) {
				events = append(events, event)
			},
		})
		lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
		bodyRan := false

		err := lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			bodyRan = true
			return nil
		})

		if !errors.Is(err, stepBeganErr) {
			t.Fatalf("run error = %v, want StepBegan error", err)
		}
		if bodyRan {
			t.Fatal("StepBegan failure entered the step body")
		}
		assertNoLiveRunBatchOrGroup(t, eng, events)
		runStates := collectRunStateEvents(events)
		if len(runStates) != 1 || runStates[0].Status != RunStatusFailed {
			t.Fatalf("start-failed run states = %+v, want one failed state", runStates)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		var events []Event
		eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{
			Model: "gpt-5",
			OnEvent: func(event Event) {
				events = append(events, event)
			},
		})
		lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := lifecycle.Run(ctx, exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			t.Fatal("canceled start entered the step body")
			return nil
		})

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run error = %v, want context canceled", err)
		}
		assertNoLiveRunBatchOrGroup(t, eng, events)
	})

	t.Run("busy", func(t *testing.T) {
		var events []Event
		eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{
			Model: "gpt-5",
			OnEvent: func(event Event) {
				events = append(events, event)
			},
		})
		lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
		started := make(chan struct{})
		release := make(chan struct{})
		firstDone := make(chan error, 1)
		go func() {
			firstDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindRuntimeMaintenance}, func(context.Context, string) error {
				close(started)
				<-release
				return nil
			})
		}()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for busy maintenance step")
		}

		err := lifecycle.Run(context.Background(), exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			t.Fatal("busy start entered the step body")
			return nil
		})
		if !errors.Is(err, ErrAgentBusy) {
			t.Fatalf("run error = %v, want ErrAgentBusy", err)
		}
		assertNoLiveRunBatchOrGroup(t, eng, events)
		close(release)
		if err := <-firstDone; err != nil {
			t.Fatalf("maintenance step: %v", err)
		}
	})

	t.Run("reservation pending", func(t *testing.T) {
		var events []Event
		eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{
			Model: "gpt-5",
			OnEvent: func(event Event) {
				events = append(events, event)
			},
		})
		lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
		held := &exclusiveStepReservation{Kind: exclusiveStepReservationManualCompaction}
		if err := lifecycle.AcquireReservation(held); err != nil {
			t.Fatalf("acquire reservation: %v", err)
		}
		t.Cleanup(func() {
			lifecycle.ReleaseReservation(held)
		})

		err := lifecycle.RunNext(context.Background(), exclusiveStepOptions{
			EmitRunState: true,
			ActiveKind:   ActiveKindCompaction,
			Reservation:  &exclusiveStepReservation{Kind: exclusiveStepReservationManualCompaction},
		}, func(context.Context, string) error {
			t.Fatal("reservation-pending start entered the step body")
			return nil
		})
		if !errors.Is(err, ErrExclusiveStepReservationPending) {
			t.Fatalf("run-next error = %v, want ErrExclusiveStepReservationPending", err)
		}
		assertNoLiveRunBatchOrGroup(t, eng, events)
	})
}

func assertNoLiveRunBatchOrGroup(t *testing.T, eng *Engine, events []Event) {
	t.Helper()
	if eng.HasActiveLiveRunGroup() {
		t.Fatal("pre-live-group start failure leaked a live-run group")
	}
	for _, event := range events {
		if event.Kind == EventLiveRunBatchFinished {
			t.Fatalf("pre-live-group start failure emitted batch-finished event: %+v", event)
		}
	}
}
