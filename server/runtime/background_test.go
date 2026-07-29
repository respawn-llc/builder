package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"

	"github.com/google/uuid"
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

func TestBackgroundDeliveryRetirementTracksReservedAndDiagnosticOnlyWork(t *testing.T) {
	engine := &Engine{}
	scheduler := &defaultBackgroundNoticeScheduler{engine: engine}
	activity := uuid.New()
	notice := newTerminalBackgroundNotice(
		"1000",
		activity,
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventDefault,
			true,
			[]llm.Message{{Role: llm.RoleDeveloper, Content: textutil.Value("terminal")}},
		),
	)
	diagnostic := newPendingBackgroundDeliveryDiagnostic(
		"1000",
		activity,
		backgroundDeliveryStageAutomaticSteering,
		1,
		errors.New("steering failed"),
	)
	notice.diagnostic = &diagnostic

	shouldSchedule := false
	scheduler.admitNotice(notice, false, &shouldSchedule)
	if shouldSchedule {
		t.Fatal("unscheduled admission unexpectedly requested a lifecycle task")
	}
	pending := scheduler.RetirementSnapshot()
	if !pending.Active {
		t.Fatal("pending background delivery did not retain its runtime")
	}

	reserved := scheduler.DrainPendingNotices()
	if len(reserved) != 1 || !sameBackgroundNotice(reserved[0], notice) {
		t.Fatalf("reserved notices = %+v, want terminal notice", reserved)
	}
	reservation := scheduler.RetirementSnapshot()
	if !reservation.Active {
		t.Fatal("reserved background delivery did not retain its runtime")
	}
	select {
	case <-pending.Changed:
	default:
		t.Fatal("reservation did not publish a retirement change")
	}

	scheduler.FinalizeCommittedBackgroundNotice(notice, session.CommitReceipt{Committed: true})
	diagnosticOnly := scheduler.RetirementSnapshot()
	if !diagnosticOnly.Active {
		t.Fatal("diagnostic-only delivery did not retain its runtime")
	}
	if len(scheduler.states) != 1 {
		t.Fatalf("scheduler states = %+v, want one diagnostic-only state", scheduler.states)
	}
	if _, ok := scheduler.states[0].(diagnosticOnlyBackgroundNotice); !ok {
		t.Fatalf("scheduler state = %T, want diagnostic-only", scheduler.states[0])
	}

	scheduler.clearCommittedDeliveryDiagnostic("1000", activity)
	settled := scheduler.RetirementSnapshot()
	if settled.Active {
		t.Fatalf("settled diagnostic retained runtime: %+v", settled)
	}
	select {
	case <-diagnosticOnly.Changed:
	default:
		t.Fatal("diagnostic settlement did not publish a retirement change")
	}
}

func TestBackgroundDeliveryRetryRequiresAnExternalPermit(t *testing.T) {
	engine := &Engine{}
	scheduler := &defaultBackgroundNoticeScheduler{engine: engine}
	notice := newTerminalBackgroundNotice(
		"1001",
		uuid.New(),
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventDefault,
			true,
			[]llm.Message{{Role: llm.RoleDeveloper, Content: textutil.Value("terminal")}},
		),
	)
	shouldSchedule := false
	scheduler.admitNotice(notice, false, &shouldSchedule)
	reserved := scheduler.DrainPendingNotices()
	if len(reserved) != 1 {
		t.Fatalf("reserved notices = %+v, want one", reserved)
	}
	scheduler.restoreUncommittedReservations(reserved, errors.New("persistence failed"))
	if scheduler.hasDeliverableNotice() {
		t.Fatal("failed delivery became self-retryable without an external permit")
	}
	if scheduler.PermitRetry() != true {
		t.Fatal("external retry permit was not granted")
	}
	if !scheduler.hasDeliverableNotice() {
		t.Fatal("external retry permit did not authorize deferred delivery")
	}
	if scheduler.PermitRetry() {
		t.Fatal("duplicate external retry permit was accepted")
	}
}

func TestBackgroundDeliveryWithdrawalClassifiesReservedReceipt(t *testing.T) {
	engine := &Engine{}
	scheduler := &defaultBackgroundNoticeScheduler{engine: engine}
	activity := uuid.New()
	notice := newTerminalBackgroundNotice(
		"1002",
		activity,
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventDefault,
			true,
			[]llm.Message{{Role: llm.RoleDeveloper, Content: textutil.Value("terminal")}},
		),
	)
	shouldSchedule := false
	scheduler.admitNotice(notice, false, &shouldSchedule)
	reserved := scheduler.DrainPendingNotices()
	if len(reserved) != 1 {
		t.Fatalf("reserved notices = %+v, want one", reserved)
	}
	diagnostic := newPendingBackgroundDeliveryDiagnostic(
		"1002",
		activity,
		backgroundDeliveryStageAutomaticSteering,
		1,
		errors.New("append failed"),
	)
	reserved[0].diagnostic = &diagnostic
	reservation := scheduler.states[0].(reservedBackgroundNotice)
	scheduler.states[0] = newWithdrawingBackgroundNotice(reservation.notice, reservation.reservation)
	scheduler.restoreUncommittedReservations(reserved, errors.New("append failed"))

	withdrawal, found, err := scheduler.Withdraw(context.Background(), "1002", activity)
	if err != nil || !found {
		t.Fatalf("withdrawal = %+v found=%t error=%v", withdrawal, found, err)
	}
	if !withdrawal.CompletionPending || withdrawal.Diagnostic == nil {
		t.Fatalf("withdrawal = %+v, want pending completion and diagnostic", withdrawal)
	}
	if scheduler.RetirementSnapshot().Active {
		t.Fatalf("withdrawn scheduler work retained runtime: %+v", scheduler.states)
	}
}
