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
		{name: "notice", row: clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowNotice, Notice: &clientui.TranscriptNoticeRow{Severity: clientui.TranscriptNoticeInfo, Data: clientui.TranscriptNoticeData{LegacyText: &legacy}}}, want: "ℹ neutral notice", wantRole: StyleRoleNotice, wantColor: ColorRoleSubdued},
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
		assertShellLineHasTypedSyntax(t, line)
	}
}

func TestPlainShellRenderHintSkipsSyntaxHighlighting(t *testing.T) {
	row := toolRow("write_stdin", clientui.ToolPresentationShell, "Polled session 1149 for 2s", false)
	row.Tool.ToolPresentation.RenderHint = &clientui.ToolRenderHint{Kind: clientui.ToolRenderKindPlain}
	for _, mode := range []Mode{ModeOngoing, ModeDetailCollapsed, ModeDetailExpanded} {
		rendered := RenderCommittedRow(row, 120, "", mode)
		if len(rendered.Lines) == 0 {
			t.Fatalf("mode %v rendered no plain shell row lines", mode)
		}
		assertShellLineIsPlainText(t, rendered.Lines[0])
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
	}, 120, "⢎ ")
	assertShellLineIsPlainText(t, pending)
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
			assertShellLineHasTypedSyntax(t, rendered.Lines[0])

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
			}, 120, "⢎ ")
			assertShellLineHasTypedSyntax(t, pending)
		})
	}
}

func assertShellLineIsPlainText(t *testing.T, line Line) {
	t.Helper()
	if len(line.Spans) < 2 {
		t.Fatalf("plain shell line has no body spans: %+v", line)
	}
	for _, span := range line.Spans[1:] {
		if span.Style.Kind != SpanStyleSemantic ||
			span.Style.SemanticRole != StyleRoleToolShell ||
			!span.Style.Has(SpanAttributeFaint) {
			t.Fatalf("plain shell body used syntax styling: %+v", line.Spans)
		}
	}
}

func assertShellLineHasTypedSyntax(t *testing.T, line Line) {
	t.Helper()
	foundSyntax := false
	for _, span := range line.Spans[1:] {
		if !span.Style.Has(SpanAttributeFaint) {
			t.Fatalf("shell syntax span is not faint: %+v", span)
		}
		switch span.Style.SemanticRole {
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
		switch span.Style.SemanticRole {
		case StyleRoleToolShellPrimary, StyleRoleToolShellSecondary, StyleRoleToolShellWarning, StyleRoleToolShellError:
			t.Fatalf("default tool row used shell syntax role span: %+v", span)
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

func TestResolveSpanStyleCarriesSemanticColorAndAttributes(t *testing.T) {
	span := SemanticSpan(
		"semantic",
		StyleRoleToolShellWarning,
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
	committed := RenderPendingTool(start, 80, "")
	pending := RenderPendingTool(start, 80, "⢎ ")

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
	if len(expanded) != len(raw.DetailLines) {
		t.Fatalf("raw patch rendered %d lines, want %d: %+v", len(expanded), len(raw.DetailLines), expanded)
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
		if got := line.Spans[len(line.Spans)-1].Text; got != raw.DetailLines[index].Text {
			t.Fatalf("raw line %d body = %q, want %q", index, got, raw.DetailLines[index].Text)
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
	}, 80, "⢎ ")

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
	if meta.Text != "passed" || meta.Style.SemanticRole != StyleRoleNotice || !meta.Style.Has(SpanAttributeFaint) {
		t.Fatalf("result summary span = %+v, want faint notice metadata", meta)
	}
	gap := spans[len(spans)-2]
	if gap.Text == "" || gap.Style.SemanticRole != StyleRoleToolShell {
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
		if len(spans) < 4 {
			t.Fatalf("mode %v rendered no spans", mode)
		}
		command := Line{Spans: spans[1 : len(spans)-2]}.Plain()
		if command != "go test ./..." {
			t.Fatalf("mode %v command = %q, want typed input", mode, command)
		}
		meta := spans[len(spans)-1]
		if meta.Text != "exit 0" {
			t.Fatalf("mode %v result metadata = %q, want exit 0", mode, meta.Text)
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
	if got, want := PlainLines(rendered.Lines), []string{"⇄ cli/tui/model.go", "│ -old", "│ +new", "└ failed"}; !slices.Equal(got, want) {
		t.Fatalf("expanded patch lines = %q, want %q", got, want)
	}
}

func TestToolErrorRowsKeepAuthoritativeInputFirstAndErrorClassification(t *testing.T) {
	row := toolRow("exec_command", clientui.ToolPresentationShell, "raw failure output", true)
	row.Tool.CondensedText = "permission denied"
	row.Tool.ResultSummary = "exit 1"
	row.Tool.ToolPresentation.Command = "cat /root/secret"
	row.Tool.ToolPresentation.CompactText = "cat /root/secret"

	tests := []struct {
		mode        Mode
		wantSummary string
	}{
		{mode: ModeOngoing},
		{mode: ModeOngoingCollapsed},
		{mode: ModeDetailCollapsed, wantSummary: "exit 1"},
	}
	for _, test := range tests {
		rendered := RenderCommittedRow(row, 80, "", test.mode)
		if len(rendered.Lines) == 0 {
			t.Fatalf("mode %v rendered no lines", test.mode)
		}
		line := rendered.Lines[0]
		assertFailedToolClassification(t, test.mode, line)
		if test.wantSummary == "" {
			if got, want := line.Plain(), "! cat /root/secret"; got != want {
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

func TestBackgroundExitStatusChangesOnlySymbolMetadata(t *testing.T) {
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
	if success.LeadingSymbol.Style == failure.LeadingSymbol.Style {
		t.Fatalf("typed success and failure statuses produced identical symbol metadata: success=%+v failure=%+v", success.LeadingSymbol, failure.LeadingSymbol)
	}
	if success.LeadingSymbol.Style != missing.LeadingSymbol.Style {
		t.Fatalf("missing legacy status diverged from non-error symbol metadata: success=%+v missing=%+v", success.LeadingSymbol, missing.LeadingSymbol)
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
				if span.Style.SemanticRole != StyleRoleNotice || !span.Style.Has(SpanAttributeFaint) {
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
