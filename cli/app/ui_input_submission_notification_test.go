package app

import (
	"errors"
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
	hydration.Payload.Hydration.CommittedRows = []clientui.TranscriptCommittedRow{
		*hydratedFinal.Payload.CommittedRow,
	}
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

func TestQueuedCompactionCompletionReducesBeforeDrainInSameReducerFlow(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	recordToolHeavyBellTurn(hooks, 1)
	model := newProjectedStaticUIModel(WithUITurnQueueHook(hooks))
	model.compactionOrigin = uiCompactionOriginManual
	model.queued = queuedInputsForTest("next queued turn")

	next, _ := model.Update(compactDoneMsg{})
	model = next.(*uiModel)
	if ringer.total() != 0 {
		t.Fatalf("queued compaction emitted %d events before queued turn drained", ringer.total())
	}
	token := model.activeSubmit.token
	if token == 0 {
		t.Fatal("queued compaction did not start the queued turn")
	}

	next, _ = model.Update(submitDoneMsg{token: token, submittedText: "next queued turn", message: "done"})
	model = next.(*uiModel)
	if ringer.notifications != 1 {
		t.Fatalf("compaction-before-drain emitted %d notifications, want one", ringer.notifications)
	}
}

func TestQueuedCompactionAbortClearsCompactionAndTurnCompletion(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	recordToolHeavyBellTurn(hooks, 1)
	model := newProjectedStaticUIModel(WithUITurnQueueHook(hooks))
	model.compactionOrigin = uiCompactionOriginManual
	model.queued = queuedInputsForTest("next queued turn")

	next, _ := model.Update(compactDoneMsg{})
	model = next.(*uiModel)
	token := model.activeSubmit.token
	if token == 0 {
		t.Fatal("queued compaction did not start the queued turn")
	}

	next, _ = model.Update(submitDoneMsg{
		token:         token,
		submittedText: "next queued turn",
		err:           errors.New("queued turn failed"),
	})
	model = next.(*uiModel)
	model.inputController().notifyTurnQueueDrainedIfIdle()
	if ringer.total() != 0 {
		t.Fatalf("queued compaction abort emitted %d events", ringer.total())
	}
}

func TestImmediateManualCompactionCompletionNotifiesOnce(t *testing.T) {
	ringer := &countRinger{}
	model := newProjectedStaticUIModel(WithUITurnQueueHook(newUnfocusedBellHooks(ringer)))
	model.compactionOrigin = uiCompactionOriginManual

	_, _ = model.Update(compactDoneMsg{})
	if ringer.notifications != 1 {
		t.Fatalf("immediate manual compaction emitted %d notifications, want one", ringer.notifications)
	}
}

func TestInjectedWorkDefersCompactionNotificationUntilDrain(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	recordToolHeavyBellTurn(hooks, 1)
	model := newProjectedStaticUIModel(WithUITurnQueueHook(hooks))
	model.injectedQueueToken = 1
	model.queuedRuntimeWorkCheckCompactionOrigin = uiCompactionOriginManual

	next, _ := model.Update(queuedRuntimeWorkCheckDoneMsg{token: 1, hasWork: true})
	model = next.(*uiModel)
	if ringer.total() != 0 {
		t.Fatalf("injected work emitted %d events before drain", ringer.total())
	}
	token := model.activeSubmit.token
	if token == 0 {
		t.Fatal("injected work did not start a submission")
	}

	next, _ = model.Update(submitDoneMsg{token: token, message: "done"})
	model = next.(*uiModel)
	if ringer.notifications != 1 {
		t.Fatalf("injected-work drain emitted %d notifications, want one", ringer.notifications)
	}
}
