package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeReactivatorSharesInFlightReactivation(t *testing.T) {
	reactivator := newRuntimeReactivator()
	started := make(chan struct{})
	release := make(chan struct{})
	var closeStarted sync.Once
	var calls atomic.Int32
	reactivator.SetReactivateFunc(func(context.Context) error {
		calls.Add(1)
		closeStarted.Do(func() { close(started) })
		<-release
		return nil
	})

	results := make(chan error, 2)
	go func() {
		results <- reactivator.Reactivate(context.Background())
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for shared reactivation to start")
	}
	go func() {
		results <- reactivator.Reactivate(context.Background())
	}()
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("reactivation call count during in-flight wait = %d, want 1", got)
	}
	close(release)

	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatalf("Reactivate result error = %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("reactivation call count = %d, want 1", got)
	}
}

func TestRuntimeReactivatorKeepsSharedReactivationAliveAfterInitiatorCancel(t *testing.T) {
	reactivator := newRuntimeReactivator()
	started := make(chan struct{})
	release := make(chan struct{})
	var closeStarted sync.Once
	var calls atomic.Int32
	reactivator.SetReactivateFunc(func(ctx context.Context) error {
		calls.Add(1)
		closeStarted.Do(func() { close(started) })
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	})

	results := make(chan error, 2)
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	go func() {
		results <- reactivator.Reactivate(firstCtx)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reactivation to start")
	}
	go func() {
		results <- reactivator.Reactivate(context.Background())
	}()

	firstCancel()
	if first := <-results; first != context.Canceled {
		t.Fatalf("first Reactivate error = %v, want %v", first, context.Canceled)
	}
	select {
	case second := <-results:
		t.Fatalf("second Reactivate completed before shared reactivation release: %v", second)
	case <-time.After(50 * time.Millisecond):
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("reactivation call count after initiator cancel = %d, want 1", got)
	}

	close(release)
	if second := <-results; second != nil {
		t.Fatalf("second Reactivate error = %v", second)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("reactivation call count after shared reactivation release = %d, want 1", got)
	}
}
