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
	if result := applySharedInputEditKeyForGOOS(key, editor, runtime.GOOS); result.Handled {
		return nil
	}
	if applySharedInputMovementKey(key, editor) {
		return nil
	}
	switch key.Type {
	case tea.KeySpace:
		insertEditorRunes(editor, []rune{' '})
	case tea.KeyHome, tea.KeyCtrlA:
		editor.SetCursor(0)
	case tea.KeyEnd, tea.KeyCtrlE, tea.KeyCtrlEnd:
		editor.SetCursor(len(editor.Text()))
	case tea.KeyRunes:
		insertEditorRunes(editor, singleLineRunes(key.Runes))
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
