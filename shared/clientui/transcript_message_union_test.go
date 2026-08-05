package clientui

import (
	"testing"
	"time"

	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestTranscriptEventDerivesKindFromItsPayload(t *testing.T) {
	event := NewTranscriptEvent(TranscriptAssistantDelta{
		StepID:   transcriptTestStepID(t),
		StreamID: transcriptTestAssistantStreamID(t),
		Delta:    "hello",
		Phase:    transcript.AssistantPhaseFinal,
	})

	if got := event.Kind(); got != TranscriptMessageAssistantDelta {
		t.Fatalf("event kind = %q, want %q", got, TranscriptMessageAssistantDelta)
	}
	payload, ok := event.Payload().(TranscriptAssistantDelta)
	if !ok {
		t.Fatalf("event payload type = %T, want TranscriptAssistantDelta", event.Payload())
	}
	if payload.Delta != "hello" {
		t.Fatalf("event payload delta = %q, want hello", payload.Delta)
	}
}

func TestTranscriptEventPayloadsUseOneTypedConstructionPath(t *testing.T) {
	update := transcriptTestRuntimeReadModelUpdate(t)
	streamID := transcriptTestAssistantStreamID(t)
	prompt := TranscriptPrompt{
		Kind:      TranscriptPromptKindQuestion,
		Status:    TranscriptPromptStatusPending,
		PromptID:  PromptID("prompt-1"),
		SessionID: transcriptTestSessionID(t),
		StepID:    transcriptTestStepID(t),
		Question:  "Choose a strategy",
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
	queueText := "queued input"
	tests := []struct {
		name  string
		event TranscriptEvent
		kind  TranscriptMessageKind
	}{
		{"hydration", NewTranscriptEvent(TranscriptHydration{
			RuntimeReadModelUpdate: update,
			SessionIdentity:        transcriptTestSessionIdentity(t),
			SessionStatus:          transcriptTestSessionStatus(),
			CommittedRows:          []TranscriptCommittedRow{},
		}), TranscriptMessageHydration},
		{"committed row", NewTranscriptEvent(TranscriptCommittedRow{
			Visibility: transcript.EntryVisibilityOngoing,
			Integrity:  transcript.RowIntegrityValid,
			Kind:       TranscriptRowAssistant,
			Assistant: &TranscriptAssistantRow{
				StepID: transcriptTestStepID(t),
				Text:   "Done",
				Phase:  transcript.AssistantPhaseFinal,
			},
		}), TranscriptMessageCommittedRow},
		{"assistant delta", NewTranscriptEvent(TranscriptAssistantDelta{
			StepID: transcriptTestStepID(t), StreamID: streamID, Delta: "hello",
			Phase: transcript.AssistantPhaseFinal,
		}), TranscriptMessageAssistantDelta},
		{"assistant stream abort", NewTranscriptEvent(TranscriptAssistantStreamAbort{
			StepID: transcriptTestStepID(t), StreamID: streamID,
			Reason: AssistantStreamAbortSuperseded,
		}), TranscriptMessageAssistantStreamAbort},
		{"thinking status", NewTranscriptEvent(TranscriptThinkingStatusUpdate{
			StepID: transcriptTestStepID(t), Text: "Thinking",
		}), TranscriptMessageThinkingStatusUpdate},
		{"reasoning trace", NewTranscriptEvent(TranscriptReasoningTraceUpdate{
			StepID: transcriptTestStepID(t),
			Identity: TranscriptReasoningTraceIdentity{Kent: func() *runtimeids.ReasoningTraceID {
				id := runtimeids.NewReasoningTraceID()
				return &id
			}()},
			CompactText: "Planning",
			Text:        "Planning",
		}), TranscriptMessageReasoningTraceUpdate},
		{"reasoning trace reset", NewTranscriptEvent(TranscriptReasoningTraceReset{
			StepID: transcriptTestStepID(t),
		}), TranscriptMessageReasoningTraceReset},
		{"tool start", NewTranscriptEvent(TranscriptToolStart{
			StepID: transcriptTestStepID(t), ToolCallID: ToolCallID("call-1"), ToolName: "shell",
		}), TranscriptMessageToolStart},
		{"tool abort", NewTranscriptEvent(TranscriptToolAbort{
			StepID: transcriptTestStepID(t), ToolCallID: ToolCallID("call-1"), Reason: ToolAbortCanceled,
		}), TranscriptMessageToolAbort},
		{"user message flushed", NewTranscriptEvent(TranscriptUserMessageFlushed{
			StepID:     transcriptTestStepID(t),
			Operations: []RuntimeOperationRef{{Kind: RuntimeOperationKindSubmit, ClientRequestID: transcriptTestClientRequestID(t)}},
		}), TranscriptMessageUserMessageFlushed},
		{"queued message state", NewTranscriptEvent(TranscriptQueuedMessageState{
			ClientRequestID: transcriptTestClientRequestID(t), QueueItemID: transcriptTestQueueItemID(t),
			Status: QueuedUserMessageAccepted, Text: &queueText,
		}), TranscriptMessageQueuedMessageState},
		{"step state", NewTranscriptEvent(TranscriptStepState{
			RunID: transcriptTestRunID(t), StepID: transcriptTestStepID(t),
			Lifecycle: StepLifecycleStarted, ActiveKind: RuntimeActivityActiveKindUserTurn, Status: RunStatusRunning,
		}), TranscriptMessageStepState},
		{"reviewer state", NewTranscriptEvent(TranscriptReviewerState{
			StepID: transcriptTestStepID(t), State: ReviewerStateRunning,
		}), TranscriptMessageReviewerState},
		{"runtime read-model update", NewTranscriptEvent(update), TranscriptMessageRuntimeReadModelUpdate},
		{"session status", NewTranscriptEvent(transcriptTestSessionStatus()), TranscriptMessageSessionStatus},
		{"session identity", NewTranscriptEvent(transcriptTestSessionIdentity(t)), TranscriptMessageSessionIdentity},
		{"compaction status", NewTranscriptEvent(TranscriptCompactionStatus{
			StepID: transcriptTestStepID(t), State: CompactionStarted, Mode: "auto",
		}), TranscriptMessageCompactionStatus},
		{"context usage", NewTranscriptEvent(TranscriptContextUsage{WindowTokens: 1_000}), TranscriptMessageContextUsage},
		{"goal status", NewTranscriptEvent(TranscriptGoalStatus{}), TranscriptMessageGoalStatus},
		{"background activity", NewTranscriptEvent(TranscriptBackgroundActivity{
			ActivityID: transcriptTestBackgroundActivityID(t), ProcessID: ProcessID("process-1"),
			OwnerRunID: transcriptTestRunID(t), OwnerStepID: transcriptTestStepID(t),
			Lifecycle: BackgroundLifecycleBackgrounded, Command: "go test ./...", Workdir: "/repo",
		}), TranscriptMessageBackgroundActivity},
		{"prompt pending", NewTranscriptEvent(prompt), TranscriptMessagePrompt},
		{"prompt resolved", NewTranscriptEvent(func() TranscriptPrompt {
			prompt.Status = TranscriptPromptStatusResolved
			return prompt
		}()), TranscriptMessagePrompt},
		{"worktree transition outcome", NewTranscriptEvent(TranscriptWorktreeTransitionOutcome{
			OperationID: NewWorktreeTransitionID(), Transition: WorktreeTransitionEnter,
			State: WorktreeTransitionCompleted,
		}), TranscriptMessageWorktreeTransitionOutcome},
		{"operational diagnostic", NewTranscriptEvent(TranscriptOperationalDiagnostic{
			Code:   OperationalDiagnosticSleepGuardFailed,
			Detail: "operating system rejected sleep prevention",
		}), TranscriptMessageOperationalDiagnostic},
		{"live run finished", NewTranscriptEvent(TranscriptLiveRunResult{
			Status: LiveRunStatusCompleted, ResultKind: LiveRunResultNoFinalAnswer,
			StartedAt: time.Unix(1_700_000_000, 0), FinishedAt: time.Unix(1_700_000_001, 0),
		}), TranscriptMessageLiveRunFinished},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.event.Kind(); got != test.kind {
				t.Fatalf("event kind = %q, want %q", got, test.kind)
			}
			if err := test.event.Validate(); err != nil {
				t.Fatalf("validate transcript event: %v", err)
			}
		})
	}
}

func TestTranscriptMessageValidationDelegatesToItsTypedEvent(t *testing.T) {
	event := NewTranscriptEvent(transcriptTestRuntimeReadModelUpdate(t))
	message := NewTranscriptMessage(2, event)
	if err := message.Validate(); err != nil {
		t.Fatalf("validate transcript message: %v", err)
	}
}

func TestTranscriptMessageRejectsUninitializedEvent(t *testing.T) {
	if err := NewTranscriptMessage(2, TranscriptEvent{}).ValidatePayload(); err == nil {
		t.Fatal("accepted uninitialized transcript event")
	}
}
