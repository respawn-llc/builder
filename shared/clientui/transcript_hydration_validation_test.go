package clientui

import (
	"testing"
	"time"

	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestTranscriptHydrationRejectsStepScopedFactsOutsideCanonicalActiveStep(t *testing.T) {
	runningHydration := func() (TranscriptHydration, runtimeids.RunID, runtimeids.StepID) {
		runID := transcriptTestRunID(t)
		stepID := transcriptTestStepID(t)
		hydration := TranscriptHydration{
			SessionIdentity:        transcriptTestSessionIdentity(t),
			SessionStatus:          transcriptTestSessionStatus(),
			RuntimeReadModelUpdate: transcriptTestRuntimeReadModelUpdate(t),
			TailSegment:            TranscriptTailSegment{Entries: []TranscriptCommittedRow{}},
			ActiveStep: &TranscriptStepState{
				RunID:      runID,
				StepID:     stepID,
				Lifecycle:  StepLifecycleStarted,
				ActiveKind: RuntimeActivityActiveKindUserTurn,
				Status:     RunStatusRunning,
			},
		}
		hydration.RuntimeReadModelUpdate.Activity = RuntimeActivity{
			State:    RuntimeActivityRunning,
			Reviewer: ReviewerActivityInactive,
			ActiveStep: &RuntimeActiveStep{
				RunID:      runID,
				StepID:     stepID,
				ActiveKind: RuntimeActivityActiveKindUserTurn,
			},
			QueueAccepting: true,
		}
		return hydration, runID, stepID
	}

	valid, _, stepID := runningHydration()
	if err := valid.Validate(); err != nil {
		t.Fatalf("validate coherent running hydration: %v", err)
	}

	otherStepID, err := runtimeids.ParseStepID("77777777-7777-4777-8777-777777777777")
	if err != nil {
		t.Fatalf("parse other step id: %v", err)
	}
	otherSessionID, err := runtimeids.ParseSessionID("session-2")
	if err != nil {
		t.Fatalf("parse other session id: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*TranscriptHydration)
	}{
		{
			name: "active step state",
			mutate: func(hydration *TranscriptHydration) {
				hydration.ActiveStep.StepID = otherStepID
			},
		},
		{
			name: "assistant stream",
			mutate: func(hydration *TranscriptHydration) {
				hydration.ActiveAssistant = &TranscriptAssistantStream{
					StepID:   otherStepID,
					StreamID: transcriptTestAssistantStreamID(t),
					Text:     "partial",
					Phase:    transcript.AssistantPhaseFinal,
				}
			},
		},
		{
			name: "reasoning",
			mutate: func(hydration *TranscriptHydration) {
				hydration.ActiveReasoningTraces = []TranscriptReasoningTraceUpdate{{
					StepID: otherStepID,
					Identity: TranscriptReasoningTraceIdentity{Kent: func() *runtimeids.ReasoningTraceID {
						id := runtimeids.NewReasoningTraceID()
						return &id
					}()},
					CompactText: "reasoning",
					Text:        "reasoning",
				}}
			},
		},
		{
			name: "compaction",
			mutate: func(hydration *TranscriptHydration) {
				hydration.ActiveCompaction = &TranscriptCompactionStatus{
					StepID: otherStepID,
					State:  CompactionStarted,
					Mode:   "auto",
				}
			},
		},
		{
			name: "tool",
			mutate: func(hydration *TranscriptHydration) {
				hydration.InFlightTools = []TranscriptToolStart{{
					StepID:     otherStepID,
					ToolCallID: ToolCallID("call-1"),
					ToolName:   "shell",
				}}
			},
		},
		{
			name: "prompt step",
			mutate: func(hydration *TranscriptHydration) {
				hydration.PendingPrompts = []TranscriptPrompt{{
					Kind:      TranscriptPromptKindQuestion,
					Status:    TranscriptPromptStatusPending,
					PromptID:  PromptID("prompt-1"),
					SessionID: hydration.SessionIdentity.SessionID,
					StepID:    otherStepID,
					Question:  "Choose",
					CreatedAt: time.Unix(1_700_000_000, 0),
				}}
			},
		},
		{
			name: "prompt session",
			mutate: func(hydration *TranscriptHydration) {
				hydration.PendingPrompts = []TranscriptPrompt{{
					Kind:      TranscriptPromptKindQuestion,
					Status:    TranscriptPromptStatusPending,
					PromptID:  PromptID("prompt-1"),
					SessionID: otherSessionID,
					StepID:    stepID,
					Question:  "Choose",
					CreatedAt: time.Unix(1_700_000_000, 0),
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hydration, _, _ := runningHydration()
			test.mutate(&hydration)
			if err := hydration.Validate(); err == nil {
				t.Fatalf("accepted incoherent running hydration: %+v", hydration)
			}
		})
	}

	idle := valid
	idle.RuntimeReadModelUpdate.Activity = RuntimeActivity{
		State:          RuntimeActivityRegisteredIdle,
		Reviewer:       ReviewerActivityInactive,
		QueueAccepting: true,
	}
	if err := idle.Validate(); err == nil {
		t.Fatal("accepted active step state without a canonical runtime active step")
	}
}

func TestTranscriptHydrationRequiresClosedTailSegmentOlderBoundary(t *testing.T) {
	olderCursor := int64(17)
	valid := TranscriptHydration{
		SessionIdentity:        transcriptTestSessionIdentity(t),
		SessionStatus:          transcriptTestSessionStatus(),
		RuntimeReadModelUpdate: transcriptTestRuntimeReadModelUpdate(t),
		TailSegment: TranscriptTailSegment{
			HasMoreAbove: true,
			OlderCursor:  &olderCursor,
			Entries:      []TranscriptCommittedRow{},
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid hydration tail rejected: %v", err)
	}

	missingCursor := valid
	missingCursor.TailSegment.OlderCursor = nil
	if err := missingCursor.Validate(); err == nil {
		t.Fatal("hydration tail with older history but no cursor was accepted")
	}

	unexpectedCursor := valid
	unexpectedCursor.TailSegment.HasMoreAbove = false
	if err := unexpectedCursor.Validate(); err == nil {
		t.Fatal("hydration tail with no older history but a cursor was accepted")
	}
}

func TestTranscriptHydrationRejectsTerminalOrNondeterministicLedgerState(t *testing.T) {
	valid := func() TranscriptHydration {
		return TranscriptHydration{
			SessionIdentity:        transcriptTestSessionIdentity(t),
			SessionStatus:          transcriptTestSessionStatus(),
			RuntimeReadModelUpdate: transcriptTestRuntimeReadModelUpdate(t),
			TailSegment:            TranscriptTailSegment{Entries: []TranscriptCommittedRow{}},
		}
	}

	resolvedPrompt := TranscriptPrompt{
		Kind:      TranscriptPromptKindQuestion,
		Status:    TranscriptPromptStatusResolved,
		PromptID:  PromptID("prompt-1"),
		SessionID: transcriptTestSessionID(t),
		StepID:    transcriptTestStepID(t),
		Question:  "Choose a strategy",
		CreatedAt: time.Unix(1_700_000_000, 0),
	}
	terminalBackground := TranscriptBackgroundActivity{
		ActivityID:  transcriptTestBackgroundActivityID(t),
		ProcessID:   ProcessID("process-1"),
		OwnerRunID:  transcriptTestRunID(t),
		OwnerStepID: transcriptTestStepID(t),
		Lifecycle:   BackgroundLifecycleCompleted,
		Command:     "go test ./...",
		Workdir:     "/repo",
	}
	tests := []TranscriptHydration{
		func() TranscriptHydration {
			hydration := valid()
			hydration.TailSegment.Entries = nil
			return hydration
		}(),
		func() TranscriptHydration {
			hydration := valid()
			hydration.PendingPrompts = []TranscriptPrompt{resolvedPrompt}
			return hydration
		}(),
		func() TranscriptHydration {
			hydration := valid()
			hydration.BackgroundActivities = []TranscriptBackgroundActivity{terminalBackground}
			return hydration
		}(),
	}
	for _, hydration := range tests {
		if err := hydration.Validate(); err == nil {
			t.Fatalf("accepted invalid hydration state: %+v", hydration)
		}
	}
}

func TestTranscriptHydrationRequiresPromptsOrderedByCreationThenID(t *testing.T) {
	createdAt := time.Unix(1_700_000_000, 0)
	prompt := func(id PromptID, created time.Time) TranscriptPrompt {
		return TranscriptPrompt{
			Kind:      TranscriptPromptKindQuestion,
			Status:    TranscriptPromptStatusPending,
			PromptID:  id,
			SessionID: transcriptTestSessionID(t),
			StepID:    transcriptTestStepID(t),
			Question:  "Choose a strategy",
			CreatedAt: created,
		}
	}
	hydration := TranscriptHydration{
		SessionIdentity:        transcriptTestSessionIdentity(t),
		SessionStatus:          transcriptTestSessionStatus(),
		RuntimeReadModelUpdate: transcriptTestRuntimeReadModelUpdate(t),
		TailSegment:            TranscriptTailSegment{Entries: []TranscriptCommittedRow{}},
		PendingPrompts: []TranscriptPrompt{
			prompt(PromptID("prompt-b"), createdAt),
			prompt(PromptID("prompt-a"), createdAt),
		},
	}
	if err := hydration.Validate(); err == nil {
		t.Fatal("accepted prompts outside creation-time/id order")
	}
}
