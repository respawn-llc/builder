package app

import tea "github.com/charmbracelet/bubbletea"

func (m *uiModel) acknowledgePendingInterrupt() tea.Cmd {
	if m == nil || !m.hasPendingInterrupt() {
		return nil
	}
	controller := m.inputController()
	cmd := controller.restorePendingInjectedIntoInput()
	controller.restoreQueuedMessagesIntoInput()
	m.setPendingInterrupt(false)
	m.activity = uiActivityInterrupted
	m.clearReviewerState()
	return tea.Batch(cmd, m.interruptedStatusNoticeCmd(), m.persistSessionDraftRecoveryCmd())
}
