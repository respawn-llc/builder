package runtimecommand

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStartGateWaitsUntilCommitted(t *testing.T) {
	gate := NewStartGate()
	waiting := make(chan error, 1)
	go func() {
		waiting <- gate.Wait(context.Background())
	}()

	select {
	case err := <-waiting:
		t.Fatalf("gate released before commit: %v", err)
	default:
	}
	if err := gate.Commit(); err != nil {
		t.Fatalf("commit gate: %v", err)
	}
	select {
	case err := <-waiting:
		if err != nil {
			t.Fatalf("wait committed gate: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("committed gate did not release waiter")
	}
}

func TestStartGateAbortJoinsBlockedOwnerWithoutStartingIt(t *testing.T) {
	gate := NewStartGate()
	waiting := make(chan error, 1)
	go func() {
		waiting <- gate.Wait(context.Background())
	}()
	if err := gate.Abort(errors.New("handoff failed")); err != nil {
		t.Fatalf("abort gate: %v", err)
	}
	select {
	case err := <-waiting:
		if !errors.Is(err, ErrStartGateAborted) || err.Error() != "runtime command start gate aborted: handoff failed" {
			t.Fatalf("wait aborted gate error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("aborted gate did not release waiter")
	}
	if err := gate.Commit(); !errors.Is(err, ErrStartGateSettled) {
		t.Fatalf("commit aborted gate error = %v, want ErrStartGateSettled", err)
	}
}

func TestStartGateCancellationOnlyCancelsTheWaiter(t *testing.T) {
	gate := NewStartGate()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled wait error = %v, want context canceled", err)
	}
	if err := gate.Commit(); err != nil {
		t.Fatalf("commit after canceled waiter: %v", err)
	}
}
