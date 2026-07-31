package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
)

type blockingBackgroundStepLifecycle struct {
	started chan struct{}
	stopped chan error
	busy    bool
}

type retainingBackgroundMutationLease struct {
	released chan struct{}
}

func (l *retainingBackgroundMutationLease) OrderedMutation(context.Context, func(OrderedMutationTurn) error) error {
	return errors.New("retained background test lease is not executable")
}

func (l *retainingBackgroundMutationLease) Release() error {
	select {
	case l.released <- struct{}{}:
	default:
	}
	return nil
}

type retainingBackgroundMutationTurn struct {
	lease OrderedMutationLease
}

func (t retainingBackgroundMutationTurn) Apply(apply func() error) error {
	return apply()
}

func (t retainingBackgroundMutationTurn) RetainLease() (OrderedMutationLease, error) {
	return t.lease, nil
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
func (s *blockingBackgroundStepLifecycle) IsBusy() bool { return s.busy }
func (s *blockingBackgroundStepLifecycle) Snapshot() *RunSnapshot {
	return nil
}
func (s *blockingBackgroundStepLifecycle) WithActiveStep(func(stepID string) error) (bool, error) {
	return false, nil
}
func (s *blockingBackgroundStepLifecycle) ApplyForActiveStep(string, func() error) error {
	return ErrActiveStepInactive
}

func TestBackgroundShellUpdateAppliesThroughLexicalTurn(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	dispatchCalled := false
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OrderedMutation: func(func(OrderedMutationTurn) error) error {
			dispatchCalled = true
			return errors.New("background update attempted nested dispatch")
		},
	})

	engine.HandleBackgroundShellUpdateWithOrderedTurn(
		testOrderedMutationTurn{},
		BackgroundShellEvent{Type: BackgroundShellEventCompleted, ID: "process-1"},
		false,
	)
	if dispatchCalled {
		t.Fatal("background update attempted to re-enter the Session dispatcher")
	}
}

func TestBackgroundNoticeReleasesRetainedLexicalCapacityAfterApply(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	engine.ensureOrchestrationCollaborators()
	scheduler, ok := engine.backgroundFlow.(*defaultBackgroundNoticeScheduler)
	if !ok {
		t.Fatal("background scheduler has unexpected implementation")
	}
	lease := &retainingBackgroundMutationLease{released: make(chan struct{}, 1)}
	if err := engine.HandleBackgroundShellUpdateWithOrderedTurn(
		retainingBackgroundMutationTurn{lease: lease},
		BackgroundShellEvent{Type: BackgroundShellEventCompleted, ID: "process-1"},
		true,
	); err != nil {
		t.Fatalf("background update: %v", err)
	}
	batch := scheduler.ClaimPendingNotices(backgroundNoticeClaimCombinedFlush)
	if !batch.BeginApply() {
		t.Fatal("background notice batch did not begin")
	}
	if _, err := batch.Apply(func(steeringIntent) error { return nil }); err != nil {
		t.Fatalf("background notice apply: %v", err)
	}
	select {
	case <-lease.released:
	case <-time.After(2 * time.Second):
		t.Fatal("retained background capacity was not released after notice application")
	}
}

func TestBackgroundNoticeReleasesRetainedLexicalCapacityOnCancellation(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	engine.ensureOrchestrationCollaborators()
	scheduler, ok := engine.backgroundFlow.(*defaultBackgroundNoticeScheduler)
	if !ok {
		t.Fatal("background scheduler has unexpected implementation")
	}
	lease := &retainingBackgroundMutationLease{released: make(chan struct{}, 1)}
	if err := engine.HandleBackgroundShellUpdateWithOrderedTurn(
		retainingBackgroundMutationTurn{lease: lease},
		BackgroundShellEvent{Type: BackgroundShellEventKilled, ID: "process-2"},
		true,
	); err != nil {
		t.Fatalf("background update: %v", err)
	}
	scheduler.CancelPendingBackgroundNotices()
	select {
	case <-lease.released:
	case <-time.After(2 * time.Second):
		t.Fatal("retained background capacity was not released on cancellation")
	}
}

func TestCombinedUserFlushAppliesRetainedNoticeInCurrentLexicalTurn(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	engine.ensureOrchestrationCollaborators()
	lease := &retainingBackgroundMutationLease{released: make(chan struct{}, 1)}
	if err := engine.HandleBackgroundShellUpdateWithOrderedTurn(
		retainingBackgroundMutationTurn{lease: lease},
		BackgroundShellEvent{Type: BackgroundShellEventCompleted, ID: "process-3"},
		true,
	); err != nil {
		t.Fatalf("background update: %v", err)
	}
	engine.QueueUserMessage("queued input")
	result, err := engine.commitPendingUserInjectionsInTurn("step-1", allPendingUserInjectionSelection{}, testOrderedMutationTurn{})
	if err != nil {
		t.Fatalf("combined user flush: %v", err)
	}
	if result.flushed != 2 || result.continueCombinedFlush {
		t.Fatalf("combined flush result = %+v, want user and notice applied in-turn", result)
	}
	select {
	case <-lease.released:
	case <-time.After(2 * time.Second):
		t.Fatal("combined flush did not release retained notice capacity")
	}
}

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

func TestBackgroundNoticeBatchSuppressesClaimedTerminalNoticeBeforeApply(t *testing.T) {
	scheduler := &defaultBackgroundNoticeScheduler{
		engine: &Engine{},
		steps:  &blockingBackgroundStepLifecycle{busy: true},
	}
	scheduler.QueueDeveloperNotice(llm.Message{
		Role:    llm.RoleDeveloper,
		Name:    textutil.Value("process-1"),
		Content: textutil.Value("terminal notice"),
	})

	batch := scheduler.ClaimPendingNotices(backgroundNoticeClaimCombinedFlush)
	if batch.Empty() {
		t.Fatal("combined flush did not claim the terminal notice")
	}
	if result := scheduler.SuppressPendingBackgroundNotice("process-1"); !result.matched || result.disposition != backgroundNoticeSuppressed {
		t.Fatalf("suppression result = %#v, want matched suppressed", result)
	}
	if batch.BeginApply() {
		t.Fatal("suppressed claim became applicable")
	}
	if scheduler.HasPendingNotices() {
		t.Fatal("suppressed notice remained pending")
	}
}

func TestBackgroundNoticeBatchSettlesEachEntryExactlyOnceAfterFailure(t *testing.T) {
	scheduler := &defaultBackgroundNoticeScheduler{
		engine: &Engine{},
		steps:  &blockingBackgroundStepLifecycle{busy: true},
	}
	for _, processID := range []string{"process-1", "process-2", "process-3"} {
		scheduler.QueueDeveloperNotice(llm.Message{
			Role:    llm.RoleDeveloper,
			Name:    textutil.Value(processID),
			Content: textutil.Value(processID),
		})
	}

	batch := scheduler.ClaimPendingNotices(backgroundNoticeClaimCombinedFlush)
	if !batch.BeginApply() {
		t.Fatal("claimed notices did not begin application")
	}
	calls := 0
	errExpected := errors.New("steering failed")
	_, err := batch.Apply(func(steeringIntent) error {
		calls++
		if calls == 2 {
			return errExpected
		}
		return nil
	})
	if !errors.Is(err, errExpected) {
		t.Fatalf("apply error = %v, want %v", err, errExpected)
	}
	if calls != 2 {
		t.Fatalf("steering calls = %d, want 2", calls)
	}
	if got := batch.Dispositions(); len(got) != 3 ||
		got[0] != backgroundNoticeApplied ||
		got[1] != backgroundNoticeFailed ||
		got[2] != backgroundNoticeNotAppliedAfterPriorFailure {
		t.Fatalf("batch dispositions = %v", got)
	}
	if scheduler.HasPendingNotices() {
		t.Fatal("terminally settled notices remained pending")
	}
}

func TestBackgroundNoticeSchedulerCancelsPendingClaimsOnShutdown(t *testing.T) {
	scheduler := &defaultBackgroundNoticeScheduler{
		engine: &Engine{},
		steps:  &blockingBackgroundStepLifecycle{busy: true},
	}
	scheduler.QueueDeveloperNotice(llm.Message{
		Role:    llm.RoleDeveloper,
		Name:    textutil.Value("process-1"),
		Content: textutil.Value("terminal notice"),
	})
	batch := scheduler.ClaimPendingNotices(backgroundNoticeClaimStandalone)
	if batch.Empty() {
		t.Fatal("shutdown test did not claim notice")
	}
	scheduler.CancelPendingBackgroundNotices()
	if scheduler.HasPendingNotices() {
		t.Fatal("shutdown left claimed notice pending")
	}
	if batch.BeginApply() {
		t.Fatal("canceled shutdown claim became applicable")
	}
}
