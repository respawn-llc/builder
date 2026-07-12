package app

import (
	"runtime"
	"strings"

	tuiinput "core/cli/tui/input"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func singleLineRunes(runes []rune) []rune {
	out := make([]rune, 0, len(runes))
	for _, r := range runes {
		if r == '\n' || r == '\r' {
			continue
		}
		out = append(out, r)
	}
	return out
}

func newSingleLineEditor(value string) tuiinput.Editor {
	editor := tuiinput.NewEditor()
	editor.Replace(strings.NewReplacer("\r", "", "\n", "").Replace(value))
	return editor
}

func updateSingleLineEditorWithAppKeys(editor *tuiinput.Editor, msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	text := editor.Text()
	cursor := runeOffsetForByteCursor(editor.Text(), editor.Cursor())
	killBuffer := editor.KillBuffer()
	apply := func(updated string, nextCursor int, nextKill string) {
		editor.Replace(strings.NewReplacer("\r", "", "\n", "").Replace(updated))
		editor.SetCursor(byteOffsetForRuneCursor(editor.Text(), nextCursor))
		editor.SetKillBuffer(nextKill)
	}
	if handleSharedInputEditKeyForGOOS(key, uiSharedInputEditActions{
		Backspace: func() bool {
			updated, nextCursor, changed := backspaceBuffer(text, cursor)
			if changed {
				apply(updated, nextCursor, killBuffer)
			}
			return changed
		},
		DeleteForward: func() bool {
			updated, nextCursor, changed := deleteForwardBuffer(text, cursor)
			if changed {
				apply(updated, nextCursor, killBuffer)
			}
			return changed
		},
		DeleteBackwardWord: func() bool {
			updated, nextCursor, nextKill, changed := deleteBackwardWordBuffer(text, cursor, killBuffer)
			if changed {
				apply(updated, nextCursor, nextKill)
			}
			return changed
		},
		DeleteForwardWord: func() bool {
			updated, nextCursor, nextKill, changed := deleteForwardWordBuffer(text, cursor, killBuffer)
			if changed {
				apply(updated, nextCursor, nextKill)
			}
			return changed
		},
		KillToLineStart: func() bool {
			updated, nextCursor, nextKill, changed := killToLineStartBuffer(text, cursor, killBuffer)
			if changed {
				apply(updated, nextCursor, nextKill)
			}
			return changed
		},
		KillToLineEnd: func() bool {
			updated, nextCursor, nextKill, changed := killToLineEndBuffer(text, cursor, killBuffer)
			if changed {
				apply(updated, nextCursor, nextKill)
			}
			return changed
		},
		Yank: func() bool {
			updated, nextCursor, changed := yankBuffer(text, cursor, killBuffer)
			if changed {
				apply(updated, nextCursor, killBuffer)
			}
			return changed
		},
		DeleteCurrentLine: func() bool {
			updated, nextCursor, changed := deleteCurrentBufferLine(text, cursor)
			if changed {
				apply(updated, nextCursor, killBuffer)
			}
			return changed
		},
	}, runtime.GOOS) {
		return nil
	}
	if handleSharedInputMovementKey(key, uiSharedInputMovementActions{
		MoveLeft: func() {
			editor.SetCursor(byteOffsetForRuneCursor(text, moveBufferCursorLeft(text, cursor)))
		},
		MoveRight: func() {
			editor.SetCursor(byteOffsetForRuneCursor(text, moveBufferCursorRight(text, cursor)))
		},
		MoveWordLeft: func() {
			editor.SetCursor(byteOffsetForRuneCursor(text, moveBufferCursorWordLeft(text, cursor)))
		},
		MoveWordRight: func() {
			editor.SetCursor(byteOffsetForRuneCursor(text, moveBufferCursorWordRight(text, cursor)))
		},
	}) {
		return nil
	}
	switch key.Type {
	case tea.KeySpace:
		updated, nextCursor, changed := insertBufferRunes(text, cursor, []rune{' '})
		if changed {
			apply(updated, nextCursor, killBuffer)
		}
	case tea.KeyHome, tea.KeyCtrlA:
		editor.SetCursor(byteOffsetForRuneCursor(editor.Text(), 0))
	case tea.KeyEnd, tea.KeyCtrlE, tea.KeyCtrlEnd:
		editor.SetCursor(len(editor.Text()))
	case tea.KeyRunes:
		updated, nextCursor, changed := insertBufferRunes(text, cursor, singleLineRunes(key.Runes))
		if changed {
			apply(updated, nextCursor, killBuffer)
		}
	}
	return nil
}

func renderSingleLineEditor(width int, maxContentLines int, editor tuiinput.Editor, prefix string, renderCursor bool, mask rune, placeholder string) tuiinput.RenderResult {
	field := tuiinput.NewField()
	field.Editor = editor
	field.Prefix = prefix
	field.MaxLines = maxContentLines
	field.Cursor = renderCursor
	field.Mask = mask
	field.Placeholder = placeholder
	return field.Render(width)
}

func renderSingleLineEditorFramedSoftCursorLines(width int, maxContentLines int, editor tuiinput.Editor, prefix string, renderCursor bool, lineStyle lipgloss.Style, borderStyle lipgloss.Style, mask rune, placeholder string) []string {
	border := borderStyle.Render(strings.Repeat("─", max(0, width)))
	lines := tuiinput.RenderSoftCursorLines(width, renderSingleLineEditor(width, maxContentLines, editor, prefix, renderCursor, mask, placeholder), lineStyle)
	out := make([]string, 0, len(lines)+2)
	out = append(out, border)
	out = append(out, lines...)
	out = append(out, border)
	return out
}
