package app

import tea "github.com/charmbracelet/bubbletea"

func (m *uiModel) acknowledgePendingInterrupt() tea.Cmd {
	if m == nil || !m.hasPendingInterrupt() {
		return nil
	}
	var cmd tea.Cmd
	restoreActiveSubmit, ambiguous := m.shouldRestoreActiveSubmitAfterInterrupt()
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
	if ambiguous {
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

func (m *uiModel) shouldRestoreActiveSubmitAfterInterrupt() (bool, bool) {
	if m == nil || !m.activeSubmit.restoreOnInterrupt {
		return false, false
	}
	if m.activeSubmit.flushed {
		return false, false
	}
	return true, false
}
