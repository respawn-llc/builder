package transcriptrender

import (
	"slices"
	"testing"

	"core/shared/clientui"
	patchformat "core/shared/transcript/patchformat"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderCommittedRowStyleMatrix(t *testing.T) {
	legacy := "neutral notice"
	cases := []struct {
		name     string
		row      clientui.TranscriptCommittedRow
		want     string
		wantRole StyleRole
	}{
		{name: "user", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: "hello"}}, want: "❯ hello", wantRole: StyleRoleUser},
		{name: "model", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: "answer"}}, want: "❮ answer", wantRole: StyleRoleAssistant},
		{name: "warning", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeWarning, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "⚠ neutral notice", wantRole: StyleRoleWarning},
		{name: "error", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeError, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "! neutral notice", wantRole: StyleRoleError},
		{name: "notice", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeInfo, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "ℹ neutral notice", wantRole: StyleRoleNotice},
		{name: "shell", row: toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false), want: "$ go test ./...", wantRole: StyleRoleToolShell},
		{name: "ask_question", row: toolRow("ask_question", clientui.ToolPresentationAskQuestion, "Pick one", false), want: "? Pick one", wantRole: StyleRoleToolQuestion},
		{name: "web_search", row: toolRow("web_search", clientui.ToolPresentationDefault, "query", false), want: "@ query", wantRole: StyleRoleToolWebSearch},
		{name: "custom", row: toolRow("custom_tool", clientui.ToolPresentationDefault, "custom preview", false), want: "• custom preview", wantRole: StyleRoleTool},
		{name: "workflow_completion", row: toolRow("workflow_completion", clientui.ToolPresentationDefault, "workflow done", false), want: "• workflow done", wantRole: StyleRoleTool},
		{name: "view_image", row: toolRow("view_image", clientui.ToolPresentationDefault, "image.png", false), want: "• image.png", wantRole: StyleRoleTool},
		{name: "trigger_handoff", row: toolRow("trigger_handoff", clientui.ToolPresentationDefault, "handoff", false), want: "• handoff", wantRole: StyleRoleTool},
		{name: "tool_error", row: toolRow("exec_command", clientui.ToolPresentationShell, "failed", true), want: "! failed", wantRole: StyleRoleToolError},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rendered := RenderCommittedRow(tt.row, 80, "", ModeOngoing)
			if len(rendered.Lines) == 0 {
				t.Fatal("rendered no lines")
			}
			line := rendered.Lines[0]
			if got := line.Plain(); got != tt.want {
				t.Fatalf("rendered line = %q, want %q", got, tt.want)
			}
			if len(line.Spans) < 3 || line.Spans[0].Role != tt.wantRole || line.Spans[2].Role != tt.wantRole {
				t.Fatalf("line has invalid style spans: %+v", line.Spans)
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
	spans := rendered.Lines[0].Spans
	want := []Span{
		{Text: "⇄", Role: StyleRoleToolPatch},
		{Text: " ", Role: StyleRoleToolPatch},
		{Text: "cli/tui/model.go", Role: StyleRoleToolPatch},
		{Text: " ", Role: StyleRoleToolPatch},
		{Text: "-1", Role: StyleRoleToolError},
		{Text: " ", Role: StyleRoleToolPatch},
		{Text: "+2", Role: StyleRoleToolSuccess},
	}
	if !slices.Equal(spans, want) {
		t.Fatalf("patch spans = %+v, want %+v", spans, want)
	}
}

func TestRenderDividerStaysWithinFrameWidth(t *testing.T) {
	assistantLabelWidth := lipgloss.Width(" assistant ")
	cases := []struct {
		name  string
		group clientui.TranscriptRowKind
		width int
	}{
		{name: "negative", group: clientui.TranscriptRowAssistant, width: -1},
		{name: "zero", group: clientui.TranscriptRowAssistant, width: 0},
		{name: "single cell", group: clientui.TranscriptRowAssistant, width: 1},
		{name: "narrower than label", group: clientui.TranscriptRowAssistant, width: 4},
		{name: "exact label width", group: clientui.TranscriptRowAssistant, width: assistantLabelWidth},
		{name: "barely wider than label", group: clientui.TranscriptRowAssistant, width: assistantLabelWidth + 1},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			line := RenderDivider(tt.group, tt.width)
			plainWidth := lipgloss.Width(line.Plain())
			if tt.width <= 0 {
				if plainWidth != 0 {
					t.Fatalf("divider width = %d, want empty for nonpositive frame width %d", plainWidth, tt.width)
				}
				return
			}
			if plainWidth > tt.width {
				t.Fatalf("divider width = %d, want <= frame width %d", plainWidth, tt.width)
			}
			if plainWidth == 0 {
				t.Fatalf("divider unexpectedly empty for positive frame width %d", tt.width)
			}
			for _, span := range line.Spans {
				if span.Role != StyleRoleNotice || !span.Faint {
					t.Fatalf("divider span has invalid style metadata: %+v", span)
				}
			}
		})
	}
}

func TestExpandedDetailWrapPreservesWhitespace(t *testing.T) {
	rendered := RenderCommittedRow(clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowAssistant,
		Assistant: &clientui.TranscriptAssistantRow{
			Text: "  indented  value\n\n    code",
		},
	}, 80, "", ModeDetailExpanded)

	got := make([]string, 0, len(rendered.Lines))
	for _, line := range rendered.Lines {
		got = append(got, line.Plain())
	}
	want := []string{"❮   indented  value", "└ ", "└     code"}
	if !slices.Equal(got, want) {
		t.Fatalf("expanded detail lines = %#v, want %#v", got, want)
	}
}

func TestCollapsedDiagnosticNoticeUsesCompactLabelForDetailVisibility(t *testing.T) {
	rendered := RenderCommittedRow(clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Severity: clientui.TranscriptNoticeInfo,
			Data:     clientui.TranscriptNoticeData{CompactLabel: "AGENTS.md file content"},
			Diagnostic: &clientui.TranscriptDiagnosticData{
				Detail: "raw diagnostic body",
			},
		},
	}, 80, "", ModeDetailCollapsed)

	if got, want := rendered.Lines[0].Plain(), "ℹ AGENTS.md file content"; got != want {
		t.Fatalf("collapsed diagnostic line = %q, want %q", got, want)
	}

	expanded := RenderCommittedRow(clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Severity: clientui.TranscriptNoticeInfo,
			Data:     clientui.TranscriptNoticeData{CompactLabel: "AGENTS.md file content"},
			Diagnostic: &clientui.TranscriptDiagnosticData{
				Detail: "raw diagnostic body",
			},
		},
	}, 80, "", ModeDetailExpanded)
	if got, want := expanded.Lines[0].Plain(), "ℹ raw diagnostic body"; got != want {
		t.Fatalf("expanded diagnostic line = %q, want %q", got, want)
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
