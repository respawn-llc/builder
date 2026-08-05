package tui

import (
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestTranscriptCommittedRowEqualDetectsReviewerFactMutations(t *testing.T) {
	stepID, err := runtimeids.ParseStepID(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	base := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoingCollapsed,
		Kind:       clientui.TranscriptRowReviewerFeedback,
		ReviewerFeedback: &clientui.TranscriptReviewerFeedbackRow{
			ID: runtimeids.NewReviewerFeedbackID(), StepID: stepID,
			Suggestions: []string{"one", "two"}, SuggestionCount: 2,
		},
	}
	changed := base
	changed.ReviewerFeedback = &clientui.TranscriptReviewerFeedbackRow{
		ID: base.ReviewerFeedback.ID, StepID: stepID,
		Suggestions: []string{"one", "changed"}, SuggestionCount: 2,
	}
	if TranscriptCommittedRowEqual(base, changed) {
		t.Fatal("Reviewer feedback source mutation was considered equal")
	}
}
