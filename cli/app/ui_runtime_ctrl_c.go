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
	if runtimeActivityAllowsOrdinaryInterrupt(m.runtimeActivityProjection) {
		return m, sequenceCmds(closeCmd, c.interruptBusyRuntime())
	}
	if m.hasPendingInterrupt() && m.interruptPreActive {
		m.exitAction = UIActionExit
		m.forcedLocalExit = true
		return m, sequenceCmds(closeCmd, tea.Quit)
	}
	if m.runtimeActivityProjection.State == clientui.RuntimeActivityStarting || m.hasLocalDispatchPending() {
		m.deferInterruptUntilActive()
		return m, closeCmd
	}
	if m.hasPendingInterrupt() {
		m.exitAction = UIActionExit
		m.forcedLocalExit = true
		return m, sequenceCmds(closeCmd, tea.Quit)
	}
	m.exitAction = UIActionExit
	return m, sequenceCmds(closeCmd, tea.Quit)
}

func runtimeActivityAllowsOrdinaryInterrupt(activity clientui.RuntimeActivity) bool {
	if (activity.State != clientui.RuntimeActivityRunning &&
		activity.State != clientui.RuntimeActivityAwaitingPrompt) ||
		activity.ActiveStep == nil {
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

func (m *uiModel) dispatchDeferredInterruptIfReady() tea.Cmd {
	if !m.bindDeferredInterruptIfReady() {
		return nil
	}
	return m.runtimeControlCommand(runtimeControlInterrupt, "", false, "")
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
