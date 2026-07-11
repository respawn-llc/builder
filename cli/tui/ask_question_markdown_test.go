package tui

import (
	"errors"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestResolveAskQuestionMarkdownLinesFallsBackForRenderErrorAndEmptyOutput(t *testing.T) {
	source := "**lit**\nnext"
	tests := []struct {
		name    string
		outcome askQuestionMarkdownRenderOutcome
	}{
		{name: "render error", outcome: askQuestionMarkdownRenderOutcome{rendered: "partial", err: errors.New("render failed")}},
		{name: "zero visible output", outcome: askQuestionMarkdownRenderOutcome{rendered: "\n\t\n"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := resolveAskQuestionMarkdownLines(source, 8, tt.outcome)
			assertPlainAskQuestionSource(t, lines, source, 8)
		})
	}
}

func TestResolveAskQuestionMarkdownLinesUsesVisibleRenderedOutput(t *testing.T) {
	lines := resolveAskQuestionMarkdownLines("**literal**", 16, askQuestionMarkdownRenderOutcome{rendered: "rendered\n"})
	if got := strings.Join(lines, "\n"); got != "rendered" {
		t.Fatalf("resolved rendered rows=%q want %q", got, "rendered")
	}
}

func TestPlainAskQuestionMarkdownFallbackPreservesSourceAndBoundsRows(t *testing.T) {
	source := "\x1b[31m**literal**\x1b[0m" +
		xansi.SetHyperlink("https://example.com/unsafe", "id=unsafe") +
		xansi.ResetHyperlink() +
		"\a\r\b\nlong-source-line"
	lines := renderPlainAskQuestionMarkdownSource(source, 6)
	if len(lines) < 3 {
		t.Fatalf("fallback rows=%q want wrapped explicit source lines", lines)
	}
	plain := strings.Join(lines, "\n")
	for _, r := range plain {
		if r != '\n' && unicode.IsControl(r) {
			t.Fatalf("fallback retained terminal control character %q in %q", r, plain)
		}
	}
	if strings.Contains(plain, "https://example.com/unsafe") {
		t.Fatalf("fallback retained OSC hyperlink metadata: %q", plain)
	}
	if !strings.Contains(strings.ReplaceAll(plain, "\n", ""), "**literal**") {
		t.Fatalf("fallback lost literal markdown punctuation: %q", plain)
	}
	for _, line := range lines {
		if width := lipgloss.Width(line); width > 6 {
			t.Fatalf("fallback row width=%d want <=6: %q", width, line)
		}
	}
}

func TestPlainAskQuestionMarkdownFallbackKeepsEmptySourceRow(t *testing.T) {
	lines := renderPlainAskQuestionMarkdownSource("", 8)
	if len(lines) != 1 || lines[0] != "" {
		t.Fatalf("empty fallback rows=%q want one empty row", lines)
	}
}

func assertPlainAskQuestionSource(t *testing.T, lines []string, source string, width int) {
	t.Helper()
	plainSource := xansi.Strip(source)
	plainRows := strings.Join(lines, "\n")
	if !strings.Contains(plainRows, plainSource) {
		t.Fatalf("fallback rows=%q missing literal source=%q", plainRows, plainSource)
	}
	for _, line := range lines {
		if rowWidth := lipgloss.Width(line); rowWidth > width {
			t.Fatalf("fallback row width=%d want <=%d: %q", rowWidth, width, line)
		}
	}
}
