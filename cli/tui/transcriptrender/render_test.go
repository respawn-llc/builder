package transcriptrender

import (
	"fmt"
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
		name      string
		row       clientui.TranscriptCommittedRow
		want      string
		wantRole  StyleRole
		wantColor ColorRole
	}{
		{name: "user", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: "hello"}}, want: "❯ hello", wantRole: StyleRoleUser, wantColor: ColorRoleForeground},
		{name: "model", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: "answer"}}, want: "❮ answer", wantRole: StyleRoleAssistant, wantColor: ColorRoleForeground},
		{name: "warning", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeWarning, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "⚠ neutral notice", wantRole: StyleRoleWarning, wantColor: ColorRoleWarning},
		{name: "error", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeError, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "! neutral notice", wantRole: StyleRoleError, wantColor: ColorRoleError},
		{name: "notice", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeInfo, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "ℹ neutral notice", wantRole: StyleRoleNotice, wantColor: ColorRoleForeground},
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
			if line.LeadingSymbol == nil || len(line.Spans) < 2 || line.LeadingSymbol.Style.SemanticRole != tt.wantRole {
				t.Fatalf("line has invalid typed symbol or body spans: %+v", line)
			}
			if tt.wantRole != StyleRoleToolShell && line.Spans[1].Style.SemanticRole != tt.wantRole {
				t.Fatalf("line content role = %v, want %v; spans: %+v", line.Spans[1].Style.SemanticRole, tt.wantRole, line.Spans)
			}
			if got := ColorRoleForStyle(tt.wantRole); got != tt.wantColor {
				t.Fatalf("style role color = %v, want %v", got, tt.wantColor)
			}
		})
	}
}

func TestFaintRowsUseBaseForegroundWithTerminalFaintAttribute(t *testing.T) {
	legacy := "neutral notice"
	notice := RenderCommittedRow(clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Severity: clientui.TranscriptNoticeInfo,
			Data:     clientui.TranscriptNoticeData{LegacyText: &legacy},
		},
	}, 80, "dark", ModeDetailCollapsed)
	tool := RenderCommittedRow(toolRow("ask_question", clientui.ToolPresentationDefault, "question", false), 80, "dark", ModeDetailCollapsed)

	for name, row := range map[string]Row{"notice": notice, "tool": tool} {
		if len(row.Lines) != 1 {
			t.Fatalf("%s row lines = %+v, want one line", name, row.Lines)
		}
		var body *Span
		for index := range row.Lines[0].Spans {
			if strings.TrimSpace(row.Lines[0].Spans[index].Text) != "" {
				body = &row.Lines[0].Spans[index]
				break
			}
		}
		if body == nil {
			t.Fatalf("%s row has no body span: %+v", name, row.Lines[0])
		}
		resolved := ResolveSpanStyle(*body, "dark")
		if resolved.Foreground.Kind != ResolvedForegroundTheme ||
			resolved.Foreground.Theme != ColorForRole(ColorRoleForeground, "dark") ||
			!resolved.Faint {
			t.Fatalf("%s faint body resolves to %+v, want base foreground with terminal faint", name, resolved)
		}
	}
}

func TestShellToolRowsUseTypedSyntaxHighlighting(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "sed -n '1,10p' cli/tui/model.go", false)
	row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{
		Kind:         clientui.ToolRenderKindShell,
		ShellDialect: clientui.ToolShellDialectPosix,
	}
	for _, mode := range []Mode{ModeOngoing, ModeDetailCollapsed, ModeDetailExpanded} {
		rendered := RenderCommittedRow(row, 120, "", mode)
		if len(rendered.Lines) == 0 {
			t.Fatalf("mode %v rendered no shell row lines", mode)
		}
		line := rendered.Lines[0]
		if got, want := line.Plain(), "$ sed -n '1,10p' cli/tui/model.go"; got != want {
			t.Fatalf("mode %v shell line = %q, want %q", mode, got, want)
		}
		assertShellLineHasTypedSyntax(t, line, mode != ModeDetailExpanded)
	}
}

func TestPlainShellRenderHintSkipsSyntaxHighlighting(t *testing.T) {
	row := toolRow("write_stdin", clientui.ToolPresentationShell, "process completed output", false)
	row.Tool.ToolPresentation.Command = "Polled session 1149 for 2s"
	row.Tool.ToolPresentation.CompactText = row.Tool.ToolPresentation.Command
	row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{Kind: clientui.ToolRenderKindPlain}
	for _, mode := range []Mode{ModeOngoing, ModeDetailCollapsed, ModeDetailExpanded} {
		rendered := RenderCommittedRow(row, 120, "", mode)
		if len(rendered.Lines) == 0 {
			t.Fatalf("mode %v rendered no plain shell row lines", mode)
		}
		assertShellLineIsPlainText(t, rendered.Lines[0], mode != ModeDetailExpanded)
	}
	if got, want := PlainLines(RenderCommittedRow(row, 120, "", ModeDetailExpanded).Lines), []string{
		"$ Polled session 1149 for 2s",
		"│ ",
		"└ process completed output",
	}; !slices.Equal(got, want) {
		t.Fatalf("expanded poll lines = %q, want %q", got, want)
	}

	pending := RenderPendingTool(clientui.TranscriptToolStart{
		ToolCallID: "b5c34536-1994-46bd-a3e4-839402a5ee1e",
		ToolName:   "write_stdin",
		ToolPresentation: &clientui.ToolCallMeta{
			ToolName:     "write_stdin",
			Presentation: clientui.ToolPresentationShell,
			Command:      "Polled session 1149 for 2s",
			CompactText:  "Polled session 1149 for 2s",
			RenderHint:   &clientui.ToolRenderHint{Kind: clientui.ToolRenderKindPlain},
		},
	}, 120, "", "⢎ ")
	assertShellLineIsPlainText(t, pending, true)
}

func TestSourceReadUsesCommandPreviewAndSourceResultDetail(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "package transcriptrender\n\nfunc example() {}", false)
	row.Tool.ToolPresentation.Command = "sed -n '1,20p' cli/tui/transcriptrender/render.go"
	row.Tool.ToolPresentation.CompactText = row.Tool.ToolPresentation.Command
	row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{
		Kind:       clientui.ToolRenderKindSource,
		Path:       "cli/tui/transcriptrender/render.go",
		ResultOnly: true,
	}

	presentation := RenderDetailPresentation(row, 120, "dark")
	if got := presentation.Collapsed[0].Plain(); !strings.Contains(got, row.Tool.ToolPresentation.Command) {
		t.Fatalf("collapsed source read = %q, want typed command preview", got)
	}
	expanded := strings.Join(PlainLines(presentation.Expanded), "\n")
	if !strings.Contains(expanded, "package transcriptrender") ||
		!strings.Contains(expanded, "func example() {}") {
		t.Fatalf("expanded source read = %q, want source result", expanded)
	}
	if !strings.Contains(expanded, row.Tool.ToolPresentation.Command) {
		t.Fatalf("expanded source read = %q, want typed command before source result", expanded)
	}
	if got, want := PlainLines(presentation.Expanded)[1], "│ "; got != want {
		t.Fatalf("expanded source separator = %q, want blank line", got)
	}
	foundSourceSyntax := false
	for _, line := range presentation.Expanded {
		if !strings.Contains(line.Plain(), "func example() {}") {
			continue
		}
		for _, span := range line.Spans {
			if span.Style.Kind != SpanStyleExplicitRGB {
				continue
			}
			foundSourceSyntax = true
			if span.Style.Has(SpanAttributeFaint) {
				t.Fatalf("expanded source result syntax span remains faint: %+v", span)
			}
		}
	}
	if !foundSourceSyntax {
		t.Fatalf("expanded source read output has no Chroma syntax spans: %+v", presentation.Expanded)
	}
}

func TestDetailExpandedRowsRemoveFaintStyling(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "package example\n\nfunc main() {}", false)
	row.Tool.ToolPresentation.Command = "sed -n '1,20p' example.go"
	row.Tool.ToolPresentation.CompactText = row.Tool.ToolPresentation.Command
	row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{
		Kind:       clientui.ToolRenderKindSource,
		Path:       "example.go",
		ResultOnly: true,
	}

	for _, line := range RenderDetailPresentation(row, 120, "dark").Expanded {
		if line.LeadingSymbol != nil && line.LeadingSymbol.Style.Has(SpanAttributeFaint) {
			t.Fatalf("expanded leading symbol remains faint: %+v", line.LeadingSymbol)
		}
		for _, span := range line.Spans {
			if role, ok := span.Style.Role(); ok && role == StyleRoleNotice {
				continue
			}
			if span.Style.Has(SpanAttributeFaint) {
				t.Fatalf("expanded span remains faint: %+v", span)
			}
		}
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
			row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{
				Kind:         clientui.ToolRenderKindShell,
				ShellDialect: tt.dialect,
			}
			rendered := RenderCommittedRow(row, 120, "", ModeOngoing)
			if len(rendered.Lines) == 0 {
				t.Fatal("rendered no committed shell row lines")
			}
			assertShellLineHasTypedSyntax(t, rendered.Lines[0], true)

			pending := RenderPendingTool(clientui.TranscriptToolStart{
				ToolCallID: "2d97d231-7765-471a-bf55-a4c17157af01",
				ToolName:   "exec_command",
				ToolPresentation: &clientui.ToolCallMeta{
					ToolName:     "exec_command",
					Presentation: clientui.ToolPresentationShell,
					Command:      tt.command,
					CompactText:  tt.command,
					RenderHint: &clientui.ToolRenderHint{
						Kind:         clientui.ToolRenderKindShell,
						ShellDialect: tt.dialect,
					},
				},
			}, 120, "", "⢎ ")
			assertShellLineHasTypedSyntax(t, pending, true)
		})
	}
}

func TestMovedToBackgroundShellRowKeepsMovedToBackgroundSuffixAtNarrowWidth(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "sleep 20; printf completed", false)
	row.Tool.ToolPresentation.MovedToBackground = true

	rendered := RenderCommittedRow(row, 24, "", ModeOngoing)
	if len(rendered.Lines) != 1 {
		t.Fatalf("backgrounded shell lines = %+v, want one line", rendered.Lines)
	}
	line := rendered.Lines[0]
	if got, want := line.Plain(), "$ sleep…  · backgrounded"; got != want {
		t.Fatalf("backgrounded shell line = %q, want %q", got, want)
	}
	if line.LeadingSymbol == nil {
		t.Fatal("backgrounded shell line has no symbol")
	}
	if role, ok := line.LeadingSymbol.Style.Role(); !ok || role != StyleRoleToolShellSecondary {
		t.Fatalf("backgrounded shell symbol style = %+v, want secondary shell role", line.LeadingSymbol.Style)
	}
	for _, span := range line.Spans {
		role, ok := span.Style.Role()
		if !ok || (role != StyleRoleToolShell && role != StyleRoleNoticeForegroundFaint) ||
			!span.Style.Has(SpanAttributeFaint) {
			t.Fatalf("backgrounded shell body span = %+v, want faint foreground", span)
		}
	}
}

func TestMovedToBackgroundMultilineShellStacksHiddenLineCount(t *testing.T) {
	command := "sleep 20;\nprintf completed;\necho done"
	row := toolRow("exec_command", clientui.ToolPresentationShell, command, false)
	row.Tool.ToolPresentation.MovedToBackground = true

	rendered := RenderCommittedRow(row, 80, "", ModeOngoing)
	if len(rendered.Lines) != 1 {
		t.Fatalf("backgrounded multiline shell lines = %+v, want one compact line", rendered.Lines)
	}
	if got, want := rendered.Lines[0].Plain(), "$ sleep 20;  · 2 more lines · backgrounded"; got != want {
		t.Fatalf("backgrounded multiline shell = %q, want %q", got, want)
	}
}

func TestMovedToBackgroundMultilineShellShowsCountOnlyWhileDetailIsCollapsed(t *testing.T) {
	command := "sleep 20;\nprintf completed;\necho done"
	row := toolRow("exec_command", clientui.ToolPresentationShell, command, false)
	row.Tool.ToolPresentation.MovedToBackground = true

	for _, tt := range []struct {
		mode Mode
		want string
	}{
		{mode: ModeDetailCollapsed, want: "$ sleep 20;  · 2 more lines · backgrounded"},
		{mode: ModeDetailExpanded, want: "$ sleep 20;  · backgrounded"},
	} {
		rendered := RenderCommittedRow(row, 80, "", tt.mode)
		if len(rendered.Lines) != 1 {
			t.Fatalf("mode %v backgrounded detail lines = %+v, want one compact line", tt.mode, rendered.Lines)
		}
		if got := rendered.Lines[0].Plain(); got != tt.want {
			t.Fatalf("mode %v backgrounded detail = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

func assertShellLineIsPlainText(t *testing.T, line Line, wantFaint bool) {
	t.Helper()
	if len(line.Spans) < 2 {
		t.Fatalf("plain shell line has no body spans: %+v", line)
	}
	for _, span := range line.Spans[1:] {
		if span.Style.Kind != SpanStyleSemantic ||
			span.Style.SemanticRole != StyleRoleToolShell ||
			span.Style.Has(SpanAttributeFaint) != wantFaint {
			t.Fatalf("plain shell body used syntax styling: %+v", line.Spans)
		}
	}
}

func assertShellLineHasTypedSyntax(t *testing.T, line Line, wantFaint bool) {
	t.Helper()
	foundSyntax := false
	for _, span := range line.Spans[1:] {
		if span.Style.Has(SpanAttributeFaint) != wantFaint {
			t.Fatalf("shell syntax span faintness = %t, want %t: %+v", span.Style.Has(SpanAttributeFaint), wantFaint, span)
		}
		if span.Style.Kind == SpanStyleExplicitRGB {
			foundSyntax = true
		}
	}
	if !foundSyntax {
		t.Fatalf("shell line did not include Chroma syntax spans: %+v", line.Spans)
	}
}

func TestDefaultToolRowsDoNotUseChromaSyntax(t *testing.T) {
	rendered := RenderCommittedRow(toolRow("custom_tool", clientui.ToolPresentationDefault, "sed -n '1,10p' cli/tui/model.go", false), 120, "", ModeOngoing)
	if len(rendered.Lines) == 0 {
		t.Fatal("rendered no custom tool row lines")
	}
	for _, span := range rendered.Lines[0].Spans {
		if span.Style.Kind == SpanStyleExplicitRGB {
			t.Fatalf("default tool row used Chroma syntax span: %+v", span)
		}
	}
}

func TestToolSymbolsUseSeparateMetadataFromBodies(t *testing.T) {
	rawShell := toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false)
	rawShell.Tool.ToolPresentation.RawOutputRequested = true
	patchTool := toolRow("patch", clientui.ToolPresentationDefault, "ignored", false)
	patchTool.Tool.ToolPresentation.PatchRender = &patchformat.RenderedPatch{
		Files:        []patchformat.RenderedFile{{RelPath: "cli/tui/model.go", Added: 2}},
		SummaryLines: []patchformat.RenderedLine{{Kind: patchformat.RenderedLineKindFile, Text: "cli/tui/model.go +2", FileIndex: 0}},
	}

	cases := []struct {
		name string
		row  clientui.TranscriptCommittedRow
	}{
		{name: "successful tool", row: toolRow("custom_tool", clientui.ToolPresentationDefault, "custom preview", false)},
		{name: "raw shell", row: rawShell},
		{name: "failed tool", row: toolRow("custom_tool", clientui.ToolPresentationDefault, "failed input", true)},
		{name: "patch tool", row: patchTool},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			rendered := RenderCommittedRow(tt.row, 120, "", ModeOngoing)
			if len(rendered.Lines) == 0 || rendered.Lines[0].LeadingSymbol == nil || len(rendered.Lines[0].Spans) < 2 {
				t.Fatalf("rendered invalid line: %+v", rendered.Lines)
			}
			symbol := *rendered.Lines[0].LeadingSymbol
			body := rendered.Lines[0].Spans[1]
			if symbol.Style == body.Style {
				t.Fatalf("symbol metadata was coupled to body metadata: symbol=%+v body=%+v", symbol, body)
			}
		})
	}
}

func TestRoleSymbolOwnsTypedLeadingSlotOutsideBodySpans(t *testing.T) {
	rendered := RenderCommittedRow(
		toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false),
		80,
		"",
		ModeOngoing,
	)
	if len(rendered.Lines) == 0 {
		t.Fatal("rendered no tool lines")
	}
	line := rendered.Lines[0]
	if line.LeadingSymbol == nil {
		t.Fatalf("rendered line has no typed leading symbol: %+v", line)
	}
	if line.LeadingSymbol.Text == "" ||
		line.LeadingSymbol.Style.SemanticRole != StyleRoleToolSuccess ||
		line.LeadingSymbol.Style.Has(SpanAttributeFaint) {
		t.Fatalf("typed leading symbol = %+v, want full-strength successful tool role", line.LeadingSymbol)
	}
	for _, span := range line.Spans {
		if span.Text == line.LeadingSymbol.Text {
			t.Fatalf("body spans duplicate typed leading symbol: symbol=%+v spans=%+v", line.LeadingSymbol, line.Spans)
		}
	}
}

func TestBackgroundNoticeUsesPrimaryInfoSymbolAndFullStrengthBody(t *testing.T) {
	row := clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Severity: clientui.TranscriptNoticeInfo,
			Data: clientui.TranscriptNoticeData{
				MessageType:  clientui.MessageTypeBackgroundNotice,
				CompactLabel: "background complete",
			},
		},
	}

	for _, mode := range []Mode{ModeOngoingCollapsed, ModeDetailCollapsed, ModeDetailExpanded} {
		rendered := RenderCommittedRow(row, 80, "", mode)
		if rendered.Group != clientui.TranscriptRowTool {
			t.Fatalf("mode %v background notice group = %q, want tool activity", mode, rendered.Group)
		}
		if len(rendered.Lines) != 1 || rendered.Lines[0].LeadingSymbol == nil {
			t.Fatalf("mode %v background notice = %+v, want one line with typed symbol", mode, rendered)
		}
		line := rendered.Lines[0]
		if line.LeadingSymbol.Text != "ℹ" ||
			line.LeadingSymbol.Style.SemanticRole != StyleRoleNoticePrimary ||
			line.LeadingSymbol.Style.Has(SpanAttributeFaint) {
			t.Fatalf("mode %v background symbol = %+v, want full-strength primary info symbol", mode, line.LeadingSymbol)
		}
		for _, span := range line.Spans {
			if strings.TrimSpace(span.Text) == "" {
				continue
			}
			if span.Style.SemanticRole != StyleRoleNoticeForeground || span.Style.Has(SpanAttributeFaint) {
				t.Fatalf("mode %v background body span = %+v, want full-strength foreground", mode, span)
			}
		}
	}
}

func TestResolveSpanStyleCarriesSemanticColorAndAttributes(t *testing.T) {
	span := SemanticSpan(
		"semantic",
		StyleRoleWarning,
		SpanAttributeFaint,
		SpanAttributeBold,
		SpanAttributeItalic,
		SpanAttributeUnderline,
	)

	resolved := ResolveSpanStyle(span, "dark")
	if resolved.Foreground.Kind != ResolvedForegroundTheme ||
		resolved.Foreground.Theme != ColorForRole(ColorRoleForStyle(span.Style.SemanticRole), "dark") {
		t.Fatalf("resolved foreground = %+v, want role color", resolved.Foreground)
	}
	if !resolved.Faint || !resolved.Bold || !resolved.Italic || !resolved.Underline {
		t.Fatalf("resolved attributes = %+v, want every semantic attribute", resolved)
	}
}

func TestRenderDetailPresentationDoesNotExpandIdenticalValidLines(t *testing.T) {
	presentation := RenderDetailPresentation(
		clientui.TranscriptCommittedRow{
			Integrity: transcript.RowIntegrityValid,
			Kind:      clientui.TranscriptRowUser,
			User:      &clientui.TranscriptUserRow{Text: "short user row"},
		},
		80,
		"",
	)

	if presentation.Expandable {
		t.Fatalf("short valid presentation is expandable: %+v", presentation)
	}
	if got, want := PlainLines(presentation.Collapsed), PlainLines(presentation.Expanded); !slices.Equal(got, want) {
		t.Fatalf("short valid lines differ: collapsed=%q expanded=%q", got, want)
	}
}

func TestRenderDetailPresentationExpandsDifferingValidLines(t *testing.T) {
	presentation := RenderDetailPresentation(
		clientui.TranscriptCommittedRow{
			Integrity: transcript.RowIntegrityValid,
			Kind:      clientui.TranscriptRowAssistant,
			Assistant: &clientui.TranscriptAssistantRow{
				Text:          "full first line\nfull second line",
				CondensedText: "compact answer",
			},
		},
		80,
		"",
	)

	if !presentation.Expandable {
		t.Fatalf("differing valid presentation is not expandable: %+v", presentation)
	}
}

func TestRenderDetailPresentationKeepsRecoverableMalformedRowsExpandable(t *testing.T) {
	legacyNotice := "legacy notice"
	rows := []clientui.TranscriptCommittedRow{
		{Integrity: transcript.RowIntegrityRecoverableMalformed, Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: "legacy user"}},
		{Integrity: transcript.RowIntegrityRecoverableMalformed, Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: "legacy assistant"}},
		{Integrity: transcript.RowIntegrityRecoverableMalformed, Kind: clientui.TranscriptRowTool, Tool: &clientui.TranscriptToolRow{Text: "legacy tool"}},
		{Integrity: transcript.RowIntegrityRecoverableMalformed, Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{
			Reason: clientui.TranscriptNoticeLegacyUntypedNotice,
			Data:   clientui.TranscriptNoticeData{LegacyText: &legacyNotice},
		}},
	}

	for _, row := range rows {
		presentation := RenderDetailPresentation(row, 80, "")
		if !presentation.Expandable {
			t.Fatalf("recoverable malformed row %q is not expandable: %+v", row.Kind, presentation)
		}
	}
}

func TestRenderDetailPresentationDoesNotExpandUnrecoverableMalformedRows(t *testing.T) {
	rows := []clientui.TranscriptCommittedRow{
		{Visibility: clientui.EntryVisibilityDetail, Integrity: transcript.RowIntegrityUnrecoverableMalformed, Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{}},
		{Visibility: clientui.EntryVisibilityDetail, Integrity: transcript.RowIntegrityUnrecoverableMalformed, Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{}},
		{Visibility: clientui.EntryVisibilityDetail, Integrity: transcript.RowIntegrityUnrecoverableMalformed, Kind: clientui.TranscriptRowTool, Tool: &clientui.TranscriptToolRow{}},
		{Visibility: clientui.EntryVisibilityDetail, Integrity: transcript.RowIntegrityUnrecoverableMalformed, Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{
			Reason: clientui.TranscriptNoticeLegacyUntypedNotice,
		}},
	}

	for _, row := range rows {
		presentation := RenderDetailPresentation(row, 80, "")
		if presentation.Expandable {
			t.Fatalf("unrecoverable malformed row %q is expandable: %+v", row.Kind, presentation)
		}
		if len(presentation.Collapsed) == 0 {
			t.Fatalf("unrecoverable malformed row %q was dropped", row.Kind)
		}
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

func TestPendingToolChangesOnlyCommittedSymbolText(t *testing.T) {
	start := clientui.TranscriptToolStart{
		ToolCallID: "2d97d231-7765-471a-bf55-a4c17157af02",
		ToolName:   "exec_command",
		ToolPresentation: &clientui.ToolCallMeta{
			Presentation: clientui.ToolPresentationShell,
			Command:      "go test ./...",
		},
	}
	committed := RenderPendingTool(start, 80, "", "")
	pending := RenderPendingTool(start, 80, "", "⢎ ")

	if got, want := pending.Plain(), "⢎  go test ./..."; got != want {
		t.Fatalf("pending tool plain line = %q, want %q", got, want)
	}
	if committed.LeadingSymbol == nil || pending.LeadingSymbol == nil || len(pending.Spans) != len(committed.Spans) {
		t.Fatalf("pending line = %+v, committed line = %+v", pending, committed)
	}
	pendingSymbol := *pending.LeadingSymbol
	committedSymbol := *committed.LeadingSymbol
	pendingSymbol.Text = committedSymbol.Text
	if pendingSymbol != committedSymbol || !slices.Equal(pending.Spans, committed.Spans) {
		t.Fatalf("pending decoration changed non-text metadata: pending=%+v committed=%+v", pending, committed)
	}
}

func TestPendingMultilineShellShowsExplicitHiddenLineCount(t *testing.T) {
	command := "body=$(kent task show KENT-224 --project . --json |\n  jq -r '.body')\necho \"$body\""
	line := RenderPendingTool(clientui.TranscriptToolStart{
		ToolCallID: "db2b88a0-3a53-442e-a47b-84bb38ba7f77",
		ToolName:   "exec_command",
		ToolPresentation: &clientui.ToolCallMeta{
			ToolName:     "exec_command",
			Presentation: clientui.ToolPresentationShell,
			Command:      command,
			CompactText:  command,
		},
	}, 120, "", "⢎ ")

	if got, want := line.Plain(), "⢎  body=$(kent task show KENT-224 --project . --json |  · 2 more lines"; got != want {
		t.Fatalf("pending multiline shell = %q, want %q", got, want)
	}
}

func TestRenderPatchToolShowsStructuredPathAndCounts(t *testing.T) {
	row := toolRow("patch", clientui.ToolPresentationDefault, "ignored", false)
	row.Tool.ToolPresentation.PatchRender = &patchformat.RenderedPatch{
		Files:        []patchformat.RenderedFile{{RelPath: "cli/tui/model.go", Added: 2, Removed: 1}},
		SummaryLines: []patchformat.RenderedLine{{Kind: patchformat.RenderedLineKindFile, Text: "cli/tui/model.go -1 +2", FileIndex: 0}},
	}
	rendered := RenderCommittedRow(row, 80, "", ModeOngoing)
	if len(rendered.Lines) == 0 {
		t.Fatal("rendered no patch lines")
	}
	if got, want := rendered.Lines[0].Plain(), "⇄ cli/tui/model.go -1 +2"; got != want {
		t.Fatalf("patch line = %q, want %q", got, want)
	}
}

func TestDetailCompilerRendersStructuredPatchSyntaxAndDiffSemantics(t *testing.T) {
	renderedPatch := patchformat.Render(
		"*** Begin Patch\n*** Update File: example.go\n@@\n package main\n-var oldValue = \"old\"\n+var newValue = \"new\"\n*** End Patch\n",
		"/workspace",
	)
	row := toolRow("patch", clientui.ToolPresentationDefault, renderedPatch.DetailText(), false)
	row.Tool.ToolPresentation.PatchRender = &renderedPatch
	row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{Kind: clientui.ToolRenderKindDiff}

	presentation := NewDetailCompiler(100, "dark").Compile(row)
	added, addedOK := detailLineContaining(presentation.Expanded, "newValue")
	removed, removedOK := detailLineContaining(presentation.Expanded, "oldValue")
	context, contextOK := detailLineContaining(presentation.Expanded, "package main")
	hunk, hunkOK := detailLineContaining(presentation.Expanded, "@@")
	if !addedOK || !removedOK || !contextOK || !hunkOK {
		t.Fatalf(
			"structured patch lines missing: added=%t removed=%t context=%t hunk=%t lines=%q",
			addedOK,
			removedOK,
			contextOK,
			hunkOK,
			PlainLines(presentation.Expanded),
		)
	}
	if added.Background != LineBackgroundDiffAdded || removed.Background != LineBackgroundDiffRemoved {
		t.Fatalf("diff backgrounds = added %v removed %v", added.Background, removed.Background)
	}
	if context.Background != LineBackgroundDefault || hunk.Background != LineBackgroundDefault {
		t.Fatalf("neutral patch backgrounds = context %v hunk %v", context.Background, hunk.Background)
	}
	if !lineHasSemanticMarker(added, "+", StyleRoleToolSuccess) ||
		!lineHasSemanticMarker(removed, "-", StyleRoleToolError) {
		t.Fatalf("patch markers lack typed add/remove semantics: added=%+v removed=%+v", added.Spans, removed.Spans)
	}
	foregrounds := make(map[RGBColor]struct{})
	for _, span := range added.Spans {
		if span.Style.Kind == SpanStyleExplicitRGB {
			foregrounds[span.Style.Foreground] = struct{}{}
		}
	}
	if len(foregrounds) < 2 {
		t.Fatalf("added source line has %d Chroma foregrounds, want syntax-token styling: %+v", len(foregrounds), added.Spans)
	}
	for _, span := range hunk.Spans {
		if span.Style.Kind == SpanStyleExplicitRGB {
			t.Fatalf("hunk metadata was syntax-highlighted as source: %+v", hunk.Spans)
		}
	}
}

func TestDetailCompilerKeepsStructuredPatchInputAheadOfResult(t *testing.T) {
	renderedPatch := patchformat.Render(
		"*** Begin Patch\n*** Update File: example.go\n@@\n-oldValue := 1\n+newValue := 2\n*** End Patch\n",
		"/workspace",
	)
	tests := []struct {
		name       string
		isError    bool
		result     string
		symbolRole StyleRole
	}{
		{name: "success", result: "applied", symbolRole: StyleRoleToolSuccess},
		{name: "failure", isError: true, result: "failed", symbolRole: StyleRoleToolError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := toolRow("patch", clientui.ToolPresentationDefault, "unstructured result fallback", test.isError)
			row.Tool.ResultSummary = test.result
			row.Tool.ToolPresentation.PatchRender = &renderedPatch
			row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{Kind: clientui.ToolRenderKindDiff}

			expanded := NewDetailCompiler(80, "dark").Compile(row).Expanded
			if len(expanded) < 3 || expanded[0].LeadingSymbol == nil {
				t.Fatalf("expanded structured patch = %+v, want input lines followed by result", expanded)
			}
			if expanded[0].LeadingSymbol.Style.SemanticRole != test.symbolRole {
				t.Fatalf("leading symbol role = %v, want %v", expanded[0].LeadingSymbol.Style.SemanticRole, test.symbolRole)
			}
			addedIndex := -1
			for index, line := range expanded {
				if line.Background == LineBackgroundDiffAdded {
					addedIndex = index
					break
				}
			}
			resultIndex := len(expanded) - 1
			if addedIndex < 0 || addedIndex >= resultIndex {
				t.Fatalf("structured input index = %d, result index = %d: %+v", addedIndex, resultIndex, expanded)
			}
			resultLine := expanded[resultIndex]
			if len(resultLine.Spans) == 0 || resultLine.Spans[len(resultLine.Spans)-1].Text != test.result {
				t.Fatalf("last expanded line = %+v, want typed result %q", resultLine, test.result)
			}
			for _, span := range resultLine.Spans {
				if span.Style.Kind == SpanStyleExplicitRGB {
					t.Fatalf("result line inherited source syntax styling: %+v", resultLine.Spans)
				}
			}
		})
	}
}

func TestDetailCompilerKeepsRawPatchFallbackSemantic(t *testing.T) {
	raw := patchformat.Raw("unstructured patch input\nsecond raw line")
	row := toolRow("patch", clientui.ToolPresentationDefault, "unstructured result fallback", false)
	row.Tool.ToolPresentation.PatchRender = &raw
	row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{Kind: clientui.ToolRenderKindDiff}

	expanded := NewDetailCompiler(80, "dark").Compile(row).Expanded
	if got, want := PlainLines(expanded), []string{
		"⇄ unstructured patch input",
		"│ second raw line",
		"│ ",
		"└ unstructured result fallback",
	}; !slices.Equal(got, want) {
		t.Fatalf("raw patch lines = %q, want %q", got, want)
	}
	for index, line := range expanded {
		if line.Background != LineBackgroundDefault {
			t.Fatalf("raw line %d received diff background %v", index, line.Background)
		}
		for _, span := range line.Spans {
			if span.Style.Kind == SpanStyleExplicitRGB {
				t.Fatalf("raw line %d received source syntax style: %+v", index, line.Spans)
			}
		}
	}
}

func TestDetailCompilerWrapsStructuredPatchWithOneMarkerPerSourceLine(t *testing.T) {
	renderedPatch := patchformat.Render(
		"*** Begin Patch\n*** Update File: example.go\n+var extremelyLongIdentifier = \"a long source value\"\n*** End Patch\n",
		"/workspace",
	)
	row := toolRow("patch", clientui.ToolPresentationDefault, renderedPatch.DetailText(), false)
	row.Tool.ToolPresentation.PatchRender = &renderedPatch
	row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{Kind: clientui.ToolRenderKindDiff}

	const width = 14
	expanded := NewDetailCompiler(width, "dark").Compile(row).Expanded
	addedLines := make([]Line, 0, 4)
	for _, line := range expanded {
		if line.Background != LineBackgroundDiffAdded {
			continue
		}
		if got := lipgloss.Width(line.Plain()); got > width {
			t.Fatalf("wrapped patch line width = %d, want <= %d: %+v", got, width, line)
		}
		addedLines = append(addedLines, line)
	}
	if len(addedLines) < 2 {
		t.Fatalf("long added source rendered %d line(s), want wrapping: %+v", len(addedLines), expanded)
	}
	if !lineHasSemanticMarker(addedLines[0], "+", StyleRoleToolSuccess) {
		t.Fatalf("first added chunk lacks typed marker: %+v", addedLines[0].Spans)
	}
	for index, line := range addedLines[1:] {
		if lineHasSemanticMarker(line, "+", StyleRoleToolSuccess) {
			t.Fatalf("continuation chunk %d repeated added marker: %+v", index+1, line.Spans)
		}
	}
}

func TestDetailLineEqualityUsesExplicitStyleValues(t *testing.T) {
	color := RGBColor{Red: 0x12, Green: 0x34, Blue: 0x56}
	left := []Line{{Spans: []Span{ExplicitRGBSpan("source", color, SpanAttributeBold)}}}
	right := []Line{{Spans: []Span{ExplicitRGBSpan("source", color, SpanAttributeBold)}}}

	if !detailLinesEqual(left, right) {
		t.Fatalf("separately constructed equivalent explicit styles compare unequal: left=%+v right=%+v", left, right)
	}
	right[0].Spans[0].Style = right[0].Spans[0].Style.With(SpanAttributeItalic)
	if detailLinesEqual(left, right) {
		t.Fatalf("different explicit attributes compare equal: left=%+v right=%+v", left, right)
	}
}

func TestDetailCompilerSanitizesStructuredPatchSpans(t *testing.T) {
	renderedPatch := patchformat.RenderedPatch{
		Files: []patchformat.RenderedFile{{AbsPath: "/workspace/example.go", RelPath: "./example.go", Added: 1}},
		SummaryLines: []patchformat.RenderedLine{{
			Kind:      patchformat.RenderedLineKindFile,
			Text:      "./example.go +1",
			FileIndex: 0,
			Path:      "./example.go",
		}},
		DetailLines: []patchformat.RenderedLine{
			{
				Kind:      patchformat.RenderedLineKindFile,
				Text:      "/workspace/example.go\x1b[31m",
				FileIndex: 0,
				Path:      "/workspace/example.go\x1b[31m",
			},
			{
				Kind:      patchformat.RenderedLineKindDiff,
				Text:      "+var safeValue = \"safe\"\x1b[0m",
				FileIndex: 0,
			},
		},
	}
	row := toolRow("patch", clientui.ToolPresentationDefault, renderedPatch.DetailText(), false)
	row.Tool.ToolPresentation.PatchRender = &renderedPatch
	row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{Kind: clientui.ToolRenderKindDiff}

	expanded := NewDetailCompiler(80, "dark").Compile(row).Expanded
	for lineIndex, line := range expanded {
		for _, span := range line.Spans {
			for _, value := range span.Text {
				if unicode.IsControl(value) {
					t.Fatalf("structured patch line %d contains control rune %U: %+v", lineIndex, value, line.Spans)
				}
			}
		}
	}
}

func detailLineContaining(lines []Line, text string) (Line, bool) {
	for _, line := range lines {
		if strings.Contains(line.Plain(), text) {
			return line, true
		}
	}
	return Line{}, false
}

func lineHasSemanticMarker(line Line, marker string, role StyleRole) bool {
	for _, span := range line.Spans {
		if span.Text == marker &&
			span.Style.Kind == SpanStyleSemantic &&
			span.Style.SemanticRole == role {
			return true
		}
	}
	return false
}

func TestPendingPatchToolUsesStructuredPathAndCounts(t *testing.T) {
	line := RenderPendingTool(clientui.TranscriptToolStart{
		ToolCallID: "e5d6245b-579f-487c-87f7-cd57e21a0d38",
		ToolName:   "patch",
		ToolPresentation: &clientui.ToolCallMeta{
			ToolName: "patch",
			PatchRender: &patchformat.RenderedPatch{
				Files:        []patchformat.RenderedFile{{RelPath: "cli/tui/model.go", Added: 2, Removed: 1}},
				SummaryLines: []patchformat.RenderedLine{{Kind: patchformat.RenderedLineKindFile, Text: "cli/tui/model.go -1 +2", FileIndex: 0}},
			},
		},
	}, 80, "", "⢎ ")

	if got, want := line.Plain(), "⢎  cli/tui/model.go -1 +2"; got != want {
		t.Fatalf("pending patch line = %q, want %q", got, want)
	}
	var removedRole, addedRole StyleRole
	for _, span := range line.Spans {
		switch span.Text {
		case "-1":
			removedRole = span.Style.SemanticRole
		case "+2":
			addedRole = span.Style.SemanticRole
		}
	}
	if removedRole != StyleRoleToolError || addedRole != StyleRoleToolSuccess {
		t.Fatalf("pending patch count roles = removed %v added %v", removedRole, addedRole)
	}
}

func TestPatchFamilyToolsDoNotFallbackToToolName(t *testing.T) {
	for _, toolName := range []string{"patch", "edit", "replace", "write"} {
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
				ToolCallID:       "2d97d231-7765-471a-bf55-a4c17157af03",
				ToolName:         toolName,
				ToolPresentation: &clientui.ToolCallMeta{ToolName: toolName},
			}, 80, "", "")
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
	if meta.Text != "passed" || meta.Style.SemanticRole != StyleRoleNotice || !meta.Style.Has(SpanAttributeFaint) {
		t.Fatalf("result summary span = %+v, want faint notice metadata", meta)
	}
	gap := spans[len(spans)-2]
	if gap.Text != "  · " || gap.Style.SemanticRole != StyleRoleNotice || !gap.Style.Has(SpanAttributeFaint) {
		t.Fatalf("result summary separator span = %+v, want faint inline separator", gap)
	}
}

func TestCommittedMultilineShellStacksHiddenLineCountBeforeStatus(t *testing.T) {
	command := "body=$(kent task show KENT-224 --project . --json |\n  jq -r '.body')\necho \"$body\""
	row := toolRow("exec_command", clientui.ToolPresentationShell, command, false)
	exitCode := 7
	row.Tool.ToolPresentation.ShellExitCode = &exitCode

	ongoing := RenderCommittedRow(row, 120, "", ModeOngoing)
	if len(ongoing.Lines) != 1 {
		t.Fatalf("ongoing committed multiline shell lines = %+v, want one compact line", ongoing.Lines)
	}
	if got, want := ongoing.Lines[0].Plain(), "$ body=$(kent task show KENT-224 --project . --json |  · 2 more lines · exit 7"; got != want {
		t.Fatalf("ongoing committed multiline shell = %q, want %q", got, want)
	}

	detail := RenderCommittedRow(row, 120, "", ModeDetailCollapsed)
	if len(detail.Lines) != 1 {
		t.Fatalf("collapsed detail multiline shell lines = %+v, want one compact line", detail.Lines)
	}
	detailText := detail.Lines[0].Plain()
	if !strings.HasPrefix(detailText, "$ body=$(kent task show KENT-224 --project . --json |") ||
		!strings.HasSuffix(detailText, "2 more lines · exit 7") {
		t.Fatalf("collapsed detail multiline shell = %q, want command plus existing aligned continuation/status metadata", detailText)
	}
}

func TestNonZeroShellExitUsesErrorDollarAndReservedStatusSuffix(t *testing.T) {
	exitCode := 7
	row := toolRow("exec_command", clientui.ToolPresentationShell, "printf a-very-long-command", false)
	row.Tool.ToolPresentation.ShellExitCode = &exitCode

	rendered := RenderCommittedRow(row, 22, "", ModeOngoing)
	if len(rendered.Lines) != 1 {
		t.Fatalf("non-zero shell lines = %+v, want one compact line", rendered.Lines)
	}
	line := rendered.Lines[0]
	if line.LeadingSymbol == nil || line.LeadingSymbol.Text != "$" {
		t.Fatalf("non-zero shell symbol = %+v, want dollar", line.LeadingSymbol)
	}
	if line.LeadingSymbol.Style.SemanticRole != StyleRoleToolError {
		t.Fatalf("non-zero shell symbol style = %+v, want tool error", line.LeadingSymbol.Style)
	}
	if got := lipgloss.Width(line.Plain()); got > 22 {
		t.Fatalf("non-zero shell width = %d, want <= 22: %q", got, line.Plain())
	}
	spans := line.Spans
	if len(spans) < 3 || spans[len(spans)-2].Text != "  · " || spans[len(spans)-1].Text != "exit 7" {
		t.Fatalf("non-zero shell spans = %+v, want reserved exit suffix", spans)
	}
}

func TestZeroShellExitDoesNotRenderStatusSuffix(t *testing.T) {
	exitCode := 0
	row := toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false)
	row.Tool.ToolPresentation.ShellExitCode = &exitCode

	rendered := RenderCommittedRow(row, 80, "", ModeOngoing)
	line := rendered.Lines[0]
	if line.LeadingSymbol == nil || line.LeadingSymbol.Text != "$" {
		t.Fatalf("zero shell symbol = %+v, want dollar", line.LeadingSymbol)
	}
	if line.LeadingSymbol.Style.SemanticRole != StyleRoleToolSuccess {
		t.Fatalf("zero shell symbol style = %+v, want tool success", line.LeadingSymbol.Style)
	}
	for _, span := range line.Spans {
		if span.Text == "exit 0" {
			t.Fatalf("zero shell rendered status suffix: %+v", line.Spans)
		}
	}
}

func TestCollapsedToolRowsKeepInputPreviewAheadOfResultCondensedText(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "raw output text", false)
	row.Tool.CondensedText = "passed"
	row.Tool.ResultSummary = "passed"
	row.Tool.ToolPresentation.Command = "go test ./..."
	row.Tool.ToolPresentation.CompactText = "go test ./..."

	for _, mode := range []Mode{ModeOngoing, ModeOngoingCollapsed, ModeDetailCollapsed} {
		rendered := RenderCommittedRow(row, 80, "", mode)
		if len(rendered.Lines) == 0 {
			t.Fatalf("mode %v rendered no lines", mode)
		}
		spans := rendered.Lines[0].Spans
		if len(spans) < 4 {
			t.Fatalf("mode %v rendered no spans", mode)
		}
		command := Line{Spans: spans[1 : len(spans)-2]}.Plain()
		if command != "go test ./..." {
			t.Fatalf("mode %v command = %q, want typed input", mode, command)
		}
		meta := spans[len(spans)-1]
		if meta.Text != "passed" {
			t.Fatalf("mode %v result metadata = %q, want passed", mode, meta.Text)
		}
	}
}

func TestExpandedToolRowsKeepTypedInputAheadOfOutput(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "raw output text", false)
	row.Tool.ResultSummary = "passed"
	row.Tool.ToolPresentation.Command = "go test ./..."
	row.Tool.ToolPresentation.CompactText = "run tests"

	rendered := RenderCommittedRow(row, 80, "", ModeDetailExpanded)
	if got, want := PlainLines(rendered.Lines), []string{"$ go test ./...", "│ ", "│ raw output text", "└ passed"}; !slices.Equal(got, want) {
		t.Fatalf("expanded tool lines = %q, want %q", got, want)
	}
}

func TestExpandedPatchRowsKeepFullTypedInputAheadOfOutput(t *testing.T) {
	row := toolRow("patch", clientui.ToolPresentationDefault, "raw patch output", true)
	row.Tool.ResultSummary = "failed"
	row.Tool.ToolPresentation.PatchDetail = "cli/tui/model.go\n-old\n+new"

	rendered := RenderCommittedRow(row, 80, "", ModeDetailExpanded)
	if got, want := PlainLines(rendered.Lines), []string{"⇄ cli/tui/model.go", "│ -old", "│ +new", "│ ", "│ raw patch output", "└ failed"}; !slices.Equal(got, want) {
		t.Fatalf("expanded patch lines = %q, want %q", got, want)
	}
}

func TestToolErrorRowsKeepAuthoritativeInputFirstAndErrorClassification(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "raw failure output", true)
	row.Tool.CondensedText = "permission denied"
	row.Tool.ResultSummary = "permission denied"
	row.Tool.ToolPresentation.Command = "cat /root/secret"
	row.Tool.ToolPresentation.CompactText = "cat /root/secret"

	tests := []struct {
		mode        Mode
		wantSummary string
	}{
		{mode: ModeOngoing},
		{mode: ModeOngoingCollapsed},
		{mode: ModeDetailCollapsed, wantSummary: "permission denied"},
	}
	for _, test := range tests {
		rendered := RenderCommittedRow(row, 80, "", test.mode)
		if len(rendered.Lines) == 0 {
			t.Fatalf("mode %v rendered no lines", test.mode)
		}
		line := rendered.Lines[0]
		assertFailedToolClassification(t, test.mode, line)
		if test.wantSummary == "" {
			if got, want := line.Plain(), "$ cat /root/secret"; got != want {
				t.Fatalf("mode %v failed tool line = %q, want %q", test.mode, got, want)
			}
			continue
		}
		if got := line.Spans[len(line.Spans)-1].Text; got != test.wantSummary {
			t.Fatalf("mode %v failed tool summary = %q, want %q", test.mode, got, test.wantSummary)
		}
	}
}

func TestUserAssistantFullRowsRenderMarkdownContent(t *testing.T) {
	rows := []clientui.TranscriptCommittedRow{
		{Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: "**bold** and `code`"}},
		{Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: "# Heading\nplain"}},
	}

	expectedAssistant := map[Mode][]string{
		ModeOngoingFull:    {"❮ Heading", "  ", "  plain"},
		ModeDetailExpanded: {"❮ Heading", "│ ", "└ plain"},
	}
	for _, mode := range []Mode{ModeOngoingFull, ModeDetailExpanded} {
		user := RenderCommittedRow(rows[0], 80, "", mode)
		if got := PlainLines(user.Lines); !slices.Equal(got, []string{"❯ bold and code"}) {
			t.Fatalf("mode %v user markdown content = %q", mode, got)
		}

		assistant := RenderCommittedRow(rows[1], 80, "", mode)
		if got := PlainLines(assistant.Lines); !slices.Equal(got, expectedAssistant[mode]) {
			t.Fatalf("mode %v assistant markdown content = %q, want %q", mode, got, expectedAssistant[mode])
		}
	}
}

func TestUserAssistantMarkdownCodeUsesPrimaryFullStrengthRole(t *testing.T) {
	row := clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowAssistant,
		Assistant: &clientui.TranscriptAssistantRow{
			Text: "Use `inline()`.\n\n```go\nblock()\n```",
		},
	}

	rendered := RenderCommittedRow(row, 80, "", ModeDetailExpanded)
	codeSpans := make([]Span, 0, 2)
	for _, line := range rendered.Lines {
		for _, span := range line.Spans {
			if span.Text == "inline()" || span.Text == "block()" {
				codeSpans = append(codeSpans, span)
			}
		}
	}
	if len(codeSpans) != 2 {
		t.Fatalf("markdown code spans = %+v, want inline and block code", codeSpans)
	}
	for _, span := range codeSpans {
		if got := ColorRoleForStyle(span.Style.SemanticRole); got != ColorRolePrimary {
			t.Fatalf("markdown code color role = %v, want primary", got)
		}
		if span.Style.Has(SpanAttributeFaint) {
			t.Fatalf("markdown code span is faint: %+v", span)
		}
	}
}

func TestStableMarkdownCollapsesSoftBreaksAndPreservesHardBreaks(t *testing.T) {
	row := clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowAssistant,
		Assistant: &clientui.TranscriptAssistantRow{
			Text: "alpha\nbeta  \ngamma",
		},
	}

	rendered := RenderCommittedRow(row, 8, "", ModeOngoingStable)
	if got, want := PlainLines(rendered.Lines), []string{"❮ alpha beta", "  gamma"}; !slices.Equal(got, want) {
		t.Fatalf("stable markdown lines = %q, want %q", got, want)
	}
}

func TestStableMarkdownTableUsesLibraryWidthLayout(t *testing.T) {
	for _, width := range []int{8, 12, 24} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			row := clientui.TranscriptCommittedRow{
				Kind: clientui.TranscriptRowAssistant,
				Assistant: &clientui.TranscriptAssistantRow{
					Text: "| Name | Result |\n| --- | ---: |\n| alpha | very long value |",
				},
			}

			rendered := RenderCommittedRow(row, width, "", ModeOngoingStable)
			if len(rendered.Lines) < 3 {
				t.Fatalf("stable table lines = %q, want a multi-row table", PlainLines(rendered.Lines))
			}
			for index, line := range rendered.Lines {
				if got := lipgloss.Width(line.Plain()); got > width {
					t.Fatalf("stable table line %d width = %d, want <= %d: %q", index, got, width, line.Plain())
				}
			}
			if width < 24 {
				return
			}
			plain := strings.Join(PlainLines(rendered.Lines), "\n")
			for _, content := range []string{"Name", "Result", "alpha", "very", "long", "value"} {
				if !strings.Contains(plain, content) {
					t.Fatalf("stable table = %q, want content %q", plain, content)
				}
			}
		})
	}
}

func TestStableMarkdownTableUsesContinuousUnicodeSeparators(t *testing.T) {
	lines := RenderMarkdownStableLines(
		StyleRoleAssistant,
		"| Name | Result |\n| --- | ---: |\n| alpha | pass |",
		24,
	)
	plain := strings.Join(PlainLines(lines), "\n")
	for _, separator := range []string{"│", "─", "┼"} {
		if !strings.Contains(plain, separator) {
			t.Fatalf("stable table = %q, want Unicode separator %q", plain, separator)
		}
	}
	for _, asciiSeparator := range []string{"|", "-"} {
		if strings.Contains(plain, asciiSeparator) {
			t.Fatalf("stable table = %q, contains ASCII separator %q", plain, asciiSeparator)
		}
	}
}

func TestStableMarkdownOnlyWidthFormatsTableBlocks(t *testing.T) {
	lines := RenderMarkdownStableLines(
		StyleRoleAssistant,
		"before alpha beta gamma delta\n\n| Name | Result |\n| --- | ---: |\n| alpha | very long value |\n\nafter alpha beta gamma delta",
		18,
	)
	plain := PlainLines(lines)
	if len(plain) < 7 {
		t.Fatalf("stable mixed markdown lines = %q, want prose, table, and prose blocks", plain)
	}
	if plain[0] != "before alpha beta gamma delta" {
		t.Fatalf("leading prose = %q, want one width-independent logical line", plain[0])
	}
	if plain[len(plain)-1] != "after alpha beta gamma delta" {
		t.Fatalf("trailing prose = %q, want one width-independent logical line", plain[len(plain)-1])
	}
	for index, line := range plain[2 : len(plain)-2] {
		if got := lipgloss.Width(line); got > 18 {
			t.Fatalf("table line %d width = %d, want <= 18: %q", index, got, line)
		}
	}
}

func TestBackgroundExitStatusDoesNotOverridePrimaryNoticeSymbol(t *testing.T) {
	render := func(exitCode *int) Line {
		row := clientui.TranscriptCommittedRow{
			Kind: clientui.TranscriptRowNotice,
			Notice: &clientui.TranscriptNoticeRow{
				Severity: clientui.TranscriptNoticeInfo,
				Data: clientui.TranscriptNoticeData{
					MessageType:        clientui.MessageTypeBackgroundNotice,
					CompactLabel:       "background complete",
					BackgroundExitCode: exitCode,
				},
			},
		}
		return RenderCommittedRow(row, 80, "", ModeOngoingCollapsed).Lines[0]
	}

	successCode, failureCode := 0, 7
	success, failure, missing := render(&successCode), render(&failureCode), render(nil)
	if success.LeadingSymbol == nil || failure.LeadingSymbol == nil || missing.LeadingSymbol == nil {
		t.Fatalf("background status lines lack typed symbols: success=%+v failure=%+v missing=%+v", success, failure, missing)
	}
	if success.LeadingSymbol.Style != failure.LeadingSymbol.Style ||
		success.LeadingSymbol.Style != missing.LeadingSymbol.Style ||
		success.LeadingSymbol.Style.SemanticRole != StyleRoleNoticePrimary {
		t.Fatalf("exit status changed primary background symbol metadata: success=%+v failure=%+v missing=%+v", success.LeadingSymbol, failure.LeadingSymbol, missing.LeadingSymbol)
	}
	if !slices.Equal(success.Spans, failure.Spans) || !slices.Equal(success.Spans, missing.Spans) {
		t.Fatalf("exit status changed background body metadata: success=%+v failure=%+v missing=%+v", success, failure, missing)
	}
}

func TestPatchToolErrorKeepsAuthoritativeInputFirstAndErrorClassification(t *testing.T) {
	row := toolRow("patch", clientui.ToolPresentationDefault, "patch failure output", true)
	row.Tool.CondensedText = "patch failed"
	row.Tool.ResultSummary = "failed"
	row.Tool.ToolPresentation.PatchRender = &patchformat.RenderedPatch{
		Files:        []patchformat.RenderedFile{{RelPath: "cli/tui/model.go", Added: 2, Removed: 1}},
		SummaryLines: []patchformat.RenderedLine{{Kind: patchformat.RenderedLineKindFile, Text: "cli/tui/model.go -1 +2", FileIndex: 0}},
	}

	tests := []struct {
		mode        Mode
		wantSummary string
	}{
		{mode: ModeOngoing},
		{mode: ModeOngoingCollapsed},
		{mode: ModeDetailCollapsed, wantSummary: "failed"},
	}
	for _, test := range tests {
		rendered := RenderCommittedRow(row, 120, "", test.mode)
		if len(rendered.Lines) == 0 {
			t.Fatalf("mode %v rendered no lines", test.mode)
		}
		assertFailedToolClassification(t, test.mode, rendered.Lines[0])
		if got, want := rendered.Lines[0].LeadingSymbol.Text, "⇄"; got != want {
			t.Fatalf("mode %v patch error symbol = %q, want %q", test.mode, got, want)
		}
		line := rendered.Lines[0]
		if test.wantSummary == "" {
			if got, want := line.Plain(), "⇄ cli/tui/model.go -1 +2"; got != want {
				t.Fatalf("mode %v failed patch line = %q, want %q", test.mode, got, want)
			}
			continue
		}
		if got := line.Spans[len(line.Spans)-1].Text; got != test.wantSummary {
			t.Fatalf("mode %v failed patch summary = %q, want %q", test.mode, got, test.wantSummary)
		}
	}
}

func assertFailedToolClassification(t *testing.T, mode Mode, line Line) {
	t.Helper()
	if line.LeadingSymbol == nil {
		t.Fatalf("mode %v failed tool line has no typed symbol", mode)
	}
	symbol := *line.LeadingSymbol
	if symbol.Style.SemanticRole != StyleRoleToolError || symbol.Style.Has(SpanAttributeFaint) {
		t.Fatalf("mode %v failed tool classification = %+v, want full-strength tool error role", mode, symbol)
	}
}

func TestCollapsedToolResultSummarySanitizesMetadataBeforeInlineRender(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false)
	row.Tool.ResultSummary = "passed\x1b[31m"

	rendered := RenderCommittedRow(row, 80, "", ModeOngoing)
	spans := rendered.Lines[0].Spans
	meta := spans[len(spans)-1]
	if meta.Text != "passed[31m" || meta.Style.SemanticRole != StyleRoleNotice || !meta.Style.Has(SpanAttributeFaint) {
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
