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

func waitTerminalOutputFailure(output *uiTerminalOutput) tea.Cmd {
	if output == nil {
		return nil
	}
	return func() tea.Msg {
		err := output.waitFailure()
		if err == nil {
			return nil
		}
		return terminalSequenceWriteErrMsg{err: err}
	}
}

func (m *uiModel) dropNativeSurface() {}
