package runtime

import (
	"testing"
	"time"

	"core/server/llm"
	"core/shared/runtimeids"
)

func TestLiveRunBatchFinishedEventIsLiveOnly(t *testing.T) {
	runID, err := runtimeids.ParseRunID("018fdd67-89ab-4cde-8123-456789abc001")
	if err != nil {
		t.Fatalf("parse run id: %v", err)
	}
	stepID, err := runtimeids.ParseStepID("018fdd67-89ab-4cde-8123-456789abc002")
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	event := liveRunBatchFinishedEvent(LiveRunResult{
		GroupID:          runtimeids.NewLiveRunGroupID(),
		RunID:            runID,
		StepID:           stepID,
		Status:           RunStatusCompleted,
		ResultKind:       LiveRunResultAssistantFinalAnswer,
		AssistantMessage: llm.Message{Role: llm.RoleAssistant, Content: "final answer"},
		StartedAt:        time.Date(2026, time.July, 19, 20, 0, 0, 0, time.UTC),
		FinishedAt:       time.Date(2026, time.July, 19, 20, 0, 1, 0, time.UTC),
	})
	if event.CommittedTranscriptChanged {
		t.Fatal("live-run batch-finished event claims committed transcript mutation")
	}
	if facts := TranscriptCommittedRowFactsFromEvent(event); len(facts) != 0 {
		t.Fatalf("live-run batch-finished event projected committed transcript rows: %+v", facts)
	}
}
