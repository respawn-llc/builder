package transcriptrender

import (
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestTypedSystemNoticesUseMarkdown(t *testing.T) {
	for _, messageType := range []clientui.TranscriptMessageType{
		clientui.TranscriptMessageAgentsMD,
		clientui.TranscriptMessageSkills,
		clientui.TranscriptMessageSubagents,
		clientui.TranscriptMessageEnvironment,
		clientui.TranscriptMessageCompactionSummary,
		clientui.TranscriptMessageHeadlessMode,
		clientui.TranscriptMessageHeadlessModeExit,
		clientui.TranscriptMessageActiveGoalContinuation,
		clientui.TranscriptMessageWorkflowMode,
	} {
		t.Run(string(messageType), func(t *testing.T) {
			if !noticeUsesMarkdown(systemNoticeRow(messageType).Notice) {
				t.Fatalf("message type %q does not use Markdown", messageType)
			}
		})
	}
}

func TestExcludedSystemNoticesDoNotUseMarkdown(t *testing.T) {
	for _, messageType := range []clientui.TranscriptMessageType{
		clientui.TranscriptMessageHandoffFutureMessage,
		clientui.TranscriptMessageManualCompactionCarryover,
	} {
		t.Run(string(messageType), func(t *testing.T) {
			if noticeUsesMarkdown(systemNoticeRow(messageType).Notice) {
				t.Fatalf("message type %q unexpectedly uses Markdown", messageType)
			}
		})
	}
}

func systemNoticeRow(messageType clientui.TranscriptMessageType) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:      clientui.TranscriptNoticeLegacyUntypedNotice,
			Severity:    clientui.TranscriptNoticeInfo,
			MessageType: &messageType,
		},
	}
}
