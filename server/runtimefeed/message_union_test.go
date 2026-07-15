package runtimefeed

import (
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestTranscriptMessagePayloadUnionAcceptsEveryTypedFact(t *testing.T) {
	update := runtimefeedTestRuntimeReadModelUpdate(t)
	streamID := runtimefeedTestAssistantStreamID(t)
	queueText := "queued input"
	prompt := TranscriptPrompt{
		Kind:      TranscriptPromptKindQuestion,
		State:     TranscriptPromptStatePending,
		PromptID:  PromptID("prompt-1"),
		SessionID: runtimefeedTestSessionID(t),
		StepID:    runtimefeedTestStepID(t),
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
					SessionIdentity:        runtimefeedTestSessionIdentity(t),
					SessionStatus:          runtimefeedTestSessionStatus(),
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
						StepID: runtimefeedTestStepID(t),
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
					StepID:   runtimefeedTestStepID(t),
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
					StepID:   runtimefeedTestStepID(t),
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
					StepID: runtimefeedTestStepID(t),
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
					StepID: runtimefeedTestStepID(t),
				}},
			},
		},
		{
			name: "tool start",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageToolStart,
				Payload: TranscriptPayload{ToolStart: &TranscriptToolStart{
					StepID:     runtimefeedTestStepID(t),
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
					StepID:     runtimefeedTestStepID(t),
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
					StepID: runtimefeedTestStepID(t),
					Operations: []RuntimeOperationRef{{
						Kind:            clientui.RuntimeOperationKindSubmit,
						ClientRequestID: runtimefeedTestClientRequestID(t),
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
					ClientRequestID: runtimefeedTestClientRequestID(t),
					QueueItemID:     runtimefeedTestQueueItemID(t),
					Status:          clientui.QueuedUserMessageAccepted,
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
					RunID:      runtimefeedTestRunID(t),
					StepID:     runtimefeedTestStepID(t),
					Lifecycle:  StepLifecycleStarted,
					ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
					Status:     clientui.RunStatusRunning,
				}},
			},
		},
		{
			name: "reviewer state",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageReviewerState,
				Payload: TranscriptPayload{ReviewerState: &TranscriptReviewerState{
					StepID: runtimefeedTestStepID(t),
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
				Payload:  TranscriptPayload{SessionStatus: func() *TranscriptSessionStatus { status := runtimefeedTestSessionStatus(); return &status }()},
			},
		},
		{
			name: "session identity",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageSessionIdentity,
				Payload:  TranscriptPayload{SessionIdentity: func() *TranscriptSessionIdentity { identity := runtimefeedTestSessionIdentity(t); return &identity }()},
			},
		},
		{
			name: "compaction status",
			message: TranscriptMessage{
				Sequence: 2,
				Kind:     TranscriptMessageCompactionStatus,
				Payload: TranscriptPayload{CompactionStatus: &TranscriptCompactionStatus{
					StepID: runtimefeedTestStepID(t),
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
					ActivityID:  runtimefeedTestBackgroundActivityID(t),
					ProcessID:   ProcessID("process-1"),
					OwnerRunID:  runtimefeedTestRunID(t),
					OwnerStepID: runtimefeedTestStepID(t),
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
					OperationID: clientui.NewWorktreeTransitionID(),
					Transition:  clientui.WorktreeTransitionEnter,
					State:       clientui.WorktreeTransitionCompleted,
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
			ReasoningReset: &TranscriptReasoningReset{StepID: runtimefeedTestStepID(t)},
		},
	}
	if err := message.ValidatePayload(); err == nil {
		t.Fatal("accepted transcript payload under the wrong discriminator")
	}
}
