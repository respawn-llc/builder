package transcriptrender

import (
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestReasoningTraceRemainsPlaintextFaintAndIsNotExpandable(t *testing.T) {
	const source = "**Preparing to investigate issue**"
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
	if noticeUsesMarkdown(row.Notice) {
		t.Fatal("reasoning trace unexpectedly uses Markdown rendering")
	}
	if _, got := noticeRoleAndText(row.Notice, row.Visibility, ModeDetailExpanded); strings.Contains(got, "**") {
		t.Fatalf("reasoning text retained provider bold delimiters: %q", got)
	}

	presentation := RenderDetailPresentation(row, 80, "dark")
	if presentation.Expandable {
		t.Fatal("reasoning trace is expandable without additional content")
	}
	for _, span := range presentation.Collapsed[0].Spans {
		if strings.TrimSpace(span.Text) == "" {
			continue
		}
		if !span.Style.Has(SpanAttributeFaint) {
			t.Fatalf("reasoning text span is not faint: %+v", span)
		}
		return
	}
	t.Fatalf("reasoning trace has no content span: %+v", presentation.Collapsed[0])
}
