package clientui

import (
	"testing"
)

func TestTranscriptStepStateUsesTypedLifecycleTransitions(t *testing.T) {
	step := TranscriptStepState{
		RunID:      transcriptTestRunID(t),
		StepID:     transcriptTestStepID(t),
		Lifecycle:  StepLifecycleStarted,
		ActiveKind: RuntimeActivityActiveKindUserTurn,
		Status:     RunStatusRunning,
	}
	if err := step.Validate(); err != nil {
		t.Fatalf("validate started step: %v", err)
	}

	step.Lifecycle = StepLifecycleFinished
	step.Status = RunStatusCompleted
	if err := step.Validate(); err != nil {
		t.Fatalf("validate finished step: %v", err)
	}
}

func TestTranscriptStepStateRejectsInvalidTransitions(t *testing.T) {
	step := TranscriptStepState{
		RunID:      transcriptTestRunID(t),
		StepID:     transcriptTestStepID(t),
		Lifecycle:  StepLifecycleStarted,
		ActiveKind: RuntimeActivityActiveKindUserTurn,
		Status:     RunStatusCompleted,
	}
	if err := step.Validate(); err == nil {
		t.Fatal("accepted started step with completed status")
	}
}
