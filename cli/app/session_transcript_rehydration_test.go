package app

import (
	"bytes"
	"errors"
	"net"
	"testing"

	"core/cli/tui/ongoing"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestOngoingTranscriptControllerScratchRehydrationTriggersResetSequence(t *testing.T) {
	controller := newTestOngoingTranscriptController(&ongoingSurfaceSpy{}, ongoingTestFrameProvider)
	if _, err := controller.Accept(ongoingHydrationMessage(1)); err != nil {
		t.Fatalf("accept hydration: %v", err)
	}
	if result, err := controller.Accept(ongoingTranscriptMessage(3, clientui.TranscriptMessageSessionStatus)); err != nil || result.Action != ongoing.ResultRequestScratchRehydration {
		t.Fatalf("sequence gap result=%+v err=%v, want scratch rehydration", result, err)
	}

	controller.ResetForScratchHydration()

	if result, err := controller.Accept(ongoingHydrationMessage(1)); err != nil || result.Action != ongoing.ResultNoop {
		t.Fatalf("post-reset hydration result=%+v err=%v, want accepted hydration", result, err)
	}
}

func TestOngoingTranscriptControllerSubscriptionLossRequestsScratchRehydration(t *testing.T) {
	controller := newTestOngoingTranscriptController(&ongoingSurfaceSpy{}, ongoingTestFrameProvider)
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

func TestMainUITranscriptTransportLossPersistsSharedDisconnect(t *testing.T) {
	owner := newInteractiveConnectionOwner()
	var output bytes.Buffer
	surface := ongoing.NewSurface(&output)
	model := sizedTestUIModel(newProjectedTestUIModel(
		&runtimeControlFakeClient{},
		WithUIConnectionState(owner),
		WithUIOngoingSurface(surface),
	), 80, 24)
	model.ongoingTranscript = newNoopOngoingTranscriptController(surface, model.ongoingFrameInput)

	transportLoss := errors.Join(
		serverapi.ErrStreamFailed,
		&net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")},
	)
	model.handleOngoingTranscriptEvent(ongoingTranscriptEvent{
		Kind: ongoingTranscriptEventLoss,
		Err:  transportLoss,
	})

	if !owner.IsDisconnected() {
		t.Fatal("transcript transport loss did not persist disconnect in the shared owner")
	}
	if output.Len() == 0 {
		t.Fatal("transcript transport loss did not repaint the main status projection")
	}
}

func TestMainUITranscriptReconnectClearsSharedDisconnect(t *testing.T) {
	owner := newInteractiveConnectionOwner()
	owner.ObserveStream(errors.Join(serverapi.ErrStreamFailed, &net.OpError{
		Op:  "read",
		Net: "tcp",
		Err: errors.New("connection reset"),
	}))
	var output bytes.Buffer
	surface := ongoing.NewSurface(&output)
	model := sizedTestUIModel(newProjectedTestUIModel(
		&runtimeControlFakeClient{},
		WithUIConnectionState(owner),
		WithUIOngoingSurface(surface),
	), 80, 24)
	model.ongoingTranscript = newNoopOngoingTranscriptController(surface, model.ongoingFrameInput)

	model.handleOngoingTranscriptEvent(ongoingTranscriptEvent{Kind: ongoingTranscriptEventReachable})

	if owner.IsDisconnected() {
		t.Fatal("reachable transcript resubscription did not clear the shared owner")
	}
	if output.Len() == 0 {
		t.Fatal("reachable transcript resubscription did not repaint the cleared main status projection")
	}
}
