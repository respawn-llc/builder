package transcriptrender

import (
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
	if !linesContainText(collapsed.Lines, "2 suggestions") || linesContainText(collapsed.Lines, "**first**") {
		t.Fatalf("collapsed feedback did not use structured count: %+v", collapsed.Lines)
	}
	if !linesContainText(expanded.Lines, "first") || !linesContainText(expanded.Lines, "second") ||
		!linesContainText(expanded.Lines, "line") ||
		!linesHaveSymbol(expanded.Lines, "§") {
		t.Fatalf("expanded feedback lost source or success glyph: %+v", expanded.Lines)
	}
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
	if !linesContainText(rendered.Lines, "raw failure detail") || !linesHaveSymbol(rendered.Lines, "!") {
		t.Fatalf("Reviewer error detail/glyph missing: %+v", rendered.Lines)
	}
}

func linesContainText(lines []Line, want string) bool {
	for _, line := range lines {
		var text strings.Builder
		if line.LeadingSymbol != nil {
			text.WriteString(line.LeadingSymbol.Text)
		}
		for _, span := range line.Spans {
			text.WriteString(span.Text)
		}
		if strings.Contains(text.String(), want) {
			return true
		}
	}
	return false
}

func linesHaveSymbol(lines []Line, want string) bool {
	for _, line := range lines {
		if line.LeadingSymbol != nil && line.LeadingSymbol.Text == want {
			return true
		}
	}
	return false
}

func mustReviewerStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	stepID, err := runtimeids.ParseStepID(uuid.NewString())
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	return stepID
}
