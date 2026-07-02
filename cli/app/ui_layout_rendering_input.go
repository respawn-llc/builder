package app

import (
	"core/cli/tui"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type wrappedAskPromptLine struct {
	Text      string
	Line      askPromptLine
	HasCursor bool
	CursorCol int
}

func (l uiViewLayout) renderInputLines(width int, style uiStyles) []string {
	m := l.model
	inputState := m.inputModeState()
	if inputState.Mode == uiInputModeProcessList {
		return []string{padRight("", width)}
	}
	if inputState.Mode == uiInputModeWorktree {
		return []string{padRight("", width)}
	}
	if inputState.Mode == uiInputModeGoal {
		return []string{padRight("", width)}
	}
	if inputState.Mode == uiInputModeRollbackSelection {
		return nil
	}
	if width < 1 {
		return []string{padRight("", width)}
	}
	if inputState.ShowsAskInput {
		return l.renderAskInputLines(width, style)
	}

	return renderFramedEditableInputLines(width, inputContentLineLimit(l.effectiveHeight()), l.mainInputRenderSpec(), style.input, l.inputBorderStyle())
}

func (l uiViewLayout) renderAskInputLines(width int, style uiStyles) []string {
	if width < 1 {
		return []string{padRight("", width)}
	}
	wrapped := l.visibleAskPromptLines(width)
	selectedStyle := lipgloss.NewStyle().Foreground(uiPalette(l.model.theme).primary).Bold(true)
	recommendedStyle := lipgloss.NewStyle().Foreground(uiPalette(l.model.theme).secondary)
	recommendedNoteStyle := style.meta.Faint(true)
	rendered := make([]string, 0, len(wrapped))
	for _, line := range wrapped {
		switch {
		case line.Line.Kind == askPromptLineKindHint:
			rendered = append(rendered, style.meta.Render(padANSIRight(line.Text, width)))
		case line.Line.Disabled:
			rendered = append(rendered, style.inputDisabled.Render(padANSIRight(line.Text, width)))
		case line.Line.Selected:
			rendered = append(rendered, selectedStyle.Render(padANSIRight(line.Text, width)))
		case line.Line.Recommended:
			rendered = append(rendered, renderRecommendedAskLine(line.Text, line.Line.MutedSuffix, width, recommendedStyle, recommendedNoteStyle))
		default:
			rendered = append(rendered, style.input.Render(padANSIRight(line.Text, width)))
		}
	}
	return renderFramedLines(width, rendered, l.inputBorderStyle())
}

func renderRecommendedAskLine(text string, mutedSuffix string, width int, recommendedStyle lipgloss.Style, noteStyle lipgloss.Style) string {
	body := text
	suffix := ""
	if mutedSuffix != "" && strings.HasSuffix(body, mutedSuffix) {
		body = strings.TrimSuffix(body, mutedSuffix)
		suffix = mutedSuffix
	}
	if strings.HasPrefix(body, "★ ") {
		body = strings.TrimPrefix(body, "★ ")
		body = "★ " + body
	}
	rendered := recommendedStyle.Render(body)
	if suffix != "" {
		rendered += noteStyle.Render(suffix)
	}
	return padANSIRight(rendered, width)
}

func (l uiViewLayout) mainInputPrefix() string {
	return "› "
}

func (l uiViewLayout) mainInputRenderSpec() uiEditableInputRenderSpec {
	return uiEditableInputRenderSpec{
		Prefix:       l.mainInputPrefix(),
		Text:         l.model.input,
		CursorIndex:  l.model.inputCursor,
		RenderCursor: l.shouldRenderSoftCursor(),
	}
}

func (l uiViewLayout) inputPaneCursor(width int) uiInputFieldCursor {
	if !l.shouldUseRealTerminalCursor() || width < 1 {
		return uiInputFieldCursor{}
	}
	inputState := l.model.inputModeState()
	if inputState.Mode == uiInputModeProcessList || inputState.Mode == uiInputModeWorktree || inputState.Mode == uiInputModeRollbackSelection {
		return uiInputFieldCursor{}
	}
	if inputState.ShowsAskInput {
		return l.askInputPaneCursor(width)
	}
	if !inputState.ShowsMainInput {
		return uiInputFieldCursor{}
	}
	spec := l.mainInputRenderSpec()
	spec.RenderCursor = true
	visible, cursorLine, cursorCol := visibleEditableInputViewport(width, inputContentLineLimit(l.effectiveHeight()), spec)
	if cursorLine < 0 {
		return uiInputFieldCursor{}
	}
	cursorLine, cursorCol = normalizeInputFieldCursorCell(cursorLine, cursorCol, width, len(visible))
	return uiInputFieldCursor{Visible: true, Row: cursorLine + 1, Col: cursorCol}
}

func (l uiViewLayout) wrappedAskPromptLines(width int) ([]wrappedAskPromptLine, int) {
	promptLines := l.model.askController().renderPromptLines()
	if len(promptLines) == 0 {
		promptLines = []askPromptLine{{Text: "", Kind: askPromptLineKindQuestion}}
	}
	promptLines = renderMarkdownAskQuestionPromptLines(promptLines, l.model.theme, width)
	out := make([]wrappedAskPromptLine, 0, len(promptLines)*2)
	cursorLineIndex := -1
	for _, line := range promptLines {
		parts := wrapLine(line.Text, width)
		lineCursor := -1
		lineCursorCol := 0
		if line.Kind == askPromptLineKindQuestion {
			parts = []string{line.Text}
		}
		if line.Kind == askPromptLineKindInput {
			spec := uiEditableInputRenderSpec{Prefix: line.InputPrefix, Text: line.InputText, CursorIndex: line.InputCursor, RenderCursor: line.ShowsCursor}
			renderedInput := renderEditableInputField(width, 0, spec)
			parts = renderedInput.Lines
			if renderedInput.Cursor.Visible {
				cursorLine, cursorCol := renderedInput.Cursor.Row, renderedInput.Cursor.Col
				if cursorLine >= 0 && cursorLine < len(parts) {
					if !l.shouldUseRealTerminalCursor() {
						parts[cursorLine] = overlayCursorOnLine(parts[cursorLine], cursorCol, width, lipgloss.NewStyle().Reverse(true))
					}
					lineCursor = cursorLine
					lineCursorCol = cursorCol
					cursorLineIndex = len(out) + cursorLine
				}
			}
		}
		if len(parts) == 0 {
			parts = []string{""}
		}
		for partIndex, part := range parts {
			wrappedLine := line
			if wrappedLine.MutedSuffix != "" && !strings.HasSuffix(part, wrappedLine.MutedSuffix) {
				wrappedLine.MutedSuffix = ""
			}
			out = append(out, wrappedAskPromptLine{Text: part, Line: wrappedLine, HasCursor: partIndex == lineCursor, CursorCol: lineCursorCol})
		}
	}
	if len(out) == 0 {
		return []wrappedAskPromptLine{{Text: "", Line: askPromptLine{Kind: askPromptLineKindQuestion}}}, -1
	}
	return out, cursorLineIndex
}

func renderMarkdownAskQuestionPromptLines(lines []askPromptLine, theme string, width int) []askPromptLine {
	if len(lines) == 0 {
		return nil
	}
	out := make([]askPromptLine, 0, len(lines))
	for idx := 0; idx < len(lines); {
		line := lines[idx]
		if line.Kind != askPromptLineKindQuestion {
			out = append(out, line)
			idx++
			continue
		}
		start := idx
		parts := make([]string, 0, 4)
		for idx < len(lines) && lines[idx].Kind == askPromptLineKindQuestion {
			parts = append(parts, lines[idx].Text)
			idx++
		}
		rendered := tui.RenderAskQuestionMarkdownLines(strings.Join(parts, "\n"), theme, width)
		if len(rendered) == 0 {
			out = append(out, lines[start:idx]...)
			continue
		}
		for _, text := range rendered {
			out = append(out, askPromptLine{Text: text, Kind: askPromptLineKindQuestion})
		}
	}
	return out
}

func (l uiViewLayout) visibleAskPromptLines(width int) []wrappedAskPromptLine {
	wrapped, _ := l.visibleAskPromptLinesWithCursor(width)
	return wrapped
}

func (l uiViewLayout) visibleAskPromptLinesWithCursor(width int) ([]wrappedAskPromptLine, int) {
	wrapped, cursorLine := l.wrappedAskPromptLines(width)
	maxContentLines := inputContentLineLimit(l.effectiveHeight())
	visibleStart := 0
	if len(wrapped) > maxContentLines {
		visibleStart = visibleWrappedLineStart(len(wrapped), maxContentLines, cursorLine, cursorLine >= 0)
		wrapped = wrapped[visibleStart : visibleStart+maxContentLines]
	}
	visibleCursorLine := cursorLine - visibleStart
	if visibleCursorLine < 0 || visibleCursorLine >= len(wrapped) {
		visibleCursorLine = -1
	}
	return wrapped, visibleCursorLine
}

func (l uiViewLayout) askInputPaneCursor(width int) uiInputFieldCursor {
	lines, cursorLine := l.visibleAskPromptLinesWithCursor(width)
	if cursorLine < 0 {
		return uiInputFieldCursor{}
	}
	cursorCol := 0
	if cursorLine < len(lines) && lines[cursorLine].HasCursor {
		cursorCol = lines[cursorLine].CursorCol
	}
	cursorLine, cursorCol = normalizeInputFieldCursorCell(cursorLine, cursorCol, width, len(lines))
	return uiInputFieldCursor{Visible: true, Row: cursorLine + 1, Col: cursorCol}
}

func normalizeInputFieldCursorCell(row int, col int, width int, lineCount int) (int, int) {
	if width < 1 {
		return row, 0
	}
	if col < width {
		return row, max(0, col)
	}
	if row+1 < lineCount {
		return row + 1, 0
	}
	return row, width - 1
}

func inputContentLineLimit(height int) int {
	maxContentLines := height - 4
	if maxContentLines < 1 {
		return 1
	}
	return maxContentLines
}

func (l uiViewLayout) inputPanelLineCount(width, height int) int {
	inputState := l.model.inputModeState()
	if inputState.Mode == uiInputModeRollbackSelection {
		return 0
	}
	contentLines := len(wrappedEditableInputLines(width, l.mainInputRenderSpec()))
	if inputState.ShowsAskInput {
		wrappedAskLines, _ := l.wrappedAskPromptLines(width)
		contentLines = len(wrappedAskLines)
	}
	if contentLines < 1 {
		contentLines = 1
	}
	maxContentLines := inputContentLineLimit(height)
	if contentLines > maxContentLines {
		contentLines = maxContentLines
	}
	return contentLines + 2
}

func (l uiViewLayout) inputBorderStyle() lipgloss.Style {
	borderColor := uiPalette(l.model.theme).primary
	if l.model.isBusy() {
		borderColor = uiPalette(l.model.theme).muted
	}
	return lipgloss.NewStyle().Foreground(borderColor)
}
