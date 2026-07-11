package app

import tea "github.com/charmbracelet/bubbletea"

type uiActivity uint8

const (
	uiActivityIdle uiActivity = iota
	uiActivityRunning
	uiActivityQuestion
	uiActivityInterrupted
	uiActivityError
)

func (m *uiModel) interruptedStatusNoticeCmd() tea.Cmd {
	if m == nil {
		return nil
	}
	return m.sendTransientStatusWithNoticeID("interrupted", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
}
