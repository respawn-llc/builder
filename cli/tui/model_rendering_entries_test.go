package tui

import (
	"fmt"
	"strings"
	"testing"

	"core/shared/transcript"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestResolveToolRenderHintPreservesShellDialectOnShellPreviewFallback(t *testing.T) {
	meta := &transcript.ToolCallMeta{
		RenderBehavior: transcript.ToolCallRenderBehaviorShell,
		RenderHint: &transcript.ToolRenderHint{
			Kind:         transcript.ToolRenderKindSource,
			Path:         "script.ps1",
			ResultOnly:   true,
			ShellDialect: transcript.ToolShellDialectPowerShell,
		},
	}

	hint, ok := resolveToolRenderHint("tool_shell_success", "Get-Content script.ps1", meta)
	if !ok {
		t.Fatal("expected shell preview fallback hint")
	}
	if hint.Kind != transcript.ToolRenderKindShell {
		t.Fatalf("expected shell fallback hint, got %+v", hint)
	}
	if hint.ShellDialect != transcript.ToolShellDialectPowerShell {
		t.Fatalf("expected powershell dialect preserved, got %+v", hint)
	}
}

func TestRenderInlineAskQuestionMarkdownLinesRendersMarkdownWithoutWrapping(t *testing.T) {
	question := strings.Join([]string{
		"Review **bold plan** and the [design note](https://example.com/really/long/path).",
		"",
		"```go",
		"fmt.Println(\"tail-marker\")",
		"```",
	}, "\n")

	lines := RenderInlineAskQuestionMarkdownLines(question, "dark", 24)
	plain := xansi.Strip(strings.Join(lines, "\n"))
	if strings.Contains(plain, "**bold plan**") || strings.Contains(plain, "```") {
		t.Fatalf("expected inline ask question markdown rendered, got %q", plain)
	}
	if !strings.Contains(plain, "Review bold plan") || !strings.Contains(plain, "fmt.Println(\"tail-marker\")") {
		t.Fatalf("expected inline ask question markdown content preserved, got %q", plain)
	}
}

func TestWrapTextForViewportDoesNotOverflowWhenPunctuationFollowsFullLine(t *testing.T) {
	for _, punctuation := range []string{".", ",", ";", "+", "-", "|"} {
		t.Run(punctuation, func(t *testing.T) {
			wrapped := wrapTextForViewport(strings.Repeat("a", 10)+punctuation, 10)

			for _, line := range splitLines(wrapped) {
				if width := lipgloss.Width(line); width > 10 {
					t.Fatalf("wrapped line width = %d, want <= 10; wrapped=%q", width, wrapped)
				}
			}
			if got, want := xansi.Strip(wrapped), strings.Repeat("a", 10)+"\n"+punctuation; got != want {
				t.Fatalf("wrapped text = %q, want %q", got, want)
			}
		})
	}
}

func TestFlattenEntryKeepsPrefixedOngoingLinesWithinViewportAtPunctuationBoundary(t *testing.T) {
	m := NewModel()
	m.viewportWidth = 12

	lines := m.flattenEntryPlain(RenderIntentAssistant, strings.Repeat("a", 10)+".")

	if got, want := len(lines), 2; got != want {
		t.Fatalf("line count = %d, want %d: %#v", got, want, lines)
	}
	for _, line := range lines {
		if width := lipgloss.Width(line); width > m.viewportWidth {
			t.Fatalf("flattened line width = %d, want <= %d; lines=%#v", width, m.viewportWidth, lines)
		}
	}
	if got, want := xansi.Strip(lines[0]), "❮ "+strings.Repeat("a", 10); got != want {
		t.Fatalf("first line = %q, want %q", got, want)
	}
	if got, want := xansi.Strip(lines[1]), "  ."; got != want {
		t.Fatalf("second line = %q, want %q", got, want)
	}
}

func TestFlattenMarkdownEntryKeepsPrefixedLinesWithinViewportAtPunctuationBoundary(t *testing.T) {
	m := NewModel()
	m.viewportWidth = 12

	lines := m.flattenEntryWithMetaAndSymbol(RenderIntentAssistant, strings.Repeat("a", 10)+".", false, nil, "")

	if got, want := len(lines), 2; got != want {
		t.Fatalf("line count = %d, want %d: %#v", got, want, lines)
	}
	for _, line := range lines {
		if width := lipgloss.Width(line); width > m.viewportWidth {
			t.Fatalf("flattened markdown line width = %d, want <= %d; lines=%#v", width, m.viewportWidth, lines)
		}
	}
	if got, want := xansi.Strip(lines[0]), "❮ "+strings.Repeat("a", 10); got != want {
		t.Fatalf("first line = %q, want %q", got, want)
	}
	if got, want := xansi.Strip(lines[1]), "  ."; got != want {
		t.Fatalf("second line = %q, want %q", got, want)
	}
}

func TestFlattenStyledMarkdownEntryKeepsPrefixedLinesWithinViewportAtPunctuationBoundary(t *testing.T) {
	m := NewModel()
	m.viewportWidth = 12

	lines := m.flattenEntryWithMetaAndSymbol(RenderIntentAssistant, "**"+strings.Repeat("a", 10)+".**", false, nil, "")

	if rendered := strings.Join(lines, "\n"); !strings.Contains(rendered, ";1m") {
		t.Fatalf("expected bold markdown ANSI styling (;1m), got %q", rendered)
	}
	if got, want := len(lines), 2; got != want {
		t.Fatalf("line count = %d, want %d: %#v", got, want, lines)
	}
	for _, line := range lines {
		if width := lipgloss.Width(line); width > m.viewportWidth {
			t.Fatalf("flattened styled markdown line width = %d, want <= %d; lines=%#v", width, m.viewportWidth, lines)
		}
	}
	if got, want := xansi.Strip(lines[0]), "❮ "+strings.Repeat("a", 10); got != want {
		t.Fatalf("first line = %q, want %q", got, want)
	}
	if got, want := xansi.Strip(lines[1]), "  ."; got != want {
		t.Fatalf("second line = %q, want %q", got, want)
	}
}

func TestRenderAssistantMarkdownProjectionHandlesStreamingMarkdownChunksAtWidths(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{
			name: "table",
			text: "| Name | Value |\n| --- | --- |\n| alpha | beta |\n",
			want: "alpha",
		},
		{
			name: "open code fence",
			text: "```go\nfunc main() {\n\tprintln(\"hello\")\n",
			want: "func main",
		},
		{
			name: "nested list",
			text: "- alpha item wraps with extra text\n  - beta nested item wraps too\n",
			want: "beta",
		},
		{
			name: "malformed bold",
			text: "**unterminated bold chunk",
			want: "unterminated",
		},
	}

	for _, width := range []int{20, 40, 80} {
		for _, tt := range cases {
			t.Run(fmt.Sprintf("%s/%d", tt.name, width), func(t *testing.T) {
				lines := RenderAssistantMarkdownProjection(tt.text, "dark", width)
				if len(lines) == 0 {
					t.Fatalf("expected rendered lines for %s at width %d", tt.name, width)
				}
				rendered := make([]string, 0, len(lines))
				for _, line := range lines {
					if got := lipgloss.Width(line.Text); got > width {
						t.Fatalf("rendered line width = %d, want <= %d; line=%q", got, width, line.Text)
					}
					rendered = append(rendered, line.Text)
				}
				if got := xansi.Strip(strings.Join(rendered, "\n")); !strings.Contains(got, tt.want) {
					t.Fatalf("expected rendered markdown to contain %q, got %q", tt.want, got)
				}
			})
		}
	}
}

func TestRenderAssistantMarkdownStreamingProjectionPreservesWhitespaceOnlySource(t *testing.T) {
	lines := RenderAssistantMarkdownStreamingProjection("   \n\t", "dark", 2)
	if len(lines) == 0 {
		t.Fatal("expected whitespace-only streaming source to render")
	}
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		rendered = append(rendered, line.Text)
		if width := lipgloss.Width(line.Text); width > 2 {
			t.Fatalf("whitespace streaming line width = %d, want <= 2: %#v", width, line.Text)
		}
	}
	if got := strings.Join(rendered, "\n"); !strings.Contains(got, " ") || !strings.Contains(got, "\t") {
		t.Fatalf("whitespace streaming projection skipped source whitespace: %#v", rendered)
	}
}

func TestOngoingViewDoesNotDuplicatePunctuationBoundaryLine(t *testing.T) {
	m := NewModel(WithPreviewLines(4))
	m = updateModel(t, m, SetViewportSizeMsg{Lines: 4, Width: 12})
	m = updateModel(t, m, AppendTranscriptMsg{Role: "assistant", Text: strings.Repeat("a", 10) + "."})

	lines := splitLines(xansi.Strip(m.View()))
	if countLines(lines, "❮ "+strings.Repeat("a", 10)) != 1 {
		t.Fatalf("expected exactly one full-width content line, got %#v", lines)
	}
	if countLines(lines, "  .") != 1 {
		t.Fatalf("expected exactly one punctuation continuation line, got %#v", lines)
	}
}

func TestWrapTextForViewportPreservesANSIBoundaryContent(t *testing.T) {
	styled := "\x1b[1m" + strings.Repeat("a", 10) + ".\x1b[0m"

	wrapped := wrapTextForViewport(styled, 10)

	if !strings.Contains(wrapped, "\x1b[1m") || !strings.Contains(wrapped, "\x1b[0m") {
		t.Fatalf("expected ANSI styling to be preserved, got %q", wrapped)
	}
	for _, line := range splitLines(wrapped) {
		if width := lipgloss.Width(line); width > 10 {
			t.Fatalf("styled wrapped line width = %d, want <= 10; wrapped=%q", width, wrapped)
		}
	}
	if got, want := xansi.Strip(wrapped), strings.Repeat("a", 10)+"\n."; got != want {
		t.Fatalf("styled wrapped text = %q, want %q", got, want)
	}
}

func countLines(lines []string, target string) int {
	count := 0
	for _, line := range lines {
		if line == target {
			count++
		}
	}
	return count
}
