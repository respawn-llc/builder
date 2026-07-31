package transcriptrender

import (
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestExpandedCompactionNoticesUseNormalTextRole(t *testing.T) {
	for _, test := range []struct {
		name        string
		messageType clientui.TranscriptMessageType
		compactRole StyleRole
	}{
		{
			name:        "summary",
			messageType: clientui.TranscriptMessageCompactionSummary,
			compactRole: StyleRoleNoticeSecondary,
		},
		{
			name:        "reminder",
			messageType: clientui.TranscriptMessageCompactionSoonReminder,
			compactRole: StyleRoleWarning,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			notice := &clientui.TranscriptNoticeRow{
				MessageType: &test.messageType,
			}
			if test.messageType == clientui.TranscriptMessageCompactionSummary {
				detail := "provider compaction detail"
				notice.Reason = clientui.TranscriptNoticeCompaction
				notice.Severity = clientui.TranscriptNoticeInfo
				notice.Compaction = &clientui.TranscriptCompactionNotice{Detail: &detail}
			}
			row := clientui.TranscriptCommittedRow{
				Visibility: transcript.EntryVisibilityDetail,
				Integrity:  transcript.RowIntegrityValid,
				Kind:       clientui.TranscriptRowNotice,
				Notice:     notice,
			}

			if got, _ := noticeRoleAndText(row.Notice, row.Visibility, ModeDetailCollapsed); got != test.compactRole {
				t.Fatalf("compact role = %v, want %v", got, test.compactRole)
			}
			if got, _ := noticeRoleAndText(row.Notice, row.Visibility, ModeDetailExpanded); got != StyleRoleNotice {
				t.Fatalf("expanded role = %v, want normal notice role %v", got, StyleRoleNotice)
			}
		})
	}
}

func TestCompactionNoticeWithoutDetailIsNotExpandable(t *testing.T) {
	messageType := clientui.TranscriptMessageCompactionSummary
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:      clientui.TranscriptNoticeCompaction,
			Severity:    clientui.TranscriptNoticeInfo,
			MessageType: &messageType,
			Compaction:  &clientui.TranscriptCompactionNotice{},
		},
	}

	presentation := RenderDetailPresentation(row, 80, "dark")
	if presentation.Expandable {
		t.Fatal("compaction notice is expandable without additional detail")
	}
}
