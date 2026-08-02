package app

import (
	"testing"

	"core/shared/clientui"
)

func TestSubmitDoneDispatchesQueuedTurnWithoutNotificationTranscriptFacts(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	hooks.OnTranscriptMessage(bellToolStartMessage(1))
	hooks.OnTranscriptMessage(bellToolStartMessage(1))

	model := newProjectedStaticUIModel(WithUITurnQueueHook(hooks))
	model.activeSubmit = activeSubmitState{token: 1, text: "current turn"}
	model.setRuntimeActivityBusyForTest(true)
	model.queued = queuedInputsForTest("next queued turn")

	next, cmd := model.Update(submitDoneMsg{token: 1, submittedText: "current turn", message: "done"})
	updated := next.(*uiModel)

	if cmd == nil {
		t.Fatal("submit completion did not dispatch the next queued turn")
	}
	if got := updated.activeSubmit.text; got != "next queued turn" {
		t.Fatalf("active submission = %q, want queued turn", got)
	}
	if len(updated.queued) != 0 {
		t.Fatalf("queued turns after dispatch = %+v, want empty", updated.queued)
	}
	if ringer.total() != 0 {
		t.Fatalf("queued dispatch emitted %d notifications without final transcript facts", ringer.total())
	}
}

func TestTranscriptHydrationClearsNotificationStateWithoutReplayingRows(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	recordToolHeavyBellTurn(hooks, 1)

	model := newProjectedStaticUIModel(WithUITurnQueueHook(hooks))
	hydration := ongoingHydrationMessage(1)
	hydratedFinal := bellAssistantFinalMessageWithText(2, "hydrated historical answer")
	hydrationPayload := hydration.Payload().(clientui.TranscriptHydration)
	hydrationPayload.CommittedRows = []clientui.TranscriptCommittedRow{
		hydratedFinal.Payload().(clientui.TranscriptCommittedRow),
	}
	hydration = clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(hydrationPayload))
	model.applyAdmittedTranscriptMessageState(hydration, runtimeTupleMergeResult{})
	hooks.OnTurnQueueDrained()

	if ringer.total() != 0 {
		t.Fatalf("hydration emitted %d historical notification events", ringer.total())
	}
}

func TestTranscriptSubscriptionLossClearsNotificationState(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	recordToolHeavyBellTurn(hooks, 1)

	model := newProjectedStaticUIModel(WithUITurnQueueHook(hooks))
	model.ongoingTranscript = newNoopOngoingTranscriptController(
		&ongoingSurfaceSpy{},
		ongoingTestFrameProvider,
	)
	model.handleOngoingTranscriptEvent(ongoingTranscriptEvent{Kind: ongoingTranscriptEventLoss})
	hooks.OnTurnQueueDrained()

	if ringer.total() != 0 {
		t.Fatalf("subscription loss retained %d notification events", ringer.total())
	}
}
