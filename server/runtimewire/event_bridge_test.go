package runtimewire

import (
	"testing"

	"core/server/runtime"
)

func TestEventBridgeCoalescesGapSignalsUntilObserved(t *testing.T) {
	bridge := NewEventBridge(1, nil)
	bridge.Publish(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "first"})
	bridge.Publish(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "second"})
	bridge.Publish(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "third"})

	if got := bridge.Dropped.Load(); got != 2 {
		t.Fatalf("dropped count = %d, want 2", got)
	}

	select {
	case <-bridge.GapEvents:
	default:
		t.Fatal("expected dropped publishes to signal a gap")
	}

	select {
	case <-bridge.GapEvents:
		t.Fatal("expected pending gap signal to stay coalesced until observed")
	default:
	}

	bridge.Publish(runtime.Event{Kind: runtime.EventToolCallStarted, StepID: "step-1"})
	if got := bridge.Dropped.Load(); got != 3 {
		t.Fatalf("dropped count after another overflow = %d, want 3", got)
	}

	select {
	case <-bridge.GapEvents:
	default:
		t.Fatal("expected another dropped publish to signal a new gap after observation")
	}
}
