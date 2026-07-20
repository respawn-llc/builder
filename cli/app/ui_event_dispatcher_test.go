package app

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"core/shared/clientui"
)

func TestUIEventDispatcherReducesAttentionDiscontinuityAndRequestsReopen(t *testing.T) {
	attentionEvents := make(chan attentionStreamOutcome, 1)
	var reopenCalls atomic.Int32
	model := newProjectedStaticUIModel()
	model.eventDispatcher.attentionEvents = attentionEvents
	model.eventDispatcher.requestAttentionReopen = func() {
		reopenCalls.Add(1)
	}
	attentionEvents <- attentionStreamDiscontinuity{reason: attentionStreamDiscontinuityInvalidEvent}

	message := model.eventDispatcher.wait()()
	next, cmd := model.Update(message)
	model = next.(*uiModel)

	if got := reopenCalls.Load(); got != 1 {
		t.Fatalf("attention reopen calls = %d, want 1", got)
	}
	if cmd == nil {
		t.Fatal("attention reduction did not schedule the next external event wait")
	}
}

func TestUIEventDispatcherFansAcceptedAttentionToNativeAndLifecycle(t *testing.T) {
	attentionEvents := make(chan attentionStreamOutcome, 1)
	ringer := &countRinger{}
	lifecycle := &recordingLifecycleAttentionSink{}
	model := newProjectedStaticUIModel(WithUINativeTurnNotificationObserver(newUnfocusedNativeTurnNotificationObserver(ringer)))
	model.lifecycleAttention = lifecycle
	model.eventDispatcher.attentionEvents = attentionEvents
	attentionEvents <- &attentionFact{
		notificationKey: attentionKeyForNotificationID(clientui.AttentionNotificationID{
			Kind: clientui.AttentionNotificationKindQuestion,
			UUID: "question-1",
		}),
		kind:       attentionFactKindQuestion,
		occurredAt: time.Unix(1, 0).UTC(),
		summary:    "What should happen next?",
	}

	message := model.eventDispatcher.wait()()
	next, _ := model.Update(message)
	model = next.(*uiModel)

	if ringer.total() != 1 {
		t.Fatalf("accepted attention emitted %d native events, want one", ringer.total())
	}
	if len(lifecycle.facts) != 1 {
		t.Fatalf("accepted attention emitted %d lifecycle facts, want one", len(lifecycle.facts))
	}
	if lifecycle.facts[0].notificationKey == (attentionNotificationKey{}) {
		t.Fatal("lifecycle attention fact lost its notification key")
	}
}

func TestUIEventDispatcherContinuesWithRemainingExternalSource(t *testing.T) {
	transcriptEvents := make(chan ongoingTranscriptEvent)
	attentionEvents := make(chan attentionStreamOutcome, 1)
	close(transcriptEvents)
	attentionEvents <- attentionStreamControl{kind: attentionStreamControlSnapshotComplete}
	dispatcher := &uiEventDispatcher{
		transcriptEvents: transcriptEvents,
		attentionEvents:  attentionEvents,
	}

	message := dispatcher.wait()()
	dispatched, ok := message.(uiDispatchedEventMsg)
	if !ok {
		t.Fatalf("dispatcher message = %T, want uiDispatchedEventMsg", message)
	}
	accepted, ok := dispatched.event.(uiAcceptedAttentionEvent)
	if !ok {
		t.Fatalf("accepted event = %T, want attention event", dispatched.event)
	}
	control, ok := accepted.outcome.(attentionStreamControl)
	if !ok || control.kind != attentionStreamControlSnapshotComplete {
		t.Fatalf("attention outcome = %+v / %t, want snapshot complete", control, ok)
	}
}

func TestBufferedTranscriptFactsCannotAffectCurrentReducerLocalDecision(t *testing.T) {
	for _, test := range []struct {
		name          string
		complete      submitDoneMsg
		wantAfterLate int
	}{
		{
			name:          "drain",
			complete:      submitDoneMsg{token: 1, submittedText: "current", message: "done"},
			wantAfterLate: 1,
		},
		{
			name:          "abort",
			complete:      submitDoneMsg{token: 1, submittedText: "current", err: errors.New("submission failed")},
			wantAfterLate: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ringer := &countRinger{}
			hooks := newUnfocusedNativeTurnNotificationObserver(ringer)
			transcriptEvents := make(chan ongoingTranscriptEvent, 8)
			model := newProjectedStaticUIModel(
				WithUINativeTurnNotificationObserver(hooks),
				WithUIOngoingTranscriptEvents(transcriptEvents),
			)
			model.ongoingTranscript = newOngoingTranscriptController(
				&ongoingSurfaceSpy{},
				ongoingTestFrameProvider,
				noopOngoingTranscriptRuntimeAdmission,
				model.applyAdmittedTranscriptMessageState,
			)

			hydration := ongoingHydrationMessage(1)
			firstTool := bellToolStartMessage(1)
			firstTool.Sequence = 2
			firstTool.Payload.ToolStart.ToolCallID = "tool-1"
			secondTool := bellToolStartMessage(1)
			secondTool.Sequence = 3
			secondTool.Payload.ToolStart.ToolCallID = "tool-2"
			for _, message := range []clientui.TranscriptMessage{hydration, firstTool, secondTool} {
				transcriptEvents <- ongoingTranscriptEvent{Kind: ongoingTranscriptEventMessage, Message: message}
				model = reduceNextAcceptedExternalEvent(t, model)
			}

			final := bellAssistantFinalMessageWithText(1, "buffered final")
			final.Sequence = 4
			finished := bellStepFinishedMessage(1)
			finished.Sequence = 5
			finished.Payload.StepState.RunID = ongoingTestRunID()
			finished.Payload.StepState.ActiveKind = clientui.RuntimeActivityActiveKindUserTurn
			finished.Payload.StepState.Status = clientui.RunStatusCompleted
			transcriptEvents <- ongoingTranscriptEvent{Kind: ongoingTranscriptEventMessage, Message: final}
			transcriptEvents <- ongoingTranscriptEvent{Kind: ongoingTranscriptEventMessage, Message: finished}

			model.activeSubmit = activeSubmitState{token: 1, text: "current"}
			model.setRuntimeActivityBusyForTest(true)
			next, _ := model.Update(test.complete)
			model = next.(*uiModel)
			if got := ringer.total(); got != 0 {
				t.Fatalf("current %s observed buffered transcript facts and emitted %d events", test.name, got)
			}

			model = reduceNextAcceptedExternalEvent(t, model)
			model = reduceNextAcceptedExternalEvent(t, model)
			model.inputController().notifyTurnQueueDrainedIfIdle()
			if got := ringer.notifications; got != test.wantAfterLate {
				t.Fatalf("notification count after late admission = %d, want %d", got, test.wantAfterLate)
			}
		})
	}
}

func reduceNextAcceptedExternalEvent(t *testing.T, model *uiModel) *uiModel {
	t.Helper()
	wait := model.eventDispatcher.wait()
	if wait == nil {
		t.Fatal("external event dispatcher wait is nil")
	}
	message := wait()
	if message == nil {
		t.Fatal("external event dispatcher returned no message")
	}
	next, _ := model.Update(message)
	return next.(*uiModel)
}

type recordingLifecycleAttentionSink struct {
	facts []attentionFact
}

func (s *recordingLifecycleAttentionSink) AcceptAttentionFact(fact attentionFact) {
	s.facts = append(s.facts, fact)
}
