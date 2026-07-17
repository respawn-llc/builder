package app

import (
	tuiinput "core/cli/tui/input"

	tea "github.com/charmbracelet/bubbletea"
)

func nextNonZeroToken(token uint64) uint64 {
	token++
	if token == 0 {
		return 1
	}
	return token
}

func (m *uiModel) replaceMainInput(text string, byteCursor int) {
	m.mainEditor.Replace(text)
	m.mainEditor.SetCursor(byteCursor)
	m.mainInputMutated()
}

func (m *uiModel) replaceMainInputAtEnd(text string) {
	m.mainEditor.Replace(text)
	m.mainInputMutated()
}

func (m *uiModel) clearInput() {
	m.replaceMainInput("", 0)
	m.resetPromptHistoryNavigation()
}

func (m *uiModel) recordMainInputMutation() {
	m.mainInputDraftToken = nextNonZeroToken(m.mainInputDraftToken)
	m.syncPromptHistorySelectionToInput()
}

func (m *uiModel) mainInputMutated() {
	m.recordMainInputMutation()
	m.refreshAutocompleteStateFromInput()
}

func (m *uiModel) insertInputRunes(chars []rune) tea.Cmd {
	if !insertEditorRunes(&m.mainEditor, chars) {
		return nil
	}
	m.recordMainInputMutation()
	return m.refreshAutocompleteFromInput()
}

func (m *uiModel) clearAskInput() {
	m.ask.editor.Replace("")
}

func (m *uiModel) insertAskInputRunes(chars []rune) {
	insertEditorRunes(&m.ask.editor, chars)
}

func insertEditorRunes(editor *tuiinput.Editor, chars []rune) bool {
	if editor == nil || len(chars) == 0 {
		return false
	}
	filtered, _ := stripMouseSGRRunes(chars)
	if len(filtered) == 0 {
		return false
	}
	editor.InsertString(string(filtered))
	cursor := runeOffsetForByteCursor(editor.Text(), editor.Cursor())
	cleaned, cleanedCursor, removed := stripMouseSGRRunesWithCursor([]rune(editor.Text()), cursor)
	if removed {
		editor.Replace(string(cleaned))
		editor.SetCursor(byteOffsetForRuneCursor(editor.Text(), cleanedCursor))
	}
	return true
}

func (m *uiModel) moveMainCursorVertical(delta int) bool {
	moved := moveEditorCursorVertical(
		&m.mainEditor,
		m.layout().effectiveWidth(),
		m.layout().mainInputPrefix(),
		delta,
	)
	m.refreshAutocompleteStateFromInput()
	return moved
}

func (m *uiModel) moveAskCursorVertical(delta int) bool {
	return moveEditorCursorVertical(&m.ask.editor, m.layout().effectiveWidth(), "› ", delta)
}

func moveEditorCursorVertical(editor *tuiinput.Editor, width int, prefix string, delta int) bool {
	if editor == nil {
		return false
	}
	field := tuiinput.NewField()
	field.Editor = *editor
	field.Prefix = prefix
	var moved bool
	if delta < 0 {
		moved = field.MoveUp(width)
	} else {
		moved = field.MoveDown(width)
	}
	*editor = field.Editor
	return moved
}

func byteOffsetForRuneCursor(text string, cursor int) int {
	if cursor < 0 {
		return len(text)
	}
	if cursor == 0 {
		return 0
	}
	runeIndex := 0
	for byteIndex := range text {
		if runeIndex == cursor {
			return byteIndex
		}
		runeIndex++
	}
	return len(text)
}

func runeOffsetForByteCursor(text string, cursor int) int {
	if cursor <= 0 {
		return 0
	}
	if cursor >= len(text) {
		return len([]rune(text))
	}
	offset := 0
	for byteIndex := range text {
		if byteIndex >= cursor {
			return offset
		}
		offset++
	}
	return len([]rune(text))
}

func clampCursor(cursor, size int) int {
	if cursor < 0 {
		return size
	}
	if cursor > size {
		return size
	}
	return cursor
}
