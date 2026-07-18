package app

import (
	tuiinput "core/cli/tui/input"

	tea "github.com/charmbracelet/bubbletea"
)

func applySharedInputMovementKey(msg tea.KeyMsg, editor *tuiinput.Editor) bool {
	if editor == nil {
		return false
	}
	switch msg.Type {
	case tea.KeyLeft:
		if msg.Alt {
			editor.MoveWordLeft()
		} else {
			editor.MoveLeft()
		}
		return true
	case tea.KeyRight:
		if msg.Alt {
			editor.MoveWordRight()
		} else {
			editor.MoveRight()
		}
		return true
	case tea.KeyCtrlLeft:
		editor.MoveWordLeft()
		return true
	case tea.KeyCtrlRight:
		editor.MoveWordRight()
		return true
	case tea.KeyRunes:
		if !msg.Alt || len(msg.Runes) != 1 {
			return false
		}
		switch msg.Runes[0] {
		case 'b':
			editor.MoveWordLeft()
			return true
		case 'f':
			editor.MoveWordRight()
			return true
		default:
			return false
		}
	default:
		return false
	}
}
