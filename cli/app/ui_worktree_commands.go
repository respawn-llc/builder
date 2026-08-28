package app

import (
	"strings"

	"core/shared/clientui"
	"core/shared/runtimeinput"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

const worktreeUsageText = "Usage: /wt | /wt status | /wt create | /wt new | /wt delete [target] | /wt remove [target] | /wt rm [target] | /wt switch <target> | /wt leave"

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
		if len(parts) < 2 {
			return m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", worktreeUsageText, ""), c.model.sendTransientStatusWithNoticeID(worktreeUsageText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
		}
		return c.handleWorktreeSwitchCommand(strings.Join(parts[1:], " "))
	case "leave":
		if len(parts) != 1 {
			return m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", worktreeUsageText, ""), c.model.sendTransientStatusWithNoticeID(worktreeUsageText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
		}
		return c.handleWorktreeTransitionCommand(runtimeinput.PendingWorkWorktreeTransition{
			Transition: runtimeinput.PendingWorkWorktreeTransitionLeave,
		})
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
	target := runtimeinput.NormalizePendingWorkArgument(token)
	return c.handleWorktreeTransitionCommand(runtimeinput.PendingWorkWorktreeTransition{
		Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
		Selector:   &target,
	})
}

func (c uiInputController) handleWorktreeTransitionCommand(transition runtimeinput.PendingWorkWorktreeTransition) (tea.Model, tea.Cmd) {
	m := c.model
	if err := transition.Validate(); err != nil {
		return m, sequenceCmds(c.model.appendLocalEntryWithNoticeID("error", worktreeUsageText, ""), c.model.sendTransientStatusWithNoticeID(worktreeUsageText, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	}
	if m.worktrees.switchPending {
		queued := transition
		m.worktrees.queuedTransition = &queued
		return m, nil
	}
	return m, m.worktreeTransitionCommand(transition)
}

func (m *uiModel) worktreeSwitchCommandForTarget(targetToken string) tea.Cmd {
	targetToken = runtimeinput.NormalizePendingWorkArgument(targetToken)
	return m.worktreeTransitionCommand(runtimeinput.PendingWorkWorktreeTransition{
		Transition: runtimeinput.PendingWorkWorktreeTransitionEnter,
		Selector:   &targetToken,
	})
}

func (m *uiModel) worktreeTransitionCommand(transition runtimeinput.PendingWorkWorktreeTransition) tea.Cmd {
	if m == nil {
		return nil
	}
	service := m.worktreeMutationService()
	m.worktrees.switchToken++
	switchToken := m.worktrees.switchToken
	m.worktrees.switchPending = true
	return func() tea.Msg {
		var ack serverapi.WorktreeScheduledAcknowledgement
		var err error
		switch transition.Transition {
		case runtimeinput.PendingWorkWorktreeTransitionEnter:
			ack, err = service.Enter(*transition.Selector)
		case runtimeinput.PendingWorkWorktreeTransitionLeave:
			ack, err = service.Leave()
		default:
			err = transition.Validate()
		}
		return worktreeSwitchDoneMsg{token: switchToken, transition: transition, ack: ack, err: err}
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
