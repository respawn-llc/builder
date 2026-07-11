package app

import tea "github.com/charmbracelet/bubbletea"

type uiSharedInputMovementActions struct {
	MoveLeft      func()
	MoveRight     func()
	MoveWordLeft  func()
	MoveWordRight func()
}

func handleSharedInputMovementKey(msg tea.KeyMsg, actions uiSharedInputMovementActions) bool {
	switch msg.Type {
	case tea.KeyLeft:
		if msg.Alt {
			runInputMovementAction(actions.MoveWordLeft)
		} else {
			runInputMovementAction(actions.MoveLeft)
		}
		return true
	case tea.KeyRight:
		if msg.Alt {
			runInputMovementAction(actions.MoveWordRight)
		} else {
			runInputMovementAction(actions.MoveRight)
		}
		return true
	case tea.KeyCtrlLeft:
		runInputMovementAction(actions.MoveWordLeft)
		return true
	case tea.KeyCtrlRight:
		runInputMovementAction(actions.MoveWordRight)
		return true
	case tea.KeyRunes:
		if !msg.Alt || len(msg.Runes) != 1 {
			return false
		}
		switch msg.Runes[0] {
		case 'b':
			runInputMovementAction(actions.MoveWordLeft)
			return true
		case 'f':
			runInputMovementAction(actions.MoveWordRight)
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func runInputMovementAction(action func()) {
	if action != nil {
		action()
	}
}
