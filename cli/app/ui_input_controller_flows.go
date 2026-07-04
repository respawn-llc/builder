package app

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (c uiInputController) startRollbackSelectionFlowCmd() tea.Cmd {
	m := c.model
	if !m.startRollbackSelectionMode() {
		return nil
	}
	overlayCmd := m.pushRollbackOverlayIfNeeded()
	if overlayCmd != nil {
		m.applyRollbackSelectionHighlight()
		m.focusRollbackSelection()
		return overlayCmd
	}
	return m.suppressRollbackAlternateScrollIfNeeded()
}

func (c uiInputController) stopRollbackSelectionFlowCmd() tea.Cmd {
	m := c.model
	overlayCmd := m.popRollbackOverlay()
	alternateScrollCmd := m.restoreRollbackAlternateScrollIfNeeded()
	m.stopRollbackSelectionMode()
	if overlayCmd != nil {
		return sequenceCmds(alternateScrollCmd, overlayCmd)
	}
	return alternateScrollCmd
}

func (c uiInputController) beginRollbackEditingFlowCmd() tea.Cmd {
	m := c.model
	_, ok := m.beginRollbackEditing()
	if !ok {
		return nil
	}
	return nil
}

func (c uiInputController) cancelRollbackEditingToSelectionFlowCmd() tea.Cmd {
	m := c.model
	if !m.cancelRollbackEditingBackToSelection() {
		return nil
	}
	overlayCmd := m.pushRollbackOverlayIfNeeded()
	if overlayCmd != nil {
		m.applyRollbackSelectionHighlight()
		m.focusRollbackSelection()
		return overlayCmd
	}
	return m.suppressRollbackAlternateScrollIfNeeded()
}

func (c uiInputController) startRollbackFork(text string) (tea.Model, tea.Cmd) {
	m := c.model
	m.nextForkRollbackTargetID = ""
	m.nextSessionInitialPrompt = text
	m.clearInput()
	m.exitAction = UIActionForkRollback
	m.rollback.phase = uiRollbackPhaseInactive
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
