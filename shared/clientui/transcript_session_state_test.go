package clientui

import (
	"testing"
	"time"
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

	goal := TranscriptGoalStatus{Goal: &TranscriptGoal{Goal: &Goal{
		ID: "goal-1", Objective: "Ship it", Status: RuntimeGoalStatusActive,
		CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0),
	}}}
	if err := goal.Validate(); err != nil {
		t.Fatalf("validate goal status: %v", err)
	}

	compaction := TranscriptCompactionStatus{
		StepID: transcriptTestStepID(t),
		State:  CompactionStarted,
		Mode:   "auto",
		Count:  1,
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
		Goal:      &Goal{ID: "goal-1", Objective: "Ship it", Status: RuntimeGoalStatusComplete, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0)},
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
}
