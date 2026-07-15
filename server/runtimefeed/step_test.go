package runtimefeed

import (
	"testing"

	"core/shared/clientui"
)

func TestTranscriptStepStateUsesTypedLifecycleTransitions(t *testing.T) {
	step := TranscriptStepState{
		RunID:      runtimefeedTestRunID(t),
		StepID:     runtimefeedTestStepID(t),
		Lifecycle:  StepLifecycleStarted,
		ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		Status:     clientui.RunStatusRunning,
	}
	if err := step.Validate(); err != nil {
		t.Fatalf("validate started step: %v", err)
	}

	step.Lifecycle = StepLifecycleFinished
	step.Status = clientui.RunStatusCompleted
	if err := step.Validate(); err != nil {
		t.Fatalf("validate finished step: %v", err)
	}
}

func TestTranscriptStepAndReviewerStateRejectInvalidTransitions(t *testing.T) {
	step := TranscriptStepState{
		RunID:      runtimefeedTestRunID(t),
		StepID:     runtimefeedTestStepID(t),
		Lifecycle:  StepLifecycleStarted,
		ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
		Status:     clientui.RunStatusCompleted,
	}
	if err := step.Validate(); err == nil {
		t.Fatal("accepted started step with completed status")
	}

	reviewer := TranscriptReviewerState{
		StepID: runtimefeedTestStepID(t),
		State:  ReviewerState("unknown"),
	}
	if err := reviewer.Validate(); err == nil {
		t.Fatal("accepted unknown reviewer transition")
	}
}
