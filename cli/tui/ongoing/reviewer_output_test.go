package ongoing

import (
	"strings"
	"testing"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"
)

func TestVerboseReviewerSuggestionsRenderFullyInOngoingMode(t *testing.T) {
	suggestions := []string{
		"Preserve the first complete supervisor suggestion across narrow terminal widths.",
		"Preserve the second complete supervisor suggestion without an ellipsis.",
	}
	stepID, err := runtimeids.ParseStepID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("parse step id: %v", err)
	}
	row := clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowReviewerFeedback,
		ReviewerFeedback: &clientui.TranscriptReviewerFeedbackRow{
			ID:              runtimeids.NewReviewerFeedbackID(),
			StepID:          stepID,
			Suggestions:     suggestions,
			SuggestionCount: len(suggestions),
		},
	}

	if got := ongoingRenderMode(row); got != transcriptrender.ModeOngoingFull {
		t.Fatalf("reviewer suggestions render mode = %d, want full ongoing mode", got)
	}
	rendered := transcriptrender.RenderCommittedRowWithLinkPresentation(
		row,
		24,
		"dark",
		ongoingRenderMode(row),
		transcriptrender.MarkdownLinkLabelOnly,
	)
	var renderedLines []string
	for _, line := range rendered.Lines {
		var text string
		for _, span := range line.Spans {
			text += span.Text
		}
		renderedLines = append(renderedLines, text)
	}
	text := strings.Join(renderedLines, "")
	compactText := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\t' {
			return -1
		}
		return r
	}, text)
	for _, suggestion := range suggestions {
		compactSuggestion := strings.ReplaceAll(suggestion, " ", "")
		if !strings.Contains(compactText, compactSuggestion) {
			t.Fatalf("verbose reviewer suggestions omitted content: %q", text)
		}
	}
	if strings.Contains(text, "…") {
		t.Fatalf("verbose reviewer suggestions were ellipsized: %q", text)
	}
}

func TestAgentSteerRenderFullyInOngoingMode(t *testing.T) {
	messageType := clientui.TranscriptMessageAgentSteer
	row := clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoing,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			MessageType: &messageType,
			Diagnostic:  &clientui.TranscriptDiagnostic{Detail: "first\nsecond"},
		},
	}
	if got := ongoingRenderMode(row); got != transcriptrender.ModeOngoingFull {
		t.Fatalf("agent steer render mode = %d, want full ongoing mode", got)
	}
	rendered := transcriptrender.RenderCommittedRowWithLinkPresentation(
		row,
		80,
		"dark",
		ongoingRenderMode(row),
		transcriptrender.MarkdownLinkLabelOnly,
	)
	if got, want := len(rendered.Lines), 2; got != want {
		t.Fatalf("agent steer ongoing rows = %d, want %d", got, want)
	}
}

func reviewerNoticeRow(visibility transcript.EntryVisibility, code, detail string) clientui.TranscriptCommittedRow {
	messageType := clientui.TranscriptMessageReviewerFeedback
	return clientui.TranscriptCommittedRow{
		Visibility: visibility,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:      clientui.TranscriptNoticeRuntimeDiagnostic,
			Severity:    clientui.TranscriptNoticeInfo,
			MessageType: &messageType,
			Diagnostic: &clientui.TranscriptDiagnostic{
				Code:   clientui.TranscriptDiagnosticCode(code),
				Detail: detail,
			},
		},
	}
}
