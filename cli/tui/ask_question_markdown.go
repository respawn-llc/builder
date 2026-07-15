package tui

import (
	"strings"

	"core/cli/tui/transcriptrender"

	xansi "github.com/charmbracelet/x/ansi"
)

func RenderAskQuestionMarkdownLines(
	question string,
	themeName string,
	width int,
	linkPresentation transcriptrender.MarkdownLinkPresentation,
) []string {
	return trimAskQuestionMarkdownEdgeLines(RenderStyledMarkdownLines(
		question,
		themeName,
		width,
		transcriptrender.StyleRoleUser,
		linkPresentation,
	))
}

func trimAskQuestionMarkdownEdgeLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(xansi.Strip(lines[0])) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(xansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
