package mutationlane

import (
	"context"
	"testing"
	"time"
)

func TestRegistrySerializesEqualKeys(t *testing.T) {
	registry := NewRegistry[string]()
	first, err := registry.Acquire(context.Background(), "workspace-1")
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	firstReleased := false
	t.Cleanup(func() {
		if !firstReleased {
			first.Release()
		}
	})

	type acquisition struct {
		lease *Lease[string]
		err   error
	}
	secondAcquired := make(chan acquisition, 1)
	go func() {
		lease, err := registry.Acquire(context.Background(), "workspace-1")
		secondAcquired <- acquisition{lease: lease, err: err}
	}()
	waitForReferences(t, registry, "workspace-1", 2)

	select {
	case result := <-secondAcquired:
		if result.lease != nil {
			result.lease.Release()
		}
		t.Fatalf("second equal-key acquisition completed before release: %v", result.err)
	default:
	}

	first.Release()
	firstReleased = true

	select {
	case result := <-secondAcquired:
		if result.err != nil {
			t.Fatalf("acquire second lease: %v", result.err)
		}
		if result.lease == nil {
			t.Fatal("second equal-key acquisition returned no lease")
		}
		result.lease.Release()
	case <-time.After(time.Second):
		t.Fatal("second equal-key acquisition did not complete after release")
	}
}

func TestRegistryAllowsDifferentKeysConcurrently(t *testing.T) {
	registry := NewRegistry[string]()
	first, err := registry.Acquire(context.Background(), "workspace-1")
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	defer first.Release()

	type acquisition struct {
		lease *Lease[string]
		err   error
	}
	secondAcquired := make(chan acquisition, 1)
	go func() {
		lease, err := registry.Acquire(context.Background(), "workspace-2")
		secondAcquired <- acquisition{lease: lease, err: err}
	}()

	select {
	case result := <-secondAcquired:
		if result.err != nil {
			t.Fatalf("acquire different-key lease: %v", result.err)
		}
		if result.lease == nil {
			t.Fatal("different-key acquisition returned no lease")
		}
		result.lease.Release()
	case <-time.After(time.Second):
		t.Fatal("different-key acquisition waited for held lease")
	}
}

func TestRegistryCancelsWhileWaitingForEqualKey(t *testing.T) {
	registry := NewRegistry[string]()
	first, err := registry.Acquire(context.Background(), "workspace-1")
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	defer first.Release()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan error, 1)
	go func() {
		_, err := registry.Acquire(ctx, "workspace-1")
		waiting <- err
	}()
	waitForReferences(t, registry, "workspace-1", 2)

	cancel()

	select {
	case err := <-waiting:
		if err != context.Canceled {
			t.Fatalf("canceled acquisition error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled equal-key acquisition did not return")
	}
}

func TestRegistryRemovesIdleEntriesAfterRelease(t *testing.T) {
	registry := NewRegistry[string]()
	lease, err := registry.Acquire(context.Background(), "workspace-1")
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	lease.Release()

	registry.mu.Lock()
	entries := len(registry.entries)
	registry.mu.Unlock()
	if entries != 0 {
		t.Fatalf("idle registry entries = %d, want 0", entries)
	}

	reacquired, err := registry.Acquire(context.Background(), "workspace-1")
	if err != nil {
		t.Fatalf("reacquire released key: %v", err)
	}
	reacquired.Release()
}

func TestLeaseReleasePanicsWhenCalledTwice(t *testing.T) {
	registry := NewRegistry[string]()
	lease, err := registry.Acquire(context.Background(), "workspace-1")
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	lease.Release()

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("second lease release did not panic")
		}
	}()
	lease.Release()
}

func waitForReferences[K comparable](t *testing.T, registry *Registry[K], key K, want int) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		registry.mu.Lock()
		refs := 0
		if current := registry.entries[key]; current != nil {
			refs = current.refs
		}
		registry.mu.Unlock()
		if refs == want {
			return
		}

		select {
		case <-deadline:
			t.Fatalf("mutation lane references for key %v = %d, want %d", key, refs, want)
		case <-time.After(time.Millisecond):
		}
	}
}
