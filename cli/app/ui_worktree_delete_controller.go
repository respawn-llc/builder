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

func renderWorktreeDeleteButtons(width int, theme string, dialog uiWorktreeDeleteDialogState) []string {
	actions := worktreeui.DeleteActions(dialog.target)
	type deleteButton struct {
		action uiWorktreeDeleteAction
		option uiChoiceOption
	}
	buttons := make([]deleteButton, 0, len(actions))
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
		buttons = append(buttons, deleteButton{action: action, option: uiChoiceOption{Label: label}})
	}
	rows := make([][]deleteButton, 0, len(buttons))
	for _, button := range buttons {
		if len(rows) == 0 {
			rows = append(rows, []deleteButton{button})
			continue
		}
		last := rows[len(rows)-1]
		options := make([]uiChoiceOption, 0, len(last)+1)
		for _, existing := range last {
			options = append(options, existing.option)
		}
		options = append(options, button.option)
		if uiChoiceGroupWidth(uiChoiceGroupKindButton, options, " ") <= width {
			rows[len(rows)-1] = append(last, button)
			continue
		}
		rows = append(rows, []deleteButton{button})
	}
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		options := make([]uiChoiceOption, 0, len(row))
		selectedIndex := -1
		for _, button := range row {
			if button.action == dialog.selectedAction {
				selectedIndex = len(options)
			}
			options = append(options, button.option)
		}
		lines = append(lines, renderUIChoiceGroupLine(width, theme, uiChoiceGroupKindButton, options, selectedIndex))
	}
	return lines
}

func (c uiInputController) handleWorktreeDeleteDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := c.model
	dialog := &m.worktrees.deleteConfirm
	if strings.ToLower(msg.String()) == "ctrl+c" {
		return c.handleRuntimeCtrlC(c.closeTranscriptSurfaceForRuntimeCtrlC(m.closeWorktreeOverlay))
	}
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
