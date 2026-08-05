package clientui

import (
	"testing"

	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestReviewerRowsValidateTheirTypedPayloads(t *testing.T) {
	stepID := transcriptTestStepID(t)
	feedback := TranscriptReviewerFeedbackRow{
		ID:              runtimeids.NewReviewerFeedbackID(),
		StepID:          stepID,
		Suggestions:     []string{"  **first**\n\n- item  ", "second"},
		SuggestionCount: 2,
	}
	feedbackRow := TranscriptCommittedRow{
		Visibility:       transcript.EntryVisibilityOngoingCollapsed,
		Kind:             TranscriptRowReviewerFeedback,
		ReviewerFeedback: &feedback,
	}
	if err := feedbackRow.Validate(); err != nil {
		t.Fatalf("validate Reviewer feedback row: %v", err)
	}

	errorRow := TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Kind:       TranscriptRowReviewerError,
		ReviewerError: &TranscriptReviewerErrorRow{
			ID:     runtimeids.NewReviewerErrorID(),
			StepID: stepID,
			Detail: "raw failure detail",
		},
	}
	if err := errorRow.Validate(); err != nil {
		t.Fatalf("validate Reviewer error row: %v", err)
	}
}

func TestReviewerRowsRejectMissingOrInconsistentFacts(t *testing.T) {
	stepID := transcriptTestStepID(t)
	validFeedback := TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoingCollapsed,
		Kind:       TranscriptRowReviewerFeedback,
		ReviewerFeedback: &TranscriptReviewerFeedbackRow{
			ID:              runtimeids.NewReviewerFeedbackID(),
			StepID:          stepID,
			Suggestions:     []string{"suggestion"},
			SuggestionCount: 1,
		},
	}
	tests := []TranscriptCommittedRow{
		{Visibility: transcript.EntryVisibilityOngoingCollapsed, Kind: TranscriptRowReviewerFeedback},
		func() TranscriptCommittedRow {
			row := validFeedback
			row.ReviewerFeedback.ID = runtimeids.ReviewerFeedbackID{}
			return row
		}(),
		func() TranscriptCommittedRow {
			row := validFeedback
			row.ReviewerFeedback.Suggestions = nil
			return row
		}(),
		func() TranscriptCommittedRow {
			row := validFeedback
			row.ReviewerFeedback.Suggestions = []string{" \t "}
			return row
		}(),
		func() TranscriptCommittedRow {
			row := validFeedback
			row.ReviewerFeedback.SuggestionCount = 2
			return row
		}(),
		{
			Visibility: transcript.EntryVisibilityOngoing,
			Kind:       TranscriptRowReviewerError,
			ReviewerError: &TranscriptReviewerErrorRow{
				ID:     runtimeids.NewReviewerErrorID(),
				StepID: stepID,
			},
		},
	}
	for _, row := range tests {
		if err := row.Validate(); err == nil {
			t.Fatalf("accepted invalid Reviewer row: %#v", row)
		}
	}
}
