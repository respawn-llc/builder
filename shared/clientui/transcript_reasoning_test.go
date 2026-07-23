package clientui

import (
	"testing"

	"core/shared/runtimeids"
)

func TestTranscriptReasoningUpdateCarriesContentCompleteCurrentStatus(t *testing.T) {
	update := TranscriptReasoningUpdate{
		StepID: transcriptTestStepID(t),
		Key:    "rs_1:part:0",
		Text:   "**Checking tests**\nDetails",
		CurrentStatus: &ReasoningStatus{
			Text: "Checking tests",
		},
	}
	if err := update.Validate(); err != nil {
		t.Fatalf("validate reasoning update: %v", err)
	}

	update.Text = ""
	update.CurrentStatus = nil
	if err := update.Validate(); err != nil {
		t.Fatalf("validate empty terminal reasoning snapshot: %v", err)
	}
}

func TestTranscriptReasoningFactsRequireStepKeyAndNonblankPresentStatus(t *testing.T) {
	base := TranscriptReasoningUpdate{
		StepID: transcriptTestStepID(t),
		Key:    "rs_1:part:0",
	}
	tests := []TranscriptReasoningUpdate{
		func() TranscriptReasoningUpdate {
			update := base
			update.StepID = runtimeids.StepID{}
			return update
		}(),
		func() TranscriptReasoningUpdate {
			update := base
			update.Key = " "
			return update
		}(),
		func() TranscriptReasoningUpdate {
			update := base
			update.CurrentStatus = &ReasoningStatus{}
			return update
		}(),
	}
	for _, update := range tests {
		if err := update.Validate(); err == nil {
			t.Fatalf("accepted invalid reasoning update: %+v", update)
		}
	}

	if err := (TranscriptReasoningReset{}).Validate(); err == nil {
		t.Fatal("accepted reasoning reset without step identity")
	}
}
