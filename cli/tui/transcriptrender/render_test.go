package transcriptrender

import (
	"slices"
	"strings"
	"testing"
	"unicode"

	"core/shared/clientui"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderCommittedRowStyleMatrix(t *testing.T) {
	legacy := "neutral notice"
	cases := []struct {
		name       string
		row        clientui.TranscriptCommittedRow
		want       string
		wantRole   StyleRole
		wantColor  ColorRole
		wantSymbol string
	}{
		{name: "user", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: "hello"}}, want: "❯ hello", wantRole: StyleRoleUser, wantColor: ColorRoleForeground},
		{name: "model", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: "answer"}}, want: "❮ answer", wantRole: StyleRoleAssistant, wantColor: ColorRoleForeground},
		{name: "warning", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeWarning, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "⚠ neutral notice", wantRole: StyleRoleWarning, wantColor: ColorRoleWarning},
		{name: "error", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeError, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "! neutral notice", wantRole: StyleRoleError, wantColor: ColorRoleError},
		{name: "notice", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeInfo, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "ℹ neutral notice", wantRole: StyleRoleNotice, wantColor: ColorRoleSubdued},
		{name: "shell", row: toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false), want: "$ go test ./...", wantRole: StyleRoleToolShell, wantColor: ColorRoleForeground},
		{name: "ask_question", row: toolRow("ask_question", clientui.ToolPresentationAskQuestion, "Pick one", false), want: "? Pick one", wantRole: StyleRoleToolQuestion, wantColor: ColorRoleForeground},
		{name: "web_search", row: toolRow("web_search", clientui.ToolPresentationDefault, "query", false), want: "@ query", wantRole: StyleRoleToolWebSearch, wantColor: ColorRoleForeground},
		{name: "custom", row: toolRow("custom_tool", clientui.ToolPresentationDefault, "custom preview", false), want: "• custom preview", wantRole: StyleRoleTool, wantColor: ColorRoleForeground},
		{name: "workflow_completion", row: toolRow("workflow_completion", clientui.ToolPresentationDefault, "workflow done", false), want: "• workflow done", wantRole: StyleRoleTool, wantColor: ColorRoleForeground},
		{name: "view_image", row: toolRow("view_image", clientui.ToolPresentationDefault, "image.png", false), want: "• image.png", wantRole: StyleRoleTool, wantColor: ColorRoleForeground},
		{name: "trigger_handoff", row: toolRow("trigger_handoff", clientui.ToolPresentationDefault, "handoff", false), want: "• handoff", wantRole: StyleRoleTool, wantColor: ColorRoleForeground},
		{name: "tool_error", row: toolRow("exec_command", clientui.ToolPresentationShell, "failed", true), want: "! failed", wantRole: StyleRoleToolShell, wantColor: ColorRoleForeground, wantSymbol: "!"},
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
			wantSymbol := tt.wantSymbol
			if wantSymbol == "" {
				wantSymbol = roleSymbol(tt.wantRole)
			}
			if len(line.Spans) < 3 || line.Spans[0].Text != wantSymbol {
				t.Fatalf("line has invalid style spans: %+v", line.Spans)
			}
			if tt.wantRole != StyleRoleToolShell && line.Spans[2].Role != tt.wantRole {
				t.Fatalf("line content role = %v, want %v; spans: %+v", line.Spans[2].Role, tt.wantRole, line.Spans)
			}
			if got := ColorRoleForStyle(tt.wantRole); got != tt.wantColor {
				t.Fatalf("style role color = %v, want %v", got, tt.wantColor)
			}
		})
	}
}

func TestShellToolRowsUseTypedSyntaxHighlighting(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "sed -n '1,10p' cli/tui/model.go", false)
	row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{ShellDialect: clientui.ToolShellDialectPosix}
	for _, mode := range []Mode{ModeOngoing, ModeDetailCollapsed, ModeDetailExpanded} {
		rendered := RenderCommittedRow(row, 120, "", mode)
		if len(rendered.Lines) == 0 {
			t.Fatalf("mode %v rendered no shell row lines", mode)
		}
		line := rendered.Lines[0]
		if got, want := line.Plain(), "$ sed -n '1,10p' cli/tui/model.go"; got != want {
			t.Fatalf("mode %v shell line = %q, want %q", mode, got, want)
		}
		assertShellLineHasTypedSyntax(t, line)
	}
}

func TestShellRowsUseRenderHintDialectsAtRenderBoundary(t *testing.T) {
	cases := []struct {
		name    string
		dialect clientui.ToolShellDialect
		command string
	}{
		{name: "posix", dialect: clientui.ToolShellDialectPosix, command: "printf 'ok' # comment"},
		{name: "powershell", dialect: clientui.ToolShellDialectPowerShell, command: "Write-Host \"ok\" # comment"},
		{name: "windows command", dialect: clientui.ToolShellDialectWindowsCommand, command: "rem comment"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			row := toolRow("exec_command", clientui.ToolPresentationShell, tt.command, false)
			row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{ShellDialect: tt.dialect}
			rendered := RenderCommittedRow(row, 120, "", ModeOngoing)
			if len(rendered.Lines) == 0 {
				t.Fatal("rendered no committed shell row lines")
			}
			assertShellLineHasTypedSyntax(t, rendered.Lines[0])

			pending := RenderPendingTool(clientui.TranscriptToolStart{
				ToolCallID: "tool-1",
				ToolName:   "exec_command",
				ToolPresentation: &clientui.ToolCallMeta{
					ToolName:     "exec_command",
					Presentation: clientui.ToolPresentationShell,
					Command:      tt.command,
					CompactText:  tt.command,
					RenderHint:   &clientui.ToolRenderHint{ShellDialect: tt.dialect},
				},
			}, 120, "⢎ ")
			assertShellLineHasTypedSyntax(t, pending)
		})
	}
}

func assertShellLineHasTypedSyntax(t *testing.T, line Line) {
	t.Helper()
	foundSyntax := false
	for _, span := range line.Spans[2:] {
		if !span.Faint {
			t.Fatalf("shell syntax span is not faint: %+v", span)
		}
		switch span.Role {
		case StyleRoleToolShellPrimary, StyleRoleToolShellSecondary, StyleRoleToolShellWarning, StyleRoleToolShellError:
			foundSyntax = true
		}
	}
	if !foundSyntax {
		t.Fatalf("shell line did not include typed syntax role spans: %+v", line.Spans)
	}
}

func TestDefaultToolRowsDoNotUseShellSyntaxRoles(t *testing.T) {
	rendered := RenderCommittedRow(toolRow("custom_tool", clientui.ToolPresentationDefault, "sed -n '1,10p' cli/tui/model.go", false), 120, "", ModeOngoing)
	if len(rendered.Lines) == 0 {
		t.Fatal("rendered no custom tool row lines")
	}
	for _, span := range rendered.Lines[0].Spans {
		switch span.Role {
		case StyleRoleToolShellPrimary, StyleRoleToolShellSecondary, StyleRoleToolShellWarning, StyleRoleToolShellError:
			t.Fatalf("default tool row used shell syntax role span: %+v", span)
		}
	}
}

func TestToolRoleSymbolsUseIndependentStatusColors(t *testing.T) {
	successTool := toolRow("custom_tool", clientui.ToolPresentationDefault, "custom preview", false)
	rawShell := toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false)
	rawShell.Tool.ToolPresentation.RawOutputRequested = true
	failedTool := toolRow("custom_tool", clientui.ToolPresentationDefault, "failed input", true)
	patchTool := toolRow("patch", clientui.ToolPresentationDefault, "ignored", false)
	patchTool.Tool.ToolPresentation.PatchRender = &patchformat.RenderedPatch{
		Files:        []patchformat.RenderedFile{{RelPath: "cli/tui/model.go", Added: 2, Removed: 1}},
		SummaryLines: []patchformat.RenderedLine{{Kind: patchformat.RenderedLineKindFile, Text: "cli/tui/model.go -1 +2", FileIndex: 0}},
	}

	cases := []struct {
		name             string
		row              clientui.TranscriptCommittedRow
		wantSymbolRole   StyleRole
		wantSymbolFaint  bool
		checkContent     bool
		wantContentRole  StyleRole
		wantContentFaint bool
		wantSymbolColor  ColorRole
		wantContentColor ColorRole
	}{
		{name: "successful tool", row: successTool, wantSymbolRole: StyleRoleToolSuccess, checkContent: true, wantContentRole: StyleRoleTool, wantContentFaint: true, wantSymbolColor: ColorRoleToolSuccess, wantContentColor: ColorRoleForeground},
		{name: "raw shell warning marker", row: rawShell, wantSymbolRole: StyleRoleWarning, wantSymbolColor: ColorRoleWarning},
		{name: "failed tool", row: failedTool, wantSymbolRole: StyleRoleToolError, checkContent: true, wantContentRole: StyleRoleTool, wantContentFaint: true, wantSymbolColor: ColorRoleError, wantContentColor: ColorRoleForeground},
		{name: "patch tool", row: patchTool, wantSymbolRole: StyleRoleToolSuccess, checkContent: true, wantContentRole: StyleRoleToolPatch, wantSymbolColor: ColorRoleToolSuccess, wantContentColor: ColorRoleForeground},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rendered := RenderCommittedRow(tt.row, 120, "", ModeOngoing)
			if len(rendered.Lines) == 0 || len(rendered.Lines[0].Spans) < 3 {
				t.Fatalf("rendered invalid line: %+v", rendered.Lines)
			}
			symbol := rendered.Lines[0].Spans[0]
			content := rendered.Lines[0].Spans[2]
			if symbol.Role != tt.wantSymbolRole || symbol.Faint != tt.wantSymbolFaint {
				t.Fatalf("symbol span = %+v, want role %v faint %v", symbol, tt.wantSymbolRole, tt.wantSymbolFaint)
			}
			if got := ColorRoleForStyle(symbol.Role); got != tt.wantSymbolColor {
				t.Fatalf("symbol color role = %v, want %v", got, tt.wantSymbolColor)
			}
			if tt.checkContent {
				if content.Role != tt.wantContentRole || content.Faint != tt.wantContentFaint {
					t.Fatalf("content span = %+v, want role %v faint %v", content, tt.wantContentRole, tt.wantContentFaint)
				}
				if got := ColorRoleForStyle(content.Role); got != tt.wantContentColor {
					t.Fatalf("content color role = %v, want %v", got, tt.wantContentColor)
				}
			}
		})
	}
}

func TestRoleSymbolsUseSpecifiedColorRoles(t *testing.T) {
	rawShell := toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false)
	rawShell.Tool.ToolPresentation.RawOutputRequested = true
	patchTool := toolRow("patch", clientui.ToolPresentationDefault, "ignored", false)
	patchTool.Tool.ToolPresentation.PatchRender = &patchformat.RenderedPatch{
		Files:        []patchformat.RenderedFile{{RelPath: "cli/tui/model.go", Added: 2}},
		SummaryLines: []patchformat.RenderedLine{{Kind: patchformat.RenderedLineKindFile, Text: "cli/tui/model.go +2", FileIndex: 0}},
	}

	cases := []struct {
		name            string
		row             clientui.TranscriptCommittedRow
		mode            Mode
		wantSymbol      string
		wantSymbolColor ColorRole
		wantSymbolFaint bool
	}{
		{name: "successful tool ongoing", row: toolRow("custom_tool", clientui.ToolPresentationDefault, "custom preview", false), mode: ModeOngoing, wantSymbol: "•", wantSymbolColor: ColorRoleToolSuccess},
		{name: "successful tool detail", row: toolRow("custom_tool", clientui.ToolPresentationDefault, "custom preview", false), mode: ModeDetailCollapsed, wantSymbol: "•", wantSymbolColor: ColorRoleToolSuccess},
		{name: "raw shell warning marker", row: rawShell, mode: ModeOngoing, wantSymbol: "$", wantSymbolColor: ColorRoleWarning},
		{name: "failed tool", row: toolRow("custom_tool", clientui.ToolPresentationDefault, "failed input", true), mode: ModeOngoing, wantSymbol: "!", wantSymbolColor: ColorRoleError},
		{name: "patch tool", row: patchTool, mode: ModeOngoing, wantSymbol: "⇄", wantSymbolColor: ColorRoleToolSuccess},
		{name: "question tool", row: toolRow("ask_question", clientui.ToolPresentationAskQuestion, "Pick one", false), mode: ModeOngoing, wantSymbol: "?", wantSymbolColor: ColorRoleToolSuccess},
		{name: "web search tool", row: toolRow("web_search", clientui.ToolPresentationDefault, "query", false), mode: ModeOngoing, wantSymbol: "@", wantSymbolColor: ColorRoleToolSuccess},
		{name: "compaction", row: noticeMessageTypeRow(clientui.MessageTypeCompactionSummary, clientui.TranscriptNoticeInfo), mode: ModeOngoing, wantSymbol: "ℹ", wantSymbolColor: ColorRoleSecondary},
		{name: "goal", row: noticeMessageTypeRow(clientui.MessageTypeGoal, clientui.TranscriptNoticeInfo), mode: ModeOngoing, wantSymbol: "ℹ", wantSymbolColor: ColorRolePrimary},
		{name: "workflow detail", row: noticeMessageTypeRow(clientui.MessageTypeWorkflowMode, clientui.TranscriptNoticeInfo), mode: ModeDetailCollapsed, wantSymbol: "ℹ", wantSymbolColor: ColorRolePrimary},
		{name: "warning", row: noticeMessageTypeRow("", clientui.TranscriptNoticeWarning), mode: ModeOngoing, wantSymbol: "⚠", wantSymbolColor: ColorRoleWarning},
		{name: "error", row: noticeMessageTypeRow("", clientui.TranscriptNoticeError), mode: ModeOngoing, wantSymbol: "!", wantSymbolColor: ColorRoleError},
		{name: "reviewer success", row: reviewerNoticeRow(clientui.TranscriptNoticeInfo, transcript.EntryRoleReviewerSuggestions), mode: ModeOngoingCollapsed, wantSymbol: "§", wantSymbolColor: ColorRoleSuccess},
		{name: "reviewer error", row: reviewerNoticeRow(clientui.TranscriptNoticeError, transcript.EntryRoleReviewerError), mode: ModeDetailCollapsed, wantSymbol: "!", wantSymbolColor: ColorRoleError},
		{name: "background completion", row: noticeMessageTypeRow(clientui.MessageTypeBackgroundNotice, clientui.TranscriptNoticeInfo), mode: ModeOngoing, wantSymbol: "ℹ", wantSymbolColor: ColorRoleToolSuccess},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rendered := RenderCommittedRow(tt.row, 120, "", tt.mode)
			if len(rendered.Lines) == 0 || len(rendered.Lines[0].Spans) == 0 {
				t.Fatalf("rendered invalid line: %+v", rendered.Lines)
			}
			symbol := rendered.Lines[0].Spans[0]
			if symbol.Text != tt.wantSymbol {
				t.Fatalf("symbol text = %q, want %q", symbol.Text, tt.wantSymbol)
			}
			if got := ColorRoleForStyle(symbol.Role); got != tt.wantSymbolColor {
				t.Fatalf("symbol color role = %v, want %v (span=%+v)", got, tt.wantSymbolColor, symbol)
			}
			if symbol.Faint != tt.wantSymbolFaint {
				t.Fatalf("symbol faint = %v, want %v (span=%+v)", symbol.Faint, tt.wantSymbolFaint, symbol)
			}
		})
	}
}

func TestNoticeMessageTypeStyleMatrix(t *testing.T) {
	cases := []struct {
		name        string
		messageType clientui.MessageType
		reason      clientui.TranscriptNoticeReason
		severity    clientui.TranscriptNoticeSeverity
		wantRole    StyleRole
		wantColor   ColorRole
	}{
		{name: "compaction summary", messageType: clientui.MessageTypeCompactionSummary, wantRole: StyleRoleNoticeSecondary, wantColor: ColorRoleSecondary},
		{name: "handoff future message", messageType: clientui.MessageTypeHandoffFutureMessage, wantRole: StyleRoleNoticeSecondary, wantColor: ColorRoleSecondary},
		{name: "manual compaction carryover", messageType: clientui.MessageTypeManualCompactionCarryover, wantRole: StyleRoleNoticeSecondary, wantColor: ColorRoleSecondary},
		{name: "goal", messageType: clientui.MessageTypeGoal, wantRole: StyleRoleNoticePrimary, wantColor: ColorRolePrimary},
		{name: "workflow", messageType: clientui.MessageTypeWorkflowMode, wantRole: StyleRoleNoticePrimary, wantColor: ColorRolePrimary},
		{name: "worktree enter", messageType: clientui.MessageTypeWorktreeMode, wantRole: StyleRoleNoticeForeground, wantColor: ColorRoleForeground},
		{name: "worktree exit", messageType: clientui.MessageTypeWorktreeModeExit, wantRole: StyleRoleNoticeForeground, wantColor: ColorRoleForeground},
		{name: "background shell completion", messageType: clientui.MessageTypeBackgroundNotice, wantRole: StyleRoleNoticeForegroundFaint, wantColor: ColorRoleForeground},
		{name: "subagents", messageType: clientui.MessageTypeSubagents, wantRole: StyleRoleNoticeForeground, wantColor: ColorRoleForeground},
		{name: "supervisor reviewer feedback", messageType: clientui.MessageTypeReviewerFeedback, wantRole: StyleRoleNoticeReviewer, wantColor: ColorRoleSuccess},
		{name: "cache warning", reason: clientui.TranscriptNoticeCacheWarning, wantRole: StyleRoleWarning, wantColor: ColorRoleWarning},
		{name: "compaction reminder", messageType: clientui.MessageTypeCompactionSoonReminder, wantRole: StyleRoleWarning, wantColor: ColorRoleWarning},
		{name: "interruption", messageType: clientui.MessageTypeInterruption, wantRole: StyleRoleError, wantColor: ColorRoleError},
		{name: "error feedback", messageType: clientui.MessageTypeErrorFeedback, wantRole: StyleRoleError, wantColor: ColorRoleError},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			notice := &clientui.TranscriptNoticeRow{
				Reason:   tt.reason,
				Severity: tt.severity,
				Data: clientui.TranscriptNoticeData{
					MessageType:  tt.messageType,
					CompactLabel: "compact label",
				},
			}

			gotRole, _ := noticeRoleAndText(notice, clientui.EntryVisibilityOngoing, ModeOngoing)
			if gotRole != tt.wantRole {
				t.Fatalf("notice role = %v, want %v", gotRole, tt.wantRole)
			}
			if gotColor := ColorRoleForStyle(gotRole); gotColor != tt.wantColor {
				t.Fatalf("notice color role = %v, want %v", gotColor, tt.wantColor)
			}
		})
	}
}

func TestOngoingUserAssistantRowsUseCompactText(t *testing.T) {
	row := clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowAssistant,
		Assistant: &clientui.TranscriptAssistantRow{
			Text:          "full first line\nfull second line",
			CondensedText: "compact assistant",
		},
	}

	ongoing := RenderCommittedRow(row, 80, "", ModeOngoing)
	if got, want := ongoing.Lines[0].Plain(), "❮ compact assistant"; got != want {
		t.Fatalf("ongoing assistant row = %q, want %q", got, want)
	}

	expanded := RenderCommittedRow(row, 80, "", ModeDetailExpanded)
	if got, want := expanded.Lines[0].Plain(), "❮ full first line"; got != want {
		t.Fatalf("expanded assistant row = %q, want %q", got, want)
	}
}

func TestToolAndBackgroundNoticeFaintMatrix(t *testing.T) {
	cases := []struct {
		name            string
		row             clientui.TranscriptCommittedRow
		wantSymbolFaint bool
	}{
		{name: "shell", row: toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false)},
		{name: "custom", row: toolRow("custom_tool", clientui.ToolPresentationDefault, "custom preview", false)},
		{
			name: "background shell completion",
			row: clientui.TranscriptCommittedRow{
				Kind: clientui.TranscriptRowNotice,
				Notice: &clientui.TranscriptNoticeRow{
					Severity: clientui.TranscriptNoticeInfo,
					Data: clientui.TranscriptNoticeData{
						MessageType:  clientui.MessageTypeBackgroundNotice,
						CompactLabel: "background done",
					},
				},
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rendered := RenderCommittedRow(tt.row, 80, "", ModeOngoing)
			if len(rendered.Lines) == 0 || len(rendered.Lines[0].Spans) < 3 {
				t.Fatalf("rendered invalid line: %+v", rendered.Lines)
			}
			if rendered.Lines[0].Spans[0].Faint != tt.wantSymbolFaint || !rendered.Lines[0].Spans[2].Faint {
				t.Fatalf("line spans are not faint: %+v", rendered.Lines[0].Spans)
			}
		})
	}
}

func TestPendingToolUsesSpinnerWithCommittedToolStyling(t *testing.T) {
	line := RenderPendingTool(clientui.TranscriptToolStart{
		ToolCallID: "tool-1",
		ToolName:   "exec_command",
		ToolPresentation: &clientui.ToolCallMeta{
			Presentation: clientui.ToolPresentationShell,
			Command:      "go test ./...",
		},
	}, 80, "⢎ ")

	if got, want := line.Plain(), "⢎  go test ./..."; got != want {
		t.Fatalf("pending tool plain line = %q, want %q", got, want)
	}
	if len(line.Spans) < 3 || line.Spans[0].Role != StyleRoleToolSuccess || line.Spans[0].Faint || !line.Spans[2].Faint {
		t.Fatalf("pending tool spans = %+v, want spinner using shell tool styling", line.Spans)
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
		{Text: "⇄", Role: StyleRoleToolSuccess},
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

func TestPatchFamilyToolsDoNotFallbackToToolName(t *testing.T) {
	for _, toolName := range []string{"patch", "edit"} {
		t.Run(toolName, func(t *testing.T) {
			row := toolRow(toolName, clientui.ToolPresentationDefault, toolName, false)
			row.Tool.ToolPresentation = &clientui.ToolCallMeta{ToolName: toolName}
			rendered := RenderCommittedRow(row, 80, "", ModeOngoing)
			if len(rendered.Lines) == 0 {
				t.Fatal("rendered no patch-family lines")
			}
			if got := rendered.Lines[0].Plain(); got != "⇄ tool call" {
				t.Fatalf("patch-family row fell back to tool name or unexpected fallback: %q", got)
			}

			pending := RenderPendingTool(clientui.TranscriptToolStart{
				ToolCallID:       toolName + "-1",
				ToolName:         toolName,
				ToolPresentation: &clientui.ToolCallMeta{ToolName: toolName},
			}, 80, "")
			if got := pending.Plain(); got != "⇄ tool call" {
				t.Fatalf("pending patch-family row fell back to tool name or unexpected fallback: %q", got)
			}
		})
	}
}

func TestCollapsedToolResultSummaryRendersAsFaintInlineMetadata(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false)
	row.Tool.ResultSummary = "passed"

	rendered := RenderCommittedRow(row, 80, "", ModeOngoing)
	if len(rendered.Lines) == 0 {
		t.Fatal("rendered no lines")
	}
	spans := rendered.Lines[0].Spans
	if len(spans) == 0 {
		t.Fatal("rendered line has no spans")
	}
	meta := spans[len(spans)-1]
	if meta.Text != "passed" || meta.Role != StyleRoleNotice || !meta.Faint {
		t.Fatalf("result summary span = %+v, want faint notice metadata", meta)
	}
	gap := spans[len(spans)-2]
	if gap.Text == "" || gap.Role != StyleRoleToolShell {
		t.Fatalf("result summary gap span = %+v, want shell-role spacing before metadata", gap)
	}
}

func TestCollapsedToolRowsKeepInputPreviewAheadOfResultCondensedText(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "raw output text", false)
	row.Tool.CondensedText = "passed"
	row.Tool.ResultSummary = "exit 0"
	row.Tool.ToolPresentation.Command = "go test ./..."
	row.Tool.ToolPresentation.CompactText = "go test ./..."

	for _, mode := range []Mode{ModeOngoing, ModeOngoingCollapsed, ModeDetailCollapsed} {
		rendered := RenderCommittedRow(row, 80, "", mode)
		if len(rendered.Lines) == 0 {
			t.Fatalf("mode %v rendered no lines", mode)
		}
		spans := rendered.Lines[0].Spans
		if len(spans) < 5 {
			t.Fatalf("mode %v rendered no spans", mode)
		}
		command := Line{Spans: spans[2 : len(spans)-2]}.Plain()
		if command != "go test ./..." {
			t.Fatalf("mode %v command = %q, want typed input", mode, command)
		}
		meta := spans[len(spans)-1]
		if meta.Text != "exit 0" || meta.Role != StyleRoleNotice || !meta.Faint {
			t.Fatalf("mode %v metadata span = %+v, want faint result metadata", mode, meta)
		}
	}
}

func TestExpandedToolRowsKeepTypedInputAheadOfOutput(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "raw output text", false)
	row.Tool.ResultSummary = "exit 0"
	row.Tool.ToolPresentation.Command = "go test ./..."
	row.Tool.ToolPresentation.CompactText = "run tests"

	rendered := RenderCommittedRow(row, 80, "", ModeDetailExpanded)
	if got, want := PlainLines(rendered.Lines), []string{"$ go test ./...", "└ exit 0"}; !slices.Equal(got, want) {
		t.Fatalf("expanded tool lines = %q, want %q", got, want)
	}
}

func TestExpandedPatchRowsKeepFullTypedInputAheadOfOutput(t *testing.T) {
	row := toolRow("patch", clientui.ToolPresentationDefault, "raw patch output", true)
	row.Tool.ResultSummary = "failed"
	row.Tool.ToolPresentation.PatchDetail = "cli/tui/model.go\n-old\n+new"

	rendered := RenderCommittedRow(row, 80, "", ModeDetailExpanded)
	if got, want := PlainLines(rendered.Lines), []string{"! cli/tui/model.go", "│ -old", "│ +new", "└ failed"}; !slices.Equal(got, want) {
		t.Fatalf("expanded patch lines = %q, want %q", got, want)
	}
}

func TestToolErrorRowsKeepInputPreviewWithErrorSymbol(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "raw failure output", true)
	row.Tool.CondensedText = "permission denied"
	row.Tool.ResultSummary = "exit 1"
	row.Tool.ToolPresentation.Command = "cat /root/secret"
	row.Tool.ToolPresentation.CompactText = "cat /root/secret"

	for _, mode := range []Mode{ModeOngoing, ModeDetailCollapsed} {
		rendered := RenderCommittedRow(row, 80, "", mode)
		if len(rendered.Lines) == 0 {
			t.Fatalf("mode %v rendered no lines", mode)
		}
		spans := rendered.Lines[0].Spans
		if len(spans) < 5 {
			t.Fatalf("mode %v rendered spans = %+v", mode, spans)
		}
		if command := (Line{Spans: spans[2 : len(spans)-2]}).Plain(); command != "cat /root/secret" {
			t.Fatalf("mode %v command = %q, want failed typed input", mode, command)
		}
		if meta := spans[len(spans)-1]; meta.Text != "exit 1" || meta.Role != StyleRoleNotice || !meta.Faint {
			t.Fatalf("mode %v metadata span = %+v, want error summary", mode, meta)
		}
		if got := ColorRoleForStyle(spans[0].Role); got != ColorRoleError {
			t.Fatalf("mode %v symbol color = %v, want error", mode, got)
		}
	}
}

func TestUserAssistantFullRowsUseTypedMarkdownStyling(t *testing.T) {
	rows := []clientui.TranscriptCommittedRow{
		{Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: "**bold** and `code`"}},
		{Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: "# Heading\nplain"}},
	}

	for _, mode := range []Mode{ModeOngoingFull, ModeDetailExpanded} {
		user := RenderCommittedRow(rows[0], 80, "", mode)
		if len(user.Lines) != 1 || len(user.Lines[0].Spans) != 5 {
			t.Fatalf("mode %v user markdown spans = %+v", mode, user.Lines)
		}
		if got := user.Lines[0].Spans[2]; got.Text != "bold" || !got.Bold || got.Faint {
			t.Fatalf("mode %v bold span = %+v", mode, got)
		}
		if got := user.Lines[0].Spans[4]; got.Text != "code" || got.Bold || !got.Faint {
			t.Fatalf("mode %v code span = %+v", mode, got)
		}

		assistant := RenderCommittedRow(rows[1], 80, "", mode)
		if len(assistant.Lines) != 3 || len(assistant.Lines[0].Spans) < 3 {
			t.Fatalf("mode %v assistant markdown lines = %+v", mode, assistant.Lines)
		}
		if got := assistant.Lines[0].Spans[2]; got.Text != "Heading" || !got.Bold {
			t.Fatalf("mode %v heading span = %+v", mode, got)
		}
		if got := assistant.Lines[2].Plain(); got != "  plain" && got != "└ plain" {
			t.Fatalf("mode %v paragraph line = %q", mode, got)
		}
	}
}

func TestBackgroundNoticeSymbolUsesTypedExitCode(t *testing.T) {
	tests := []struct {
		name      string
		exitCode  int
		wantColor ColorRole
	}{
		{name: "success", exitCode: 0, wantColor: ColorRoleToolSuccess},
		{name: "failure", exitCode: 7, wantColor: ColorRoleError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exitCode := tt.exitCode
			row := clientui.TranscriptCommittedRow{
				Kind: clientui.TranscriptRowNotice,
				Notice: &clientui.TranscriptNoticeRow{
					Severity: clientui.TranscriptNoticeInfo,
					Data: clientui.TranscriptNoticeData{
						MessageType:        clientui.MessageTypeBackgroundNotice,
						CompactLabel:       "background complete",
						BackgroundExitCode: &exitCode,
					},
				},
			}
			rendered := RenderCommittedRow(row, 80, "", ModeOngoingCollapsed)
			if len(rendered.Lines) != 1 || len(rendered.Lines[0].Spans) < 3 {
				t.Fatalf("background row = %+v", rendered.Lines)
			}
			if got := ColorRoleForStyle(rendered.Lines[0].Spans[0].Role); got != tt.wantColor {
				t.Fatalf("symbol color = %v, want %v", got, tt.wantColor)
			}
			if body := rendered.Lines[0].Spans[2]; body.Role != StyleRoleNoticeForegroundFaint || !body.Faint {
				t.Fatalf("background body = %+v, want faint foreground", body)
			}
		})
	}
}

func TestPatchToolErrorKeepsPatchInputShapeWithErrorSymbol(t *testing.T) {
	row := toolRow("patch", clientui.ToolPresentationDefault, "patch failure output", true)
	row.Tool.CondensedText = "patch failed"
	row.Tool.ResultSummary = "failed"
	row.Tool.ToolPresentation.PatchRender = &patchformat.RenderedPatch{
		Files:        []patchformat.RenderedFile{{RelPath: "cli/tui/model.go", Added: 2, Removed: 1}},
		SummaryLines: []patchformat.RenderedLine{{Kind: patchformat.RenderedLineKindFile, Text: "cli/tui/model.go -1 +2", FileIndex: 0}},
	}

	for _, mode := range []Mode{ModeOngoing, ModeDetailCollapsed} {
		rendered := RenderCommittedRow(row, 120, "", mode)
		if len(rendered.Lines) == 0 {
			t.Fatalf("mode %v rendered no lines", mode)
		}
		spans := rendered.Lines[0].Spans
		want := []Span{
			{Text: "!", Role: StyleRoleToolError},
			{Text: " ", Role: StyleRoleToolPatch},
			{Text: "cli/tui/model.go", Role: StyleRoleToolPatch},
			{Text: " ", Role: StyleRoleToolPatch},
			{Text: "-1", Role: StyleRoleToolError},
			{Text: " ", Role: StyleRoleToolPatch},
			{Text: "+2", Role: StyleRoleToolSuccess},
		}
		if len(spans) < len(want) || !slices.Equal(spans[:len(want)], want) {
			t.Fatalf("mode %v patch error prefix spans = %+v, want %+v", mode, spans, want)
		}
		if meta := spans[len(spans)-1]; meta.Text != "failed" || meta.Role != StyleRoleNotice || !meta.Faint {
			t.Fatalf("mode %v patch error metadata = %+v, want failed summary", mode, meta)
		}
	}
}

func TestCollapsedToolResultSummarySanitizesMetadataBeforeInlineRender(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false)
	row.Tool.ResultSummary = "passed\x1b[31m"

	rendered := RenderCommittedRow(row, 80, "", ModeOngoing)
	spans := rendered.Lines[0].Spans
	meta := spans[len(spans)-1]
	if meta.Text != "passed[31m" || meta.Role != StyleRoleNotice || !meta.Faint {
		t.Fatalf("sanitized result summary span = %+v, want sanitized faint notice metadata", meta)
	}
}

func TestCommittedRowsStripUnicodeControlCharacters(t *testing.T) {
	row := clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowUser,
		User: &clientui.TranscriptUserRow{Text: "safe\u009b31m text\u009d"},
	}

	rendered := RenderCommittedRow(row, 80, "", ModeOngoing)

	for _, r := range rendered.Lines[0].Plain() {
		if unicode.IsControl(r) {
			t.Fatalf("sanitized committed row still contains control rune %U", r)
		}
	}
}

func TestCacheWarningNoticeRendersStructuredPayload(t *testing.T) {
	warning := transcript.CacheWarning{
		Scope:           transcript.CacheWarningScopeReviewer,
		Reason:          transcript.CacheWarningReasonNonPostfix,
		LostInputTokens: 1500,
	}
	row := clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:   clientui.TranscriptNoticeReason(transcript.NoticeReasonCacheWarning),
			Severity: clientui.TranscriptNoticeWarning,
			Data: clientui.TranscriptNoticeData{
				CacheWarning: &clientui.TranscriptCacheWarningData{
					Scope:           string(warning.Scope),
					Reason:          string(warning.Reason),
					LostInputTokens: warning.LostInputTokens,
				},
			},
		},
	}

	role, text := noticeRoleAndText(row.Notice, row.Visibility, ModeOngoing)

	if role != StyleRoleWarning {
		t.Fatalf("cache warning role = %q, want warning role", role)
	}
	if got, want := text, transcript.CacheWarningText(warning); got != want {
		t.Fatalf("cache warning notice text = %q, want shared formatter output %q", got, want)
	}
}

func TestRenderDividerStaysWithinFrameWidth(t *testing.T) {
	cases := []struct {
		name  string
		group clientui.TranscriptRowKind
		width int
	}{
		{name: "negative", group: clientui.TranscriptRowAssistant, width: -1},
		{name: "zero", group: clientui.TranscriptRowAssistant, width: 0},
		{name: "single cell", group: clientui.TranscriptRowAssistant, width: 1},
		{name: "narrow", group: clientui.TranscriptRowAssistant, width: 4},
		{name: "wide", group: clientui.TranscriptRowUser, width: 120},
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
				t.Fatalf("divider unexpectedly empty for positive frame width %d", plainWidth)
			}
			for _, span := range line.Spans {
				if span.Role != StyleRoleNotice || !span.Faint {
					t.Fatalf("divider span has invalid style metadata: %+v", span)
				}
			}
		})
	}
}

// A divider is a plain horizontal rule: every visible rune is the box-drawing
// horizontal "─" (or the ellipsis fallback at width 1), with no embedded
// group-kind label, letters, or digits. Group kind only selects presence, not text.
func TestRenderDividerIsPlainRuleWithoutLabel(t *testing.T) {
	for _, group := range []clientui.TranscriptRowKind{
		clientui.TranscriptRowUser,
		clientui.TranscriptRowAssistant,
		clientui.TranscriptRowTool,
		clientui.TranscriptRowNotice,
	} {
		line := RenderDivider(group, 80)
		plain := line.Plain()
		if plain == "" {
			t.Fatalf("group %q: divider empty", group)
		}
		for _, r := range plain {
			if r == '─' || r == '…' {
				continue
			}
			t.Fatalf("group %q: divider contains non-rule rune %q in %q", group, r, plain)
		}
		if w := lipgloss.Width(plain); w != 80 {
			t.Fatalf("group %q: divider width %d, want full frame width 80", group, w)
		}
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
	want := []string{"❮   indented  value", "│ ", "└     code"}
	if !slices.Equal(got, want) {
		t.Fatalf("expanded detail lines = %#v, want %#v", got, want)
	}
}

func TestOngoingUsesCompactSingleLineWithoutContinuationTree(t *testing.T) {
	rendered := RenderCommittedRow(clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowUser,
		User: &clientui.TranscriptUserRow{Text: "first\nsecond"},
	}, 80, "", ModeOngoing)

	got := make([]string, 0, len(rendered.Lines))
	for _, line := range rendered.Lines {
		got = append(got, line.Plain())
	}
	want := []string{"❯ first…"}
	if !slices.Equal(got, want) {
		t.Fatalf("ongoing compact lines = %#v, want %#v", got, want)
	}
}

// Detail mode renders a real tree of continuation guides: the middle
// continuation lines of an entry use the vertical "│" guide, and the LAST
// continuation line closes the tree with the corner "└". The first line keeps
// the normal role symbol; ongoing rows stay compact and do not render
// continuations.
func TestDetailContinuationUsesTreeGuidesWithCornerOnLastLine(t *testing.T) {
	rendered := RenderCommittedRow(clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowAssistant,
		Assistant: &clientui.TranscriptAssistantRow{
			Text: "line one\nline two\nline three\nline four",
		},
	}, 80, "", ModeDetailExpanded)

	if len(rendered.Lines) != 4 {
		t.Fatalf("detail expanded lines = %d, want 4", len(rendered.Lines))
	}
	assertContinuationGuide(t, "first/role-symbol", rendered.Lines[0].Plain(), "")
	assertContinuationGuide(t, "middle-1", rendered.Lines[1].Plain(), "│")
	assertContinuationGuide(t, "middle-2", rendered.Lines[2].Plain(), "│")
	assertContinuationGuide(t, "last", rendered.Lines[3].Plain(), "└")
}

func assertContinuationGuide(t *testing.T, label, row, wantGuide string) {
	t.Helper()
	if wantGuide == "" {
		if r := firstRune(row); r == '│' || r == '└' {
			t.Fatalf("%s: first rune of %q is a tree guide %q, want role symbol", label, row, string(r))
		}
		return
	}
	if r := firstRune(row); r != []rune(wantGuide)[0] {
		t.Fatalf("%s: first rune of %q = %q, want guide %q", label, row, string(r), wantGuide)
	}
}

func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
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

// Spec tui-transcript.md: collapsed detail + ongoing use compact text, expansion
// reveals full entry content verbatim. For user/assistant, the compact form is
// the server-provided CondensedText when present (else first non-empty line).
func TestUserAssistantCompactTextToggleBetweenCollapsedAndExpanded(t *testing.T) {
	for _, kind := range []struct {
		name   string
		row    func(text, condensed string) clientui.TranscriptCommittedRow
		symbol string
	}{{"user", func(text, condensed string) clientui.TranscriptCommittedRow {
		return clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: text, CondensedText: condensed}}
	}, "❯"}, {"assistant", func(text, condensed string) clientui.TranscriptCommittedRow {
		return clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: text, CondensedText: condensed}}
	}, "❮"}} {
		t.Run(kind.name, func(t *testing.T) {
			full := "first body line\nsecond body line\nthird body line"
			condensed := "server compact summary"

			collapsed := RenderCommittedRow(kind.row(full, condensed), 80, "", ModeDetailCollapsed)
			if len(collapsed.Lines) != 1 {
				t.Fatalf("collapsed lines = %d, want 1 (compact)", len(collapsed.Lines))
			}
			if got, want := collapsed.Lines[0].Plain(), kind.symbol+" "+condensed; got != want {
				t.Fatalf("collapsed line = %q, want compact %q", got, want)
			}

			expanded := RenderCommittedRow(kind.row(full, condensed), 80, "", ModeDetailExpanded)
			plain := expanded.Lines[0].Plain()
			if !strings.Contains(plain, "first body line") {
				t.Fatalf("expanded line = %q, want full first body line", plain)
			}
			if strings.Contains(plain, condensed) {
				t.Fatalf("expanded line leaked compact text %q into full body: %q", condensed, plain)
			}
		})
	}
}

func TestReviewerNoticeRendersReviewerGlyph(t *testing.T) {
	row := clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoingCollapsed,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Severity: clientui.TranscriptNoticeInfo,
			Data: clientui.TranscriptNoticeData{
				MessageType:  clientui.MessageTypeReviewerFeedback,
				CompactLabel: "Reviewer made 1 suggestion.",
			},
			Diagnostic: &clientui.TranscriptDiagnosticData{Code: string(transcript.EntryRoleReviewerSuggestions)},
		},
	}

	for _, mode := range []Mode{ModeOngoingCollapsed, ModeDetailCollapsed, ModeDetailExpanded} {
		rendered := RenderCommittedRow(row, 80, "", mode)
		if len(rendered.Lines) != 1 {
			t.Fatalf("mode %v rendered lines = %d, want 1", mode, len(rendered.Lines))
		}
		if got := rendered.Lines[0].Plain(); !strings.HasPrefix(got, "§ ") {
			t.Fatalf("mode %v reviewer line = %q, want reviewer glyph", mode, got)
		}
		if got := ColorRoleForStyle(rendered.Lines[0].Spans[0].Role); got != ColorRoleSuccess {
			t.Fatalf("mode %v reviewer glyph color role = %v, want success", mode, got)
		}
	}
}

func TestReviewerErrorNoticeRendersErrorGlyph(t *testing.T) {
	row := clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoing,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Severity: clientui.TranscriptNoticeError,
			Data: clientui.TranscriptNoticeData{
				MessageType:  clientui.MessageTypeReviewerFeedback,
				CompactLabel: "Supervisor failed.",
			},
			Diagnostic: &clientui.TranscriptDiagnosticData{Code: string(transcript.EntryRoleReviewerError)},
		},
	}

	rendered := RenderCommittedRow(row, 80, "", ModeOngoing)
	if len(rendered.Lines) != 1 {
		t.Fatalf("rendered lines = %d, want 1", len(rendered.Lines))
	}
	if got := rendered.Lines[0].Plain(); !strings.HasPrefix(got, "! ") {
		t.Fatalf("reviewer error line = %q, want error glyph", got)
	}
	if got := ColorRoleForStyle(rendered.Lines[0].Spans[0].Role); got != ColorRoleError {
		t.Fatalf("reviewer error glyph color role = %v, want error", got)
	}
}

func noticeMessageTypeRow(messageType clientui.MessageType, severity clientui.TranscriptNoticeSeverity) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Severity: severity,
			Data: clientui.TranscriptNoticeData{
				MessageType:  messageType,
				CompactLabel: "notice",
			},
		},
	}
}

func reviewerNoticeRow(severity clientui.TranscriptNoticeSeverity, role transcript.EntryRole) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Severity: severity,
			Data: clientui.TranscriptNoticeData{
				MessageType:  clientui.MessageTypeReviewerFeedback,
				CompactLabel: "reviewer",
			},
			Diagnostic: &clientui.TranscriptDiagnosticData{Code: string(role)},
		},
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
