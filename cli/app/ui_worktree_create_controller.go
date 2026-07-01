package app

import (
	"strings"
	"time"

	"core/cli/app/internal/worktreeui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

var worktreeCreateResolveDebounce = 150 * time.Millisecond

type worktreeCreateTargetResolveDebounceMsg struct {
	token uint64
}

type worktreeCreateTargetResolveDoneMsg struct {
	token uint64
	query string
	resp  serverapi.WorktreeCreateTargetResolveResponse
	err   error
}

func (d *uiWorktreeCreateDialogState) syncFocus() {
	if d == nil {
		return
	}
	if d.resolution.Kind != serverapi.WorktreeCreateTargetResolutionKindNewBranch && d.focus == uiWorktreeCreateFieldBaseRef {
		d.focus = uiWorktreeCreateFieldBranchTarget
	}
}

func (d *uiWorktreeCreateDialogState) moveFocus(delta int) {
	if d == nil {
		return
	}
	d.focus = worktreeui.MoveField(d.focus, d.resolution.Kind, delta)
	if d.focus == uiWorktreeCreateFieldBranchTarget {
		d.branchTarget.SetCursor(len(d.branchTarget.Text()))
	}
	if d.focus == uiWorktreeCreateFieldBaseRef {
		d.baseRef.SetCursor(len(d.baseRef.Text()))
	}
}

func (d *uiWorktreeCreateDialogState) moveAction(delta int) {
	if d == nil {
		return
	}
	d.action = worktreeui.MoveCreateFormAction(d.action, delta)
}

func (d uiWorktreeCreateDialogState) resolveState() worktreeui.State {
	return worktreeui.State{
		ErrorText:     d.errorText,
		Resolving:     d.resolving,
		SubmitPending: d.submitPending,
		Token:         d.resolveToken,
		Resolution:    d.resolution,
	}
}

func (d *uiWorktreeCreateDialogState) applyResolveState(state worktreeui.State) {
	if d == nil {
		return
	}
	d.errorText = state.ErrorText
	d.resolving = state.Resolving
	d.submitPending = state.SubmitPending
	d.resolveToken = state.Token
	d.resolution = state.Resolution
	d.syncFocus()
}

func (d *uiWorktreeCreateDialogState) beginResolveSubmit(query string) (worktreeui.BeginSubmitOutcome, bool) {
	if d == nil {
		return worktreeui.BeginSubmitOutcome{}, false
	}
	state, outcome, err := worktreeui.BeginSubmit(d.resolveState(), query)
	d.applyResolveState(state)
	return outcome, err == nil
}

func (m *uiModel) scheduleWorktreeCreateTargetResolution() tea.Cmd {
	if m == nil || !m.worktrees.open || m.worktrees.phase != uiWorktreeOverlayPhaseCreate {
		return nil
	}
	dialog := &m.worktrees.create
	query := strings.TrimSpace(dialog.branchTarget.Text())
	state, outcome := worktreeui.Schedule(dialog.resolveState(), query)
	dialog.applyResolveState(state)
	if !outcome.Debounce {
		return nil
	}
	return tea.Tick(worktreeCreateResolveDebounce, func(time.Time) tea.Msg {
		return worktreeCreateTargetResolveDebounceMsg{token: outcome.Token}
	})
}

func (m *uiModel) worktreeCreateTargetResolveCmd(query string, token uint64) tea.Cmd {
	if m == nil || m.worktreeClient == nil {
		return nil
	}
	service := m.worktreeMutationService()
	return func() tea.Msg {
		resp, err := service.ResolveCreateTarget(query)
		return worktreeCreateTargetResolveDoneMsg{token: token, query: query, resp: resp, err: err}
	}
}

func (c uiInputController) handleWorktreeCreateDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m := c.model
	dialog := &m.worktrees.create
	if dialog.submitting {
		return m, nil
	}
	switch strings.ToLower(msg.String()) {
	case "esc":
		m.closeWorktreeDialog()
		return m, nil
	case "tab", "down":
		dialog.moveFocus(1)
		return m, nil
	case "shift+tab", "up":
		dialog.moveFocus(-1)
		return m, nil
	case "left", "h":
		if dialog.focus == uiWorktreeCreateFieldActions {
			dialog.moveAction(-1)
			return m, nil
		}
	case "right", "l":
		if dialog.focus == uiWorktreeCreateFieldActions {
			dialog.moveAction(1)
			return m, nil
		}
	case "enter":
		switch dialog.focus {
		case uiWorktreeCreateFieldActions:
			if dialog.action == uiWorktreeCreateActionCancel {
				m.closeWorktreeDialog()
				return m, nil
			}
			query := strings.TrimSpace(dialog.branchTarget.Text())
			outcome, ok := dialog.beginResolveSubmit(query)
			if !ok {
				return m, nil
			}
			return m, m.worktreeCreateTargetResolveCmd(outcome.Query, outcome.Token)
		default:
			dialog.moveFocus(1)
			return m, nil
		}
	}
	var cmd tea.Cmd
	var resolveCmd tea.Cmd
	switch dialog.focus {
	case uiWorktreeCreateFieldBaseRef:
		cmd = updateSingleLineEditorWithAppKeys(&dialog.baseRef, msg)
	case uiWorktreeCreateFieldBranchTarget:
		before := dialog.branchTarget.Text()
		cmd = updateSingleLineEditorWithAppKeys(&dialog.branchTarget, msg)
		if dialog.branchTarget.Text() != before {
			resolveCmd = m.scheduleWorktreeCreateTargetResolution()
		}
	default:
		return m, nil
	}
	if resolveCmd != nil {
		return m, tea.Batch(cmd, resolveCmd)
	}
	if dialog.focus == uiWorktreeCreateFieldBaseRef {
		dialog.errorText = ""
	}
	return m, cmd
}
