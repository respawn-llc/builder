package input

import (
	"strings"

	"github.com/rivo/uniseg"
)

type Field struct {
	Editor      Editor
	Prefix      string
	Placeholder string
	MaxLines    int
	Framed      bool
	Mask        rune
	Cursor      bool
}

type RenderResult struct {
	Lines  []string
	Width  int
	Cursor FieldCursor
}

type FieldCursor struct {
	Visible bool
	Row     int
	Col     int
}

type fieldLayout struct {
	text             string
	lines            []LineRange
	cursorLine       int
	cursorCol        int
	cursorBoundaries []fieldCursorBoundary
}

type fieldDisplay struct {
	text             string
	cursor           int
	cursorBoundaries []fieldCursorBoundary
}

type fieldCursorBoundary struct {
	displayOffset int
	sourceOffset  int
}

func NewField() Field {
	return Field{Cursor: true}
}

func (f *Field) Render(width int) RenderResult {
	if width < 1 {
		width = 1
	}
	layout := f.layout(width)
	visibleStart := visibleLineStart(len(layout.lines), f.maxContentLines(), layout.cursorLine)
	visibleEnd := visibleStart + f.maxContentLines()
	if visibleEnd > len(layout.lines) {
		visibleEnd = len(layout.lines)
	}
	lines := make([]string, 0, visibleEnd-visibleStart+2)
	for _, line := range layout.lines[visibleStart:visibleEnd] {
		lines = append(lines, padDisplayRight(layout.text[line.Start:line.End], width))
	}
	cursor := FieldCursor{}
	if f.Cursor {
		cursorLine := layout.cursorLine - visibleStart
		if cursorLine >= 0 && cursorLine < len(lines) {
			cursor.Visible = true
			cursor.Row = cursorLine
			cursor.Col = normalizeCursorCol(layout.cursorCol, width)
		}
	}

	if f.Framed {
		border := strings.Repeat("-", width)
		lines = append([]string{border}, append(lines, border)...)
		if cursor.Visible {
			cursor.Row++
		}
	}

	return RenderResult{Lines: lines, Width: width, Cursor: cursor}
}

func (f *Field) MoveUp(width int) bool {
	return f.moveVertical(width, -1)
}

func (f *Field) MoveDown(width int) bool {
	return f.moveVertical(width, 1)
}

func (f *Field) moveVertical(width, delta int) bool {
	if width < 1 {
		width = 1
	}
	layout := f.layout(width)
	if delta < 0 && layout.cursorLine == 0 {
		changed := f.Editor.Cursor() != 0
		f.Editor.SetCursor(0)
		return changed
	}
	if delta > 0 && layout.cursorLine+1 >= len(layout.lines) {
		next := len(f.Editor.Text())
		changed := f.Editor.Cursor() != next
		f.Editor.SetCursor(next)
		return changed
	}
	targetLine := layout.lines[layout.cursorLine+delta]
	targetCol := layout.cursorCol
	prefixWidth := uniseg.StringWidth(f.Prefix)
	currentHasPrefix := layout.lines[layout.cursorLine].Start < len(f.Prefix)
	targetHasPrefix := targetLine.Start < len(f.Prefix)
	switch {
	case currentHasPrefix && !targetHasPrefix:
		targetCol -= prefixWidth
	case !currentHasPrefix && targetHasPrefix:
		targetCol += prefixWidth
	}
	if targetCol < 0 {
		targetCol = 0
	}
	targetDisplayOffset := cursorAtDisplayColumn(layout.text, targetLine, targetCol)
	next := layout.sourceCursor(targetDisplayOffset)
	changed := f.Editor.Cursor() != next
	f.Editor.SetCursor(next)
	return changed
}

func (f Field) layout(width int) fieldLayout {
	display := f.display()
	renderText := f.Prefix + display.text
	cursor := len(f.Prefix) + display.cursor
	cursorBoundaries := make([]fieldCursorBoundary, 0, len(display.cursorBoundaries)+1)
	cursorBoundaries = append(cursorBoundaries, fieldCursorBoundary{})
	for _, boundary := range display.cursorBoundaries {
		cursorBoundaries = append(cursorBoundaries, fieldCursorBoundary{
			displayOffset: len(f.Prefix) + boundary.displayOffset,
			sourceOffset:  boundary.sourceOffset,
		})
	}
	if f.Editor.Text() == "" && f.Placeholder != "" {
		renderText = f.Prefix + f.Placeholder
		cursor = len(f.Prefix)
	}
	renderEditor := NewEditor()
	renderEditor.Replace(renderText)
	renderEditor.SetCursor(cursor)
	position := renderEditor.CursorPosition(width)
	return fieldLayout{
		text:             renderText,
		lines:            renderEditor.WrappedLines(width),
		cursorLine:       position.Line,
		cursorCol:        position.Col,
		cursorBoundaries: cursorBoundaries,
	}
}

func (l fieldLayout) sourceCursor(displayOffset int) int {
	cursor := 0
	for _, boundary := range l.cursorBoundaries {
		if boundary.displayOffset > displayOffset {
			break
		}
		cursor = boundary.sourceOffset
	}
	return cursor
}

func (f Field) display() fieldDisplay {
	text := f.Editor.Text()
	cursor := f.Editor.Cursor()
	clusters := graphemes(text)
	var out strings.Builder
	cursorOffset := 0
	cursorBoundaries := make([]fieldCursorBoundary, 1, len(clusters)+1)
	for _, cluster := range clusters {
		start := out.Len()
		switch {
		case cluster.text == "\n":
			out.WriteByte('\n')
		case f.Mask != 0:
			out.WriteRune(f.Mask)
		default:
			out.WriteString(cluster.text)
		}
		if cluster.end <= cursor {
			cursorOffset += out.Len() - start
		}
		cursorBoundaries = append(cursorBoundaries, fieldCursorBoundary{
			displayOffset: out.Len(),
			sourceOffset:  cluster.end,
		})
	}
	return fieldDisplay{
		text:             out.String(),
		cursor:           cursorOffset,
		cursorBoundaries: cursorBoundaries,
	}
}

func (f Field) maxContentLines() int {
	if f.MaxLines < 1 {
		return 1 << 30
	}
	return f.MaxLines
}

func visibleLineStart(totalLines int, maxLines int, cursorLine int) int {
	if maxLines < 1 || totalLines <= maxLines {
		return 0
	}
	start := cursorLine - maxLines + 1
	if start < 0 {
		return 0
	}
	maxStart := totalLines - maxLines
	if start > maxStart {
		return maxStart
	}
	return start
}

func normalizeCursorCol(col int, width int) int {
	if width < 1 {
		return 0
	}
	if col < 0 {
		return 0
	}
	if col >= width {
		return width - 1
	}
	return col
}

func padDisplayRight(text string, width int) string {
	current := uniseg.StringWidth(text)
	if current >= width {
		return text
	}
	return text + strings.Repeat(" ", width-current)
}
