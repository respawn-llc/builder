package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"core/server/llm"
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

func (s *blockingBackgroundStepLifecycle) Interrupt() error { return nil }
func (s *blockingBackgroundStepLifecycle) IsBusy() bool     { return false }
func (s *blockingBackgroundStepLifecycle) Snapshot() *RunSnapshot {
	return nil
}
func (s *blockingBackgroundStepLifecycle) WithActiveStep(func(stepID string) error) (bool, error) {
	return false, nil
}

func TestBackgroundNoticeSchedulerCancelsQueuedContinuationOnEngineClose(t *testing.T) {
	steps := &blockingBackgroundStepLifecycle{
		started: make(chan struct{}, 1),
		stopped: make(chan error, 1),
	}
	eng := &Engine{}
	scheduler := &defaultBackgroundNoticeScheduler{engine: eng, steps: steps}

	scheduler.QueueDeveloperNotice(llm.Message{Role: llm.RoleDeveloper, Content: "queued background notice"})

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
			scheduler.QueueDeveloperNotice(llm.Message{Role: llm.RoleDeveloper, Content: "queued background notice"})
		})
		runSafe(func() {
			scheduler.QueueDeveloperNotice(llm.Message{Role: llm.RoleDeveloper, Content: "queued schedule-if-idle"})
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
