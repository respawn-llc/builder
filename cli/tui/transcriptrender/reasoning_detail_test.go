package transcriptrender

import (
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestReasoningTraceUsesMarkdownAndIsNotExpandable(t *testing.T) {
	const source = "2 ** 3"
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:   clientui.TranscriptNoticeRuntimeDiagnostic,
			Severity: clientui.TranscriptNoticeInfo,
			Diagnostic: &clientui.TranscriptDiagnostic{
				Code:   clientui.TranscriptDiagnosticCode(transcript.EntryRoleReasoning),
				Detail: source,
			},
		},
	}
	if !noticeUsesMarkdown(row.Notice) {
		t.Fatal("reasoning trace does not use Markdown rendering")
	}
	if _, got := noticeRoleAndText(row.Notice, row.Visibility, ModeDetailExpanded); got != source {
		t.Fatalf("reasoning text = %q, want unchanged source %q", got, source)
	}

	presentation := RenderDetailPresentation(row, 80, "dark")
	if presentation.Expandable {
		t.Fatal("reasoning trace is expandable without additional content")
	}
}
