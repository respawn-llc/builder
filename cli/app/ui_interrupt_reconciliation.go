package app

import (
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) acknowledgePendingInterrupt() tea.Cmd {
	if m == nil || !m.hasPendingInterrupt() {
		return nil
	}
	m.activeSubmit = activeSubmitState{}
	m.clearPendingRuntimeOperations(
		clientui.RuntimeOperationKindSubmit,
		clientui.RuntimeOperationKindPreSubmitCompact,
		clientui.RuntimeOperationKindUserShell,
		clientui.RuntimeOperationKindCompact,
	)
	restoreCmd := m.inputController().restoreInterruptedInputsIntoComposer()
	m.setPendingInterrupt(false)
	m.activity = uiActivityInterrupted
	m.clearReviewerState()
	return tea.Batch(restoreCmd, m.interruptedStatusNoticeCmd())
}
