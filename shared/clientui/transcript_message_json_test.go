package clientui

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestTranscriptMessageJSONUsesTaggedPayloadEnvelope(t *testing.T) {
	message := NewTranscriptMessage(2, NewTranscriptEvent(TranscriptAssistantDelta{
		StepID:   transcriptTestStepID(t),
		StreamID: transcriptTestAssistantStreamID(t),
		Delta:    "hello",
		Phase:    transcript.AssistantPhaseFinal,
	}))

	data, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal transcript message: %v", err)
	}
	const want = `{"sequence":2,"kind":"assistant_delta","payload":{"StepID":"22222222-2222-4222-8222-222222222222","StreamID":"44444444-4444-4444-8444-444444444444","Delta":"hello","Phase":"final_answer"}}`
	if string(data) != want {
		t.Fatalf("transcript message JSON = %s, want %s", data, want)
	}

	var roundTrip TranscriptMessage
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("unmarshal transcript message: %v", err)
	}
	if roundTrip.Sequence != message.Sequence || roundTrip.Kind() != message.Kind() {
		t.Fatalf("round-trip envelope = sequence %d kind %q", roundTrip.Sequence, roundTrip.Kind())
	}
	if err := roundTrip.ValidatePayload(); err != nil {
		t.Fatalf("validate round-trip payload: %v", err)
	}
}

func TestTranscriptPromptJSONPreservesStateKeyInBothContexts(t *testing.T) {
	prompt := TranscriptPrompt{
		Kind:      TranscriptPromptKindQuestion,
		Status:    TranscriptPromptStatusResolved,
		PromptID:  PromptID("prompt-1"),
		SessionID: transcriptTestSessionID(t),
		StepID:    transcriptTestStepID(t),
		Question:  "Choose",
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
	standalone, err := json.Marshal(NewTranscriptMessage(2, NewTranscriptEvent(prompt)))
	if err != nil {
		t.Fatalf("marshal standalone prompt: %v", err)
	}
	assertPromptStateWire(t, standalone, "resolved", false)

	hydration, err := json.Marshal(NewTranscriptMessage(1, NewTranscriptEvent(TranscriptHydration{
		SessionIdentity:        transcriptTestSessionIdentity(t),
		SessionStatus:          transcriptTestSessionStatus(),
		RuntimeReadModelUpdate: transcriptTestRuntimeReadModelUpdate(t),
		PendingPrompts: []TranscriptPrompt{func() TranscriptPrompt {
			prompt.Status = TranscriptPromptStatusPending
			return prompt
		}()},
	})))
	if err != nil {
		t.Fatalf("marshal hydration: %v", err)
	}
	assertPromptStateWire(t, hydration, "pending", true)
}

func TestTranscriptMessageJSONRejectsMalformedEnvelopeShapes(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"missing payload", `{"sequence":2,"kind":"assistant_delta"}`, true},
		{"null payload", `{"sequence":2,"kind":"assistant_delta","payload":null}`, true},
		{"unknown kind", `{"sequence":2,"kind":"unknown","payload":{}}`, true},
		{"incompatible field type", `{"sequence":2,"kind":"assistant_delta","payload":{"StepID":7}}`, true},
		{"unknown field", `{"sequence":2,"kind":"assistant_delta","payload":{"StepID":"22222222-2222-4222-8222-222222222222","StreamID":"44444444-4444-4444-8444-444444444444","Delta":"hello","Phase":"final_answer"},"unknown":true}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var message TranscriptMessage
			err := json.Unmarshal([]byte(test.input), &message)
			if test.wantErr && err == nil {
				t.Fatalf("accepted malformed transcript message: %s", test.input)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unknown field rejected: %q: %v", test.input, err)
			}
		})
	}
}

func TestTranscriptMessageJSONAcceptsSemanticallyInvalidPayloadValues(t *testing.T) {
	var message TranscriptMessage
	if err := json.Unmarshal([]byte(`{"sequence":2,"kind":"assistant_delta","payload":{"StepID":"22222222-2222-4222-8222-222222222222","StreamID":"44444444-4444-4444-8444-444444444444","Delta":"","Phase":"invalid"}}`), &message); err != nil {
		t.Fatalf("rejected structurally decodable payload: %v", err)
	}
	if err := message.ValidatePayload(); err == nil {
		t.Fatal("semantically invalid payload unexpectedly validated")
	}
}

func TestTranscriptMessageJSONRejectsUninitializedSerialization(t *testing.T) {
	if _, err := json.Marshal(TranscriptMessage{}); err == nil {
		t.Fatal("marshaled uninitialized transcript message")
	}
}

func assertPromptStateWire(t *testing.T, data []byte, want string, hydration bool) {
	t.Helper()
	var envelope struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if hydration {
		var payload struct {
			PendingPrompts []map[string]json.RawMessage `json:"PendingPrompts"`
		}
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			t.Fatalf("decode hydration payload: %v", err)
		}
		if len(payload.PendingPrompts) != 1 {
			t.Fatalf("hydration pending prompts = %d, want 1", len(payload.PendingPrompts))
		}
		if got := string(payload.PendingPrompts[0]["State"]); got != `"`+want+`"` {
			t.Fatalf("hydration prompt state = %s, want %q", got, want)
		}
		return
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		t.Fatalf("decode prompt payload: %v", err)
	}
	if got := string(payload["State"]); got != `"`+want+`"` {
		t.Fatalf("prompt state = %s, want %q", got, want)
	}
}

func TestTranscriptMessageJSONRoundTripsEveryVariant(t *testing.T) {
	update := transcriptTestRuntimeReadModelUpdate(t)
	stepID := transcriptTestStepID(t)
	streamID := transcriptTestAssistantStreamID(t)
	prompt := TranscriptPrompt{
		Kind: TranscriptPromptKindQuestion, Status: TranscriptPromptStatusPending,
		PromptID: "prompt-1", SessionID: transcriptTestSessionID(t), StepID: stepID,
		Question: "Choose", CreatedAt: time.Unix(1_700_000_000, 0),
	}
	text := "queued"
	final := "done"
	events := []TranscriptEvent{
		NewTranscriptEvent(TranscriptHydration{RuntimeReadModelUpdate: update, SessionIdentity: transcriptTestSessionIdentity(t), SessionStatus: transcriptTestSessionStatus(), CommittedRows: []TranscriptCommittedRow{}}),
		NewTranscriptEvent(TranscriptCommittedRow{Visibility: transcript.EntryVisibilityOngoing, Integrity: transcript.RowIntegrityValid, Kind: TranscriptRowAssistant, Assistant: &TranscriptAssistantRow{StepID: stepID, Text: "done", Phase: transcript.AssistantPhaseFinal}}),
		NewTranscriptEvent(TranscriptAssistantDelta{StepID: stepID, StreamID: streamID, Delta: "hello", Phase: transcript.AssistantPhaseFinal}),
		NewTranscriptEvent(TranscriptAssistantStreamAbort{StepID: stepID, StreamID: streamID, Reason: AssistantStreamAbortSuperseded}),
		NewTranscriptEvent(TranscriptThinkingStatusUpdate{StepID: stepID, Text: "Thinking"}),
		NewTranscriptEvent(TranscriptReasoningTraceUpdate{
			StepID: stepID,
			Identity: TranscriptReasoningTraceIdentity{Kent: func() *runtimeids.ReasoningTraceID {
				id := runtimeids.NewReasoningTraceID()
				return &id
			}()},
			CompactText: "reasoning",
			Text:        "reasoning",
		}),
		NewTranscriptEvent(TranscriptReasoningTraceReset{StepID: stepID}),
		NewTranscriptEvent(TranscriptToolStart{StepID: stepID, ToolCallID: "call-1", ToolName: "shell"}),
		NewTranscriptEvent(TranscriptToolAbort{StepID: stepID, ToolCallID: "call-1", Reason: ToolAbortCanceled}),
		NewTranscriptEvent(TranscriptUserMessageFlushed{StepID: stepID}),
		NewTranscriptEvent(TranscriptQueuedMessageState{QueueItemID: transcriptTestQueueItemID(t), Status: QueuedUserMessageAccepted, Text: &text}),
		NewTranscriptEvent(TranscriptStepState{RunID: transcriptTestRunID(t), StepID: stepID, Lifecycle: StepLifecycleStarted, ActiveKind: RuntimeActivityActiveKindUserTurn, Status: RunStatusRunning}),
		NewTranscriptEvent(TranscriptReviewerState{StepID: stepID, State: ReviewerStateRunning}),
		NewTranscriptEvent(update),
		NewTranscriptEvent(transcriptTestSessionStatus()),
		NewTranscriptEvent(transcriptTestSessionIdentity(t)),
		NewTranscriptEvent(TranscriptCompactionStatus{StepID: stepID, State: CompactionStarted, Mode: "auto"}),
		NewTranscriptEvent(TranscriptContextUsage{WindowTokens: 1000}),
		NewTranscriptEvent(TranscriptGoalStatus{}),
		NewTranscriptEvent(TranscriptBackgroundActivity{ActivityID: transcriptTestBackgroundActivityID(t), ProcessID: "process-1", OwnerRunID: transcriptTestRunID(t), OwnerStepID: stepID, Lifecycle: BackgroundLifecycleBackgrounded, Command: "go test", Workdir: "/repo"}),
		NewTranscriptEvent(prompt),
		NewTranscriptEvent(TranscriptWorktreeTransitionOutcome{OperationID: NewWorktreeTransitionID(), Transition: WorktreeTransitionEnter, State: WorktreeTransitionCompleted}),
		NewTranscriptEvent(TranscriptOperationalDiagnostic{Code: OperationalDiagnosticSleepGuardFailed, Detail: "failed"}),
		NewTranscriptEvent(TranscriptLiveRunResult{Status: LiveRunStatusCompleted, ResultKind: LiveRunResultAssistantFinalAnswer, FinalAnswer: &final, StartedAt: time.Unix(1_700_000_000, 0), FinishedAt: time.Unix(1_700_000_001, 0)}),
	}
	for _, event := range events {
		data, err := json.Marshal(NewTranscriptMessage(2, event))
		if err != nil {
			t.Fatalf("marshal %q: %v", event.Kind(), err)
		}
		var decoded TranscriptMessage
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal %q: %v", event.Kind(), err)
		}
		if decoded.Kind() != event.Kind() || reflect.TypeOf(decoded.Payload()) != reflect.TypeOf(event.Payload()) {
			t.Fatalf("round-trip %q produced %T", event.Kind(), decoded.Payload())
		}
	}
}
