package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

type blockingBackgroundStepLifecycle struct {
	started chan struct{}
	stopped chan error
}

func (s *blockingBackgroundStepLifecycle) Run(ctx context.Context, _ exclusiveStepOptions, _ func(stepCtx context.Context, stepID string) error) error {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	err := ctx.Err()
	select {
	case s.stopped <- err:
	default:
	}
	return err
}

func (s *blockingBackgroundStepLifecycle) RunNext(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error {
	return s.Run(ctx, options, fn)
}

func (s *blockingBackgroundStepLifecycle) AcquireReservation(*exclusiveStepReservation) error {
	return nil
}
func (s *blockingBackgroundStepLifecycle) ReleaseReservation(*exclusiveStepReservation) {}
func (s *blockingBackgroundStepLifecycle) Interrupt() error                             { return nil }
func (s *blockingBackgroundStepLifecycle) InterruptCurrent(func(*RunSnapshot)) (*RunSnapshot, error) {
	return nil, nil
}
func (s *blockingBackgroundStepLifecycle) IsBusy() bool { return false }
func (s *blockingBackgroundStepLifecycle) Snapshot() *RunSnapshot {
	return nil
}
func (s *blockingBackgroundStepLifecycle) WithActiveStep(func(stepID string) error) (bool, error) {
	return false, nil
}
func (s *blockingBackgroundStepLifecycle) ApplyForActiveStep(string, func() error) error {
	return ErrActiveStepInactive
}
func (s *blockingBackgroundStepLifecycle) DrainAgentStepBoundary(context.Context) error {
	return nil
}
func (s *blockingBackgroundStepLifecycle) EndAgentStepBoundary() {}

func TestBackgroundNoticeSchedulerCancelsQueuedContinuationOnEngineClose(t *testing.T) {
	t.Parallel()
	steps := &blockingBackgroundStepLifecycle{
		started: make(chan struct{}, 1),
		stopped: make(chan error, 1),
	}
	eng := &Engine{}
	scheduler := &defaultBackgroundNoticeScheduler{engine: eng, steps: steps}

	scheduler.QueueDeveloperNotice(llm.Message{Role: llm.RoleDeveloper, Content: textutil.Value("queued background notice")})

	select {
	case <-steps.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background continuation did not start")
	}

	closeDone := make(chan struct{})
	go func() {
		_ = eng.Close()
		close(closeDone)
	}()

	select {
	case err := <-steps.stopped:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("step lifecycle stopped with %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("background continuation was not canceled on engine close")
	}

	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("engine close did not wait for queued background continuation")
	}
}

func TestBackgroundNoticeSchedulerSchedulingRaceWithEngineCloseDoesNotPanic(t *testing.T) {
	t.Parallel()
	for i := 0; i < 200; i++ {
		steps := &blockingBackgroundStepLifecycle{
			started: make(chan struct{}, 1),
			stopped: make(chan error, 1),
		}
		eng := &Engine{}
		scheduler := &defaultBackgroundNoticeScheduler{engine: eng, steps: steps}
		panicErrs := make(chan error, 4)
		start := make(chan struct{})
		var wg sync.WaitGroup

		runSafe := func(fn func()) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if recovered := recover(); recovered != nil {
						panicErrs <- fmt.Errorf("panic: %v", recovered)
					}
				}()
				<-start
				fn()
			}()
		}

		runSafe(func() {
			scheduler.QueueDeveloperNotice(llm.Message{Role: llm.RoleDeveloper, Content: textutil.Value("queued background notice")})
		})
		runSafe(func() {
			scheduler.QueueDeveloperNotice(llm.Message{Role: llm.RoleDeveloper, Content: textutil.Value("queued schedule-if-idle")})
			scheduler.ScheduleIfIdle()
		})
		runSafe(func() {
			_ = eng.Close()
		})

		close(start)
		wg.Wait()
		close(panicErrs)
		for err := range panicErrs {
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
		}

		select {
		case err := <-steps.stopped:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("iteration %d: stopped with %v, want context canceled", i, err)
			}
		default:
		}

		closeDone := make(chan struct{})
		go func() {
			_ = eng.Close()
			close(closeDone)
		}()
		select {
		case <-closeDone:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: close remained blocked after race", i)
		}
	}
}

func TestBackgroundNoticeSchedulerPreservesNoticeWhenMetaContextPreparationFails(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	mustBlockTestEventLogAppends(t, store)
	steps := &stubExclusiveStepLifecycle{busy: true}
	scheduler := &defaultBackgroundNoticeScheduler{engine: engine, steps: steps}

	scheduler.QueueDeveloperNotice(llm.Message{
		Role:    llm.RoleDeveloper,
		Content: textutil.Value("queued background notice"),
	})

	if _, err := scheduler.runQueuedNotices(context.Background()); err == nil {
		t.Fatal("background notice preparation unexpectedly succeeded")
	}
	if !scheduler.HasPendingNotices() {
		t.Fatal("background notice was lost after meta-context preparation failed")
	}
}

func TestBackgroundNoticeOwnershipFollowsWriteStdinCompletionCommitReceipt(t *testing.T) {
	for _, tt := range []struct {
		name        string
		block       bool
		wantPending bool
	}{
		{name: "committed"},
		{name: "uncommitted append failure", block: true, wantPending: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
			steps := &stubExclusiveStepLifecycle{busy: true}
			scheduler := &defaultBackgroundNoticeScheduler{engine: engine, steps: steps}
			engine.stepLifecycle = steps
			engine.backgroundFlow = scheduler

			scheduler.QueueDeveloperNotice(llm.Message{
				Role:    llm.RoleDeveloper,
				Name:    textutil.Value("42"),
				Content: textutil.Value("queued background notice"),
			})
			if tt.block {
				mustBlockTestEventLogAppends(t, store)
			}

			presentation := transcript.NormalizeToolCallMeta(transcript.ToolCallMeta{ToolName: string(toolspec.ToolWriteStdin)})
			receipt, err := engine.persistToolCompletionRaw("step", tools.Result{
				CallID:       "write-stdin-call",
				Name:         toolspec.ToolWriteStdin,
				Output:       json.RawMessage(`{"background_session_id":42,"background_running":false,"backgrounded":true}`),
				Presentation: &presentation,
			})
			if receipt.Committed == tt.wantPending {
				t.Fatalf("completion receipt = %+v, want committed=%t", receipt, !tt.wantPending)
			}
			if tt.wantPending && err == nil {
				t.Fatal("uncommitted completion did not surface append failure")
			}
			if !tt.wantPending && err != nil {
				t.Fatalf("persist committed completion: %v", err)
			}
			if got := scheduler.HasPendingNotices(); got != tt.wantPending {
				t.Fatalf("pending notice after completion = %t, want %t", got, tt.wantPending)
			}
		})
	}
}

func TestBackgroundNoticeSchedulerRestoresUncommittedSteerFailure(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	steps := &stubExclusiveStepLifecycle{busy: true}
	scheduler := &defaultBackgroundNoticeScheduler{engine: engine, steps: steps}
	if err := engine.ensureMetaContextForRequest(context.Background(), "seed"); err != nil {
		t.Fatalf("prepare meta context: %v", err)
	}
	for _, sessionID := range []string{"first", "second"} {
		scheduler.QueueDeveloperNotice(llm.Message{
			Role:    llm.RoleDeveloper,
			Name:    textutil.Value(sessionID),
			Content: textutil.Value(sessionID + " notice"),
		})
	}
	mustBlockTestEventLogAppends(t, store)

	if _, err := scheduler.runQueuedNotices(context.Background()); err == nil {
		t.Fatal("background notice steer unexpectedly succeeded")
	}
	pending := scheduler.pendingSnapshot()
	if len(pending) != 2 || pending[0].sessionID != "first" || pending[1].sessionID != "second" {
		t.Fatalf("restored pending notices = %+v", pending)
	}
}

func TestFlushPendingUserInjectionsRestoresOnlyLaterNoticeAfterCommittedObserverFailure(t *testing.T) {
	observerErr := errors.New("background notice observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	steps := &stubExclusiveStepLifecycle{busy: true}
	scheduler := &defaultBackgroundNoticeScheduler{engine: engine, steps: steps}
	lifecycle := newDefaultMessageLifecycle(engine, scheduler)
	for _, sessionID := range []string{"first", "second"} {
		scheduler.QueueDeveloperNotice(llm.Message{
			Role:    llm.RoleDeveloper,
			Name:    textutil.Value(sessionID),
			Content: textutil.Value(sessionID + " notice"),
		})
	}
	gate.FailNext(observerErr)

	_, err := lifecycle.FlushPendingUserInjections("step", allPendingUserInjectionSelection{})
	if !errors.Is(err, observerErr) {
		t.Fatalf("flush error = %v, want observer failure", err)
	}
	pending := scheduler.pendingSnapshot()
	if len(pending) != 1 || pending[0].sessionID != "second" {
		t.Fatalf("pending notices after committed failure = %+v", pending)
	}
}
