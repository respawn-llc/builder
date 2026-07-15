package tui

import "core/cli/tui/transcriptrender"

func RenderStyledMarkdownLines(
	source string,
	themeName string,
	width int,
	role transcriptrender.StyleRole,
	linkPresentation transcriptrender.MarkdownLinkPresentation,
) []string {
	lines := transcriptrender.RenderMarkdownLinesWithLinkPresentation(
		role,
		source,
		max(1, width),
		linkPresentation,
	)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, renderTranscriptSemanticLine(
			line,
			themeName,
			detailResolvedBackground{},
		))
	}
	return out
}
