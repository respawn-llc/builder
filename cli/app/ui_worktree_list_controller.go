package app

import (
	"strings"

	"core/cli/app/internal/worktreeui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) worktreeRowCount() int {
	if m == nil {
		return 1
	}
	return worktreeui.RowCount(m.worktrees.entries)
}

func (m *uiModel) clampWorktreeSelection() {
	if m == nil {
		return
	}
	m.worktrees.selection = worktreeui.Clamp(m.worktrees.selection, m.worktrees.entries)
}

func (m *uiModel) moveWorktreeSelection(delta int) {
	if m == nil {
		return
	}
	m.worktrees.selection = worktreeui.Clamp(m.worktrees.selection+delta, m.worktrees.entries)
}

func (m *uiModel) moveWorktreeSelectionPage(deltaPages int) {
	rows := worktreeui.RowsPerPage(m.termHeight, worktreeOverlayHeaderLines, worktreeOverlayFooterLines, worktreeOverlayRowLines)
	m.moveWorktreeSelection(rows * deltaPages)
}

func (m *uiModel) selectFirstWorktreeRow() {
	if m == nil {
		return
	}
	m.worktrees.selection = 0
}

func (m *uiModel) selectLastWorktreeRow() {
	if m == nil {
		return
	}
	m.worktrees.selection = max(0, m.worktreeRowCount()-1)
}

func (m *uiModel) selectedWorktreeRow() (serverapi.WorktreeView, bool) {
	if m == nil {
		return serverapi.WorktreeView{}, false
	}
	return worktreeui.SelectedWorktree(m.worktrees.entries, m.worktrees.selection)
}

func (m *uiModel) selectedWorktreeID() string {
	if m == nil {
		return worktreeCreateRowID
	}
	return worktreeui.SelectedID(m.worktrees.entries, m.worktrees.selection)
}

func (m *uiModel) recordWorktreeSelection() {
	if m == nil {
		return
	}
	m.worktrees.selectedID = m.selectedWorktreeID()
}

func (m *uiModel) restoreWorktreeSelection() {
	if m == nil {
		return
	}
	m.worktrees.selection = worktreeui.Restore(m.worktrees.entries, m.worktrees.selection, m.worktrees.selectedID)
}

func (c uiInputController) startWorktreeOverlayCmd(intent uiWorktreeOpenIntent) tea.Cmd {
	m := c.model
	m.openWorktreeOverlay(intent)
	refreshCmd := m.requestWorktreeListCmd()
	spinnerCmd := m.reconcileSpinnerTicking(false)
	if overlayCmd := m.activateSurface(uiSurfaceWorktree); overlayCmd != nil {
		return tea.Batch(overlayCmd, refreshCmd, spinnerCmd)
	}
	return tea.Batch(refreshCmd, spinnerCmd)
}

func (c uiInputController) stopWorktreeOverlayCmd() tea.Cmd {
	m := c.model
	if m.worktrees.switchPending {
		return nil
	}
	overlayCmd := m.restoreTranscriptSurface()
	m.closeWorktreeOverlay()
	spinnerCmd := m.reconcileSpinnerTicking(false)
	if overlayCmd != nil {
		return tea.Batch(overlayCmd, spinnerCmd)
	}
	return spinnerCmd
}

func (c uiInputController) handleWorktreeOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := c.model
	if m.worktrees.phase == uiWorktreeOverlayPhaseCreate {
		return c.handleWorktreeCreateDialogKey(msg)
	}
	if m.worktrees.phase == uiWorktreeOverlayPhaseDeleteConfirm {
		return c.handleWorktreeDeleteDialogKey(msg)
	}
	switch strings.ToLower(msg.String()) {
	case "ctrl+c":
		if m.isBusy() {
			return m, c.interruptBusyRuntime()
		}
		m.exitAction = UIActionExit
		if overlayCmd := m.restoreTranscriptSurface(); overlayCmd != nil {
			m.closeWorktreeOverlay()
			return m, tea.Sequence(overlayCmd, tea.Quit)
		}
		return m, tea.Quit
	case "esc", "q":
		return m, c.stopWorktreeOverlayCmd()
	case "up", "k":
		m.moveWorktreeSelection(-1)
		return m, nil
	case "down", "j":
		m.moveWorktreeSelection(1)
		return m, nil
	case "pgup":
		m.moveWorktreeSelectionPage(-1)
		return m, nil
	case "pgdown":
		m.moveWorktreeSelectionPage(1)
		return m, nil
	case "home":
		m.selectFirstWorktreeRow()
		return m, nil
	case "end":
		m.selectLastWorktreeRow()
		return m, nil
	case "r":
		return m, tea.Batch(m.requestWorktreeListCmd(), m.reconcileSpinnerTicking(false))
	case "c", "n":
		return m, m.openCreateWorktreeDialog()
	case "d":
		target, ok := m.selectedWorktreeRow()
		if !ok {
			return m, c.model.sendTransientStatusWithNoticeID("Select a worktree to delete", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		if target.IsMain {
			return m, c.model.sendTransientStatusWithNoticeID("Main workspace is not deletable", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		m.worktrees.intent = uiWorktreeOpenIntent{OpenDelete: true, ConfirmDeleteTarget: target.WorktreeID}
		return m, tea.Batch(m.requestWorktreeListCmd(), m.reconcileSpinnerTicking(false))
	case "x":
		target, ok := m.selectedWorktreeRow()
		if !ok {
			return m, c.model.sendTransientStatusWithNoticeID("Select a worktree to delete", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		if target.IsMain {
			return m, c.model.sendTransientStatusWithNoticeID("Main workspace is not deletable", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		m.worktrees.intent = uiWorktreeOpenIntent{OpenDelete: true, ConfirmDeleteTarget: target.WorktreeID, PreferDeleteBranch: true}
		return m, tea.Batch(m.requestWorktreeListCmd(), m.reconcileSpinnerTicking(false))
	case "enter":
		if m.worktrees.selection == 0 {
			return m, m.openCreateWorktreeDialog()
		}
		target, ok := m.selectedWorktreeRow()
		if !ok {
			return m, nil
		}
		if target.IsCurrent {
			return m, c.model.sendTransientStatusWithNoticeID("Already current worktree", uiStatusNoticeNeutral, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		return m, m.worktreeSwitchCmd(target)
	default:
		return m, nil
	}
}
