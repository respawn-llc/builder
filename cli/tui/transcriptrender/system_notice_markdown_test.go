package transcriptrender

import (
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestTypedSystemNoticesRenderMarkdown(t *testing.T) {
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
			presentation := RenderDetailPresentation(systemNoticeRow(messageType), 80, "dark")
			if got := presentation.Expanded[0].Plain(); strings.Contains(got, "**") {
				t.Fatalf("typed system notice retained Markdown delimiters: %q", got)
			}
		})
	}
}

func TestExcludedSystemNoticesRemainPlaintext(t *testing.T) {
	for _, messageType := range []clientui.TranscriptMessageType{
		clientui.TranscriptMessageHandoffFutureMessage,
		clientui.TranscriptMessageManualCompactionCarryover,
	} {
		t.Run(string(messageType), func(t *testing.T) {
			presentation := RenderDetailPresentation(systemNoticeRow(messageType), 80, "dark")
			if got := presentation.Expanded[0].Plain(); !strings.Contains(got, "**") {
				t.Fatalf("excluded system notice unexpectedly rendered Markdown: %q", got)
			}
		})
	}
}

func systemNoticeRow(messageType clientui.TranscriptMessageType) clientui.TranscriptCommittedRow {
	content := "**content**"
	return clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityDetail,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:      clientui.TranscriptNoticeLegacyUntypedNotice,
			Severity:    clientui.TranscriptNoticeInfo,
			MessageType: &messageType,
			LegacyText:  &content,
		},
	}
}
