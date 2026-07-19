package clientui

import (
	"testing"
)

func TestTranscriptContextGoalAndCompactionFactsValidateTypedState(t *testing.T) {
	cacheHit := 80
	usage := TranscriptContextUsage{
		UsedTokens:      100,
		WindowTokens:    1_000,
		CacheHitPercent: &cacheHit,
	}
	if err := usage.Validate(); err != nil {
		t.Fatalf("validate context usage: %v", err)
	}

	goal := TranscriptGoalStatus{Goal: &TranscriptGoal{
		ID:        "goal-1",
		Objective: "Ship it",
		Status:    RuntimeGoalStatusActive,
	}}
	if err := goal.Validate(); err != nil {
		t.Fatalf("validate goal status: %v", err)
	}

	compaction := TranscriptCompactionStatus{
		StepID:    transcriptTestStepID(t),
		State:     CompactionStarted,
		Mode:      "auto",
		Initiator: CompactionInitiatorAutomatic,
		Count:     1,
	}
	if err := compaction.Validate(); err != nil {
		t.Fatalf("validate compaction status: %v", err)
	}
}

func TestTranscriptContextGoalAndCompactionFactsRejectInvalidState(t *testing.T) {
	cacheHit := 101
	if err := (TranscriptContextUsage{WindowTokens: 1_000, CacheHitPercent: &cacheHit}).Validate(); err == nil {
		t.Fatal("accepted context usage with invalid cache percentage")
	}
	if err := (TranscriptContextUsage{WindowTokens: 0}).Validate(); err == nil {
		t.Fatal("accepted context usage without a window")
	}
	if err := (TranscriptGoalStatus{Goal: &TranscriptGoal{
		ID:        "goal-1",
		Objective: "Ship it",
		Status:    RuntimeGoalStatusComplete,
		Suspended: true,
	}}).Validate(); err == nil {
		t.Fatal("accepted suspended completed goal")
	}
	if err := (TranscriptCompactionStatus{
		StepID: transcriptTestStepID(t),
		State:  CompactionFailed,
		Mode:   "auto",
	}).Validate(); err == nil {
		t.Fatal("accepted failed compaction without diagnostic")
	}
	if err := (TranscriptCompactionStatus{
		StepID: transcriptTestStepID(t),
		State:  CompactionStarted,
		Mode:   "auto",
	}).Validate(); err == nil {
		t.Fatal("accepted compaction without initiator")
	}
	if err := (TranscriptCompactionStatus{
		StepID:    transcriptTestStepID(t),
		State:     CompactionStarted,
		Mode:      "manual",
		Initiator: CompactionInitiator("scripted"),
	}).Validate(); err == nil {
		t.Fatal("accepted compaction with unknown initiator")
	}
}
