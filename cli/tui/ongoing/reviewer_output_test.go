package ongoing

import (
	"strings"
	"testing"

	"core/cli/tui/transcriptrender"
	"core/shared/clientui"
	"core/shared/transcript"
)

func TestVerboseReviewerSuggestionsRenderFullyInOngoingMode(t *testing.T) {
	const suggestions = "heading\nfirst\nsecond"
	row := reviewerNoticeRow(
		clientui.EntryVisibilityOngoing,
		string(transcript.EntryRoleReviewerSuggestions),
		suggestions,
	)

	if got := ongoingRenderMode(row); got != transcriptrender.ModeOngoingFull {
		t.Fatalf("reviewer suggestions render mode = %d, want full ongoing mode", got)
	}
	rendered := transcriptrender.RenderCommittedRowWithLinkPresentation(
		row,
		80,
		"dark",
		ongoingRenderMode(row),
		transcriptrender.MarkdownLinkLabelOnly,
	)
	if got, want := len(rendered.Lines), len(strings.Split(suggestions, "\n")); got != want {
		t.Fatalf("verbose reviewer suggestion rows = %d, want %d", got, want)
	}
	if got := transcriptLineText(rendered.Lines); !strings.Contains(got, "second") {
		t.Fatalf("verbose reviewer suggestions omitted final suggestion: %q", got)
	}
}

func TestCollapsedReviewerStatusDoesNotImplyHiddenDetail(t *testing.T) {
	row := reviewerNoticeRow(
		clientui.EntryVisibilityOngoingCollapsed,
		string(transcript.EntryRoleReviewerStatus),
		"status\nsecondary",
	)

	rendered := transcriptrender.RenderCommittedRowWithLinkPresentation(
		row,
		80,
		"dark",
		ongoingRenderMode(row),
		transcriptrender.MarkdownLinkLabelOnly,
	)
	if got := len(rendered.Lines); got != 1 {
		t.Fatalf("collapsed reviewer status rows = %d, want one", got)
	}
	if got := transcriptLineText(rendered.Lines); strings.Contains(got, "…") {
		t.Fatalf("collapsed reviewer status implies hidden detail: %q", got)
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

func transcriptLineText(lines []transcriptrender.Line) string {
	text := make([]string, 0, len(lines))
	for _, line := range lines {
		text = append(text, line.Plain())
	}
	return strings.Join(text, "\n")
}
