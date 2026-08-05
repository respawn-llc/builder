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
	feedbackMutations := []struct {
		name string
		edit func(*clientui.TranscriptReviewerFeedbackRow)
	}{
		{"id", func(row *clientui.TranscriptReviewerFeedbackRow) { row.ID = runtimeids.NewReviewerFeedbackID() }},
		{"step", func(row *clientui.TranscriptReviewerFeedbackRow) {
			row.StepID, _ = runtimeids.ParseStepID(uuid.NewString())
		}},
		{"suggestions", func(row *clientui.TranscriptReviewerFeedbackRow) { row.Suggestions[1] = "changed" }},
		{"count", func(row *clientui.TranscriptReviewerFeedbackRow) { row.SuggestionCount++ }},
	}
	for _, mutation := range feedbackMutations {
		t.Run("feedback "+mutation.name, func(t *testing.T) {
			changed := base
			changed.ReviewerFeedback = cloneReviewerFeedbackRow(base.ReviewerFeedback)
			mutation.edit(changed.ReviewerFeedback)
			if TranscriptCommittedRowEqual(base, changed) {
				t.Fatalf("Reviewer feedback %s mutation was considered equal", mutation.name)
			}
		})
	}

	errorBase := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Kind:       clientui.TranscriptRowReviewerError,
		ReviewerError: &clientui.TranscriptReviewerErrorRow{
			ID: runtimeids.NewReviewerErrorID(), StepID: stepID, Detail: "detail",
		},
	}
	errorMutations := []struct {
		name string
		edit func(*clientui.TranscriptReviewerErrorRow)
	}{
		{"id", func(row *clientui.TranscriptReviewerErrorRow) { row.ID = runtimeids.NewReviewerErrorID() }},
		{"step", func(row *clientui.TranscriptReviewerErrorRow) {
			row.StepID, _ = runtimeids.ParseStepID(uuid.NewString())
		}},
		{"detail", func(row *clientui.TranscriptReviewerErrorRow) { row.Detail = "changed" }},
	}
	for _, mutation := range errorMutations {
		t.Run("error "+mutation.name, func(t *testing.T) {
			changed := errorBase
			changed.ReviewerError = cloneReviewerErrorRow(errorBase.ReviewerError)
			mutation.edit(changed.ReviewerError)
			if TranscriptCommittedRowEqual(errorBase, changed) {
				t.Fatalf("Reviewer error %s mutation was considered equal", mutation.name)
			}
		})
	}
}

func cloneReviewerFeedbackRow(row *clientui.TranscriptReviewerFeedbackRow) *clientui.TranscriptReviewerFeedbackRow {
	cloned := *row
	cloned.Suggestions = append([]string(nil), row.Suggestions...)
	return &cloned
}

func cloneReviewerErrorRow(row *clientui.TranscriptReviewerErrorRow) *clientui.TranscriptReviewerErrorRow {
	cloned := *row
	return &cloned
}
