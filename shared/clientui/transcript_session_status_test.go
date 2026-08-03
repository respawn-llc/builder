package clientui

import (
	"testing"

	"core/shared/runtimeids"
)

func TestTranscriptSessionStatusSeparatesRuntimeContextAndGoalFacts(t *testing.T) {
	status := TranscriptSessionStatus{
		ReviewerFrequency: "off",
		ThinkingLevel:     "medium",
		CompactionMode:    "auto",
		CompactionCount:   4,
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("validate session status: %v", err)
	}
	if status.CompactionCount != 4 {
		t.Fatalf("compaction count = %d, want 4", status.CompactionCount)
	}
}

func TestTranscriptSessionStatusRejectsPartialNestedState(t *testing.T) {
	base := TranscriptSessionStatus{
		ReviewerFrequency: "off",
		ThinkingLevel:     "medium",
		CompactionMode:    "auto",
	}
	tests := []TranscriptSessionStatus{
		func() TranscriptSessionStatus {
			status := base
			status.ReviewerFrequency = ""
			return status
		}(),
		func() TranscriptSessionStatus {
			status := base
			status.FastModeEnabled = true
			return status
		}(),
		func() TranscriptSessionStatus {
			status := base
			status.PreviousSessionID = &runtimeids.SessionID{}
			return status
		}(),
		func() TranscriptSessionStatus {
			status := base
			status.ParentAgentSessionID = &runtimeids.SessionID{}
			return status
		}(),
		func() TranscriptSessionStatus {
			status := base
			status.NavigationTargetSessionID = &runtimeids.SessionID{}
			return status
		}(),
		func() TranscriptSessionStatus {
			status := base
			status.Workflow = &TranscriptWorkflowSession{TaskID: "task-1"}
			return status
		}(),
	}
	for _, status := range tests {
		if err := status.Validate(); err == nil {
			t.Fatalf("accepted invalid session status: %+v", status)
		}
	}
}
