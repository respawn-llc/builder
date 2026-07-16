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
	}
	if err := status.Validate(); err != nil {
		t.Fatalf("validate session status: %v", err)
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
			status.ParentSessionID = &runtimeids.SessionID{}
			return status
		}(),
		func() TranscriptSessionStatus {
			status := base
			status.Workflow = &TranscriptWorkflowSession{RunID: "run-1"}
			return status
		}(),
	}
	for _, status := range tests {
		if err := status.Validate(); err == nil {
			t.Fatalf("accepted invalid session status: %+v", status)
		}
	}
}
