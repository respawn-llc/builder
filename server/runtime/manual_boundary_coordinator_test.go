package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestManualBoundaryCoordinatorSealsOneGenerationAndFencesLateAdmission(t *testing.T) {
	coordinator := newManualBoundaryCoordinator()
	coordinator.beginGeneration()
	first, err := coordinator.enqueueForGeneration(context.Background(), compactionInstructionsInput{}, nil)
	if err != nil {
		t.Fatalf("enqueue first: %v", err)
	}
	detached := coordinator.sealAndTake()
	if len(detached) != 1 || detached[0] != first {
		t.Fatalf("detached generation entries = %+v, want first entry", detached)
	}

	lateCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lateDone := make(chan error, 1)
	go func() {
		_, err := coordinator.enqueueForGeneration(lateCtx, compactionInstructionsInput{}, nil)
		lateDone <- err
	}()
	select {
	case err := <-lateDone:
		t.Fatalf("late admission escaped sealed generation: %v", err)
	default:
	}

	coordinator.finishGeneration()
	coordinator.endTurn()
	select {
	case err := <-lateDone:
		if !errors.Is(err, errManualBoundaryNoGeneration) {
			t.Fatalf("late admission error = %v, want no-generation fence", err)
		}
	case <-time.After(time.Second):
		t.Fatal("late admission remained blocked after turn ended")
	}
}

func TestManualBoundaryCoordinatorRejectsAdmissionWithoutDispatchedGeneration(t *testing.T) {
	coordinator := newManualBoundaryCoordinator()
	_, err := coordinator.enqueueForGeneration(context.Background(), compactionInstructionsInput{}, nil)
	if !errors.Is(err, errManualBoundaryNoGeneration) {
		t.Fatalf("admission without generation error = %v, want no-generation error", err)
	}
}

func TestManualBoundaryCoordinatorRejectsArmedAdmissionWhenDispatchAborts(t *testing.T) {
	coordinator := newManualBoundaryCoordinator()
	coordinator.armNextGeneration()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := coordinator.enqueueForGeneration(ctx, compactionInstructionsInput{}, nil)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("armed admission completed before dispatch outcome: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	dispatchErr := errors.New("request preparation failed")
	coordinator.abortArmedGeneration(dispatchErr)
	select {
	case err := <-done:
		if !errors.Is(err, dispatchErr) {
			t.Fatalf("armed admission error = %v, want dispatch error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("armed admission remained blocked after dispatch abort")
	}
}

func TestManualBoundaryCoordinatorMovesLateAdmissionToNextGeneration(t *testing.T) {
	coordinator := newManualBoundaryCoordinator()
	firstGeneration := coordinator.beginGeneration()
	if firstGeneration == 0 {
		t.Fatal("first generation id is absent")
	}
	coordinator.sealAndTake()

	entryDone := make(chan *pendingManualCompaction, 1)
	go func() {
		entry, err := coordinator.enqueueForGeneration(context.Background(), compactionInstructionsInput{}, nil)
		if err != nil {
			t.Errorf("late admission: %v", err)
			return
		}
		entryDone <- entry
	}()
	select {
	case <-entryDone:
		t.Fatal("late admission escaped the sealed generation")
	case <-time.After(10 * time.Millisecond):
	}

	coordinator.finishGeneration()
	secondGeneration := coordinator.beginGeneration()
	if secondGeneration == firstGeneration {
		t.Fatalf("next generation id = %d, want a distinct generation", secondGeneration)
	}
	var entry *pendingManualCompaction
	select {
	case entry = <-entryDone:
	case <-time.After(time.Second):
		t.Fatal("late admission did not join the next generation")
	}
	if entry.generationID != secondGeneration {
		t.Fatalf("late entry generation = %d, want %d", entry.generationID, secondGeneration)
	}
}

func TestManualBoundaryCoordinatorCancellationOnlyRemovesQueuedGenerationEntry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coordinator := newManualBoundaryCoordinator()
	coordinator.beginGeneration()
	entry, err := coordinator.enqueueForGeneration(ctx, compactionInstructionsInput{}, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	cancel()
	if !coordinator.cancel(entry) {
		t.Fatal("queued cancellation did not win before execution ownership")
	}
	select {
	case result := <-entry.done:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("canceled result = %+v, want context cancellation", result)
		}
	default:
		t.Fatal("queued cancellation did not complete its waiter")
	}
}

func TestManualBoundaryCoordinatorExecutionOwnershipIsOneShot(t *testing.T) {
	coordinator := newManualBoundaryCoordinator()
	coordinator.beginGeneration()
	entry, err := coordinator.enqueueForGeneration(context.Background(), compactionInstructionsInput{}, nil)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	detached := coordinator.sealAndTake()
	if len(detached) != 1 || !coordinator.beginExecution(entry) {
		t.Fatal("sealed entry did not transfer execution ownership")
	}
	if coordinator.beginExecution(entry) {
		t.Fatal("execution ownership transferred twice")
	}
	entry.complete(manualCompactionResult{err: errors.New("first")})
	entry.complete(manualCompactionResult{err: errors.New("duplicate")})
	select {
	case result := <-entry.done:
		if result.err == nil || result.err.Error() != "first" {
			t.Fatalf("entry result = %+v, want first completion", result)
		}
	default:
		t.Fatal("entry did not complete")
	}
}
