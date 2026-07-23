package transcriptrender

import (
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestReasoningTraceDetailRemainsFaintAndIsNotExpandable(t *testing.T) {
	presentation := RenderDetailPresentation(clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:   clientui.TranscriptNoticeRuntimeDiagnostic,
			Severity: clientui.TranscriptNoticeInfo,
			Diagnostic: &clientui.TranscriptDiagnostic{
				Code:   clientui.TranscriptDiagnosticCode(transcript.EntryRoleReasoning),
				Detail: "**brief**",
			},
		},
	}, 80, "dark")

	if presentation.Expandable {
		t.Fatal("reasoning trace is expandable without additional content")
	}
	if got := presentation.Collapsed[0].Plain(); strings.Contains(got, "**") {
		t.Fatalf("reasoning trace retained Markdown emphasis markers: %q", got)
	}
	if !presentation.Collapsed[0].Spans[0].Style.Has(SpanAttributeFaint) {
		t.Fatal("reasoning trace is not faint")
	}
}
