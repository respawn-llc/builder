package app

import (
	tuiinput "core/cli/tui/input"

	tea "github.com/charmbracelet/bubbletea"
)

type uiEditorKeyResult struct {
	Handled bool
	Mutated bool
}

func applySharedInputEditKeyForGOOS(msg tea.KeyMsg, editor *tuiinput.Editor, goos string) uiEditorKeyResult {
	if editor == nil {
		return uiEditorKeyResult{}
	}
	if isDeleteCurrentLineKeyForGOOS(msg, goos) {
		return uiEditorKeyResult{Handled: true, Mutated: editor.DeleteCurrentLine()}
	}
	switch msg.Type {
	case tea.KeyBackspace, tea.KeyCtrlH:
		if msg.Alt {
			return uiEditorKeyResult{Handled: true, Mutated: editor.DeleteBackwardWord()}
		}
		return uiEditorKeyResult{Handled: true, Mutated: editor.DeleteBackward()}
	case tea.KeyDelete:
		if msg.Alt {
			return uiEditorKeyResult{Handled: true, Mutated: editor.DeleteForwardWord()}
		}
		return uiEditorKeyResult{Handled: true, Mutated: editor.DeleteForward()}
	case tea.KeyCtrlW:
		return uiEditorKeyResult{Handled: true, Mutated: editor.DeleteBackwardWord()}
	case tea.KeyCtrlK:
		return uiEditorKeyResult{Handled: true, Mutated: editor.KillToLineEnd()}
	case tea.KeyCtrlU:
		return uiEditorKeyResult{Handled: true, Mutated: editor.KillToLineStart()}
	case tea.KeyCtrlY:
		return uiEditorKeyResult{Handled: true, Mutated: editor.Yank()}
	default:
		return uiEditorKeyResult{}
	}
}
