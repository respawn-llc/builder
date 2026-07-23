package transcriptrender

import (
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestErrorNoticeClassifiesAsError(t *testing.T) {
	row := &clientui.TranscriptNoticeRow{
		Reason:   clientui.TranscriptNoticeRuntimeDiagnostic,
		Severity: clientui.TranscriptNoticeError,
		Diagnostic: &clientui.TranscriptDiagnostic{
			Code:   clientui.TranscriptDiagnosticCode(transcript.EntryRoleDeveloperErrorFeedback),
			Detail: "failure",
		},
	}
	if got := noticeStyleRole(row); got != StyleRoleError {
		t.Fatalf("error notice role = %v, want %v", got, StyleRoleError)
	}
}
