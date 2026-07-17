package input

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

type Editor struct {
	text       string
	cursor     int
	killBuffer string
}

type LineRange struct {
	Start int
	End   int
}

type CursorPosition struct {
	Line int
	Col  int
}

type EditorSnapshot struct {
	text   string
	cursor int
}

func NewEditor() Editor {
	return Editor{}
}

func (e *Editor) Text() string {
	return e.text
}

func (e *Editor) SetText(text string) {
	e.text = text
	e.cursor = clampCursor(text, e.cursor)
}

func (e *Editor) Replace(text string) {
	e.text = text
	e.cursor = len(text)
}

func (e *Editor) Cursor() int {
	return e.cursor
}

func (e *Editor) KillBuffer() string {
	return e.killBuffer
}

func (e *Editor) SetKillBuffer(text string) {
	e.killBuffer = text
}

func (e *Editor) SetCursor(cursor int) {
	e.cursor = clampCursor(e.text, cursor)
}

func (e *Editor) Snapshot() EditorSnapshot {
	return EditorSnapshot{text: e.text, cursor: e.cursor}
}

func (e *Editor) Restore(snapshot EditorSnapshot) {
	e.text = snapshot.text
	e.cursor = clampCursor(e.text, snapshot.cursor)
}

func (e *Editor) InsertString(text string) {
	if text == "" {
		return
	}
	cursor := clampCursor(e.text, e.cursor)
	e.text = e.text[:cursor] + text + e.text[cursor:]
	e.cursor = cursor + len(text)
}

func (e *Editor) DeleteBackward() bool {
	if e.cursor <= 0 || e.text == "" {
		return false
	}
	start := previousGraphemeBoundary(e.text, e.cursor)
	e.replaceRange(start, e.cursor, "")
	return true
}

func (e *Editor) DeleteForward() bool {
	if e.cursor >= len(e.text) || e.text == "" {
		return false
	}
	end := nextGraphemeBoundary(e.text, e.cursor)
	e.replaceRange(e.cursor, end, "")
	return true
}

func (e *Editor) MoveLeft() bool {
	next := previousGraphemeBoundary(e.text, e.cursor)
	if next == e.cursor {
		return false
	}
	e.cursor = next
	return true
}

func (e *Editor) MoveRight() bool {
	next := nextGraphemeBoundary(e.text, e.cursor)
	if next == e.cursor {
		return false
	}
	e.cursor = next
	return true
}

func (e *Editor) MoveLineStart() bool {
	next := logicalLineStart(e.text, e.cursor)
	if next == e.cursor {
		return false
	}
	e.cursor = next
	return true
}

func (e *Editor) MoveLineEnd() bool {
	next := logicalLineEnd(e.text, e.cursor)
	if next == e.cursor {
		return false
	}
	e.cursor = next
	return true
}

func (e *Editor) MoveWordLeft() bool {
	next := previousWordBoundary(e.text, e.cursor)
	if next == e.cursor {
		return false
	}
	e.cursor = next
	return true
}

func (e *Editor) MoveWordRight() bool {
	next := nextWordBoundary(e.text, e.cursor)
	if next == e.cursor {
		return false
	}
	e.cursor = next
	return true
}

func (e *Editor) DeleteBackwardWord() bool {
	start := previousWordBoundary(e.text, e.cursor)
	if start == e.cursor {
		return false
	}
	e.killRange(start, e.cursor)
	return true
}

func (e *Editor) DeleteForwardWord() bool {
	end := nextWordBoundary(e.text, e.cursor)
	if end == e.cursor {
		return false
	}
	e.killRange(e.cursor, end)
	return true
}

func (e *Editor) KillToLineStart() bool {
	start := logicalLineStart(e.text, e.cursor)
	if start == e.cursor {
		if start == 0 {
			return false
		}
		start = previousGraphemeBoundary(e.text, start)
	}
	e.killRange(start, e.cursor)
	return true
}

func (e *Editor) KillToLineEnd() bool {
	end := logicalLineEnd(e.text, e.cursor)
	if end == e.cursor {
		if end >= len(e.text) {
			return false
		}
		end = nextGraphemeBoundary(e.text, end)
	}
	e.killRange(e.cursor, end)
	return true
}

func (e *Editor) DeleteCurrentLine() bool {
	if e.text == "" {
		return false
	}
	cursor := clampCursor(e.text, e.cursor)
	start := logicalLineStart(e.text, cursor)
	end := logicalLineEnd(e.text, cursor)
	deleteStart := start
	deleteEnd := end
	if end < len(e.text) && e.text[end] == '\n' {
		deleteEnd = end + 1
	} else if start > 0 && e.text[start-1] == '\n' {
		deleteStart = start - 1
	}
	if deleteStart >= deleteEnd {
		return false
	}
	e.replaceRange(deleteStart, deleteEnd, "")
	return true
}

func (e *Editor) Yank() bool {
	if e.killBuffer == "" {
		return false
	}
	e.InsertString(e.killBuffer)
	return true
}

func (e *Editor) WrappedLines(width int) []LineRange {
	return wrapRanges(e.text, width)
}

func (e *Editor) CursorPosition(width int) CursorPosition {
	lines := e.WrappedLines(width)
	lineIndex := wrappedLineIndex(lines, e.cursor)
	if lineIndex < 0 {
		return CursorPosition{}
	}
	return CursorPosition{
		Line: lineIndex,
		Col:  uniseg.StringWidth(e.text[lines[lineIndex].Start:e.cursor]),
	}
}

func (e *Editor) CursorAtDisplayColumn(line LineRange, targetCol int) int {
	return cursorAtDisplayColumn(e.text, line, targetCol)
}

func (e *Editor) replaceRange(start int, end int, replacement string) {
	start = clampCursor(e.text, start)
	end = clampCursor(e.text, end)
	if start > end {
		start, end = end, start
	}
	e.text = e.text[:start] + replacement + e.text[end:]
	e.cursor = start + len(replacement)
}

func (e *Editor) killRange(start int, end int) {
	start = clampCursor(e.text, start)
	end = clampCursor(e.text, end)
	if start > end {
		start, end = end, start
	}
	if start == end {
		return
	}
	e.killBuffer = e.text[start:end]
	e.replaceRange(start, end, "")
}

type grapheme struct {
	start int
	end   int
	width int
	text  string
}

func graphemes(text string) []grapheme {
	out := make([]grapheme, 0, utf8.RuneCountInString(text))
	state := -1
	offset := 0
	remaining := text
	for remaining != "" {
		cluster, rest, boundaries, nextState := uniseg.StepString(remaining, state)
		end := offset + len(cluster)
		width := boundaries >> uniseg.ShiftWidth
		out = append(out, grapheme{start: offset, end: end, width: width, text: cluster})
		offset = end
		remaining = rest
		state = nextState
	}
	return out
}

func previousGraphemeBoundary(text string, cursor int) int {
	cursor = clampCursor(text, cursor)
	prev := 0
	for _, cluster := range graphemes(text) {
		if cluster.end >= cursor {
			if cluster.end == cursor {
				return cluster.start
			}
			return prev
		}
		prev = cluster.end
	}
	return prev
}

func nextGraphemeBoundary(text string, cursor int) int {
	cursor = clampCursor(text, cursor)
	for _, cluster := range graphemes(text) {
		if cluster.start >= cursor {
			return cluster.end
		}
		if cluster.start < cursor && cluster.end > cursor {
			return cluster.end
		}
	}
	return len(text)
}

func wrapRanges(text string, width int) []LineRange {
	if width < 1 {
		width = 1
	}
	lines := make([]LineRange, 0, strings.Count(text, "\n")+1)
	lineStart := 0
	col := 0
	for _, cluster := range graphemes(text) {
		if cluster.text == "\n" {
			lines = append(lines, LineRange{Start: lineStart, End: cluster.start})
			lineStart = cluster.end
			col = 0
			continue
		}
		if col > 0 && col+cluster.width > width {
			lines = append(lines, LineRange{Start: lineStart, End: cluster.start})
			lineStart = cluster.start
			col = 0
		}
		col += cluster.width
	}
	lines = append(lines, LineRange{Start: lineStart, End: len(text)})
	return lines
}

func wrappedLineIndex(lines []LineRange, cursor int) int {
	if len(lines) == 0 {
		return -1
	}
	for index, line := range lines {
		if cursor < line.Start {
			continue
		}
		if cursor < line.End {
			return index
		}
		if cursor == line.End {
			if index+1 < len(lines) && lines[index+1].Start == cursor {
				continue
			}
			return index
		}
	}
	return len(lines) - 1
}

func cursorAtDisplayColumn(text string, line LineRange, targetCol int) int {
	if targetCol <= 0 {
		return line.Start
	}
	col := 0
	for _, cluster := range graphemes(text[line.Start:line.End]) {
		if col+cluster.width > targetCol {
			return line.Start + cluster.start
		}
		col += cluster.width
	}
	return line.End
}

func logicalLineStart(text string, cursor int) int {
	cursor = clampCursor(text, cursor)
	if index := strings.LastIndex(text[:cursor], "\n"); index >= 0 {
		return index + 1
	}
	return 0
}

func logicalLineEnd(text string, cursor int) int {
	cursor = clampCursor(text, cursor)
	if index := strings.Index(text[cursor:], "\n"); index >= 0 {
		return cursor + index
	}
	return len(text)
}

func previousWordBoundary(text string, cursor int) int {
	cursor = clampCursor(text, cursor)
	clusters := graphemes(text[:cursor])
	index := len(clusters) - 1
	for index >= 0 && isSpaceCluster(clusters[index].text) {
		index--
	}
	if index < 0 {
		return 0
	}
	class := wordClass(clusters[index].text)
	for index >= 0 && !isSpaceCluster(clusters[index].text) && wordClass(clusters[index].text) == class {
		index--
	}
	if index < 0 {
		return 0
	}
	return clusters[index].end
}

func nextWordBoundary(text string, cursor int) int {
	cursor = clampCursor(text, cursor)
	all := graphemes(text)
	index := 0
	for index < len(all) && all[index].end <= cursor {
		index++
	}
	for index < len(all) && isSpaceCluster(all[index].text) {
		index++
	}
	if index >= len(all) {
		return len(text)
	}
	class := wordClass(all[index].text)
	for index < len(all) && !isSpaceCluster(all[index].text) && wordClass(all[index].text) == class {
		index++
	}
	if index >= len(all) {
		return len(text)
	}
	return all[index-1].end
}

func wordClass(cluster string) int {
	for _, r := range cluster {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return 1
		}
	}
	return 2
}

func isSpaceCluster(cluster string) bool {
	for _, r := range cluster {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return cluster != ""
}

func clampCursor(text string, cursor int) int {
	cursor = clampByteCursor(text, cursor)
	for _, cluster := range graphemes(text) {
		if cursor == cluster.start || cursor == cluster.end {
			return cursor
		}
		if cursor > cluster.start && cursor < cluster.end {
			if cursor-cluster.start <= cluster.end-cursor {
				return cluster.start
			}
			return cluster.end
		}
	}
	return cursor
}

func clampByteCursor(text string, cursor int) int {
	if cursor <= 0 {
		return 0
	}
	if cursor >= len(text) {
		return len(text)
	}
	if text[cursor] == '\n' || utf8.RuneStart(text[cursor]) {
		return cursor
	}
	prev := cursor
	for prev > 0 && !utf8.RuneStart(text[prev]) {
		prev--
	}
	next := cursor + 1
	for next < len(text) && !utf8.RuneStart(text[next]) {
		next++
	}
	if cursor-prev <= next-cursor {
		return prev
	}
	return next
}
