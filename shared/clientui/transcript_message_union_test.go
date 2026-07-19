package clientui

import (
	"testing"
	"time"

	"core/shared/transcript"
)

func TestTranscriptMessagePayloadUnionAcceptsEveryTypedFact(t *testing.T) {
	update := transcriptTestRuntimeReadModelUpdate(t)
	streamID := transcriptTestAssistantStreamID(t)
	queueText := "queued input"
	prompt := TranscriptPrompt{
		Kind:      TranscriptPromptKindQuestion,
		State:     TranscriptPromptStatePending,
		PromptID:  PromptID("prompt-1"),
		SessionID: transcriptTestSessionID(t),
		StepID:    transcriptTestStepID(t),
		Question:  "Choose a strategy",
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
	tests := []struct {
		name    string
		message TranscriptMessage
	}{
		{
			name: "hydration",
			message: TranscriptMessage{
				Sequence: 1,
				Kind:     TranscriptMessageHydration,
				Payload: TranscriptPayload{Hydration: &TranscriptHydration{
					RuntimeReadModelUpdate: update,
					SessionIdentity:        transcriptTestSessionIdentity(t),
					SessionStatus:          transcriptTestSessionStatus(),
					CommittedRows:          []TranscriptCommittedRow{},
				}},
			},
		},
		{
			name: "committed row",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageCommittedRow,
				Payload: TranscriptPayload{CommittedRow: &TranscriptCommittedRow{
					Visibility: transcript.EntryVisibilityOngoing,
					Integrity:  transcript.RowIntegrityValid,
					Kind:       TranscriptRowAssistant,
					Assistant: &TranscriptAssistantRow{
						StepID: transcriptTestStepID(t),
						Text:   "Done",
						Phase:  transcript.AssistantPhaseFinal,
					},
				}},
			},
		},
		{
			name: "assistant delta",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageAssistantDelta,
				Payload: TranscriptPayload{AssistantDelta: &TranscriptAssistantDelta{
					StepID:   transcriptTestStepID(t),
					StreamID: streamID,
					Delta:    "hello",
					Phase:    transcript.AssistantPhaseFinal,
				}},
			},
		},
		{
			name: "assistant stream abort",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageAssistantStreamAbort,
				Payload: TranscriptPayload{AssistantStreamAbort: &TranscriptAssistantStreamAbort{
					StepID:   transcriptTestStepID(t),
					StreamID: streamID,
					Reason:   AssistantStreamAbortSuperseded,
				}},
			},
		},
		{
			name: "reasoning update",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageReasoningUpdate,
				Payload: TranscriptPayload{ReasoningUpdate: &TranscriptReasoningUpdate{
					StepID: transcriptTestStepID(t),
					Key:    "rs_1:part:0",
				}},
			},
		},
		{
			name: "reasoning reset",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageReasoningReset,
				Payload: TranscriptPayload{ReasoningReset: &TranscriptReasoningReset{
					StepID: transcriptTestStepID(t),
				}},
			},
		},
		{
			name: "tool start",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageToolStart,
				Payload: TranscriptPayload{ToolStart: &TranscriptToolStart{
					StepID:     transcriptTestStepID(t),
					ToolCallID: ToolCallID("call-1"),
					ToolName:   "shell",
				}},
			},
		},
		{
			name: "tool abort",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageToolAbort,
				Payload: TranscriptPayload{ToolAbort: &TranscriptToolAbort{
					StepID:     transcriptTestStepID(t),
					ToolCallID: ToolCallID("call-1"),
					Reason:     ToolAbortCanceled,
				}},
			},
		},
		{
			name: "user message flushed",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageUserMessageFlushed,
				Payload: TranscriptPayload{UserMessageFlushed: &TranscriptUserMessageFlushed{
					StepID: transcriptTestStepID(t),
					Operations: []RuntimeOperationRef{{
						Kind:            RuntimeOperationKindSubmit,
						ClientRequestID: transcriptTestClientRequestID(t),
					}},
				}},
			},
		},
		{
			name: "queued message state",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageQueuedMessageState,
				Payload: TranscriptPayload{QueuedMessageState: &TranscriptQueuedMessageState{
					ClientRequestID: transcriptTestClientRequestID(t),
					QueueItemID:     transcriptTestQueueItemID(t),
					Status:          QueuedUserMessageAccepted,
					Text:            &queueText,
				}},
			},
		},
		{
			name: "step state",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageStepState,
				Payload: TranscriptPayload{StepState: &TranscriptStepState{
					RunID:      transcriptTestRunID(t),
					StepID:     transcriptTestStepID(t),
					Lifecycle:  StepLifecycleStarted,
					ActiveKind: RuntimeActivityActiveKindUserTurn,
					Status:     RunStatusRunning,
				}},
			},
		},
		{
			name: "reviewer state",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageReviewerState,
				Payload: TranscriptPayload{ReviewerState: &TranscriptReviewerState{
					StepID: transcriptTestStepID(t),
					State:  ReviewerStateRunning,
				}},
			},
		},
		{
			name: "runtime read-model update",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageRuntimeReadModelUpdate,
				Payload:  TranscriptPayload{RuntimeReadModelUpdate: &update},
			},
		},
		{
			name: "session status",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageSessionStatus,
				Payload:  TranscriptPayload{SessionStatus: func() *TranscriptSessionStatus { status := transcriptTestSessionStatus(); return &status }()},
			},
		},
		{
			name: "session identity",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageSessionIdentity,
				Payload:  TranscriptPayload{SessionIdentity: func() *TranscriptSessionIdentity { identity := transcriptTestSessionIdentity(t); return &identity }()},
			},
		},
		{
			name: "compaction status",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageCompactionStatus,
				Payload: TranscriptPayload{CompactionStatus: &TranscriptCompactionStatus{
					StepID: transcriptTestStepID(t),
					State:  CompactionStarted,
					Mode:   "auto",
				}},
			},
		},
		{
			name: "context usage",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageContextUsage,
				Payload: TranscriptPayload{ContextUsage: &TranscriptContextUsage{
					WindowTokens: 1_000,
				}},
			},
		},
		{
			name: "goal status",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageGoalStatus,
				Payload:  TranscriptPayload{GoalStatus: &TranscriptGoalStatus{}},
			},
		},
		{
			name: "background activity",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageBackgroundActivity,
				Payload: TranscriptPayload{BackgroundActivity: &TranscriptBackgroundActivity{
					ActivityID:  transcriptTestBackgroundActivityID(t),
					ProcessID:   ProcessID("process-1"),
					OwnerRunID:  transcriptTestRunID(t),
					OwnerStepID: transcriptTestStepID(t),
					Lifecycle:   BackgroundLifecycleBackgrounded,
					Command:     "go test ./...",
					Workdir:     "/repo",
				}},
			},
		},
		{
			name: "prompt pending",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessagePromptPending,
				Payload:  TranscriptPayload{PromptPending: &prompt},
			},
		},
		{
			name: "prompt resolved",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessagePromptResolved,
				Payload: TranscriptPayload{PromptResolved: func() *TranscriptPrompt {
					resolved := prompt
					resolved.State = TranscriptPromptStateResolved
					return &resolved
				}()},
			},
		},
		{
			name: "worktree transition outcome",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageWorktreeTransitionOutcome,
				Payload: TranscriptPayload{WorktreeTransitionOutcome: &TranscriptWorktreeTransitionOutcome{
					OperationID: NewWorktreeTransitionID(),
					Transition:  WorktreeTransitionEnter,
					State:       WorktreeTransitionCompleted,
				}},
			},
		},
		{
			name: "operational diagnostic",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageOperationalDiagnostic,
				Payload: TranscriptPayload{OperationalDiagnostic: &TranscriptOperationalDiagnostic{
					Code:   OperationalDiagnosticSleepGuardFailed,
					Detail: "operating system rejected sleep prevention",
				}},
			},
		},
		{
			name: "live run batch finished",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageLiveRunBatchFinished,
				Payload: TranscriptPayload{LiveRunBatchFinished: &TranscriptLiveRunBatchFinished{
					Disposition:        LiveRunBatchDispositionFinalAnswer,
					FinishedAt:         time.Unix(1_700_000_000, 0),
					FinalAnswerPreview: &TranscriptFinalAnswerPreview{Markdown: "Done"},
				}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.message.ValidatePayload(); err != nil {
				t.Fatalf("validate transcript message: %v", err)
			}
		})
	}
}

func TestTranscriptMessagePayloadUnionRejectsKindMismatchAcrossAllArms(t *testing.T) {
	message := TranscriptMessage{
		Sequence: 2,
		Kind:     TranscriptMessageToolStart,
		Payload: TranscriptPayload{
			ReasoningReset: &TranscriptReasoningReset{StepID: transcriptTestStepID(t)},
		},
	}
	if err := message.ValidatePayload(); err == nil {
		t.Fatal("accepted transcript payload under the wrong discriminator")
	}
}
