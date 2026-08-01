package runtimeops

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/serverapi"
)

type terminalOperationRequest struct {
	SessionID string
	Args      string
}

func TestCoordinatorRetainsTypedTerminalRejectionForOperationReplay(t *testing.T) {
	t.Parallel()

	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindCompact)
	req := terminalOperationRequest{SessionID: "session-1", Args: "keep"}
	rejection := &serverapi.ManualCompactionAdmissionError{
		Reason: serverapi.ManualCompactionAdmissionTooSoon,
	}
	attempts := 0
	run := func(context.Context, Attempt) (struct{}, error) {
		attempts++
		return struct{}{}, rejection
	}

	if _, err := Do(coord, context.Background(), "session-1", ref, req, func(a, b terminalOperationRequest) bool {
		return a == b
	}, run); !errors.Is(err, rejection) {
		t.Fatalf("first operation error = %v, want typed rejection", err)
	}
	if _, err := Do(coord, context.Background(), "session-1", ref, req, func(a, b terminalOperationRequest) bool {
		return a == b
	}, run); !errors.Is(err, rejection) {
		t.Fatalf("replayed operation error = %v, want retained typed rejection", err)
	}
	if attempts != 1 {
		t.Fatalf("provider attempts = %d, want one retained terminal outcome", attempts)
	}
}

func TestCoordinatorAttemptOnlyCancellationDoesNotRequestActiveInterrupt(t *testing.T) {
	t.Parallel()

	coord := NewCoordinator()
	ref := testRuntimeOperationRef(clientui.RuntimeOperationKindCompact)
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := Do(coord, context.Background(), "session-1", ref, terminalOperationRequest{}, func(a, b terminalOperationRequest) bool { return a == b }, func(_ context.Context, attempt Attempt) (struct{}, error) {
			close(started)
			<-attempt.Context().Done()
			return struct{}{}, attempt.Context().Err()
		})
		done <- err
	}()
	<-started
	coord.MarkOperationAttemptOnly("session-1", ref)
	result, err := coord.CancelOperationTarget("session-1", ref)
	if err != nil {
		t.Fatalf("CancelOperationTarget: %v", err)
	}
	if result.InterruptActive {
		t.Fatal("attempt-only cancellation requested active-run interruption")
	}
	result.CancelOperationAttempt()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("attempt-only cancellation did not cancel operation attempt")
	}
}
