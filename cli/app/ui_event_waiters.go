package app

import tea "github.com/charmbracelet/bubbletea"

func waitAskEvent(events <-chan askEvent) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-events
		if !ok {
			return nil
		}
		return askEventMsg{event: event}
	}
}

func waitRuntimeConnectionStateChange(events <-chan runtimeConnectionStateChangedMsg) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		return <-events
	}
}

func waitRuntimeReconnectWarning(events <-chan runtimeReconnectWarningMsg) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		return <-events
	}
}

func (m *uiModel) dropNativeSurface() {}
