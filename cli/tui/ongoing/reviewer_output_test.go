package ongoing

import (
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
	if got, want := len(rendered.Lines), 3; got != want {
		t.Fatalf("verbose reviewer suggestion rows = %d, want %d", got, want)
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
