package app

import (
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func (c uiInputController) handleRuntimeCtrlC(closeSurface func() tea.Cmd) (tea.Model, tea.Cmd) {
	m := c.model
	closeCmd := tea.Cmd(nil)
	if closeSurface != nil {
		closeCmd = closeSurface()
	}
	if m.hasPendingInterrupt() {
		m.exitAction = UIActionExit
		m.forcedLocalExit = true
		return m, sequenceCmds(closeCmd, tea.Quit)
	}
	if runtimeActivityAllowsOrdinaryInterrupt(m.runtimeActivityProjection) {
		return m, sequenceCmds(closeCmd, c.interruptBusyRuntime())
	}
	m.exitAction = UIActionExit
	return m, sequenceCmds(closeCmd, tea.Quit)
}

func runtimeActivityAllowsOrdinaryInterrupt(activity clientui.RuntimeActivity) bool {
	if activity.State != clientui.RuntimeActivityRunning || activity.ActiveStep == nil {
		return false
	}
	switch activity.ActiveStep.ActiveKind {
	case clientui.RuntimeActivityActiveKindUserTurn,
		clientui.RuntimeActivityActiveKindWorkflowTurn,
		clientui.RuntimeActivityActiveKindGoalLoop:
		return true
	default:
		return false
	}
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
