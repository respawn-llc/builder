package app

import (
	"errors"
	"strings"

	"core/cli/app/internal/runtimeattach"
	"core/cli/app/internal/worktreeui"
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
	rows := worktreeui.RowsPerPage(m.layout().effectiveHeight(), worktreeOverlayHeaderLines, worktreeOverlayFooterLines, worktreeOverlayRowLines)
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

func (m *uiModel) selectedWorktreeRow() (worktreeui.Item, bool) {
	if m == nil {
		return worktreeui.Item{}, false
	}
	return worktreeui.SelectedWorktree(m.worktrees.entries, m.worktrees.selection)
}

func (m *uiModel) selectedWorktreeIdentity() (worktreeui.SelectionIdentity, error) {
	if m == nil {
		return worktreeui.SelectionIdentity{Kind: worktreeui.SelectionIdentityKindCreateRow}, nil
	}
	return worktreeui.SelectedIdentity(m.worktrees.entries, m.worktrees.selection)
}

func (m *uiModel) recordWorktreeSelection() error {
	if m == nil {
		return nil
	}
	identity, err := m.selectedWorktreeIdentity()
	if err != nil {
		return err
	}
	m.worktrees.selectedIdentity = identity
	return nil
}

func (m *uiModel) restoreWorktreeSelection() error {
	if m == nil {
		return nil
	}
	selection, err := worktreeui.Restore(m.worktrees.entries, m.worktrees.selection, m.worktrees.selectedIdentity)
	if err != nil {
		return err
	}
	m.worktrees.selection = selection
	return nil
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
		return c.handleRuntimeCtrlC(c.closeTranscriptSurfaceForRuntimeCtrlC(m.closeWorktreeOverlay))
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
		return c.startSelectedWorktreeDelete(false)
	case "x":
		return c.startSelectedWorktreeDelete(true)
	case "enter":
		if m.worktrees.selection == 0 {
			return m, m.openCreateWorktreeDialog()
		}
		target, ok := m.selectedWorktreeRow()
		if !ok {
			return m, nil
		}
		if target.IsCurrent {
			return m, c.model.sendTransientStatusWithNoticeID("Already current worktree", uiStatusNoticeInfo, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		return m, m.worktreeSwitchCmd(target)
	default:
		return m, nil
	}
}

func (c uiInputController) startSelectedWorktreeDelete(preferDeleteBranch bool) (tea.Model, tea.Cmd) {
	m := c.model
	target, ok := m.selectedWorktreeRow()
	if !ok {
		return m, c.model.sendTransientStatusWithNoticeID("Select a worktree to delete", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	if err := worktreeui.ValidateDeletionTarget(target); err != nil {
		if errors.Is(err, worktreeui.ErrMainWorkspaceNotDeletable) {
			return m, c.model.sendTransientStatusWithNoticeID("Main workspace is not deletable", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
		status := runtimeattach.FormatSubmissionError(err)
		return m, c.model.sendTransientStatusWithNoticeID(status, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	identity, err := worktreeui.SelectionIdentityForItem(target)
	if err != nil {
		status := runtimeattach.FormatSubmissionError(err)
		return m, c.model.sendTransientStatusWithNoticeID(status, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	m.invalidateWorktreeDeleteTargetResolution()
	m.worktrees.intent = uiWorktreeOpenIntent{
		OpenDelete: true,
		DeleteTarget: uiWorktreeDeleteIntentTarget{
			kind:     uiWorktreeDeleteIntentTargetIdentity,
			identity: identity,
		},
		PreferDeleteBranch: preferDeleteBranch,
	}
	return m, tea.Batch(m.requestWorktreeListCmd(), m.reconcileSpinnerTicking(false))
}
