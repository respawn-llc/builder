package app

import (
	"testing"
	"time"

	"core/server/runtime"
	"core/server/runtimewire"
)

func TestRuntimeEventBridgeDropsWhenFullWithoutBlocking(t *testing.T) {
	var dropCount uint64
	bridge := runtimewire.NewEventBridge(1, func(total uint64, _ runtime.Event) {
		dropCount = total
	})

	bridge.Publish(runtime.Event{Kind: runtime.EventAssistantDelta, StepID: "s1"})

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10_000; i++ {
			bridge.Publish(runtime.Event{Kind: runtime.EventAssistantDelta, StepID: "s2"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Publish appears to block under full buffer")
	}

	if got := bridge.Dropped.Load(); got == 0 {
		t.Fatal("expected dropped events under saturation")
	}
	if dropCount == 0 {
		t.Fatal("expected onDrop callback to be invoked")
	}
}

func TestRuntimeEventBridgeDeliversWhenConsumerReady(t *testing.T) {
	bridge := runtimewire.NewEventBridge(2, nil)
	want := runtime.Event{Kind: runtime.EventAssistantMessage, StepID: "step-1"}
	bridge.Publish(want)

	select {
	case got := <-bridge.Events:
		if got.Kind != want.Kind || got.StepID != want.StepID {
			t.Fatalf("unexpected event: %+v", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected event delivery")
	}

	if got := bridge.Dropped.Load(); got != 0 {
		t.Fatalf("unexpected dropped count: %d", got)
	}
}
