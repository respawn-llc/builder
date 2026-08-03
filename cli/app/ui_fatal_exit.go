package app

import (
	"errors"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) handleFatalUIError(operatorMessage string, err error) tea.Cmd {
	if err == nil {
		err = errors.New("fatal UI error")
	}
	if m != nil && m.debugMode {
		panic(err)
	}
	if m != nil {
		m.exitAction = UIActionExit
		m.forcedLocalExit = true
		m.transientStatus = operatorMessage
		m.transientStatusKind = uiStatusNoticeError
	}
	return tea.Quit
}
