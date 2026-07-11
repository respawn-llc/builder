package app

import (
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/clientui"
)

func TestOngoingTranscriptControllerScratchRehydrationTriggersResetSequence(t *testing.T) {
	controller := newOngoingTranscriptController(&ongoingSurfaceSpy{}, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if result, err := controller.Accept(ongoingTranscriptMessage(3, clientui.TranscriptMessageRunState)); err != nil || result.Action != ongoing.ResultRequestScratchRehydration {
		t.Fatalf("sequence gap result=%+v err=%v, want scratch rehydration", result, err)
	}

	controller.ResetForScratchHydration()

	if result, err := controller.Accept(ongoingHydrationMessage(1)); err != nil || result.Action != ongoing.ResultNoop {
		t.Fatalf("post-reset hydration result=%+v err=%v, want accepted hydration", result, err)
	}
}

func TestOngoingTranscriptControllerSubscriptionLossRequestsScratchRehydration(t *testing.T) {
	controller := newOngoingTranscriptController(&ongoingSurfaceSpy{}, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}

	result := controller.HandleSubscriptionLoss()

	if result.Action != ongoing.ResultRequestScratchRehydration {
		t.Fatalf("subscription loss action = %q, want scratch rehydration", result.Action)
	}
	controller.ResetForScratchHydration()
	if result, err := controller.Accept(ongoingHydrationMessage(1)); err != nil || result.Action != ongoing.ResultNoop {
		t.Fatalf("post-loss hydration result=%+v err=%v, want accepted hydration", result, err)
	}
}
