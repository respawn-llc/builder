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

func TestManualCompactionNotificationWaitsForTerminalTranscriptOutcome(t *testing.T) {
	ringer := &countRinger{}
	hooks := newUnfocusedBellHooks(ringer)
	model := newProjectedStaticUIModel(WithUITurnQueueHook(hooks))
	model.pendingManualCompaction = true

	next, _ := model.Update(compactDoneMsg{})
	model = next.(*uiModel)
	if ringer.total() != 0 {
		t.Fatalf("compaction scheduling emitted %d notification events before terminal outcome", ringer.total())
	}

	for sequence, status := range []clientui.TranscriptCompactionStatus{
		{
			StepID: ongoingTestStepID(),
			State:  clientui.CompactionStarted,
			Mode:   clientui.CompactionModeManual,
			Count:  1,
		},
		{
			StepID: ongoingTestStepID(),
			State:  clientui.CompactionCompleted,
			Mode:   clientui.CompactionModeAuto,
			Count:  1,
		},
	} {
		model.applyAdmittedTranscriptMessageState(clientui.NewTranscriptMessage(uint64(sequence+2), clientui.NewTranscriptEvent(status)), runtimeTupleMergeResult{})
	}
	if ringer.total() != 0 {
		t.Fatalf("non-success manual outcomes emitted %d notification events", ringer.total())
	}
	if !model.pendingManualCompaction {
		t.Fatal("non-manual terminal compaction cleared the initiating TUI state")
	}

	model.applyAdmittedTranscriptMessageState(clientui.NewTranscriptMessage(2, clientui.NewTranscriptEvent(clientui.TranscriptCompactionStatus{
		StepID: ongoingTestStepID(),
		State:  clientui.CompactionCompleted,
		Mode:   clientui.CompactionModeManual,
		Count:  1,
	})), runtimeTupleMergeResult{})
	if ringer.notifications != 1 {
		t.Fatalf("terminal manual compaction emitted %d notifications, want 1", ringer.notifications)
	}
	if model.pendingManualCompaction {
		t.Fatal("terminal compaction retained pending notification state")
	}
}

func TestManualCompactionTerminalEventDoesNotNotifyOtherAttachedTUI(t *testing.T) {
	initiatorRinger := &countRinger{}
	observerRinger := &countRinger{}
	initiator := newProjectedStaticUIModel(WithUITurnQueueHook(newUnfocusedBellHooks(initiatorRinger)))
	observer := newProjectedStaticUIModel(WithUITurnQueueHook(newUnfocusedBellHooks(observerRinger)))
	initiator.pendingManualCompaction = true

	event := clientui.NewTranscriptMessage(1, clientui.NewTranscriptEvent(clientui.TranscriptCompactionStatus{
		StepID: ongoingTestStepID(),
		State:  clientui.CompactionCompleted,
		Mode:   clientui.CompactionModeManual,
		Count:  1,
	}))
	initiator.applyAdmittedTranscriptMessageState(event, runtimeTupleMergeResult{})
	observer.applyAdmittedTranscriptMessageState(event, runtimeTupleMergeResult{})

	if initiatorRinger.notifications != 1 {
		t.Fatalf("initiating TUI notifications = %d, want 1", initiatorRinger.notifications)
	}
	if observerRinger.total() != 0 {
		t.Fatalf("observing TUI received %d spurious notifications", observerRinger.total())
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
	model.pendingManualCompaction = true
	model.ongoingTranscript = newNoopOngoingTranscriptController(
		&ongoingSurfaceSpy{},
		ongoingTestFrameProvider,
	)
	model.handleOngoingTranscriptEvent(ongoingTranscriptEvent{Kind: ongoingTranscriptEventLoss})
	hooks.OnTurnQueueDrained()

	if ringer.total() != 0 {
		t.Fatalf("subscription loss retained %d notification events", ringer.total())
	}
	if model.pendingManualCompaction {
		t.Fatal("subscription loss retained pending compaction notification state")
	}
}
