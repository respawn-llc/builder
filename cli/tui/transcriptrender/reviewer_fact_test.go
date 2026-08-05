package transcriptrender

import (
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
}

func mustReviewerStepID(t *testing.T) runtimeids.StepID {
	t.Helper()
	stepID, err := runtimeids.ParseStepID(uuid.NewString())
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	return stepID
}
