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
		{name: "shell", row: toolRow("exec_command", clientui.ToolPresentationShell, "go test ./...", false), want: "$ go test ./...", wantRole: StyleRoleToolShell, wantColor: ColorRoleForeground},
		{name: "ask_question", row: toolRow("ask_question", clientui.ToolPresentationAskQuestion, "Pick one", false), want: "? Pick one", wantRole: StyleRoleToolQuestion, wantColor: ColorRoleForeground},
		{name: "web_search", row: toolRow("web_search", clientui.ToolPresentationDefault, "query", false), want: "@ query", wantRole: StyleRoleToolWebSearch, wantColor: ColorRoleForeground},
		{name: "custom", row: toolRow("custom_tool", clientui.ToolPresentationDefault, "custom preview", false), want: "• custom preview", wantRole: StyleRoleTool, wantColor: ColorRoleForeground},
		{name: "workflow_completion", row: toolRow("workflow_completion", clientui.ToolPresentationDefault, "workflow done", false), want: "• workflow done", wantRole: StyleRoleTool, wantColor: ColorRoleForeground},
		{name: "view_image", row: toolRow("view_image", clientui.ToolPresentationDefault, "image.png", false), want: "• image.png", wantRole: StyleRoleTool, wantColor: ColorRoleForeground},
		{name: "trigger_handoff", row: toolRow("trigger_handoff", clientui.ToolPresentationDefault, "handoff", false), want: "• handoff", wantRole: StyleRoleTool, wantColor: ColorRoleForeground},
		{name: "tool_error", row: toolRow("exec_command", clientui.ToolPresentationShell, "failed", true), want: "! failed", wantRole: StyleRoleToolError, wantColor: ColorRoleError},
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
			if got := ColorRoleForStyle(tt.wantRole); got != tt.wantColor {
				t.Fatalf("style role color = %v, want %v", got, tt.wantColor)
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
		name string
		row  clientui.TranscriptCommittedRow
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
			if !rendered.Lines[0].Spans[0].Faint || !rendered.Lines[0].Spans[2].Faint {
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
	if len(line.Spans) < 3 || line.Spans[0].Role != StyleRoleToolShell || !line.Spans[0].Faint || !line.Spans[2].Faint {
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
