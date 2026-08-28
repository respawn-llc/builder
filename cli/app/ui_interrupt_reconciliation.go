package app

import tea "github.com/charmbracelet/bubbletea"

func (m *uiModel) acknowledgePendingInterrupt() tea.Cmd {
	if m == nil || !m.hasPendingInterrupt() {
		return nil
	}
	m.activeSubmit = activeSubmitState{}
	restoreCmd := m.inputController().restoreInterruptedInputsIntoComposer()
	m.setPendingInterrupt(false)
	m.activity = uiActivityInterrupted
	return tea.Batch(restoreCmd, m.interruptedStatusNoticeCmd())
}
