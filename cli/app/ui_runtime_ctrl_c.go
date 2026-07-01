package app

import tea "github.com/charmbracelet/bubbletea"

func (c uiInputController) handleRuntimeCtrlC(closeSurface func() tea.Cmd) (tea.Model, tea.Cmd) {
	m := c.model
	if m.hasPendingInterrupt() {
		m.exitAction = UIActionExit
		m.forcedLocalExit = true
		if closeSurface != nil {
			if cmd := closeSurface(); cmd != nil {
				return m, tea.Sequence(cmd, tea.Quit)
			}
		}
		return m, tea.Quit
	}
	if m.blocksRuntimeInput() {
		return m, c.interruptBusyRuntime()
	}
	m.exitAction = UIActionExit
	if closeSurface != nil {
		if cmd := closeSurface(); cmd != nil {
			return m, tea.Sequence(cmd, tea.Quit)
		}
	}
	return m, tea.Quit
}

func (c uiInputController) closeTranscriptSurfaceForRuntimeCtrlC(close func()) func() tea.Cmd {
	return func() tea.Cmd {
		if cmd := c.model.restoreTranscriptSurface(); cmd != nil {
			if close != nil {
				close()
			}
			return cmd
		}
		if close != nil {
			close()
		}
		return nil
	}
}
