package app

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
)

func TestUIEventDispatcherDrainsAttachmentOpenBeforeReadyExternalSources(t *testing.T) {
	transcriptEvents := make(chan ongoingTranscriptEvent, 1)
	transcriptEvents <- ongoingTranscriptEvent{Kind: ongoingTranscriptEventLoss}
	dispatcher := newUIEventDispatcher(transcriptEvents)
	sessionID := runtimeids.NewSessionID()
	opened := clientHookAttachmentOpenFact{
		occurredAt:  time.Unix(1, 0).UTC(),
		openingKind: lifecyclecontract.OpeningKindNew,
		sessionID:   sessionID,
	}
	if installed := dispatcher.OpenClientHookAttachment(opened); !installed {
		t.Fatal("first attachment open was not installed")
	}
	if installed := dispatcher.OpenClientHookAttachment(opened); installed {
		t.Fatal("repeated attachment open installed a duplicate initial event")
	}

	first := dispatcher.wait()()
	dispatched, ok := first.(uiDispatchedEventMsg)
	if !ok {
		t.Fatalf("first dispatcher message = %T, want typed dispatched event", first)
	}
	accepted, ok := dispatched.event.(uiAcceptedClientHookAttachmentOpen)
	if !ok || accepted.fact.sessionID != sessionID {
		t.Fatalf("first accepted event = %+v / %t, want attachment open", dispatched.event, ok)
	}

	second := dispatcher.wait()()
	dispatched, ok = second.(uiDispatchedEventMsg)
	if !ok {
		t.Fatalf("second dispatcher message = %T, want typed dispatched event", second)
	}
	if _, ok := dispatched.event.(uiAcceptedTranscriptEvent); !ok {
		t.Fatalf("second accepted event = %T, want ready transcript event", dispatched.event)
	}
}

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
	lifecycle := &recordingLifecycleEnvelopeSink{}
	model := newProjectedStaticUIModel(
		WithUINativeTurnNotificationObserver(newUnfocusedNativeTurnNotificationObserver(ringer)),
		WithUIClientLifecycleCoordinator(newClientLifecycleCoordinator(lifecycle, nil, nil, nil)),
	)
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
	if len(lifecycle.envelopes) != 1 {
		t.Fatalf("accepted attention emitted %d lifecycle envelopes, want one", len(lifecycle.envelopes))
	}
	raw, err := json.Marshal(lifecycle.envelopes[0])
	if err != nil {
		t.Fatalf("marshal lifecycle attention envelope: %v", err)
	}
	var got struct {
		Category lifecyclecontract.Category `json:"category"`
		Details  struct {
			Summary string `json:"summary"`
		} `json:"details"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode lifecycle attention envelope: %v", err)
	}
	if got.Category != lifecyclecontract.CategoryInputRequired ||
		got.Details.Summary != "What should happen next?" {
		t.Fatalf("lifecycle attention envelope = %+v", got)
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

func TestUIEventDispatcherSerializesTypedLifecycleHookIssues(t *testing.T) {
	mailbox := newLifecycleHookIssueMailbox()
	sink := &recordingLifecycleHookIssueSink{}
	model := newProjectedStaticUIModel(WithUILifecycleHookIssues(mailbox, sink))
	exitCode := 7
	mailbox.Report(lifecycleHookIssue{
		Kind:     lifecycleHookIssueNonzeroExit,
		ExitCode: &exitCode,
	})

	message := model.eventDispatcher.wait()()
	next, cmd := model.Update(message)
	model = next.(*uiModel)

	if len(sink.issues) != 1 ||
		sink.issues[0].Kind != lifecycleHookIssueNonzeroExit ||
		sink.issues[0].ExitCode == nil || *sink.issues[0].ExitCode != exitCode {
		t.Fatalf("accepted lifecycle hook issues = %+v", sink.issues)
	}
	if cmd == nil {
		t.Fatal("lifecycle hook issue reduction did not schedule the next external event wait")
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

type recordingLifecycleHookIssueSink struct {
	issues []lifecycleHookIssue
}

func (s *recordingLifecycleHookIssueSink) AcceptLifecycleHookIssue(issue lifecycleHookIssue) {
	s.issues = append(s.issues, cloneLifecycleHookIssue(issue))
}
