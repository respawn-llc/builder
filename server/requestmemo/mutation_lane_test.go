package requestmemo

import (
	"context"
	"testing"
	"time"
)

func TestMutationLaneRegistrySerializesEqualKeysAndAllowsDifferentKeys(t *testing.T) {
	registry := NewMutationLaneRegistry[string]()
	first, err := registry.Acquire(context.Background(), "first")
	if err != nil {
		t.Fatalf("acquire first: %v", err)
	}
	firstReleased := false
	defer func() {
		if !firstReleased {
			first.Release()
		}
	}()

	equal := make(chan *MutationLaneLease[string], 1)
	go func() {
		lease, acquireErr := registry.Acquire(context.Background(), "first")
		if acquireErr != nil {
			t.Errorf("acquire equal key: %v", acquireErr)
			return
		}
		equal <- lease
	}()
	different, err := registry.Acquire(context.Background(), "second")
	if err != nil {
		t.Fatalf("acquire different key: %v", err)
	}
	different.Release()
	select {
	case lease := <-equal:
		lease.Release()
		t.Fatal("equal key acquired before first release")
	default:
	}
	first.Release()
	firstReleased = true
	select {
	case lease := <-equal:
		lease.Release()
	case <-time.After(time.Second):
		t.Fatal("equal key did not acquire after release")
	}
}

func TestMutationLaneRegistryCancelsWaitingLeaseAndRemovesIdleEntry(t *testing.T) {
	registry := NewMutationLaneRegistry[string]()
	first, err := registry.Acquire(context.Background(), "key")
	if err != nil {
		t.Fatalf("acquire first: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan error, 1)
	go func() {
		_, acquireErr := registry.Acquire(ctx, "key")
		waiting <- acquireErr
	}()
	cancel()
	if err := <-waiting; err != context.Canceled {
		t.Fatalf("canceled acquisition error = %v, want context canceled", err)
	}
	first.Release()
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if len(registry.entries) != 0 {
		t.Fatalf("idle entries = %d, want 0", len(registry.entries))
	}
}
