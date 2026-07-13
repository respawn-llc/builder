package app

import (
	"strings"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

const worktreeUsageText = "Usage: /wt | /wt status | /wt create | /wt new | /wt delete [target] | /wt remove [target] | /wt rm [target] | /wt switch <target>"

func (c uiInputController) handleWorktreeCommand(args string) (tea.Model, tea.Cmd) {
	m := c.model
	if m.worktreeClient == nil {
		errText := "worktree client is unavailable"
		return m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", errText, ""), c.model.sendTransientStatusWithNoticeID(errText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	}
	parts := strings.Fields(strings.TrimSpace(args))
	if len(parts) == 0 {
		return m, c.startWorktreeOverlayCmd(uiWorktreeOpenIntent{})
	}
	subcommand := strings.ToLower(strings.TrimSpace(parts[0]))
	switch subcommand {
	case "status":
		if len(parts) != 1 {
			return m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", worktreeUsageText, ""), c.model.sendTransientStatusWithNoticeID(worktreeUsageText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
		}
		return m, c.startWorktreeOverlayCmd(uiWorktreeOpenIntent{})
	case "new", "create":
		if len(parts) != 1 {
			return m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", worktreeUsageText, ""), c.model.sendTransientStatusWithNoticeID(worktreeUsageText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
		}
		return m, c.startWorktreeOverlayCmd(uiWorktreeOpenIntent{OpenCreate: true})
	case "switch":
		if len(parts) != 2 {
			return m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", worktreeUsageText, ""), c.model.sendTransientStatusWithNoticeID(worktreeUsageText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
		}
		return c.handleWorktreeSwitchCommand(parts[1])
	case "delete", "remove", "rm":
		if len(parts) > 2 {
			return m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", worktreeUsageText, ""), c.model.sendTransientStatusWithNoticeID(worktreeUsageText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
		}
		target := uiWorktreeDeleteIntentTarget{kind: uiWorktreeDeleteIntentTargetCurrent}
		if len(parts) == 2 {
			target = uiWorktreeDeleteIntentTarget{
				kind:     uiWorktreeDeleteIntentTargetSelector,
				selector: parts[1],
			}
		}
		return m, c.startWorktreeOverlayCmd(uiWorktreeOpenIntent{
			OpenDelete:   true,
			DeleteTarget: target,
		})
	default:
		return m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", worktreeUsageText, ""), c.model.sendTransientStatusWithNoticeID(worktreeUsageText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	}
}

func (c uiInputController) handleWorktreeSwitchCommand(token string) (tea.Model, tea.Cmd) {
	m := c.model
	target := strings.TrimSpace(token)
	if m.worktrees.switchPending {
		m.worktrees.queuedSwitch = uiWorktreeQueuedSwitch{TargetToken: target}
		return m, nil
	}
	return m, m.worktreeSwitchCommandForTarget(target)
}

func (m *uiModel) worktreeSwitchCommandForTarget(targetToken string) tea.Cmd {
	if m == nil {
		return nil
	}
	service := m.worktreeMutationService()
	m.worktrees.switchToken++
	switchToken := m.worktrees.switchToken
	m.worktrees.switchPending = true
	targetToken = strings.TrimSpace(targetToken)
	return func() tea.Msg {
		ack, err := service.Enter(targetToken)
		return worktreeSwitchDoneMsg{token: switchToken, target: targetToken, ack: ack, err: err}
	}
}

func (m *uiModel) suggestedWorktreeSessionName() string {
	if trimmed := strings.TrimSpace(m.sessionName); trimmed != "" {
		return trimmed
	}
	if cached, ok := m.runtimeClient().(interface {
		CachedMainView() (clientui.RuntimeMainView, bool)
	}); ok {
		if view, hasCached := cached.CachedMainView(); hasCached {
			return strings.TrimSpace(view.Session.SessionName)
		}
	}
	return ""
}
