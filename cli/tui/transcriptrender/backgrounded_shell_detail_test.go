package transcriptrender

import (
	"testing"

	"core/shared/clientui"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestBackgroundedShellDetailExpansionShowsFullCommandAndCommittedOutput(t *testing.T) {
	const (
		command = "printf first-line\nprintf full-command-line"
		output  = "server supplied output"
	)
	row := clientui.TranscriptCommittedRow{
		Visibility: transcript.EntryVisibilityOngoingCollapsed,
		Integrity:  transcript.RowIntegrityValid,
		Kind:       clientui.TranscriptRowTool,
		Tool: &clientui.TranscriptToolRow{
			ToolName: string(toolspec.ToolExecCommand),
			Text:     output,
			Presentation: &transcript.ToolCallMeta{
				ToolName:          string(toolspec.ToolExecCommand),
				IsShell:           true,
				Command:           command,
				CompactText:       "printf first-line",
				MovedToBackground: true,
			},
		},
	}
	presentation := RenderDetailPresentation(row, 120, "dark")

	if !presentation.Expandable {
		t.Fatal("backgrounded shell with full command and committed output is not expandable")
	}
	if got := len(presentation.Collapsed); got != 1 {
		t.Fatalf("backgrounded shell collapsed rows = %d, want one", got)
	}
	if got := len(presentation.Expanded); got <= len(presentation.Collapsed) {
		t.Fatalf("backgrounded shell expansion did not reveal additional rows: collapsed=%d expanded=%d", len(presentation.Collapsed), got)
	}
	if got := len(RenderCommittedRow(row, 120, "dark", ModeOngoing).Lines); got != len(presentation.Collapsed) {
		t.Fatalf("backgrounded shell ongoing rows = %d, want compact row count %d", got, len(presentation.Collapsed))
	}
}
