package input

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

func RenderSoftCursorLines(width int, rendered RenderResult, lineStyle lipgloss.Style) []string {
	lines := make([]string, 0, len(rendered.Lines))
	for index, line := range rendered.Lines {
		if rendered.Cursor.Visible && index == rendered.Cursor.Row {
			lines = append(lines, renderLineWithSoftCursor(line, width, rendered.Cursor.Col, lineStyle))
			continue
		}
		lines = append(lines, lineStyle.Render(padDisplayRight(line, width)))
	}
	return lines
}

func RenderSoftCursorLine(width int, line string, cursorCol int, lineStyle lipgloss.Style) string {
	return renderLineWithSoftCursor(line, width, cursorCol, lineStyle)
}

func renderLineWithSoftCursor(line string, width int, cursorCol int, lineStyle lipgloss.Style) string {
	if width < 1 {
		return lineStyle.Render("")
	}
	runes := []rune(line)
	displayCol := 0
	for index, r := range runes {
		rw := runewidth.RuneWidth(r)
		if rw < 1 {
			rw = 1
		}
		if cursorCol < displayCol+rw {
			prefix := string(runes[:index])
			suffix := string(runes[index+1:])
			return lineStyle.Render(prefix) + lineStyle.Reverse(true).Render(string(r)) + lineStyle.Render(padDisplayRight(suffix, width-displayCol-rw))
		}
		displayCol += rw
	}
	if displayCol < width {
		cursorCol = min(max(cursorCol, displayCol), width-1)
		beforeCursor := strings.Repeat(" ", cursorCol-displayCol)
		afterCursor := strings.Repeat(" ", max(0, width-cursorCol-1))
		return lineStyle.Render(line+beforeCursor) + lineStyle.Reverse(true).Render(" ") + lineStyle.Render(afterCursor)
	}
	if len(runes) == 0 {
		return lineStyle.Reverse(true).Render(" ")
	}
	last := len(runes) - 1
	return lineStyle.Render(string(runes[:last])) + lineStyle.Reverse(true).Render(string(runes[last]))
}
