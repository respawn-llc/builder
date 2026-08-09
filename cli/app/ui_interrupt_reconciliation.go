package app

import (
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

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
	m.clearPendingRuntimeOperations(
		clientui.RuntimeOperationKindSubmit,
		clientui.RuntimeOperationKindPreSubmitCompact,
		clientui.RuntimeOperationKindUserShell,
		clientui.RuntimeOperationKindCompact,
	)
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
	ref := m.activeSubmit.operationRef
	if err := ref.Validate(); err != nil {
		return true, true
	}
	view := m.cachedRuntimeMainView()
	for _, record := range view.InputReconciliation.Operations {
		if record.Operation != ref {
			continue
		}
		switch record.State {
		case clientui.RuntimeInputReconciliationCommitted, clientui.RuntimeInputReconciliationSubmitted:
			return false, false
		case clientui.RuntimeInputReconciliationCanceledNotCommitted, clientui.RuntimeInputReconciliationFailedWithRestore:
			return true, false
		case clientui.RuntimeInputReconciliationUnknown, clientui.RuntimeInputReconciliationEvicted:
			if m.activeSubmit.flushed {
				return false, false
			}
			return true, true
		}
	}
	if m.activeSubmit.flushed {
		return false, false
	}
	return true, true
}

func (m *uiModel) pendingInterruptMissingInputReconciliation(view clientui.RuntimeMainView) bool {
	if m == nil {
		return false
	}
	ref := m.activeSubmit.operationRef
	if err := ref.Validate(); err != nil {
		return false
	}
	for _, record := range view.InputReconciliation.Operations {
		if record.Operation == ref {
			return false
		}
	}
	return true
}

func (m *uiModel) canRequestInputReconciliationRefresh() bool {
	if m == nil {
		return false
	}
	client := m.runtimeClient()
	if _, ok := client.(interface{ sessionRuntimeBoundary() }); !ok {
		return false
	}
	_, canRefresh := client.(runtimeMainViewReconciliationClient)
	_, canInterrupt := client.(runtimeInterruptReconciliationClient)
	return canRefresh && canInterrupt
}

func (m *uiModel) requestInputReconciliationRefresh() tea.Cmd {
	if !m.canRequestInputReconciliationRefresh() {
		return nil
	}
	return m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequest{
		cause:    runtimeMainViewRefreshCauseManual,
		class:    runtimeSyncPolicyClassAllowed,
		priority: 100,
	}).cmd
}
