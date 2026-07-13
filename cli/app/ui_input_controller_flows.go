package app

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (c uiInputController) startRollbackSelectionFlowCmd() tea.Cmd {
	return c.model.beginRollbackSelectionHydration()
}

func (c uiInputController) stopRollbackSelectionFlowCmd() tea.Cmd {
	m := c.model
	cancelCmd := m.cancelPendingDetailTranscriptRequest()
	overlayCmd := m.popRollbackOverlay()
	m.stopRollbackSelectionMode()
	return sequenceCmds(cancelCmd, overlayCmd)
}

func (c uiInputController) beginRollbackEditingFlowCmd() tea.Cmd {
	c.model.beginRollbackEditing()
	return nil
}

func (c uiInputController) cancelRollbackEditingToSelectionFlowCmd() tea.Cmd {
	c.model.cancelRollbackEditingBackToSelection()
	return nil
}

func (c uiInputController) startRollbackFork(text string) (tea.Model, tea.Cmd) {
	m := c.model
	if m.rollback.editingCandidate == nil {
		return m, m.sendTransientStatusWithNoticeID(
			"Rollback target is unavailable",
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
	}
	m.nextForkRollbackTargetID = m.rollback.editingCandidate.RollbackTargetID
	m.nextSessionInitialPrompt = text
	m.clearInput()
	m.exitAction = UIActionForkRollback
	m.resetRollbackState()
	return m, tea.Quit
}

func (c uiInputController) startProcessListFlowCmd() tea.Cmd {
	m := c.model
	m.openProcessList()
	initialRefreshCmd := m.requestProcessListRefresh()
	refreshCmd := tea.Tick(processListRefreshInterval, func(time.Time) tea.Msg { return processListRefreshTickMsg{} })
	spinnerCmd := m.reconcileSpinnerTicking(false)
	if overlayCmd := m.activateSurface(uiSurfaceProcessList); overlayCmd != nil {
		return tea.Batch(overlayCmd, initialRefreshCmd, refreshCmd, spinnerCmd)
	}
	return tea.Batch(initialRefreshCmd, refreshCmd, spinnerCmd)
}

func (c uiInputController) stopProcessListFlowCmd() tea.Cmd {
	m := c.model
	overlayCmd := m.restoreTranscriptSurface()
	m.closeProcessList()
	spinnerCmd := m.reconcileSpinnerTicking(false)
	releaseCmd := m.releaseDeferredRuntimeSyncs()
	if overlayCmd != nil {
		return tea.Batch(overlayCmd, spinnerCmd, releaseCmd)
	}
	return tea.Batch(spinnerCmd, releaseCmd)
}

func (c uiInputController) markPendingCSIShiftEnter() {
	m := c.model
	m.pendingCSIShiftEnter = true
	m.pendingCSIShiftEnterAt = time.Now()
}

func (c uiInputController) clearPendingCSIShiftEnter() {
	m := c.model
	m.pendingCSIShiftEnter = false
	m.pendingCSIShiftEnterAt = time.Time{}
}

func (c uiInputController) normalizePendingCSIShiftEnterOnEnter() {
	m := c.model
	if !m.pendingCSIShiftEnter {
		return
	}
	if m.pendingCSIShiftEnterAt.IsZero() || time.Since(m.pendingCSIShiftEnterAt) > csiShiftEnterDedupWindow {
		c.clearPendingCSIShiftEnter()
		return
	}
	if strings.HasSuffix(m.input, "\n") {
		m.input = strings.TrimSuffix(m.input, "\n")
		m.inputCursor = -1
		m.refreshSlashCommandFilterFromInputWithAuth(true)
	}
	c.clearPendingCSIShiftEnter()
}
