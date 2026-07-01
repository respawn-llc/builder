package app

import (
	"strings"

	"core/cli/app/internal/worktreeui"

	tea "github.com/charmbracelet/bubbletea"
)

func (d *uiWorktreeDeleteDialogState) clampSelection() {
	if d == nil {
		return
	}
	d.selectedAction = worktreeui.ClampDeleteAction(d.target, d.selectedAction, d.preferDeleteBranch)
}

func (d *uiWorktreeDeleteDialogState) moveSelection(delta int) {
	if d == nil {
		return
	}
	d.selectedAction = worktreeui.MoveDeleteAction(d.target, d.selectedAction, delta)
}

type worktreeDeletePreviewLineKind = worktreeui.PreviewLineKind

const (
	worktreeDeletePreviewLineKindHeader  = worktreeui.PreviewLineKindHeader
	worktreeDeletePreviewLineKindBullet  = worktreeui.PreviewLineKindBullet
	worktreeDeletePreviewLineKindWarning = worktreeui.PreviewLineKindWarning
)

type worktreeDeletePreviewLine = worktreeui.PreviewLine

func renderWorktreeDeleteButtons(width int, theme string, dialog uiWorktreeDeleteDialogState) string {
	actions := worktreeui.DeleteActions(dialog.target)
	options := make([]uiChoiceOption, 0, len(actions))
	selectedIndex := 0
	for _, action := range actions {
		label := ""
		switch action {
		case uiWorktreeDeleteActionCancel:
			label = "Cancel"
		case uiWorktreeDeleteActionDelete:
			label = "Delete"
		case uiWorktreeDeleteActionDeleteBranch:
			label = "Delete + Branch"
		}
		if action == dialog.selectedAction {
			selectedIndex = len(options)
		}
		options = append(options, uiChoiceOption{Label: label})
	}
	return renderUIChoiceGroupLine(width, theme, uiChoiceGroupKindButton, options, selectedIndex)
}

func (c uiInputController) handleWorktreeDeleteDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := c.model
	dialog := &m.worktrees.deleteConfirm
	if dialog.submitting {
		return m, nil
	}
	switch strings.ToLower(msg.String()) {
	case "esc":
		m.closeWorktreeDialog()
		return m, nil
	case "tab", "right", "l":
		dialog.moveSelection(1)
		return m, nil
	case "shift+tab", "left", "h":
		dialog.moveSelection(-1)
		return m, nil
	case "enter":
		switch dialog.selectedAction {
		case uiWorktreeDeleteActionCancel:
			m.closeWorktreeDialog()
			return m, nil
		case uiWorktreeDeleteActionDelete, uiWorktreeDeleteActionDeleteBranch:
			deleteBranch := dialog.selectedAction == uiWorktreeDeleteActionDeleteBranch
			deleteCmd := m.worktreeDeleteCmd(dialog.target, deleteBranch)
			return m, tea.Batch(deleteCmd, m.reconcileSpinnerTicking(false))
		default:
			return m, nil
		}
	}
	return m, nil
}
