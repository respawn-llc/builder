package app

import tea "github.com/charmbracelet/bubbletea"

func (m *uiModel) refreshRollbackCandidates() {
	m.rollback.selection = 0
	m.rollback.phase = uiRollbackPhaseInactive
	if m.inputMode() == uiInputModeRollbackSelection || m.inputMode() == uiInputModeRollbackEdit {
		m.restorePrimaryInputMode()
	}
}

func (m *uiModel) startRollbackSelectionMode() bool {
	m.refreshRollbackCandidates()
	return false
}

func (m *uiModel) stopRollbackSelectionMode() {
	m.rollback.phase = uiRollbackPhaseInactive
	m.restorePrimaryInputMode()
}

func (m *uiModel) applyRollbackSelectionHighlight() {}

func (m *uiModel) focusRollbackSelection() {}

func (m *uiModel) moveRollbackSelection(int) {}

func (m *uiModel) requestRollbackSelectionPage(int) tea.Cmd {
	return nil
}

func (m *uiModel) beginRollbackEditing() (int, bool) {
	return -1, false
}

func (m *uiModel) cancelRollbackEditingBackToSelection() bool {
	if !m.rollback.isEditing() {
		return false
	}
	m.rollback.phase = uiRollbackPhaseInactive
	m.restorePrimaryInputMode()
	m.replaceMainInput("", -1)
	return false
}

func (m *uiModel) pushRollbackOverlayIfNeeded() tea.Cmd {
	return nil
}

func (m *uiModel) suppressRollbackAlternateScrollIfNeeded() tea.Cmd {
	return nil
}

func (m *uiModel) restoreRollbackAlternateScrollIfNeeded() tea.Cmd {
	return nil
}

func (m *uiModel) popRollbackOverlay() tea.Cmd {
	return nil
}
