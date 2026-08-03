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
	return m.exitWithUIError(operatorMessage)
}

func (m *uiModel) exitWithUIError(operatorMessage string) tea.Cmd {
	if m != nil {
		m.exitAction = UIActionExit
		m.forcedLocalExit = true
		m.transientStatus = operatorMessage
		m.transientStatusKind = uiStatusNoticeError
	}
	return tea.Quit
}

func (m *uiModel) handleExpectedUIError(operatorMessage string, err error) tea.Cmd {
	if err == nil {
		err = errors.New("UI operation failed")
	}
	return m.exitWithUIError(operatorMessage)
}
