package app

import (
	"core/cli/tui"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) refreshRollbackCandidates() {
	entries, baseOffset := m.rollbackCandidateEntries()
	previousAnchor := m.rollback.pendingSelectionAnchor
	previousDelta := m.rollback.pendingSelectionDelta
	candidates := make([]rollbackCandidate, 0)
	for idx, entry := range entries {
		if entry.Role != tui.TranscriptRoleUser {
			continue
		}
		targetID := entry.RollbackTargetID
		if targetID == "" {
			continue
		}
		candidates = append(candidates, rollbackCandidate{
			TranscriptIndex:  baseOffset + idx,
			RollbackTargetID: targetID,
			Text:             entry.Text,
		})
	}
	m.rollback.candidates = candidates
	if len(m.rollback.candidates) == 0 {
		m.rollback.selection = 0
		m.rollback.phase = uiRollbackPhaseInactive
		m.rollback.restoreTranscriptMode = ""
		m.rollback.selectedTranscriptEntry = -1
		m.rollback.selectedTargetID = ""
		m.rollback.pendingSelectionAnchor = -1
		m.rollback.pendingSelectionDelta = 0
		m.forwardToView(tui.SetSelectedTranscriptEntryMsg{Active: false, EntryIndex: -1, RefreshDetailSnapshot: false})
		if m.inputMode() == uiInputModeRollbackSelection || m.inputMode() == uiInputModeRollbackEdit {
			m.restorePrimaryInputMode()
		}
		return
	}
	if m.rollback.selection < 0 {
		m.rollback.selection = 0
	}
	if m.rollback.selection >= len(m.rollback.candidates) {
		m.rollback.selection = len(m.rollback.candidates) - 1
	}
	hasPendingPageSelection := previousAnchor >= 0 && previousDelta != 0
	if hasPendingPageSelection {
		m.rollback.pendingSelectionAnchor = -1
		m.rollback.pendingSelectionDelta = 0
	}
	if m.rollback.isSelecting() && hasPendingPageSelection {
		for idx, candidate := range m.rollback.candidates {
			if candidate.TranscriptIndex == previousAnchor {
				m.rollback.selection = idx + previousDelta
				if m.rollback.selection < 0 {
					m.rollback.selection = 0
				}
				if m.rollback.selection >= len(m.rollback.candidates) {
					m.rollback.selection = len(m.rollback.candidates) - 1
				}
				break
			}
		}
	}
	if m.rollback.isSelecting() {
		m.applyRollbackSelectionHighlight()
	}
}

func (m *uiModel) rollbackCandidateEntries() ([]tui.TranscriptEntry, int) {
	if m == nil {
		return nil, 0
	}
	if m.view.Mode() == tui.ModeDetail && m.detailTranscript.loaded {
		return m.detailTranscript.entries, m.detailTranscript.offset
	}
	return m.transcriptEntries, m.transcriptBaseOffset
}

func (m *uiModel) startRollbackSelectionMode() bool {
	m.refreshRollbackCandidates()
	if len(m.rollback.candidates) == 0 {
		return false
	}
	if m.rollback.selectedTranscriptEntry >= 0 {
		matched := -1
		for idx, candidate := range m.rollback.candidates {
			if candidate.TranscriptIndex == m.rollback.selectedTranscriptEntry {
				matched = idx
				break
			}
		}
		if matched >= 0 {
			m.rollback.selection = matched
		}
	} else {
		m.rollback.selection = len(m.rollback.candidates) - 1
	}
	m.rollback.phase = uiRollbackPhaseSelection
	m.rollback.selectedTranscriptEntry = -1
	m.rollback.selectedTargetID = ""
	m.rollback.pendingSelectionAnchor = -1
	m.rollback.pendingSelectionDelta = 0
	m.setInputMode(uiInputModeRollbackSelection)
	m.clearInput()
	m.applyRollbackSelectionHighlight()
	return true
}

func (m *uiModel) stopRollbackSelectionMode() {
	m.rollback.phase = uiRollbackPhaseInactive
	m.forwardToView(tui.SetSelectedTranscriptEntryMsg{Active: false, EntryIndex: -1, RefreshDetailSnapshot: false})
	m.forwardToView(tui.SetSelectedTranscriptEntryMsg{Active: false, EntryIndex: -1, RefreshDetailSnapshot: true})
	m.restorePrimaryInputMode()
}

func (m *uiModel) applyRollbackSelectionHighlight() {
	if !m.rollback.isSelecting() || len(m.rollback.candidates) == 0 {
		m.forwardToView(tui.SetSelectedTranscriptEntryMsg{Active: false, EntryIndex: -1, RefreshDetailSnapshot: false})
		return
	}
	candidate := m.rollback.candidates[m.rollback.selection]
	m.forwardToView(tui.SetSelectedTranscriptEntryMsg{Active: true, EntryIndex: candidate.TranscriptIndex, RefreshDetailSnapshot: false})
	m.focusRollbackSelection()
}

func (m *uiModel) focusRollbackSelection() {
	if !m.rollback.isSelecting() || len(m.rollback.candidates) == 0 {
		return
	}
	candidate := m.rollback.candidates[m.rollback.selection]
	m.forwardToView(tui.FocusTranscriptEntryMsg{EntryIndex: candidate.TranscriptIndex, Center: true})
}

func (m *uiModel) moveRollbackSelection(delta int) {
	if len(m.rollback.candidates) == 0 {
		return
	}
	m.rollback.selection += delta
	if m.rollback.selection < 0 {
		m.rollback.selection = 0
	}
	if m.rollback.selection >= len(m.rollback.candidates) {
		m.rollback.selection = len(m.rollback.candidates) - 1
	}
	m.applyRollbackSelectionHighlight()
}

func (m *uiModel) requestRollbackSelectionPage(delta int) tea.Cmd {
	return nil
}

func (m *uiModel) beginRollbackEditing() (int, bool) {
	if !m.rollback.isSelecting() || len(m.rollback.candidates) == 0 {
		return -1, false
	}
	selected := m.rollback.candidates[m.rollback.selection]
	m.rollback.selectedTranscriptEntry = selected.TranscriptIndex
	m.rollback.selectedTargetID = selected.RollbackTargetID
	m.rollback.phase = uiRollbackPhaseEditing
	m.setInputMode(uiInputModeRollbackEdit)
	m.replaceMainInput(selected.Text, -1)
	m.forwardToView(tui.SetSelectedTranscriptEntryMsg{Active: true, EntryIndex: selected.TranscriptIndex, RefreshDetailSnapshot: false})
	return selected.TranscriptIndex, true
}

func (m *uiModel) cancelRollbackEditingBackToSelection() bool {
	if !m.rollback.isEditing() {
		return false
	}
	m.rollback.phase = uiRollbackPhaseSelection
	m.setInputMode(uiInputModeRollbackSelection)
	m.replaceMainInput("", -1)
	if m.rollback.selection < 0 {
		m.rollback.selection = 0
	}
	if m.rollback.selection >= len(m.rollback.candidates) {
		m.rollback.selection = len(m.rollback.candidates) - 1
	}
	m.applyRollbackSelectionHighlight()
	return len(m.rollback.candidates) > 0
}

func (m *uiModel) pushRollbackOverlayIfNeeded() tea.Cmd {
	if m.surface() == uiSurfaceRollbackSelection {
		return nil
	}
	if m.rollback.restoreTranscriptMode == "" {
		m.rollback.restoreTranscriptMode = m.view.Mode()
	}
	if m.view.Mode() == tui.ModeOngoing {
		surfaceCmd := m.activateSurface(uiSurfaceRollbackSelection)
		transitionCmd := m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{
			target:            tui.ModeDetail,
			suppressAltScreen: true,
			preserveSurface:   true,
		})
		return sequenceCmds(surfaceCmd, transitionCmd)
	}
	return m.activateSurface(uiSurfaceRollbackSelection)
}

func (m *uiModel) suppressRollbackAlternateScrollIfNeeded() tea.Cmd {
	if m == nil || m.rollback.suppressedAlternateScroll {
		return nil
	}
	if m.view.Mode() != tui.ModeDetail || !m.altScreenActive {
		return nil
	}
	m.rollback.suppressedAlternateScroll = true
	return disableAlternateScrollCmd()
}

func (m *uiModel) restoreRollbackAlternateScrollIfNeeded() tea.Cmd {
	if m == nil || !m.rollback.suppressedAlternateScroll {
		return nil
	}
	m.rollback.suppressedAlternateScroll = false
	if m.view.Mode() != tui.ModeDetail || !m.altScreenActive {
		return nil
	}
	return enableAlternateScrollCmd()
}

func (m *uiModel) popRollbackOverlay() tea.Cmd {
	if m.surface() != uiSurfaceRollbackSelection {
		return nil
	}
	m.rollback.suppressedAlternateScroll = false
	restoreMode := m.rollback.restoreTranscriptMode
	m.rollback.restoreTranscriptMode = ""
	if restoreMode == "" {
		restoreMode = m.view.Mode()
	}
	surfaceCmd := m.activateSurface(surfaceForTranscriptMode(restoreMode))
	transitionCmd := tea.Cmd(nil)
	if restoreMode != m.view.Mode() {
		transitionCmd = m.transitionTranscriptModeWithOptions(transcriptModeTransitionOptions{
			target:            restoreMode,
			suppressAltScreen: true,
			preserveSurface:   true,
		})
	}
	return sequenceCmds(surfaceCmd, transitionCmd)
}
