package app

import tea "github.com/charmbracelet/bubbletea"

type uiSharedInputEditActions struct {
	Backspace          func() bool
	DeleteForward      func() bool
	DeleteBackwardWord func() bool
	DeleteForwardWord  func() bool
	KillToLineStart    func() bool
	KillToLineEnd      func() bool
	Yank               func() bool
	DeleteCurrentLine  func() bool
}

func handleSharedInputEditKeyForGOOS(msg tea.KeyMsg, actions uiSharedInputEditActions, goos string) bool {
	if isDeleteCurrentLineKeyForGOOS(msg, goos) {
		runInputEditAction(actions.DeleteCurrentLine)
		return true
	}
	switch msg.Type {
	case tea.KeyBackspace, tea.KeyCtrlH:
		if msg.Alt {
			runInputEditAction(actions.DeleteBackwardWord)
		} else {
			runInputEditAction(actions.Backspace)
		}
		return true
	case tea.KeyDelete:
		if msg.Alt {
			runInputEditAction(actions.DeleteForwardWord)
		} else {
			runInputEditAction(actions.DeleteForward)
		}
		return true
	case tea.KeyCtrlW:
		runInputEditAction(actions.DeleteBackwardWord)
		return true
	case tea.KeyCtrlK:
		runInputEditAction(actions.KillToLineEnd)
		return true
	case tea.KeyCtrlU:
		runInputEditAction(actions.KillToLineStart)
		return true
	case tea.KeyCtrlY:
		runInputEditAction(actions.Yank)
		return true
	default:
		return false
	}
}

func runInputEditAction(action func() bool) {
	if action != nil {
		action()
	}
}
