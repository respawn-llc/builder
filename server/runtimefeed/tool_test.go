package runtimefeed

import (
	"testing"

	"core/shared/runtimeids"
)

func TestTranscriptToolStartRequiresStepCallAndToolIdentity(t *testing.T) {
	start := TranscriptToolStart{
		StepID:     runtimefeedTestStepID(t),
		ToolCallID: ToolCallID("call-1"),
		ToolName:   "shell",
	}
	if err := start.Validate(); err != nil {
		t.Fatalf("validate tool start: %v", err)
	}
}

func TestTranscriptToolFactsRejectMissingIdentityAndUntypedFailure(t *testing.T) {
	validStart := TranscriptToolStart{
		StepID:     runtimefeedTestStepID(t),
		ToolCallID: ToolCallID("call-1"),
		ToolName:   "shell",
	}
	starts := []TranscriptToolStart{
		func() TranscriptToolStart {
			start := validStart
			start.StepID = runtimeids.StepID{}
			return start
		}(),
		func() TranscriptToolStart {
			start := validStart
			start.ToolCallID = ""
			return start
		}(),
		func() TranscriptToolStart {
			start := validStart
			start.ToolName = " "
			return start
		}(),
	}
	for _, start := range starts {
		if err := start.Validate(); err == nil {
			t.Fatalf("accepted invalid tool start: %+v", start)
		}
	}

	abort := TranscriptToolAbort{
		StepID:     runtimefeedTestStepID(t),
		ToolCallID: ToolCallID("call-1"),
		Reason:     ToolAbortFailed,
	}
	if err := abort.Validate(); err == nil {
		t.Fatal("accepted failed tool abort without diagnostic")
	}
}
