package requestmemo

import (
	"context"
	"testing"
	"time"
)

func TestSharedMutationLaneAllowsReadersAndBlocksWriter(t *testing.T) {
	registry := NewSharedMutationLaneRegistry[string]()
	first, err := registry.AcquireShared(context.Background(), "workflow")
	if err != nil {
		t.Fatalf("AcquireShared first: %v", err)
	}
	second, err := registry.AcquireShared(context.Background(), "workflow")
	if err != nil {
		t.Fatalf("AcquireShared second: %v", err)
	}
	acquired := make(chan *SharedMutationLaneLease[string], 1)
	go func() {
		lease, acquireErr := registry.AcquireExclusive(context.Background(), "workflow")
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
