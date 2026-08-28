package transcriptrender

import (
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestSessionRebindNoticeIsCompactAndExpandsToFullReminder(t *testing.T) {
	messageType := clientui.TranscriptMessageSessionRebind
	fullReminder := t.Name()
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoingCollapsed,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:      clientui.TranscriptNoticeRuntimeDiagnostic,
			Severity:    clientui.TranscriptNoticeInfo,
			MessageType: &messageType,
			Diagnostic: &clientui.TranscriptDiagnostic{
				Code:   clientui.TranscriptDiagnosticCode(transcript.EntryRoleDeveloperContext),
				Detail: fullReminder,
			},
		},
	}
	if _, got := noticeRoleAndText(row.Notice, row.Visibility, ModeDetailCollapsed); got != clientui.SessionRebindCompactLabel {
		t.Fatalf("collapsed Session rebind notice = %q", got)
	}
	if _, got := noticeRoleAndText(row.Notice, row.Visibility, ModeDetailExpanded); got != row.Notice.Diagnostic.Detail {
		t.Fatalf("expanded Session rebind notice = %q", got)
	}
}
