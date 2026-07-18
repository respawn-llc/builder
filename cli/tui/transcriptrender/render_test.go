package transcriptrender

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/transcript"
	patchformat "core/shared/transcript/patchformat"

	"github.com/charmbracelet/lipgloss"
)

func TestShellToolRowsUseTypedSyntaxHighlighting(t *testing.T) {
	row := toolRow("exec_command", transcript.ToolPresentationShell, "sed -n '1,10p' cli/tui/model.go", false)
	row.Tool.Presentation.RenderHint = &transcript.ToolRenderHint{
		Kind:         transcript.ToolRenderKindShell,
		ShellDialect: transcript.ToolShellDialectPosix,
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
	row := toolRow("write_stdin", transcript.ToolPresentationShell, "process completed output", false)
	row.Tool.Presentation.Command = "Polled session 1149 for 2s"
	row.Tool.Presentation.CompactText = row.Tool.Presentation.Command
	row.Tool.Presentation.RenderHint = &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindPlain}
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
		Presentation: &transcript.ToolCallMeta{
			ToolName:     "write_stdin",
			Presentation: transcript.ToolPresentationShell,
			Command:      "Polled session 1149 for 2s",
			CompactText:  "Polled session 1149 for 2s",
			RenderHint:   &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindPlain},
		},
	}, 120, "", "⢎ ")
	assertShellLineIsPlainText(t, pending, true)
}

func TestSourceReadUsesCommandPreviewAndSourceResultDetail(t *testing.T) {
	row := toolRow("exec_command", transcript.ToolPresentationShell, "package transcriptrender\n\nfunc example() {}", false)
	row.Tool.Presentation.Command = "sed -n '1,20p' cli/tui/transcriptrender/render.go"
	row.Tool.Presentation.CompactText = row.Tool.Presentation.Command
	row.Tool.Presentation.RenderHint = &transcript.ToolRenderHint{
		Kind:       transcript.ToolRenderKindSource,
		Path:       "cli/tui/transcriptrender/render.go",
		ResultOnly: true,
	}

	presentation := RenderDetailPresentation(row, 120, "dark")
	if got := presentation.Collapsed[0].Plain(); !strings.Contains(got, row.Tool.Presentation.Command) {
		t.Fatalf("collapsed source read = %q, want typed command preview", got)
	}
	expanded := strings.Join(PlainLines(presentation.Expanded), "\n")
	if !strings.Contains(expanded, "package transcriptrender") ||
		!strings.Contains(expanded, "func example() {}") {
		t.Fatalf("expanded source read = %q, want source result", expanded)
	}
	if !strings.Contains(expanded, row.Tool.Presentation.Command) {
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

func TestShellRowsUseRenderHintDialectsAtRenderBoundary(t *testing.T) {
	cases := []struct {
		name    string
		dialect transcript.ToolShellDialect
		command string
	}{
		{name: "posix", dialect: transcript.ToolShellDialectPosix, command: "printf 'ok' # comment"},
		{name: "powershell", dialect: transcript.ToolShellDialectPowerShell, command: "Write-Host \"ok\" # comment"},
		{name: "windows command", dialect: transcript.ToolShellDialectWindowsCommand, command: "rem comment"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			row := toolRow("exec_command", transcript.ToolPresentationShell, tt.command, false)
			row.Tool.Presentation.RenderHint = &transcript.ToolRenderHint{
				Kind:         transcript.ToolRenderKindShell,
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
				Presentation: &transcript.ToolCallMeta{
					ToolName:     "exec_command",
					Presentation: transcript.ToolPresentationShell,
					Command:      tt.command,
					CompactText:  tt.command,
					RenderHint: &transcript.ToolRenderHint{
						Kind:         transcript.ToolRenderKindShell,
						ShellDialect: tt.dialect,
					},
				},
			}, 120, "", "⢎ ")
			assertShellLineHasTypedSyntax(t, pending, true)
		})
	}
}

func TestMovedToBackgroundShellRowKeepsMovedToBackgroundSuffixAtNarrowWidth(t *testing.T) {
	row := toolRow("exec_command", transcript.ToolPresentationShell, "sleep 20; printf completed", false)
	row.Tool.Presentation.MovedToBackground = true

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
	rendered := RenderCommittedRow(toolRow("custom_tool", transcript.ToolPresentationDefault, "sed -n '1,10p' cli/tui/model.go", false), 120, "", ModeOngoing)
	if len(rendered.Lines) == 0 {
		t.Fatal("rendered no custom tool row lines")
	}
	for _, span := range rendered.Lines[0].Spans {
		if span.Style.Kind == SpanStyleExplicitRGB {
			t.Fatalf("default tool row used Chroma syntax span: %+v", span)
		}
	}
}

func TestWorktreeNoticeRendersTypedClientContext(t *testing.T) {
	branch := "feature/client-rendered-context"
	effectiveCWD := "/tmp/worktree/client-rendered-context"
	messageType := clientui.TranscriptMessageWorktreeMode
	row := clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:      clientui.TranscriptNoticeRuntimeDiagnostic,
			Severity:    clientui.TranscriptNoticeInfo,
			MessageType: &messageType,
			Worktree: &clientui.TranscriptWorktreeContext{
				Branch:        &branch,
				WorktreePath:  "/tmp/worktree",
				WorkspaceRoot: "/tmp/workspace",
				EffectiveCwd:  effectiveCWD,
			},
			Diagnostic: &clientui.TranscriptDiagnostic{
				Code:   "worktree_transition",
				Detail: "model-only instructions",
			},
		},
	}

	for _, mode := range []Mode{ModeOngoing, ModeDetailCollapsed, ModeDetailExpanded} {
		rendered := RenderCommittedRow(row, 120, "", mode)
		if len(rendered.Lines) != 1 {
			t.Fatalf("mode %v rendered lines = %+v, want one client-composed notice", mode, rendered.Lines)
		}
		text := rendered.Lines[0].Plain()
		if !strings.Contains(text, branch) || !strings.Contains(text, effectiveCWD) {
			t.Fatalf("mode %v rendered worktree facts = %q, want branch and effective cwd", mode, text)
		}
		if strings.Contains(text, row.Notice.Diagnostic.Detail) {
			t.Fatalf("mode %v rendered model instructions instead of client presentation: %q", mode, text)
		}
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
	condensedText := "compact answer"
	presentation := RenderDetailPresentation(
		clientui.TranscriptCommittedRow{
			Integrity: transcript.RowIntegrityValid,
			Kind:      clientui.TranscriptRowAssistant,
			Assistant: &clientui.TranscriptAssistantRow{
				Text:          "full first line\nfull second line",
				CondensedText: &condensedText,
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
			Reason:     clientui.TranscriptNoticeLegacyUntypedNotice,
			LegacyText: &legacyNotice,
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

func TestPendingMultilineShellShowsExplicitHiddenLineCount(t *testing.T) {
	command := "body=$(kent task show KENT-224 --project . --json |\n  jq -r '.body')\necho \"$body\""
	line := RenderPendingTool(clientui.TranscriptToolStart{
		ToolCallID: "db2b88a0-3a53-442e-a47b-84bb38ba7f77",
		ToolName:   "exec_command",
		Presentation: &transcript.ToolCallMeta{
			ToolName:     "exec_command",
			Presentation: transcript.ToolPresentationShell,
			Command:      command,
			CompactText:  command,
		},
	}, 120, "", "⢎ ")

	if got, want := line.Plain(), "⢎  body=$(kent task show KENT-224 --project . --json |  · 2 more lines"; got != want {
		t.Fatalf("pending multiline shell = %q, want %q", got, want)
	}
}

func TestDetailCompilerRendersStructuredPatchSyntaxAndDiffSemantics(t *testing.T) {
	renderedPatch := patchformat.Render(
		"*** Begin Patch\n*** Update File: example.go\n@@\n package main\n-var oldValue = \"old\"\n+var newValue = \"new\"\n*** End Patch\n",
		"/workspace",
	)
	row := toolRow("patch", transcript.ToolPresentationDefault, renderedPatch.DetailText(), false)
	row.Tool.Presentation.PatchRender = &renderedPatch
	row.Tool.Presentation.RenderHint = &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindDiff}

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
			row := toolRow("patch", transcript.ToolPresentationDefault, "unstructured result fallback", test.isError)
			result := test.result
			row.Tool.ResultSummary = &result
			row.Tool.Presentation.PatchRender = &renderedPatch
			row.Tool.Presentation.RenderHint = &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindDiff}

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
	row := toolRow("patch", transcript.ToolPresentationDefault, "unstructured result fallback", false)
	row.Tool.Presentation.PatchRender = &raw
	row.Tool.Presentation.RenderHint = &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindDiff}

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
	row := toolRow("patch", transcript.ToolPresentationDefault, renderedPatch.DetailText(), false)
	row.Tool.Presentation.PatchRender = &renderedPatch
	row.Tool.Presentation.RenderHint = &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindDiff}

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
	row := toolRow("patch", transcript.ToolPresentationDefault, renderedPatch.DetailText(), false)
	row.Tool.Presentation.PatchRender = &renderedPatch
	row.Tool.Presentation.RenderHint = &transcript.ToolRenderHint{Kind: transcript.ToolRenderKindDiff}

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
		Presentation: &transcript.ToolCallMeta{
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

func TestWholeFileDeletionBadgeUsesTypedRemovedCountInPendingOngoingAndDetail(t *testing.T) {
	rendered := patchformat.Render(
		"*** Begin Patch\n*** Delete File: target.txt\n*** End Patch\n",
		"/workspace",
	)
	pending := RenderPendingTool(clientui.TranscriptToolStart{
		ToolCallID: "f0b891b5-f353-4c5c-b70f-3f907f2a7807",
		ToolName:   "patch",
		Presentation: &transcript.ToolCallMeta{
			ToolName:    "patch",
			PatchRender: &rendered,
		},
	}, 80, "", "⢎ ")
	if semanticSpanCount(pending, StyleRoleToolError) != 0 {
		t.Fatalf("pending deletion rendered removal badge: %+v", pending.Spans)
	}

	id := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
	finalized, mismatch := patchformat.ApplyWholeFileDeletionFacts(rendered, []patchformat.WholeFileDeletionFact{{
		PhysicalGroup: patchformat.WholeFileDeletionGroupID{FirstOperation: id},
		OperationIDs:  []patchformat.WholeFileDeletionOperationID{id},
		Removed:       0,
	}})
	if mismatch != nil {
		t.Fatalf("finalize empty-file deletion: %+v", mismatch)
	}
	removed := patchformat.RemovedLineCount(finalized.Files[0])
	if removed == nil || *removed != 0 {
		t.Fatalf("shared removed projection = %v, want present zero", removed)
	}
	row := toolRow("patch", transcript.ToolPresentationDefault, finalized.DetailText(), false)
	row.Tool.Presentation.PatchRender = &finalized

	ongoing := RenderCommittedRow(row, 80, "", ModeOngoing)
	if len(ongoing.Lines) != 1 ||
		semanticSpanCount(ongoing.Lines[0], StyleRoleToolError) != 1 ||
		!hasSemanticSpanText(ongoing.Lines[0], StyleRoleToolError, fmt.Sprintf("-%d", *removed)) {
		t.Fatalf("ongoing deletion badge structure = %+v", ongoing.Lines)
	}
	t.Logf("empty deletion ongoing: %s", ongoing.Lines[0].Plain())
	detail := RenderCommittedRow(row, 80, "", ModeDetailCollapsed)
	if len(detail.Lines) != 1 ||
		semanticSpanCount(detail.Lines[0], StyleRoleToolError) != 1 ||
		!hasSemanticSpanText(detail.Lines[0], StyleRoleToolError, fmt.Sprintf("-%d", *removed)) {
		t.Fatalf("detail deletion badge structure = %+v", detail.Lines)
	}
	t.Logf("empty deletion detail: %s", detail.Lines[0].Plain())
}

func TestWholeFileDeletionBadgeDeduplicatesPhysicalGroupPerFileAndProjectsAliases(t *testing.T) {
	rendered := patchformat.Format(patchformat.Document{Hunks: []any{
		patchformat.DeleteFile{Path: "target.txt"},
		patchformat.DeleteFile{Path: "target.txt"},
		patchformat.DeleteFile{Path: "alias.txt"},
	}}, "/workspace")
	first := patchformat.WholeFileDeletionOperationID{HunkOrdinal: 0}
	finalized, mismatch := patchformat.ApplyWholeFileDeletionFacts(rendered, []patchformat.WholeFileDeletionFact{{
		PhysicalGroup: patchformat.WholeFileDeletionGroupID{FirstOperation: first},
		OperationIDs: []patchformat.WholeFileDeletionOperationID{
			first,
			{HunkOrdinal: 1},
			{HunkOrdinal: 2},
		},
		Removed: 6,
	}})
	if mismatch != nil {
		t.Fatalf("finalize alias deletion: %+v", mismatch)
	}
	row := toolRow("patch", transcript.ToolPresentationDefault, finalized.DetailText(), false)
	row.Tool.Presentation.PatchRender = &finalized

	ongoing := RenderCommittedRow(row, 80, "", ModeOngoing)
	if len(ongoing.Lines) != 2 {
		t.Fatalf("ongoing alias rows = %d, want two", len(ongoing.Lines))
	}
	for index, line := range ongoing.Lines {
		if semanticSpanCount(line, StyleRoleToolError) != 1 ||
			!hasSemanticSpanText(line, StyleRoleToolError, fmt.Sprintf("-%d", 6)) {
			t.Fatalf("alias row %d badge structure = %+v", index, line.Spans)
		}
	}
	t.Logf("populated deletion ongoing: %s", ongoing.Lines[0].Plain())
}

func semanticSpanCount(line Line, role StyleRole) int {
	count := 0
	for _, span := range line.Spans {
		if span.Style.Kind == SpanStyleSemantic && span.Style.SemanticRole == role {
			count++
		}
	}
	return count
}

func hasSemanticSpanText(line Line, role StyleRole, text string) bool {
	for _, span := range line.Spans {
		if span.Text == text &&
			span.Style.Kind == SpanStyleSemantic &&
			span.Style.SemanticRole == role {
			return true
		}
	}
	return false
}

func TestCommittedMultilineShellStacksHiddenLineCountBeforeStatus(t *testing.T) {
	command := "body=$(kent task show KENT-224 --project . --json |\n  jq -r '.body')\necho \"$body\""
	row := toolRow("exec_command", transcript.ToolPresentationShell, command, false)
	exitCode := 7
	row.Tool.Presentation.ShellExitCode = &exitCode

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
	row := toolRow("exec_command", transcript.ToolPresentationShell, "printf a-very-long-command", false)
	row.Tool.Presentation.ShellExitCode = &exitCode

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
	row := toolRow("exec_command", transcript.ToolPresentationShell, "go test ./...", false)
	row.Tool.Presentation.ShellExitCode = &exitCode

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
	row := toolRow("exec_command", transcript.ToolPresentationShell, "raw output text", false)
	resultSummary := "passed"
	row.Tool.CondensedText = &resultSummary
	row.Tool.ResultSummary = &resultSummary
	row.Tool.Presentation.Command = "go test ./..."
	row.Tool.Presentation.CompactText = "go test ./..."

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

func TestToolErrorRowsKeepAuthoritativeInputFirstAndErrorClassification(t *testing.T) {
	row := toolRow("exec_command", transcript.ToolPresentationShell, "raw failure output", true)
	resultSummary := "permission denied"
	row.Tool.CondensedText = &resultSummary
	row.Tool.ResultSummary = &resultSummary
	row.Tool.Presentation.Command = "cat /root/secret"
	row.Tool.Presentation.CompactText = "cat /root/secret"

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

func TestStableMarkdownCollapsesSoftBreaksAndPreservesHardBreaks(t *testing.T) {
	for _, tt := range []struct {
		name string
		row  clientui.TranscriptCommittedRow
		want []string
	}{
		{
			name: "user",
			row: clientui.TranscriptCommittedRow{
				Kind: clientui.TranscriptRowUser,
				User: &clientui.TranscriptUserRow{Text: "alpha\nbeta  \ngamma"},
			},
			want: []string{"❯ alpha beta", "gamma"},
		},
		{
			name: "assistant",
			row: clientui.TranscriptCommittedRow{
				Kind:      clientui.TranscriptRowAssistant,
				Assistant: &clientui.TranscriptAssistantRow{Text: "alpha\nbeta  \ngamma"},
			},
			want: []string{"❮ alpha beta", "gamma"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rendered := RenderCommittedRow(tt.row, 8, "", ModeOngoingStable)
			if got := PlainLines(rendered.Lines); !slices.Equal(got, tt.want) {
				t.Fatalf("stable markdown lines = %q, want %q", got, tt.want)
			}
		})
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

func TestMarkdownAutolinksRenderDestinationOnceInProseAndTables(t *testing.T) {
	const target = "https://example.com/autolink"
	for _, source := range []string{
		"<" + target + ">",
		"| Link |\n| --- |\n| <" + target + "> |",
	} {
		for _, presentation := range []MarkdownLinkPresentation{
			MarkdownLinkLabelOnly,
			MarkdownLinkLabelAndDestination,
		} {
			lines := RenderMarkdownLinesWithLinkPresentation(
				StyleRoleAssistant,
				source,
				80,
				presentation,
			)
			var linked strings.Builder
			for _, line := range lines {
				for _, span := range line.Spans {
					if span.Hyperlink != nil && span.Hyperlink.URL == target {
						linked.WriteString(span.Text)
					}
				}
			}
			if got := linked.String(); got != target {
				t.Fatalf(
					"linked autolink text = %q, want %q for presentation %d and source %q",
					got,
					target,
					presentation,
					source,
				)
			}
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

func TestNestedMarkdownTablesPreserveEnclosingContent(t *testing.T) {
	const table = "| Name | Result |\n| --- | --- |\n| alpha | pass |"
	for _, test := range []struct {
		name            string
		source          string
		wantPrefixFaint bool
	}{
		{
			name:            "blockquote",
			source:          "> before\n>\n> " + strings.ReplaceAll(table, "\n", "\n> ") + "\n>\n> after",
			wantPrefixFaint: true,
		},
		{
			name:   "list",
			source: "- before\n\n  " + strings.ReplaceAll(table, "\n", "\n  ") + "\n\n  after",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			lines := RenderMarkdownStableLines(StyleRoleAssistant, test.source, 30)
			if len(lines) != 5 {
				t.Fatalf("nested Markdown line count = %d, want 5: %+v", len(lines), lines)
			}
			prefixWidth := spansWidth(lines[0].Spans[:1])
			for index, line := range lines {
				if len(line.Spans) != 2 {
					t.Fatalf("nested Markdown line %d spans = %+v, want container prefix plus content", index, line.Spans)
				}
				prefix := line.Spans[0]
				if got := spansWidth([]Span{prefix}); got == 0 || got != prefixWidth {
					t.Fatalf("nested Markdown line %d prefix width = %d, want structural width %d", index, got, prefixWidth)
				}
				if index == 0 && strings.TrimSpace(prefix.Text) == "" {
					t.Fatalf("nested Markdown first-line prefix = %+v, want visible container marker", prefix)
				}
				if index > 0 && strings.TrimSpace(prefix.Text) != "" {
					t.Fatalf("nested Markdown continuation prefix = %+v, want structural indentation", prefix)
				}
				if prefix.Style.Has(SpanAttributeFaint) != test.wantPrefixFaint {
					t.Fatalf("nested Markdown line %d prefix faint = %v, want %v", index, prefix.Style.Has(SpanAttributeFaint), test.wantPrefixFaint)
				}
				if width := spansWidth(line.Spans); width > 30 {
					t.Fatalf("nested Markdown line %d width = %d, want <= 30", index, width)
				}
			}
			if got := lines[0].Spans[1].Text; got != "before" {
				t.Fatalf("nested leading content = %q, want authored prose", got)
			}
			if got := lines[4].Spans[1].Text; got != "after" {
				t.Fatalf("nested trailing content = %q, want authored prose", got)
			}
			if got, want := strings.Fields(lines[1].Spans[1].Text), []string{"Name", "│", "Result"}; !slices.Equal(got, want) {
				t.Fatalf("nested table header tokens = %q, want %q", got, want)
			}
			if got, want := strings.Fields(lines[3].Spans[1].Text), []string{"alpha", "│", "pass"}; !slices.Equal(got, want) {
				t.Fatalf("nested table row tokens = %q, want %q", got, want)
			}
			separator := []rune(strings.TrimSpace(lines[2].Spans[1].Text))
			centerCount := 0
			for _, character := range separator {
				switch character {
				case '─':
				case '┼':
					centerCount++
				default:
					t.Fatalf("nested table separator contains unexpected rune %U", character)
				}
			}
			if centerCount != 1 {
				t.Fatalf("nested table separator center count = %d, want 1", centerCount)
			}
		})
	}
}

func TestMarkdownRendererPreservesGFMSemantics(t *testing.T) {
	t.Run("linkify", func(t *testing.T) {
		const target = "https://example.com/linkified"
		lines := RenderMarkdownLines(StyleRoleAssistant, "Visit "+target, 80)
		var linked strings.Builder
		for _, line := range lines {
			for _, span := range line.Spans {
				if span.Hyperlink != nil && span.Hyperlink.URL == target {
					linked.WriteString(span.Text)
				}
			}
		}
		if got := linked.String(); got != target {
			t.Fatalf("linked GFM URL text = %q, want %q", got, target)
		}
	})

	t.Run("strikethrough", func(t *testing.T) {
		lines := RenderMarkdownLines(StyleRoleAssistant, "before ~~removed~~ after", 80)
		for _, line := range lines {
			for _, span := range line.Spans {
				if span.Text == "removed" && span.Style.Has(SpanAttributeStrikethrough) {
					return
				}
			}
		}
		t.Fatalf("strikethrough Markdown lost semantic attribute: %+v", lines)
	})

	t.Run("task list", func(t *testing.T) {
		lines := RenderMarkdownLines(StyleRoleAssistant, "- [x] done\n- [ ] todo", 80)
		if len(lines) != 2 {
			t.Fatalf("task-list line count = %d, want 2: %+v", len(lines), lines)
		}
		for index, wantLabel := range []string{"done", "todo"} {
			if len(lines[index].Spans) != 1 {
				t.Fatalf("task-list line %d spans = %+v, want one task-owned span", index, lines[index].Spans)
			}
			fields := strings.Fields(lines[index].Spans[0].Text)
			if len(fields) < 2 || fields[len(fields)-1] != wantLabel {
				t.Fatalf("task-list line %d fields = %q, want authored label %q", index, fields, wantLabel)
			}
		}
		if lines[0].Spans[0].Text == lines[1].Spans[0].Text {
			t.Fatalf("checked and unchecked task states rendered identically: %+v", lines)
		}
	})

	t.Run("definition list", func(t *testing.T) {
		lines := RenderMarkdownLines(StyleRoleAssistant, "Term\n: Definition", 80)
		if len(lines) != 2 || len(lines[0].Spans) != 1 || len(lines[1].Spans) != 1 {
			t.Fatalf("definition-list typed lines = %+v, want term and indented description", lines)
		}
		if got := lines[0].Spans[0].Text; got != "Term" {
			t.Fatalf("definition term = %q, want authored term", got)
		}
		description := []rune(lines[1].Spans[0].Text)
		contentStart := 0
		for contentStart < len(description) && unicode.IsSpace(description[contentStart]) {
			contentStart++
		}
		if contentStart == 0 {
			t.Fatalf("definition description = %q, want structural indentation", string(description))
		}
		if got := string(description[contentStart:]); got != "Definition" {
			t.Fatalf("definition description = %q, want authored description", got)
		}
	})
}

func TestBackgroundExitStatusDoesNotOverridePrimaryNoticeSymbol(t *testing.T) {
	render := func(exitCode *int) Line {
		messageType := clientui.TranscriptMessageBackgroundNotice
		compactLabel := "background complete"
		row := clientui.TranscriptCommittedRow{
			Kind: clientui.TranscriptRowNotice,
			Notice: &clientui.TranscriptNoticeRow{
				Reason:       clientui.TranscriptNoticeRuntimeDiagnostic,
				Severity:     clientui.TranscriptNoticeInfo,
				MessageType:  &messageType,
				CompactLabel: &compactLabel,
				Diagnostic: &clientui.TranscriptDiagnostic{
					Code:   "background_completion",
					Detail: compactLabel,
				},
				Background: &clientui.TranscriptBackgroundNoticeIdentity{
					ActivityID: runtimeids.NewBackgroundActivityID(),
					ProcessID:  "background-process",
					ExitCode:   exitCode,
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
	row := toolRow("exec_command", transcript.ToolPresentationShell, "go test ./...", false)
	resultSummary := "passed\x1b[31m"
	row.Tool.ResultSummary = &resultSummary

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
			CacheWarning: &clientui.TranscriptCacheWarning{
				Scope:           string(warning.Scope),
				Reason:          string(warning.Reason),
				LostInputTokens: warning.LostInputTokens,
				Visibility:      transcript.EntryVisibilityOngoing,
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
	compactLabel := "AGENTS.md file content"
	rendered := RenderCommittedRow(clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityDetail,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:       clientui.TranscriptNoticeRuntimeDiagnostic,
			Severity:     clientui.TranscriptNoticeInfo,
			CompactLabel: &compactLabel,
			Diagnostic: &clientui.TranscriptDiagnostic{
				Code:   "agents_context",
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
			Reason:       clientui.TranscriptNoticeRuntimeDiagnostic,
			Severity:     clientui.TranscriptNoticeInfo,
			CompactLabel: &compactLabel,
			Diagnostic: &clientui.TranscriptDiagnostic{
				Code:   "agents_context",
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
		return clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowUser, User: &clientui.TranscriptUserRow{Text: text, CondensedText: &condensed}}
	}, "❯"}, {"assistant", func(text, condensed string) clientui.TranscriptCommittedRow {
		return clientui.TranscriptCommittedRow{Kind: clientui.TranscriptRowAssistant, Assistant: &clientui.TranscriptAssistantRow{Text: text, CondensedText: &condensed}}
	}, "❮"}} {
		t.Run(kind.name, func(t *testing.T) {
			full := "first body line\nsecond body line\nthird body line"
			condensed := "server compact summary"

			for _, mode := range []Mode{ModeOngoing, ModeDetailCollapsed} {
				collapsed := RenderCommittedRow(kind.row(full, condensed), 80, "", mode)
				if len(collapsed.Lines) != 1 {
					t.Fatalf("mode %v lines = %d, want 1 compact line", mode, len(collapsed.Lines))
				}
				if got, want := collapsed.Lines[0].Plain(), kind.symbol+" "+condensed; got != want {
					t.Fatalf("mode %v line = %q, want compact %q", mode, got, want)
				}
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
	messageType := clientui.TranscriptMessageReviewerFeedback
	compactLabel := "Reviewer made 1 suggestion."
	row := clientui.TranscriptCommittedRow{
		Visibility: clientui.EntryVisibilityOngoingCollapsed,
		Kind:       clientui.TranscriptRowNotice,
		Notice: &clientui.TranscriptNoticeRow{
			Reason:       clientui.TranscriptNoticeRuntimeDiagnostic,
			Severity:     clientui.TranscriptNoticeInfo,
			MessageType:  &messageType,
			CompactLabel: &compactLabel,
			Diagnostic: &clientui.TranscriptDiagnostic{
				Code:   clientui.TranscriptDiagnosticCode(transcript.EntryRoleReviewerSuggestions),
				Detail: compactLabel,
			},
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

func toolRow(name string, presentation transcript.ToolPresentationKind, text string, isError bool) clientui.TranscriptCommittedRow {
	return clientui.TranscriptCommittedRow{
		Kind: clientui.TranscriptRowTool,
		Tool: &clientui.TranscriptToolRow{
			ToolName: name,
			Text:     text,
			IsError:  isError,
			Presentation: &transcript.ToolCallMeta{
				ToolName:     name,
				Presentation: presentation,
				Command:      text,
				CompactText:  text,
			},
		},
	}
}
