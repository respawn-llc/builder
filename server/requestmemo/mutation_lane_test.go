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

func TestMutationLaneRegistrySharedAcquisitionAllowsPeersAndBlocksExclusive(t *testing.T) {
	registry := NewMutationLaneRegistry[string]()
	first, err := registry.AcquireShared(context.Background(), "workflow")
	if err != nil {
		t.Fatalf("AcquireShared first: %v", err)
	}
	second, err := registry.AcquireShared(context.Background(), "workflow")
	if err != nil {
		t.Fatalf("AcquireShared second: %v", err)
	}
	acquired := make(chan *MutationLaneLease[string], 1)
	go func() {
		lease, acquireErr := registry.Acquire(context.Background(), "workflow")
		if acquireErr == nil {
			acquired <- lease
		}
	}()
	select {
	case lease := <-acquired:
		lease.Release()
		t.Fatal("exclusive lease acquired while shared leases were held")
	case <-time.After(20 * time.Millisecond):
	}
	first.Release()
	second.Release()
	select {
	case lease := <-acquired:
		lease.Release()
	case <-time.After(time.Second):
		t.Fatal("exclusive lease remained blocked after shared leases released")
	}
}

func TestMutationLaneRegistrySharedAcquisitionBypassesWaitingExclusiveLease(t *testing.T) {
	registry := NewMutationLaneRegistry[string]()
	first, err := registry.AcquireShared(context.Background(), "workflow")
	if err != nil {
		t.Fatalf("AcquireShared first: %v", err)
	}
	exclusive := make(chan *MutationLaneLease[string], 1)
	go func() {
		lease, acquireErr := registry.Acquire(context.Background(), "workflow")
		if acquireErr == nil {
			exclusive <- lease
		}
	}()
	select {
	case lease := <-exclusive:
		lease.Release()
		t.Fatal("exclusive lease acquired while a shared lease was held")
	case <-time.After(20 * time.Millisecond):
	}
	peer, err := registry.AcquireShared(context.Background(), "workflow")
	if err != nil {
		t.Fatalf("AcquireShared peer behind waiting exclusive lease: %v", err)
	}
	peer.Release()
	first.Release()
	select {
	case lease := <-exclusive:
		lease.Release()
	case <-time.After(time.Second):
		t.Fatal("exclusive lease remained blocked after shared leases released")
	}
}
