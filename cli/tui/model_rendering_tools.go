package tui

// Deprecated emergency path: this file survives tui-redesign demolition only
// as the temporary non-native transcript tool renderer for this branch. Do not extend.

import (
	"core/shared/toolspec"
	"core/shared/transcript"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func ongoingDividerGroup(role RenderIntent) string {
	normalized := normalizeOngoingDividerRole(role)
	if normalized == RenderIntentToolQuestion || normalized == RenderIntentToolQuestionError {
		return "question"
	}
	if normalized.IsToolHeadline() {
		return "tool"
	}
	return normalized.String()
}

func normalizeOngoingDividerRole(role RenderIntent) RenderIntent {
	if role == RenderIntentAssistantCommentary {
		return RenderIntentAssistant
	}
	return role
}

func compactOngoingShellPreviewText(command string) string {
	normalized := strings.ReplaceAll(command, "\r\n", "\n")
	if !strings.Contains(normalized, "\n") {
		return command
	}
	for _, line := range strings.Split(normalized, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		return trimmed + "\n…"
	}
	return "…"
}

func compactReviewerStatusForOngoing(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	for _, line := range strings.Split(trimmed, "\n") {
		candidate := strings.TrimSpace(line)
		if candidate != "" {
			return candidate
		}
	}
	return trimmed
}

func askQuestionDisplay(meta *transcript.ToolCallMeta, text string) (string, []string, int) {
	_ = text
	question := ""
	suggestions := make([]string, 0)
	recommendedOptionIndex := 0
	if meta != nil {
		question = normalizeAskQuestionQuestion(meta.Question)
		if question == "" {
			question = normalizeAskQuestionQuestion(meta.Command)
		}
		recommendedOptionIndex = meta.RecommendedOptionIndex
		for _, suggestion := range meta.Suggestions {
			trimmed := strings.TrimSpace(suggestion)
			if trimmed == "" {
				continue
			}
			suggestions = append(suggestions, trimmed)
		}
	}
	if question == "" {
		question = "ask_question"
	}
	return question, suggestions, recommendedOptionIndex
}

// RenderAskQuestionMarkdownLines renders question markdown for committed
// ask_question transcript entries using theme and width, returning wrapped
// display lines.
func RenderAskQuestionMarkdownLines(question string, theme string, width int) []string {
	renderer := transcriptProjectionRenderer(theme, width, 0)
	return renderer.renderAskQuestionMarkdownLines(RenderIntentToolQuestion, question, width)
}

// RenderInlineAskQuestionMarkdownLines renders question markdown for compact
// inline previews using theme and width, returning display lines without
// transcript wrapping. It clamps width and falls back to raw line splitting.
func RenderInlineAskQuestionMarkdownLines(question string, theme string, width int) []string {
	renderer := transcriptProjectionRenderer(theme, width, 0)
	if width < 1 {
		width = 1
	}
	if renderer.md != nil {
		if rendered, err := renderer.md.renderWithRenderer(RenderIntentToolQuestion, question, width, "plain", renderer.md.getRenderer); err == nil {
			lines := trimZeroWidthEdgeLines(splitLines(rendered))
			if len(lines) > 0 {
				return lines
			}
		}
	}
	lines := splitLines(question)
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func trimZeroWidthEdgeLines(lines []string) []string {
	for len(lines) > 0 && lipgloss.Width(lines[0]) == 0 {
		lines = lines[1:]
	}
	for len(lines) > 0 && lipgloss.Width(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func normalizeAskQuestionQuestion(question string) string {
	trimmed := strings.TrimSpace(question)
	if trimmed == "" {
		return ""
	}
	if strings.EqualFold(trimmed, "ask_question") {
		return ""
	}
	return trimmed
}

func (m Model) flattenAskQuestionEntryWithSymbol(role RenderIntent, question string, suggestions []string, recommendedOptionIndex int, answer string, includeSuggestions bool, symbolOverride string) []string {
	renderWidth := m.entryRenderWidth(role, symbolOverride)
	continuationPrefix := strings.Repeat(" ", max(0, lipgloss.Width(m.entryPrefix(role, symbolOverride))))

	type askQuestionLine struct {
		text string
		kind string
	}

	lines := make([]askQuestionLine, 0, len(suggestions)+4)
	question = strings.TrimSpace(question)
	if question == "" {
		question = "ask question"
	}
	for _, line := range m.renderAskQuestionMarkdownLines(role, question, renderWidth) {
		lines = append(lines, askQuestionLine{text: line, kind: "question"})
	}
	if includeSuggestions {
		for index, suggestion := range suggestions {
			suggestion = strings.TrimSpace(suggestion)
			if suggestion == "" {
				continue
			}
			recommended := recommendedOptionIndex == index+1
			wrapped := splitLines(wrapTextForViewport(suggestion, max(1, renderWidth-2)))
			for idx, line := range wrapped {
				if idx == 0 {
					kind := "suggestion"
					if recommended {
						kind = "recommended_suggestion"
					}
					lines = append(lines, askQuestionLine{text: "- " + line, kind: kind})
					continue
				}
				kind := "suggestion"
				if recommended {
					kind = "recommended_suggestion"
				}
				lines = append(lines, askQuestionLine{text: "  " + line, kind: kind})
			}
		}
	}
	answer = strings.TrimSpace(answer)
	if answer != "" {
		for _, line := range splitLines(wrapTextForViewport(answer, renderWidth)) {
			lines = append(lines, askQuestionLine{text: line, kind: "answer"})
		}
	}
	if len(lines) == 0 {
		lines = append(lines, askQuestionLine{text: "", kind: "question"})
	}

	prefix := m.entryPrefix(role, symbolOverride)
	out := make([]string, 0, len(lines))
	for idx, line := range lines {
		display := line.text
		switch line.kind {
		case "suggestion":
			display = m.palette().preview.Faint(true).Render(display)
		case "recommended_suggestion":
			display = m.palette().model.Render(display)
		case "answer":
			if role == RenderIntentToolQuestionError {
				display = applyANSIStyleIntents(display, m.ansiIntentPalette(), ErrorForeground)
			} else {
				display = m.palette().user.Render(display)
			}
		}
		if idx == 0 {
			if prefix == "" {
				out = append(out, display)
				continue
			}
			out = append(out, prefix+display)
			continue
		}
		if strings.TrimSpace(display) == "" {
			out = append(out, "")
			continue
		}
		out = append(out, continuationPrefix+display)
	}
	return out
}

func (m Model) renderAskQuestionMarkdownLines(role RenderIntent, question string, renderWidth int) []string {
	if renderWidth < 1 {
		renderWidth = 1
	}
	if m.md != nil {
		if rendered, err := m.md.renderWithRenderer(role, question, renderWidth, "wrapped", m.md.getWrappedRenderer); err == nil {
			lines := splitLines(hardWrapOverflowingRenderedLines(rendered, renderWidth))
			if len(lines) > 0 {
				return lines
			}
		}
	}
	lines := splitLines(wrapTextForViewport(question, renderWidth))
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func toolCallDisplayText(meta *transcript.ToolCallMeta, text string) string {
	command := strings.TrimSpace(text)
	inlineMeta := ""
	if meta != nil && strings.TrimSpace(meta.Command) != "" {
		command = strings.TrimSpace(meta.Command)
	}
	if meta != nil && strings.TrimSpace(meta.PatchDetail) != "" && strings.TrimSpace(command) == "" {
		command = strings.TrimSpace(meta.PatchDetail)
	}
	if command == "" {
		command = transcript.CompactToolCallText(meta, text)
	}
	if meta != nil && meta.UsesShellRendering() && meta.UserInitiated {
		command = "User ran: " + command
	}
	if meta != nil {
		inlineMeta = strings.TrimSpace(meta.InlineMeta)
		if inlineMeta == "" {
			inlineMeta = strings.TrimSpace(meta.TimeoutLabel)
		}
	}
	if inlineMeta == "" {
		return command
	}
	return command + transcript.InlineMetaSeparator + inlineMeta
}

func isPatchToolCall(meta *transcript.ToolCallMeta) bool {
	if meta == nil {
		return false
	}
	switch strings.TrimSpace(meta.ToolName) {
	case "patch", "edit":
		return true
	default:
		return meta.HasPatchDetail()
	}
}

func isPatchToolBlock(role RenderIntent, meta *transcript.ToolCallMeta) bool {
	if meta != nil && (isPatchToolCall(meta) || meta.PatchRender != nil) {
		return true
	}
	switch role {
	case RenderIntentToolPatch, RenderIntentToolPatchSuccess, RenderIntentToolPatchError:
		return true
	default:
		return false
	}
}

func isAskQuestionToolCall(meta *transcript.ToolCallMeta) bool {
	return meta != nil && meta.UsesAskQuestionRendering()
}

func isWebSearchToolCall(meta *transcript.ToolCallMeta) bool {
	return meta != nil && strings.TrimSpace(meta.ToolName) == string(toolspec.ToolWebSearch)
}

func (m Model) renderPatchSummaryContent(meta *transcript.ToolCallMeta) (transcriptRenderContent, bool) {
	if meta == nil || meta.PatchRender == nil {
		return transcriptRenderContent{}, false
	}
	lines := make([]transcriptRenderLine, 0, len(meta.PatchRender.SummaryLines))
	for _, summary := range meta.PatchRender.SummaryLines {
		if summary.Kind != "file" || summary.FileIndex < 0 || summary.FileIndex >= len(meta.PatchRender.Files) {
			continue
		}
		file := meta.PatchRender.Files[summary.FileIndex]
		path := strings.TrimSpace(file.RelPath)
		if path == "" {
			path = strings.TrimSpace(file.AbsPath)
		}
		if path == "" {
			continue
		}
		plain := patchSummaryPlainLine(path, file.Added, file.Removed)
		lines = append(lines, transcriptRenderLine{
			Text: plain,
			PatchSummary: &transcriptPatchSummaryLine{
				Path:    path,
				Added:   file.Added,
				Removed: file.Removed,
			},
		})
	}
	if len(lines) == 0 {
		return transcriptRenderContent{}, false
	}
	return transcriptRenderContent{Lines: lines, WrapMode: transcriptRenderWrapModePreserved}, true
}

func patchSummaryPlainLine(path string, added int, removed int) string {
	parts := []string{path}
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("-%d", removed))
	}
	if added > 0 {
		parts = append(parts, fmt.Sprintf("+%d", added))
	}
	return strings.Join(parts, " ")
}

func (m Model) renderPatchSummaryLine(line transcriptPatchSummaryLine) string {
	parts := []string{line.Path}
	if line.Removed > 0 {
		parts = append(parts, m.palette().toolError.Render(fmt.Sprintf("-%d", line.Removed)))
	}
	if line.Added > 0 {
		parts = append(parts, m.palette().toolSuccess.Render(fmt.Sprintf("+%d", line.Added)))
	}
	return strings.Join(parts, " ")
}

func (m Model) renderToolHeadline(line string, width int) string {
	command, meta := transcript.SplitInlineMeta(line)
	if meta == "" {
		return command
	}
	metaText := m.palette().preview.Faint(true).Render(meta)
	if command == "" {
		return metaText
	}
	space := width - lipgloss.Width(command) - lipgloss.Width(metaText)
	if space < 1 {
		space = 1
	}
	return command + strings.Repeat(" ", space) + metaText
}

func (m Model) tintToolDiffLine(line, kind string) string {
	if strings.TrimSpace(line) == "" {
		return line
	}
	if width := m.viewportWidth; width > 0 {
		lineWidth := lipgloss.Width(line)
		if lineWidth < width {
			line += strings.Repeat(" ", width-lineWidth)
		}
	}
	addBg, removeBg := m.diffLineBackgroundEscapes()
	if kind == "add" {
		return applyBackgroundTint(line, addBg)
	}
	if kind == "remove" {
		return applyBackgroundTint(line, removeBg)
	}
	return line
}

func (m Model) diffLineBackgroundEscapes() (string, string) {
	p := m.palette()
	return bgEscape(p.diffAddBackground), bgEscape(p.diffRemoveBackground)
}

func (m Model) styleToolLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return line
	}
	if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
		return m.palette().toolSuccess.Render("+") + line[1:]
	}
	if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
		return m.palette().toolError.Render("-") + line[1:]
	}
	return line
}
