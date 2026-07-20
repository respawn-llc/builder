package app

import (
	"encoding/json"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/lifecyclecontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type recordingLifecycleEnvelopeSink struct {
	envelopes []lifecyclecontract.Envelope
}

func (s *recordingLifecycleEnvelopeSink) EnqueueLifecycleEnvelope(envelope lifecyclecontract.Envelope) bool {
	s.envelopes = append(s.envelopes, envelope)
	return true
}

func TestClientLifecycleCoordinatorEmitsImmediateTaskCompletion(t *testing.T) {
	sink := &recordingLifecycleEnvelopeSink{}
	coordinator := newClientLifecycleCoordinator(
		sink,
		func() lifecyclecontract.Context { return lifecyclecontract.Context{} },
		func() bool { return true },
		nil,
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
	coordinator := newClientLifecycleCoordinator(sink, nil, nil, nil)
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
	coordinator := newClientLifecycleCoordinator(sink, nil, nil, nil)

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
	coordinator := newClientLifecycleCoordinator(sink, nil, nil, nil)

	for _, fact := range facts {
		coordinator.AcceptLiveRunBatchFinished(fact)
	}

	if len(sink.envelopes) != 0 {
		t.Fatalf("excluded terminal outcomes emitted %d envelopes", len(sink.envelopes))
	}
}

func TestClientLifecycleCoordinatorEmitsAcceptedAttentionWithCurrentContextAndFocus(t *testing.T) {
	sink := &recordingLifecycleEnvelopeSink{}
	focused := false
	coordinator := newClientLifecycleCoordinator(
		sink,
		func() lifecyclecontract.Context { return lifecyclecontract.Context{} },
		func() bool { return focused },
		nil,
	)
	sessionID := runtimeids.NewSessionID()
	firstTitle := "opening title"
	coordinator.AcceptSessionIdentity(clientui.TranscriptSessionIdentity{
		SessionID:             sessionID,
		SessionName:           &firstTitle,
		ConversationFreshness: clientui.ConversationFreshnessEstablished,
	})
	focused = true
	taskID, err := lifecyclecontract.ParseWorkflowTaskID("dynamic-task-1")
	if err != nil {
		t.Fatalf("parse workflow task id: %v", err)
	}
	occurredAt := time.Unix(5, 0).UTC()
	summary := "**dynamic question source**"

	coordinator.AcceptAttentionFact(attentionFact{
		kind:             attentionFactKindQuestion,
		occurredAt:       occurredAt,
		summary:          summary,
		summaryTruncated: true,
		workflowTaskID:   &taskID,
	})

	if len(sink.envelopes) != 1 {
		t.Fatalf("attention envelopes = %d, want 1", len(sink.envelopes))
	}
	raw, err := json.Marshal(sink.envelopes[0])
	if err != nil {
		t.Fatalf("marshal attention envelope: %v", err)
	}
	var got struct {
		Category   lifecyclecontract.Category `json:"category"`
		OccurredAt time.Time                  `json:"occurred_at"`
		Focused    bool                       `json:"focused"`
		Context    struct {
			SessionID      string `json:"session_id"`
			SessionTitle   string `json:"session_title"`
			WorkflowTaskID string `json:"workflow_task_id"`
		} `json:"context"`
		Details struct {
			Kind    lifecyclecontract.InputKind `json:"kind"`
			Summary string                      `json:"summary"`
		} `json:"details"`
		Truncation struct {
			Fields []lifecyclecontract.TruncationField `json:"fields"`
		} `json:"truncation"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode attention envelope: %v", err)
	}
	if got.Category != lifecyclecontract.CategoryInputRequired ||
		!got.OccurredAt.Equal(occurredAt) || !got.Focused {
		t.Fatalf("attention envelope header = %+v", got)
	}
	if got.Context.SessionID != sessionID.String() ||
		got.Context.SessionTitle != firstTitle ||
		got.Context.WorkflowTaskID != taskID.String() {
		t.Fatalf("attention context = %+v", got.Context)
	}
	if got.Details.Kind != lifecyclecontract.InputKindQuestion || got.Details.Summary != summary {
		t.Fatalf("attention details = %+v", got.Details)
	}
	if len(got.Truncation.Fields) != 1 ||
		got.Truncation.Fields[0] != lifecyclecontract.TruncationFieldInputSummary {
		t.Fatalf("attention truncation = %+v", got.Truncation.Fields)
	}
}

func TestClientLifecycleCoordinatorEmitsEveryAcceptedAttentionOccurrence(t *testing.T) {
	sink := &recordingLifecycleEnvelopeSink{}
	coordinator := newClientLifecycleCoordinator(sink, nil, nil, nil)
	occurredAt := time.Unix(8, 0).UTC()
	question := attentionFact{
		notificationKey: attentionKeyForNotificationID(clientui.AttentionNotificationID{
			Kind: clientui.AttentionNotificationKindQuestion,
			UUID: "same-occurrence",
		}),
		kind:       attentionFactKindQuestion,
		occurredAt: occurredAt,
		summary:    "dynamic question",
	}
	approval := attentionFact{
		notificationKey: attentionKeyForNotificationID(clientui.AttentionNotificationID{
			Kind: clientui.AttentionNotificationKindApproval,
			UUID: "approval-occurrence",
		}),
		kind:       attentionFactKindApproval,
		occurredAt: occurredAt.Add(time.Second),
		summary:    "dynamic approval",
	}

	coordinator.AcceptAttentionFact(question)
	coordinator.AcceptAttentionFact(approval)
	coordinator.AcceptAttentionFact(question)

	if len(sink.envelopes) != 3 {
		t.Fatalf("accepted attention envelopes = %d, want 3 including repeated snapshot occurrence", len(sink.envelopes))
	}
	wantKinds := []lifecyclecontract.InputKind{
		lifecyclecontract.InputKindQuestion,
		lifecyclecontract.InputKindApproval,
		lifecyclecontract.InputKindQuestion,
	}
	for index, envelope := range sink.envelopes {
		raw, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("marshal accepted attention envelope %d: %v", index, err)
		}
		var got struct {
			Details struct {
				Kind lifecyclecontract.InputKind `json:"kind"`
			} `json:"details"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode accepted attention envelope %d: %v", index, err)
		}
		if got.Details.Kind != wantKinds[index] {
			t.Fatalf("accepted attention kind %d = %q, want %q", index, got.Details.Kind, wantKinds[index])
		}
	}
}

func TestUIReducerRepeatsAcceptedAttentionAfterStreamReopen(t *testing.T) {
	attentionEvents := make(chan attentionStreamOutcome, 2)
	sink := &recordingLifecycleEnvelopeSink{}
	model := newProjectedStaticUIModel(
		WithUIClientLifecycleCoordinator(newClientLifecycleCoordinator(sink, nil, nil, nil)),
	)
	model.eventDispatcher.attentionEvents = attentionEvents
	fact := &attentionFact{
		notificationKey: attentionKeyForNotificationID(clientui.AttentionNotificationID{
			Kind: clientui.AttentionNotificationKindQuestion,
			UUID: "reopened-snapshot-occurrence",
		}),
		kind:       attentionFactKindQuestion,
		occurredAt: time.Unix(9, 0).UTC(),
		summary:    "still unresolved after reopen",
	}
	attentionEvents <- fact
	attentionEvents <- fact

	model = reduceNextAcceptedExternalEvent(t, model)
	model = reduceNextAcceptedExternalEvent(t, model)

	if len(sink.envelopes) != 2 {
		t.Fatalf("reopened accepted attention envelopes = %d, want 2", len(sink.envelopes))
	}
}

func TestClientLifecycleCoordinatorEmitsOnlyAcceptedAutomaticCompactionStart(t *testing.T) {
	sink := &recordingLifecycleEnvelopeSink{}
	triggeredAt := time.Unix(6, 0).UTC()
	coordinator := newClientLifecycleCoordinator(
		sink,
		func() lifecyclecontract.Context { return lifecyclecontract.Context{} },
		func() bool { return true },
		func() time.Time { return triggeredAt },
	)
	sessionID := runtimeids.NewSessionID()
	title := "latest session title"
	coordinator.AcceptSessionIdentity(clientui.TranscriptSessionIdentity{
		SessionID:             sessionID,
		SessionName:           &title,
		ConversationFreshness: clientui.ConversationFreshnessEstablished,
	})
	failure := &clientui.TranscriptDiagnostic{
		Code:   clientui.TranscriptDiagnosticCode("dynamic_compaction_failure"),
		Detail: "dynamic failure detail",
	}
	statuses := []clientui.TranscriptCompactionStatus{
		{
			StepID:    bellTestStepID(1),
			State:     clientui.CompactionStarted,
			Mode:      "manual",
			Initiator: clientui.CompactionInitiatorUserRequested,
		},
		{
			StepID:    bellTestStepID(1),
			State:     clientui.CompactionCompleted,
			Mode:      "automatic",
			Initiator: clientui.CompactionInitiatorAutomatic,
		},
		{
			StepID:     bellTestStepID(1),
			State:      clientui.CompactionFailed,
			Mode:       "automatic",
			Initiator:  clientui.CompactionInitiatorAutomatic,
			Diagnostic: failure,
		},
		{
			StepID:    bellTestStepID(1),
			State:     clientui.CompactionStarted,
			Mode:      "manual",
			Initiator: clientui.CompactionInitiatorAutomatic,
		},
	}

	for _, status := range statuses {
		coordinator.AcceptCompactionStatus(status)
	}

	if len(sink.envelopes) != 1 {
		t.Fatalf("compaction envelopes = %d, want one automatic start", len(sink.envelopes))
	}
	raw, err := json.Marshal(sink.envelopes[0])
	if err != nil {
		t.Fatalf("marshal compaction envelope: %v", err)
	}
	var got struct {
		Category   lifecyclecontract.Category `json:"category"`
		OccurredAt time.Time                  `json:"occurred_at"`
		Focused    bool                       `json:"focused"`
		Context    struct {
			SessionID    string `json:"session_id"`
			SessionTitle string `json:"session_title"`
		} `json:"context"`
		Details struct {
			Mode string `json:"compaction_mode"`
		} `json:"details"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode compaction envelope: %v", err)
	}
	if got.Category != lifecyclecontract.CategoryResourceLimit ||
		!got.OccurredAt.Equal(triggeredAt) || !got.Focused {
		t.Fatalf("compaction envelope header = %+v", got)
	}
	if got.Context.SessionID != sessionID.String() || got.Context.SessionTitle != title {
		t.Fatalf("compaction context = %+v", got.Context)
	}
	if got.Details.Mode != "manual" {
		t.Fatalf("compaction mode = %q, want overlapping automatic mode", got.Details.Mode)
	}
}

func TestUIReducerRefreshesLifecycleContextAndSuppressesHydratedCompaction(t *testing.T) {
	sink := &recordingLifecycleEnvelopeSink{}
	triggeredAt := time.Unix(7, 0).UTC()
	coordinator := newClientLifecycleCoordinator(
		sink,
		nil,
		nil,
		func() time.Time { return triggeredAt },
	)
	model := newProjectedStaticUIModel(WithUIClientLifecycleCoordinator(coordinator))
	hydration := ongoingHydrationMessage(1)
	hydrationIdentity := hydration.Payload.Hydration.SessionIdentity
	title := "hydrated lifecycle title"
	hydrationIdentity.SessionName = &title
	hydration.Payload.Hydration.SessionIdentity = hydrationIdentity
	hydration.Payload.Hydration.ActiveCompaction = &clientui.TranscriptCompactionStatus{
		StepID:    bellTestStepID(1),
		State:     clientui.CompactionStarted,
		Mode:      "hydrated_auto",
		Initiator: clientui.CompactionInitiatorAutomatic,
	}

	model.applyAdmittedTranscriptMessageState(hydration, runtimeTupleMergeResult{})
	if len(sink.envelopes) != 0 {
		t.Fatalf("hydration emitted %d lifecycle envelopes", len(sink.envelopes))
	}

	live := clientui.TranscriptMessage{
		Kind: clientui.TranscriptMessageCompactionStatus,
		Payload: clientui.TranscriptPayload{
			CompactionStatus: &clientui.TranscriptCompactionStatus{
				StepID:    bellTestStepID(1),
				State:     clientui.CompactionStarted,
				Mode:      "live_auto",
				Initiator: clientui.CompactionInitiatorAutomatic,
			},
		},
	}
	model.applyAdmittedTranscriptMessageState(live, runtimeTupleMergeResult{})

	if len(sink.envelopes) != 1 {
		t.Fatalf("live compaction envelopes = %d, want 1", len(sink.envelopes))
	}
	raw, err := json.Marshal(sink.envelopes[0])
	if err != nil {
		t.Fatalf("marshal live compaction envelope: %v", err)
	}
	var got struct {
		Context struct {
			SessionID    string `json:"session_id"`
			SessionTitle string `json:"session_title"`
		} `json:"context"`
		Details struct {
			Mode string `json:"compaction_mode"`
		} `json:"details"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode live compaction envelope: %v", err)
	}
	if got.Context.SessionID != hydrationIdentity.SessionID.String() ||
		got.Context.SessionTitle != title ||
		got.Details.Mode != "live_auto" {
		t.Fatalf("live compaction envelope = %+v", got)
	}
}

func TestUIReducerUsesOnlyAcceptedBatchFinishedFactsForLifecycleTerminalEvents(t *testing.T) {
	sink := &recordingLifecycleEnvelopeSink{}
	model := newProjectedStaticUIModel(WithUIClientLifecycleCoordinator(
		newClientLifecycleCoordinator(sink, nil, nil, nil),
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
	model.lifecycleCoordinator = newClientLifecycleCoordinator(emptySink, nil, nil, nil)
	model.setRuntimeDisconnected(true)
	if model.nativeTurnNotifications != nil {
		model.nativeTurnNotifications.ReduceNativeInput(nativeTurnQueueAbortedInput{})
		model.nativeTurnNotifications.ReduceNativeInput(nativeTurnQueueDrainedInput{})
	}
	if len(emptySink.envelopes) != 0 {
		t.Fatalf("local queue or connection state inferred %d lifecycle terminal events", len(emptySink.envelopes))
	}
}
