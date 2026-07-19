package app

import (
	"strings"

	tuiinput "core/cli/tui/input"

	"github.com/charmbracelet/lipgloss"
)

// uiInputPaneProjection is one frame-local projection of the active editor pane.
// It is deliberately not retained: terminal geometry and cursor visibility are
// frame facts, not editor state.
type uiInputPaneProjection struct {
	Lines       []string
	Cursor      uiInputFieldCursor
	PanelHeight int
}

type uiInputPaneContentLine struct {
	text      string
	prompt    askPromptLine
	cursorCol int
}

type askInputViewport struct {
	lines  []uiInputPaneContentLine
	cursor uiInputFieldCursor
}

func (l uiViewLayout) inputPaneProjection(width, height int, style uiStyles) uiInputPaneProjection {
	inputState := l.model.inputModeState()
	if inputState.Mode == uiInputModeRollbackSelection {
		return uiInputPaneProjection{}
	}
	if width < 1 {
		return uiInputPaneProjection{Lines: []string{padRight("", width)}, PanelHeight: 1}
	}
	if inputState.Mode == uiInputModeProcessList || inputState.Mode == uiInputModeWorktree || inputState.Mode == uiInputModeGoal {
		return uiInputPaneProjection{Lines: []string{padRight("", width)}, PanelHeight: 1}
	}

	maxContentLines := inputContentLineLimit(height)
	if inputState.ShowsAskInput {
		return l.projectAskInputPane(width, maxContentLines, style)
	}
	if !inputState.ShowsMainInput {
		return uiInputPaneProjection{}
	}

	field := tuiinput.NewField()
	field.Editor = l.model.mainEditor
	field.Prefix = l.mainInputPrefix()
	field.MaxLines = maxContentLines
	field.Cursor = l.shouldUseRealTerminalCursor() || l.shouldRenderSoftCursor()
	rendered := field.Render(width)
	lines := renderInputFieldLines(width, rendered, style.input, l.shouldRenderSoftCursor())
	projection := uiInputPaneProjection{
		Lines:       renderFramedLines(width, lines, l.inputBorderStyle()),
		PanelHeight: len(rendered.Lines) + 2,
	}
	if rendered.Cursor.Visible && l.shouldUseRealTerminalCursor() {
		row, col := normalizeInputFieldCursorCell(rendered.Cursor.Row, rendered.Cursor.Col, width, len(rendered.Lines))
		projection.Cursor = uiInputFieldCursor{Visible: true, Row: framedInputContentCursorRow(row), Col: col}
	}
	return projection
}

func (l uiViewLayout) projectAskInputPane(width, maxContentLines int, style uiStyles) uiInputPaneProjection {
	viewport := l.askInputViewport(width, maxContentLines)
	visible := viewport.lines

	lines := make([]string, 0, len(visible))
	selectedStyle := lipgloss.NewStyle().Foreground(uiPalette(l.model.theme).primary).Bold(true)
	recommendedStyle := lipgloss.NewStyle().Foreground(uiPalette(l.model.theme).secondary)
	recommendedNoteStyle := style.meta.Faint(true)
	for index, line := range visible {
		lines = append(lines, renderAskPaneLine(
			line,
			viewport.cursor.Visible && index == viewport.cursor.Row && l.shouldRenderSoftCursor(),
			width,
			style,
			selectedStyle,
			recommendedStyle,
			recommendedNoteStyle,
		))
	}

	projection := uiInputPaneProjection{
		Lines:       renderFramedLines(width, lines, l.inputBorderStyle()),
		PanelHeight: len(visible) + 2,
	}
	if viewport.cursor.Visible && l.shouldUseRealTerminalCursor() {
		row, col := normalizeInputFieldCursorCell(viewport.cursor.Row, viewport.cursor.Col, width, len(visible))
		projection.Cursor = uiInputFieldCursor{Visible: true, Row: framedInputContentCursorRow(row), Col: col}
	}
	return projection
}

func (l uiViewLayout) askInputViewport(width, maxContentLines int) askInputViewport {
	priorityLines, priorityCursor := l.askPriorityPaneContent(width)
	questionRows := []string(nil)
	if l.model.ask.activeProjection != nil {
		questionRows = l.model.ask.activeProjection.rows
	}
	return projectBoundedAskViewport(questionRows, priorityLines, priorityCursor, width, maxContentLines)
}

func (l uiViewLayout) askPriorityPaneContent(width int) ([]uiInputPaneContentLine, uiInputFieldCursor) {
	promptLines := l.model.askController().renderPriorityPromptLines()
	content := make([]uiInputPaneContentLine, 0, len(promptLines)*2)
	cursor := uiInputFieldCursor{}
	for _, prompt := range promptLines {
		if prompt.Kind == askPromptLineKindInput {
			field := tuiinput.NewField()
			field.Editor = prompt.InputEditor
			field.Prefix = prompt.InputPrefix
			field.Cursor = prompt.ShowsCursor
			rendered := field.Render(width)
			for index, text := range rendered.Lines {
				line := uiInputPaneContentLine{text: text, prompt: prompt}
				if rendered.Cursor.Visible && index == rendered.Cursor.Row {
					line.cursorCol = rendered.Cursor.Col
					cursor = uiInputFieldCursor{Visible: true, Row: len(content), Col: rendered.Cursor.Col}
				}
				content = append(content, line)
			}
			continue
		}
		parts := []string{prompt.Text}
		if prompt.Kind != askPromptLineKindQuestion {
			parts = wrapLine(prompt.Text, width)
		}
		for _, text := range parts {
			content = append(content, uiInputPaneContentLine{text: text, prompt: prompt})
		}
	}
	return content, cursor
}

func projectBoundedAskViewport(
	questionRows []string,
	priorityLines []uiInputPaneContentLine,
	priorityCursor uiInputFieldCursor,
	width int,
	maxContentLines int,
) askInputViewport {
	if maxContentLines < 1 {
		return askInputViewport{}
	}
	if len(priorityLines) > maxContentLines {
		var cursorLine *int
		if priorityCursor.Visible {
			row := priorityCursor.Row
			cursorLine = &row
		}
		start := cursorAwareInputPaneViewportStart(len(priorityLines), maxContentLines, cursorLine)
		end := min(len(priorityLines), start+maxContentLines)
		viewport := askInputViewport{
			lines: append([]uiInputPaneContentLine(nil), priorityLines[start:end]...),
		}
		if priorityCursor.Visible {
			row := priorityCursor.Row - start
			if row >= 0 && row < len(viewport.lines) {
				viewport.cursor = uiInputFieldCursor{
					Visible: true,
					Row:     row,
					Col:     priorityCursor.Col,
				}
			}
		}
		return viewport
	}

	questionCapacity := maxContentLines - len(priorityLines)
	visibleQuestionCount := min(len(questionRows), questionCapacity)
	questionTruncated := len(questionRows) > visibleQuestionCount
	viewport := askInputViewport{
		lines: make([]uiInputPaneContentLine, 0, visibleQuestionCount+len(priorityLines)),
	}
	for index := 0; index < visibleQuestionCount; index++ {
		text := truncateANSIRight(questionRows[index], width)
		if questionTruncated && index == visibleQuestionCount-1 {
			text = truncateANSIRightWithEllipsis(questionRows[index], width, true)
		}
		viewport.lines = append(viewport.lines, uiInputPaneContentLine{
			text:   text,
			prompt: askPromptLine{Kind: askPromptLineKindQuestion},
		})
	}
	viewport.lines = append(viewport.lines, priorityLines...)
	if priorityCursor.Visible {
		viewport.cursor = uiInputFieldCursor{
			Visible: true,
			Row:     visibleQuestionCount + priorityCursor.Row,
			Col:     priorityCursor.Col,
		}
	}
	return viewport
}

func cursorAwareInputPaneViewportStart(totalLines, maxLines int, cursorLine *int) int {
	if maxLines < 1 || totalLines <= maxLines {
		return 0
	}
	maxStart := totalLines - maxLines
	if cursorLine == nil {
		return maxStart
	}
	start := *cursorLine - maxLines + 1
	if start < 0 {
		return 0
	}
	if start > maxStart {
		return maxStart
	}
	return start
}

func renderInputFieldLines(width int, rendered tuiinput.RenderResult, style lipgloss.Style, softCursor bool) []string {
	if softCursor {
		return tuiinput.RenderSoftCursorLines(width, rendered, style)
	}
	lines := make([]string, 0, len(rendered.Lines))
	for _, line := range rendered.Lines {
		lines = append(lines, style.Render(padANSIRight(line, width)))
	}
	return lines
}

func renderAskPaneLine(
	line uiInputPaneContentLine,
	softCursor bool,
	width int,
	style uiStyles,
	selectedStyle lipgloss.Style,
	recommendedStyle lipgloss.Style,
	recommendedNoteStyle lipgloss.Style,
) string {
	text := line.text
	if softCursor {
		return tuiinput.RenderSoftCursorLine(width, text, line.cursorCol, style.input)
	}
	switch {
	case line.prompt.Kind == askPromptLineKindHint:
		return style.meta.Render(padANSIRight(text, width))
	case line.prompt.Disabled:
		return style.inputDisabled.Render(padANSIRight(text, width))
	case line.prompt.Selected:
		return selectedStyle.Render(padANSIRight(text, width))
	case line.prompt.Recommended:
		return renderRecommendedAskLine(text, line.prompt.MutedSuffix, width, recommendedStyle, recommendedNoteStyle)
	default:
		return style.input.Render(padANSIRight(text, width))
	}
}

func renderRecommendedAskLine(text string, mutedSuffix string, width int, recommendedStyle lipgloss.Style, noteStyle lipgloss.Style) string {
	body := text
	suffix := ""
	if mutedSuffix != "" && strings.HasSuffix(body, mutedSuffix) {
		body = strings.TrimSuffix(body, mutedSuffix)
		suffix = mutedSuffix
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

func framedInputContentCursorRow(contentRow int) int {
	return contentRow + 2
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

func (l uiViewLayout) inputBorderStyle() lipgloss.Style {
	borderColor := uiPalette(l.model.theme).primary
	if l.model.isBusy() {
		borderColor = uiPalette(l.model.theme).muted
	}
	return lipgloss.NewStyle().Foreground(borderColor)
}
