package clientui

import (
	"testing"

	"core/shared/runtimeids"
)

func TestTranscriptThinkingStatusAndReasoningTraceRequireValidFacts(t *testing.T) {
	stepID := transcriptTestStepID(t)
	index := int64(0)
	trace := TranscriptReasoningTraceUpdate{
		StepID: stepID,
		Identity: TranscriptReasoningTraceIdentity{
			Provider: &TranscriptProviderReasoningTraceIdentity{
				ItemID:       "rs_1",
				SummaryIndex: &index,
			},
		},
		CompactText: "Planning",
		Text:        "Planning",
	}
	if err := (TranscriptThinkingStatusUpdate{StepID: stepID, Text: "Thinking"}).Validate(); err != nil {
		t.Fatalf("validate status: %v", err)
	}
	if err := trace.Validate(); err != nil {
		t.Fatalf("validate trace: %v", err)
	}
	tests := []TranscriptThinkingStatusUpdate{
		{Text: "Thinking"},
		{StepID: stepID},
	}
	for _, value := range tests {
		if err := value.Validate(); err == nil {
			t.Fatalf("accepted invalid thinking status: %+v", value)
		}
	}
	trace.StepID = runtimeids.StepID{}
	if err := trace.Validate(); err == nil {
		t.Fatal("accepted reasoning trace without step identity")
	}
}

func TestTranscriptReasoningTraceResetRequiresStepIdentity(t *testing.T) {
	if err := (TranscriptReasoningTraceReset{}).Validate(); err == nil {
		t.Fatal("accepted reasoning trace reset without step identity")
	}
}
