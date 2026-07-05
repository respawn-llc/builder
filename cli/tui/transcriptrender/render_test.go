package transcriptrender

import (
	"strings"
	"testing"

	"core/shared/clientui"
	patchformat "core/shared/transcript/patchformat"

	xansi "github.com/charmbracelet/x/ansi"
)

func TestRenderCommittedRowStyleMatrix(t *testing.T) {
	legacy := "neutral notice"
	cases := []struct {
		name string
		row  clientui.TranscriptCommittedRow
		want string
	}{
		{name: "user", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: "hello"}}, want: "❯ hello"},
		{name: "model", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: "answer"}}, want: "❮ answer"},
		{name: "warning", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeWarning, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "⚠ neutral notice"},
		{name: "error", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeError, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "! neutral notice"},
		{name: "notice", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeInfo, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "ℹ neutral notice"},
		{name: "shell", row: toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false), want: "$ go test ./..."},
		{name: "ask_question", row: toolRow("ask_question", clientui.ToolPresentationAskQuestion, "Pick one", false), want: "? Pick one"},
		{name: "web_search", row: toolRow("web_search", clientui.ToolPresentationDefault, "query", false), want: "@ query"},
		{name: "custom", row: toolRow("custom_tool", clientui.ToolPresentationDefault, "custom preview", false), want: "• custom preview"},
		{name: "workflow_completion", row: toolRow("workflow_completion", clientui.ToolPresentationDefault, "workflow done", false), want: "• workflow done"},
		{name: "view_image", row: toolRow("view_image", clientui.ToolPresentationDefault, "image.png", false), want: "• image.png"},
		{name: "trigger_handoff", row: toolRow("trigger_handoff", clientui.ToolPresentationDefault, "handoff", false), want: "• handoff"},
		{name: "tool_error", row: toolRow("exec_command", clientui.ToolPresentationShell, "failed", true), want: "! failed"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rendered := RenderCommittedRow(tt.row, 80, "", ModeOngoing)
			if len(rendered.Lines) == 0 {
				t.Fatal("rendered no lines")
			}
			line := rendered.Lines[0]
			if got := xansi.Strip(line); got != tt.want {
				t.Fatalf("rendered line = %q, want %q", got, tt.want)
			}
			if !RoleHasStyledANSI(line) {
				t.Fatalf("rendered line = %q, want styled ANSI", line)
			}
		})
	}
}

func TestRenderPatchToolStylesPathAndCounts(t *testing.T) {
	row := toolRow("patch", clientui.ToolPresentationDefault, "ignored", false)
	row.Tool.ToolPresentation.PatchRender = &patchformat.RenderedPatch{
		Files:        []patchformat.RenderedFile{{RelPath: "cli/tui/model.go", Added: 2, Removed: 1}},
		SummaryLines: []patchformat.RenderedLine{{Kind: patchformat.RenderedLineKindFile, Text: "cli/tui/model.go -1 +2", FileIndex: 0}},
	}
	rendered := RenderCommittedRow(row, 80, "", ModeOngoing)
	if len(rendered.Lines) == 0 {
		t.Fatal("rendered no patch lines")
	}
	stripped := xansi.Strip(rendered.Lines[0])
	for _, want := range []string{"⇄", "cli/tui/model.go", "-1", "+2"} {
		if !strings.Contains(stripped, want) {
			t.Fatalf("patch line = %q, want %q", stripped, want)
		}
	}
	if !RoleHasStyledANSI(rendered.Lines[0]) {
		t.Fatalf("patch line = %q, want styled ANSI", rendered.Lines[0])
	}
}

func toolRow(name string, presentation clientui.ToolPresentationKind, text string, isError bool) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowTool,
		Tool: &clientui.TranscriptToolRow{
			ToolName: name,
			Text:     text,
			IsError:  isError,
			ToolPresentation: &clientui.ToolCallMeta{
				ToolName:     name,
				Presentation: presentation,
				Command:      text,
				CompactText:  text,
			},
		},
	}
}
