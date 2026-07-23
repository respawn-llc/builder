package transcriptrender

import (
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestOngoingErrorNoticeUsesErrorRoleForSymbolAndText(t *testing.T) {
	rendered := RenderCommittedRow(clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:   clientui.TranscriptNoticeRuntimeDiagnostic,
			Severity: clientui.TranscriptNoticeError,
			Diagnostic: &clientui.TranscriptDiagnostic{
				Code:   clientui.TranscriptDiagnosticCode(transcript.EntryRoleDeveloperErrorFeedback),
				Detail: "failure",
			},
		},
	}, 120, "dark", ModeOngoing)

	if len(rendered.Lines) != 1 || rendered.Lines[0].LeadingSymbol == nil {
		t.Fatalf("ongoing error notice = %+v, want one prefixed line", rendered.Lines)
	}
	if role, ok := rendered.Lines[0].LeadingSymbol.Style.Role(); !ok || role != StyleRoleError {
		t.Fatalf("error notice symbol role = %v present=%t, want %v", role, ok, StyleRoleError)
	}
	for _, span := range rendered.Lines[0].Spans {
		if strings.TrimSpace(span.Text) == "" {
			continue
		}
		if role, ok := span.Style.Role(); !ok || role != StyleRoleError {
			t.Fatalf("error notice text role = %v present=%t, want %v", role, ok, StyleRoleError)
		}
		return
	}
	t.Fatalf("error notice is missing text spans: %+v", rendered.Lines[0])
}
