package app

import tea "github.com/charmbracelet/bubbletea"

func waitRuntimeConnectionStateChange(events <-chan runtimeConnectionStateChangedMsg) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		return <-events
	}
}

func (m *uiModel) dropNativeSurface() {}
