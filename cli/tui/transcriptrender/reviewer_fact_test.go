package transcriptrender

import (
	"reflect"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"

	"github.com/google/uuid"
)

func TestTypedReviewerFeedbackRendersCountCollapsedAndMarkdownExpanded(t *testing.T) {
	stepID, err := runtimeids.ParseStepID(uuid.NewString())
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoingCollapsed,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowReviewerFeedback,
		ReviewerFeedback: &clientui.TranscriptReviewerFeedbackRow{
			ID: runtimeids.NewReviewerFeedbackID(), StepID: stepID,
			Suggestions: []string{"  **first**  ", "second\nline"}, SuggestionCount: 2,
		},
	}
	collapsed := RenderCommittedRow(row, 80, "dark", ModeOngoingCollapsed)
	expanded := RenderCommittedRow(row, 80, "dark", ModeDetailExpanded)
	if len(collapsed.Lines) == 0 || len(expanded.Lines) <= len(collapsed.Lines) {
		t.Fatalf("typed feedback presentation did not expand: collapsed=%+v expanded=%+v", collapsed, expanded)
	}
	if expanded.Group != clientui.TranscriptRowReviewerFeedback {
		t.Fatalf("typed feedback group = %q", expanded.Group)
	}
	if reflect.DeepEqual(collapsed.Lines, RenderCommittedRow(row, 80, "dark", ModeDetailExpanded).Lines) {
		t.Fatal("collapsed feedback unexpectedly rendered the full source")
	}
	countChanged := row
	countChanged.ReviewerFeedback = cloneReviewerFeedbackForRender(row.ReviewerFeedback)
	countChanged.ReviewerFeedback.SuggestionCount++
	if reflect.DeepEqual(collapsed.Lines, RenderCommittedRow(countChanged, 80, "dark", ModeOngoingCollapsed).Lines) {
		t.Fatal("collapsed feedback ignored the structured suggestion count")
	}
	sourceChanged := row
	sourceChanged.ReviewerFeedback = cloneReviewerFeedbackForRender(row.ReviewerFeedback)
	sourceChanged.ReviewerFeedback.Suggestions[0] = "different source"
	if reflect.DeepEqual(expanded.Lines, RenderCommittedRow(sourceChanged, 80, "dark", ModeDetailExpanded).Lines) {
		t.Fatal("expanded feedback ignored the persisted suggestion source")
	}
}

func cloneReviewerFeedbackForRender(row *clientui.TranscriptReviewerFeedbackRow) *clientui.TranscriptReviewerFeedbackRow {
	cloned := *row
	cloned.Suggestions = append([]string(nil), row.Suggestions...)
	return &cloned
}

func TestTypedReviewerErrorRendersExpandedDiagnostic(t *testing.T) {
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowReviewerError,
		ReviewerError: &clientui.TranscriptReviewerErrorRow{
			ID: runtimeids.NewReviewerErrorID(), StepID: mustReviewerStepID(t), Detail: "raw failure detail",
		},
	}
	rendered := RenderCommittedRow(row, 80, "dark", ModeOngoing)
	if len(rendered.Lines) == 0 || rendered.Group != clientui.TranscriptRowReviewerError {
		t.Fatalf("typed Reviewer error presentation = %+v", rendered)
	}
	var renderedDetail string
	for _, span := range rendered.Lines[0].Spans {
		renderedDetail += span.Text
	}
	if strings.TrimSpace(renderedDetail) != strings.TrimSpace(row.ReviewerError.Detail) {
		t.Fatalf("Reviewer error detail was not preserved: got %q", renderedDetail)
	}
}

func mustReviewerStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	stepID, err := runtimeids.ParseStepID(uuid.NewString())
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	return stepID
}
