package clientui

import (
	"testing"
	"time"

	"core/shared/runtimeids"
	"core/shared/runtimeinput"
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
	fastModeEnabled := true
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
			Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
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
			StepID: transcriptTestStepIDPointer(t),
		}), TranscriptMessageUserMessageFlushed},
		{"queued message state", NewTranscriptEvent(TranscriptQueuedMessageState{
			QueueItemID: transcriptTestQueueItemID(t),
			Status:      QueuedUserMessageAccepted, Text: &queueText,
		}), TranscriptMessageQueuedMessageState},
		{"pending work changed", NewTranscriptEvent(TranscriptPendingWorkChanged{}), TranscriptMessagePendingWorkChanged},
		{"pending work technical restoration", NewTranscriptEvent(TranscriptPendingWorkRestored{
			Restoration: runtimeinput.PendingWorkTechnicalRestoration{
				ItemID:         transcriptTestQueueItemID(t),
				Kind:           runtimeinput.PendingWorkItemKindManualCompaction,
				CanonicalInput: "/compact",
			},
		}), TranscriptMessagePendingWorkRestored},
		{"session setting feedback", NewTranscriptEvent(TranscriptSessionSettingFeedback{
			Kind:     SessionSettingFastMode,
			Changed:  true,
			FastMode: &fastModeEnabled,
		}), TranscriptMessageSessionSettingFeedback},
		{"interrupted human input", NewTranscriptEvent(TranscriptHumanInputInterrupted{
			Items: []TranscriptInterruptedHumanInputItem{{
				QueueItemID: transcriptTestQueueItemID(t),
				Text:        "restore verbatim",
			}},
		}), TranscriptMessageHumanInputInterrupted},
		{"step state", NewTranscriptEvent(TranscriptStepState{
			RunID: transcriptTestRunID(t), StepID: transcriptTestStepID(t),
			Lifecycle: StepLifecycleStarted, ActiveKind: RuntimeActivityActiveKindUserTurn, Status: RunStatusRunning,
		}), TranscriptMessageStepState},
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

func TestSessionSettingFeedbackRequiresOneTypedResultForItsKind(t *testing.T) {
	name := "renamed"
	enabled := true
	tests := []TranscriptSessionSettingFeedback{
		{Kind: SessionSettingFastMode},
		{Kind: SessionSettingFastMode, SessionName: &name},
		{Kind: SessionSettingFastMode, FastMode: &enabled, Questions: &enabled},
	}
	for _, feedback := range tests {
		if err := feedback.Validate(); err == nil {
			t.Fatalf("accepted incoherent setting feedback: %+v", feedback)
		}
	}
}
