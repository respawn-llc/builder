package app

import tea "github.com/charmbracelet/bubbletea"

func (m *uiModel) acknowledgePendingInterrupt() tea.Cmd {
	if m == nil || !m.hasPendingInterrupt() {
		return nil
	}
	var cmd tea.Cmd
	restoreActiveSubmit := m.activeSubmit.restoreOnInterrupt && !m.activeSubmit.flushed
	if restoreActiveSubmit {
		m.inputController().restoreSubmittedTextIntoInput(m.activeSubmit.text)
	}
	m.activeSubmit = activeSubmitState{}
	controller := m.inputController()
	cmd = controller.restorePendingInjectedIntoInput()
	controller.restoreQueuedMessagesIntoInput()
	m.setPendingInterrupt(false)
	m.activity = uiActivityInterrupted
	m.clearReviewerState()
	cmd = tea.Batch(cmd, m.interruptedStatusNoticeCmd())
	if restoreActiveSubmit {
		cmd = tea.Batch(cmd, m.sendTransientStatusWithNoticeID(
			"runtime input state is unknown; restored local text for review",
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		))
	}
	return cmd
}
