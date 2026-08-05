package invariant

import (
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestValidateTranscriptCommittedRowAcceptsReviewerFacts(t *testing.T) {
	stepID, err := runtimeids.ParseStepID(uuid.NewString())
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	rows := []clientui.TranscriptCommittedRow{
		{
			Visibility: transcript.EntryVisibilityOngoingCollapsed,
			Locator:    transcript.CommittedRowLocator{EventSequence: 1, RowOrdinal: 1},
			Kind:       clientui.TranscriptRowReviewerFeedback,
			ReviewerFeedback: &clientui.TranscriptReviewerFeedbackRow{
				ID:              runtimeids.NewReviewerFeedbackID(),
				StepID:          stepID,
				Suggestions:     []string{"first", "second"},
				SuggestionCount: 2,
			},
		},
		{
			Visibility: transcript.EntryVisibilityOngoing,
			Locator:    transcript.CommittedRowLocator{EventSequence: 2, RowOrdinal: 1},
			Kind:       clientui.TranscriptRowReviewerError,
			ReviewerError: &clientui.TranscriptReviewerErrorRow{
				ID:     runtimeids.NewReviewerErrorID(),
				StepID: stepID,
				Detail: "failure detail",
			},
		},
	}
	for _, row := range rows {
		if err := ValidateTranscriptCommittedRow(row); err != nil {
			t.Fatalf("valid Reviewer row rejected: %v", err)
		}
	}
}

func TestValidateTranscriptCommittedRowRejectsReviewerFactContractViolations(t *testing.T) {
	stepID, err := runtimeids.ParseStepID(uuid.NewString())
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	feedback := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoingCollapsed,
		Locator:    transcript.CommittedRowLocator{EventSequence: 3, RowOrdinal: 1},
		Kind:       clientui.TranscriptRowReviewerFeedback,
		ReviewerFeedback: &clientui.TranscriptReviewerFeedbackRow{
			ID:              runtimeids.NewReviewerFeedbackID(),
			StepID:          stepID,
			Suggestions:     []string{"suggestion"},
			SuggestionCount: 1,
		},
	}
	tests := []clientui.TranscriptCommittedRow{
		func() clientui.TranscriptCommittedRow {
			row := feedback
			row.ReviewerFeedback.SuggestionCount = 2
			return row
		}(),
		func() clientui.TranscriptCommittedRow {
			row := feedback
			row.ReviewerFeedback.Suggestions = []string{"", "second"}
			row.ReviewerFeedback.SuggestionCount = 2
			return row
		}(),
		func() clientui.TranscriptCommittedRow {
			row := feedback
			row.ReviewerFeedback.ID = runtimeids.ReviewerFeedbackID{}
			return row
		}(),
		{
			Visibility: transcript.EntryVisibilityOngoing,
			Kind:       clientui.TranscriptRowReviewerError,
			ReviewerError: &clientui.TranscriptReviewerErrorRow{
				ID:     runtimeids.NewReviewerErrorID(),
				StepID: stepID,
			},
		},
	}
	for _, row := range tests {
		if err := ValidateTranscriptCommittedRow(row); err == nil {
			t.Fatalf("invalid Reviewer row accepted: %#v", row)
		}
	}
}
