package transcriptrender

import (
	"testing"

	"core/shared/clientui"
	"core/shared/transcript"
)

func TestAgentSteerNoticeUsesFullOngoingAndDetailExpansion(t *testing.T) {
	messageType := clientui.TranscriptMessageAgentSteer
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoing,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			MessageType:  &messageType,
			CompactLabel: stringPtr("compact"),
			Diagnostic:   &clientui.TranscriptDiagnostic{Detail: "full"},
		},
	}
	ongoing := RenderCommittedRow(row, 80, "dark", ModeOngoing)
	if PlainLines(ongoing.Lines)[0] == "compact" {
		t.Fatal("ongoing agent steer used collapsed content")
	}
	collapsed := RenderCommittedRow(row, 80, "dark", ModeDetailCollapsed)
	if PlainLines(collapsed.Lines)[0] == "full" {
		t.Fatal("collapsed detail agent steer used full content")
	}
	expanded := RenderCommittedRow(row, 80, "dark", ModeDetailExpanded)
	if PlainLines(expanded.Lines)[0] == "compact" {
		t.Fatal("expanded detail agent steer used compact content")
	}
}

func stringPtr(value string) *string {
	return &value
}
