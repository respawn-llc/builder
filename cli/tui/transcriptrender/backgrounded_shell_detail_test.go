package transcriptrender

import (
	"strings"
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

	collapsed := strings.Join(PlainLines(presentation.Collapsed), "\n")
	ongoing := strings.Join(PlainLines(RenderCommittedRow(row, 120, "dark", ModeOngoing).Lines), "\n")
	for _, compact := range []struct {
		name string
		text string
	}{
		{name: "ongoing", text: ongoing},
		{name: "collapsed detail", text: collapsed},
	} {
		t.Run(compact.name, func(t *testing.T) {
			if !strings.Contains(compact.text, BackgroundedShellSuffix) {
				t.Fatalf("backgrounded shell = %q, want backgrounded suffix", compact.text)
			}
			if strings.Contains(compact.text, "printf full-command-line") {
				t.Fatalf("backgrounded shell exposed full command while compact: %q", compact.text)
			}
			if strings.Contains(compact.text, output) {
				t.Fatalf("backgrounded shell exposed committed output while compact: %q", compact.text)
			}
		})
	}
	if !presentation.Expandable {
		t.Fatal("backgrounded shell with full command and committed output is not expandable")
	}

	expanded := strings.Join(PlainLines(presentation.Expanded), "\n")
	for _, commandLine := range strings.Split(command, "\n") {
		if !strings.Contains(expanded, commandLine) {
			t.Fatalf("expanded backgrounded shell omits command line %q: %q", commandLine, expanded)
		}
	}
	if !strings.Contains(expanded, output) {
		t.Fatalf("expanded backgrounded shell omits committed output: %q", expanded)
	}
}
