package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/tools"
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

func TestManualBoundaryCoordinatorOrdersPendingEntriesByAcceptanceOrder(t *testing.T) {
	coordinator := newManualBoundaryCoordinator()
	coordinator.beginGeneration()
	firstOrder := uint64(1)
	secondOrder := uint64(2)

	second, err := coordinator.enqueueForGenerationOrdered(
		context.Background(),
		compactionInstructionsInput{},
		nil,
		&secondOrder,
	)
	if err != nil {
		t.Fatalf("enqueue second accepted request: %v", err)
	}
	first, err := coordinator.enqueueForGenerationOrdered(
		context.Background(),
		compactionInstructionsInput{},
		nil,
		&firstOrder,
	)
	if err != nil {
		t.Fatalf("enqueue first accepted request: %v", err)
	}

	entries := coordinator.sealAndTake()
	if len(entries) != 2 || entries[0] != first || entries[1] != second {
		t.Fatalf("pending entries = %+v, want acceptance order first then second", entries)
	}
}

func TestManualBoundaryCoordinatorWaitsForEarlierAcceptanceBeforeSealing(t *testing.T) {
	coordinator := newManualBoundaryCoordinator()
	coordinator.beginGeneration()
	firstOrder := uint64(1)
	secondOrder := uint64(2)
	coordinator.registerAcceptance(&secondOrder, nil)
	second, err := coordinator.enqueueForGenerationOrdered(
		context.Background(),
		compactionInstructionsInput{},
		nil,
		&secondOrder,
	)
	if err != nil {
		t.Fatalf("enqueue second accepted request: %v", err)
	}

	sealed := make(chan []*pendingManualCompaction, 1)
	go func() {
		sealed <- coordinator.sealAndTake()
	}()
	select {
	case entries := <-sealed:
		t.Fatalf("sealed entries before earlier acceptance arrived: %+v", entries)
	case <-time.After(20 * time.Millisecond):
	}

	coordinator.registerAcceptance(&firstOrder, nil)
	first, err := coordinator.enqueueForGenerationOrdered(
		context.Background(),
		compactionInstructionsInput{},
		nil,
		&firstOrder,
	)
	if err != nil {
		t.Fatalf("enqueue first accepted request: %v", err)
	}
	select {
	case entries := <-sealed:
		if len(entries) != 2 || entries[0] != first || entries[1] != second {
			t.Fatalf("sealed entries = %+v, want acceptance order first then second", entries)
		}
	case <-time.After(time.Second):
		t.Fatal("boundary did not seal after all earlier accepted requests arrived")
	}
}

func TestManualBoundaryCoordinatorRebasesSettledAcceptanceOrder(t *testing.T) {
	coordinator := newManualBoundaryCoordinator()
	coordinator.beginGeneration()
	order := uint64(3)
	settledThrough := uint64(2)
	coordinator.registerAcceptance(&order, &settledThrough)
	entry, err := coordinator.enqueueForGenerationOrdered(
		context.Background(),
		compactionInstructionsInput{},
		nil,
		&order,
	)
	if err != nil {
		t.Fatalf("enqueue replacement-runtime request: %v", err)
	}
	entries := coordinator.sealAndTake()
	if len(entries) != 1 || entries[0] != entry {
		t.Fatalf("replacement-runtime entries = %+v, want one settled request", entries)
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

func TestOwnedManualCompactionWaitsForCompletionAfterCallerCancellation(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeCompactionClient{}, tools.NewRegistry(), Config{})
	steps := &stubExclusiveStepLifecycle{
		snapshot: &RunSnapshot{ActiveKind: ActiveKindUserTurn},
	}
	compactor := engine.compactionFlow.(*defaultContextCompactor)
	compactor.steps = steps
	engine.compactionRuntimeState().SetManualCompactionEligible(true)
	coordinator := engine.compactionRuntimeState().manualBoundaryCoordinator()
	coordinator.beginGeneration()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultDone := make(chan manualCompactionResult, 1)
	go func() {
		receipt, err := compactor.compactManualContext(ctx, compactionInstructionsInput{}, nil, true, nil, nil)
		resultDone <- manualCompactionResult{receipt: receipt, err: err}
	}()

	var entry *pendingManualCompaction
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		coordinator.mu.Lock()
		if coordinator.current != nil && len(coordinator.current.pending) == 1 {
			entry = coordinator.current.pending[0]
			coordinator.mu.Unlock()
			break
		}
		coordinator.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	if entry == nil {
		t.Fatal("manual compaction did not enqueue an active-generation entry")
	}
	detached := coordinator.sealAndTake()
	if len(detached) != 1 || detached[0] != entry || !coordinator.beginExecution(entry) {
		t.Fatal("manual compaction entry did not transfer execution ownership")
	}
	cancel()

	select {
	case result := <-resultDone:
		t.Fatalf("owned compaction returned before completion: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}

	lateErr := errors.New("owned compaction completed after cancellation")
	entry.complete(manualCompactionResult{err: lateErr})
	select {
	case result := <-resultDone:
		if !errors.Is(result.err, lateErr) || errors.Is(result.err, context.Canceled) {
			t.Fatalf("owned compaction result = %+v, want late completion error", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("owned compaction waiter did not receive completion")
	}
	coordinator.finishGeneration()
	coordinator.endTurn()
}
