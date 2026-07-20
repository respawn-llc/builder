package app

import (
	"encoding/json"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/lifecyclecontract"
	"core/shared/serverapi"
)

type recordingLifecycleEnvelopeSink struct {
	envelopes []lifecyclecontract.Envelope
}

func (s *recordingLifecycleEnvelopeSink) AcceptLifecycleEnvelope(envelope lifecyclecontract.Envelope) {
	s.envelopes = append(s.envelopes, envelope)
}

func TestClientLifecycleCoordinatorEmitsImmediateTaskCompletion(t *testing.T) {
	sink := &recordingLifecycleEnvelopeSink{}
	coordinator := newClientLifecycleCoordinator(
		sink,
		func() lifecyclecontract.Context { return lifecyclecontract.Context{} },
		func() bool { return true },
	)
	finishedAt := time.Unix(123, 456).UTC()
	preview := "dynamic **final answer**"

	coordinator.AcceptLiveRunBatchFinished(clientui.TranscriptLiveRunBatchFinished{
		Disposition:   clientui.LiveRunBatchDispositionFinalAnswer,
		FinishedAt:    finishedAt,
		WorkPerformed: true,
		FinalAnswerPreview: &clientui.TranscriptFinalAnswerPreview{
			Markdown: preview,
		},
	})

	if len(sink.envelopes) != 1 {
		t.Fatalf("completion envelopes = %d, want 1", len(sink.envelopes))
	}
	raw, err := json.Marshal(sink.envelopes[0])
	if err != nil {
		t.Fatalf("marshal completion envelope: %v", err)
	}
	var got struct {
		Category   lifecyclecontract.Category `json:"category"`
		OccurredAt time.Time                  `json:"occurred_at"`
		Focused    bool                       `json:"focused"`
		Details    struct {
			FinalAnswer   string `json:"final_answer"`
			WorkPerformed bool   `json:"work_performed"`
		} `json:"details"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode completion envelope: %v", err)
	}
	if got.Category != lifecyclecontract.CategoryTaskComplete {
		t.Fatalf("completion category = %q, want %q", got.Category, lifecyclecontract.CategoryTaskComplete)
	}
	if !got.OccurredAt.Equal(finishedAt) {
		t.Fatalf("completion occurred_at = %s, want %s", got.OccurredAt, finishedAt)
	}
	if !got.Focused {
		t.Fatal("completion focus snapshot = false, want true")
	}
	if got.Details.FinalAnswer != preview || !got.Details.WorkPerformed {
		t.Fatalf("completion details = %+v", got.Details)
	}
}

func TestClientLifecycleCoordinatorEmitsImmediateRuntimeError(t *testing.T) {
	sink := &recordingLifecycleEnvelopeSink{}
	coordinator := newClientLifecycleCoordinator(sink, nil, nil)
	finishedAt := time.Unix(456, 789).UTC()
	diagnostic := "dynamic runtime failure"

	coordinator.AcceptLiveRunBatchFinished(clientui.TranscriptLiveRunBatchFinished{
		Disposition: clientui.LiveRunBatchDispositionRuntimeFailure,
		FinishedAt:  finishedAt,
		FailureDiagnostic: &clientui.TranscriptDiagnostic{
			Code:   clientui.TranscriptDiagnosticCode("dynamic_code"),
			Detail: diagnostic,
		},
	})

	if len(sink.envelopes) != 1 {
		t.Fatalf("runtime-error envelopes = %d, want 1", len(sink.envelopes))
	}
	raw, err := json.Marshal(sink.envelopes[0])
	if err != nil {
		t.Fatalf("marshal runtime-error envelope: %v", err)
	}
	var got struct {
		Category   lifecyclecontract.Category `json:"category"`
		OccurredAt time.Time                  `json:"occurred_at"`
		Details    struct {
			Diagnostic string `json:"diagnostic"`
		} `json:"details"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode runtime-error envelope: %v", err)
	}
	if got.Category != lifecyclecontract.CategoryTaskError {
		t.Fatalf("runtime-error category = %q, want %q", got.Category, lifecyclecontract.CategoryTaskError)
	}
	if !got.OccurredAt.Equal(finishedAt) || got.Details.Diagnostic != diagnostic {
		t.Fatalf("runtime-error envelope = %+v", got)
	}
}

func TestClientLifecycleCoordinatorTreatsCompletedWithoutFinalAnswerAsTaskError(t *testing.T) {
	sink := &recordingLifecycleEnvelopeSink{}
	coordinator := newClientLifecycleCoordinator(sink, nil, nil)

	coordinator.AcceptLiveRunBatchFinished(clientui.TranscriptLiveRunBatchFinished{
		Disposition: clientui.LiveRunBatchDispositionNoFinalAnswer,
		FinishedAt:  time.Unix(789, 123).UTC(),
	})

	if len(sink.envelopes) != 1 {
		t.Fatalf("no-final-answer envelopes = %d, want 1", len(sink.envelopes))
	}
	raw, err := json.Marshal(sink.envelopes[0])
	if err != nil {
		t.Fatalf("marshal no-final-answer envelope: %v", err)
	}
	var got struct {
		Category lifecyclecontract.Category `json:"category"`
		Details  struct {
			Diagnostic string `json:"diagnostic"`
		} `json:"details"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode no-final-answer envelope: %v", err)
	}
	if got.Category != lifecyclecontract.CategoryTaskError {
		t.Fatalf("no-final-answer category = %q, want %q", got.Category, lifecyclecontract.CategoryTaskError)
	}
	if got.Details.Diagnostic != serverapi.ErrRuntimeNoFinalAnswer.Error() {
		t.Fatalf("no-final-answer diagnostic = %q, want shared runtime diagnostic", got.Details.Diagnostic)
	}
}

func TestClientLifecycleCoordinatorExcludesNonErrorTerminalOutcomes(t *testing.T) {
	workflowCompleted := clientui.LiveRunBatchExclusionWorkflowCompleted
	nonTaskActivity := clientui.LiveRunBatchExclusionNonTaskActivity
	facts := []clientui.TranscriptLiveRunBatchFinished{
		{
			Disposition: clientui.LiveRunBatchDispositionInterrupted,
			FinishedAt:  time.Unix(1, 0).UTC(),
		},
		{
			Disposition:     clientui.LiveRunBatchDispositionExcluded,
			ExclusionReason: &workflowCompleted,
			FinishedAt:      time.Unix(2, 0).UTC(),
		},
		{
			Disposition:     clientui.LiveRunBatchDispositionExcluded,
			ExclusionReason: &nonTaskActivity,
			FinishedAt:      time.Unix(3, 0).UTC(),
		},
	}
	sink := &recordingLifecycleEnvelopeSink{}
	coordinator := newClientLifecycleCoordinator(sink, nil, nil)

	for _, fact := range facts {
		coordinator.AcceptLiveRunBatchFinished(fact)
	}

	if len(sink.envelopes) != 0 {
		t.Fatalf("excluded terminal outcomes emitted %d envelopes", len(sink.envelopes))
	}
}

func TestUIReducerUsesOnlyAcceptedBatchFinishedFactsForLifecycleTerminalEvents(t *testing.T) {
	sink := &recordingLifecycleEnvelopeSink{}
	model := newProjectedStaticUIModel(WithUIClientLifecycleCoordinator(
		newClientLifecycleCoordinator(sink, nil, nil),
	))
	fact := clientui.TranscriptLiveRunBatchFinished{
		Disposition: clientui.LiveRunBatchDispositionFinalAnswer,
		FinishedAt:  time.Unix(4, 0).UTC(),
		FinalAnswerPreview: &clientui.TranscriptFinalAnswerPreview{
			Markdown: "accepted batch result",
		},
	}

	model.applyAdmittedTranscriptMessageState(clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageLiveRunBatchFinished,
		Payload: clientui.TranscriptPayload{
			LiveRunBatchFinished: &fact,
		},
	}, runtimeTupleMergeResult{})
	model.setRuntimeDisconnected(true)
	if model.nativeTurnNotifications != nil {
		model.nativeTurnNotifications.ReduceNativeInput(nativeTurnQueueDrainedInput{})
	}

	if len(sink.envelopes) != 1 {
		t.Fatalf("accepted batch plus local/connection decisions emitted %d envelopes, want 1", len(sink.envelopes))
	}

	emptySink := &recordingLifecycleEnvelopeSink{}
	model.lifecycleCoordinator = newClientLifecycleCoordinator(emptySink, nil, nil)
	model.setRuntimeDisconnected(true)
	if model.nativeTurnNotifications != nil {
		model.nativeTurnNotifications.ReduceNativeInput(nativeTurnQueueAbortedInput{})
		model.nativeTurnNotifications.ReduceNativeInput(nativeTurnQueueDrainedInput{})
	}
	if len(emptySink.envelopes) != 0 {
		t.Fatalf("local queue or connection state inferred %d lifecycle terminal events", len(emptySink.envelopes))
	}
}
