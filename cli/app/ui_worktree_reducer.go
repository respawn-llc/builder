package app

import (
	"strings"

	"core/cli/app/internal/runtimeattach"
	"core/cli/app/internal/worktreeui"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

type uiWorktreeFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) worktreeReducer() uiWorktreeFeatureReducer {
	return uiWorktreeFeatureReducer{model: m}
}

func (r uiWorktreeFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
	switch msg := msg.(type) {
	case worktreeListDoneMsg:
		if !m.worktrees.open || msg.token != m.worktrees.refreshToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.worktrees.loading = false
		if msg.err != nil {
			m.worktrees.errorText = runtimeattach.FormatSubmissionError(msg.err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
		}
		m.worktrees.errorText = ""
		m.applyWorktreeListResponse(msg.resp)
		cmd := m.applyWorktreeIntent()
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, tea.Batch(cmd, m.reconcileSpinnerTicking(false)))
	case worktreeCreateDoneMsg:
		if msg.token != m.worktrees.mutationToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		if m.worktrees.create.setupProgress != nil && m.worktrees.create.setupProgress.cancel != nil {
			m.worktrees.create.setupProgress.cancel()
		}
		m.worktrees.create.setupProgress = nil
		m.worktrees.create.submitting = false
		if msg.err != nil {
			if !m.worktrees.open {
				status := runtimeattach.FormatSubmissionError(msg.err)
				m.layout().syncViewport()
				return handledUIFeatureUpdate(m, m.sendTransientStatusWithNoticeID(status, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
			}
			m.worktrees.create.errorText = runtimeattach.FormatSubmissionError(msg.err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
		}
		var overlayCmd tea.Cmd
		if m.worktrees.open {
			overlayCmd = m.restoreTranscriptSurface()
			m.closeWorktreeOverlay()
		}
		status := "Created worktree " + strings.TrimSpace(msg.resp.Worktree.DisplayName)
		feedbackCmd := m.sendTransientStatusWithNoticeID(status, uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeReplace, "")
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, tea.Batch(overlayCmd, feedbackCmd, m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseWorktreeMutation)).cmd, m.reconcileSpinnerTicking(false)))
	case worktreeSetupEventMsg:
		if msg.token != m.worktrees.mutationToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		if msg.err != nil {
			m.worktrees.create.errorText = runtimeattach.FormatSubmissionError(msg.err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
		}
		event := msg.event
		m.worktrees.create.setupEvent = &event
		m.layout().syncViewport()
		if event.Phase == serverapi.WorktreeSetupPhaseCompleted || event.Phase == serverapi.WorktreeSetupPhaseFailed {
			return handledUIFeatureUpdate(m, nil)
		}
		return handledUIFeatureUpdate(m, worktreeSetupEventCmd(msg.events))
	case worktreeSwitchDoneMsg:
		if msg.token != m.worktrees.switchToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.worktrees.switchPending = false
		followUp := tea.Cmd(nil)
		if msg.err != nil {
			followUp = m.takeQueuedWorktreeSwitchCmd()
			if !m.worktrees.open {
				status := runtimeattach.FormatSubmissionError(msg.err)
				m.layout().syncViewport()
				return handledUIFeatureUpdate(m, tea.Batch(m.sendTransientStatusWithNoticeID(status, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""), followUp))
			}
			m.worktrees.errorText = runtimeattach.FormatSubmissionError(msg.err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, tea.Batch(followUp, m.reconcileSpinnerTicking(false)))
		}
		var overlayCmd tea.Cmd
		if m.worktrees.open {
			overlayCmd = m.restoreTranscriptSurface()
			m.closeWorktreeOverlay()
		}
		status := "Switched to " + strings.TrimSpace(msg.resp.Worktree.DisplayName)
		feedbackCmd := m.sendTransientStatusWithNoticeID(status, uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeReplace, "")
		followUp = m.takeQueuedWorktreeSwitchCmd()
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, tea.Batch(overlayCmd, feedbackCmd, m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseWorktreeMutation)).cmd, followUp, m.reconcileSpinnerTicking(false)))
	case worktreeDeleteDoneMsg:
		if msg.token != m.worktrees.mutationToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.worktrees.deleteConfirm.submitting = false
		if msg.err != nil {
			if !m.worktrees.open {
				status := runtimeattach.FormatSubmissionError(msg.err)
				m.layout().syncViewport()
				return handledUIFeatureUpdate(m, m.sendTransientStatusWithNoticeID(status, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
			}
			m.worktrees.deleteConfirm.errorText = runtimeattach.FormatSubmissionError(msg.err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
		}
		var listCmd tea.Cmd
		if m.worktrees.open {
			m.closeWorktreeDialog()
			m.worktrees.selectedID = worktreeCreateRowID
			listCmd = m.requestWorktreeListCmd()
		}
		feedbackCmd := m.sendTransientStatusWithNoticeID(worktreeDeleteSuccessStatus(msg.resp), uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeReplace, "")
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, tea.Batch(feedbackCmd, listCmd, m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseWorktreeMutation)).cmd, m.reconcileSpinnerTicking(false)))
	case worktreeCreateTargetResolveDebounceMsg:
		if !m.worktrees.open || m.worktrees.phase != uiWorktreeOverlayPhaseCreate {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		state, outcome := worktreeui.DebounceReady(m.worktrees.create.resolveState(), msg.token, m.worktrees.create.branchTarget.Text())
		m.worktrees.create.applyResolveState(state)
		if outcome.Ignored || !outcome.Start {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, m.worktreeCreateTargetResolveCmd(outcome.Query, outcome.Token))
	case worktreeCreateTargetResolveDoneMsg:
		if !m.worktrees.open || m.worktrees.phase != uiWorktreeOverlayPhaseCreate {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		errorText := ""
		if msg.err != nil {
			errorText = runtimeattach.FormatSubmissionError(msg.err)
		}
		state, outcome := worktreeui.Done(m.worktrees.create.resolveState(), worktreeui.DoneInput{
			Token:         msg.token,
			CurrentQuery:  m.worktrees.create.branchTarget.Text(),
			ResponseQuery: msg.query,
			Resolution:    msg.resp.Resolution,
			HasError:      msg.err != nil,
			ErrorText:     errorText,
		})
		m.worktrees.create.applyResolveState(state)
		m.layout().syncViewport()
		if outcome.Submit {
			req, err := worktreeui.Request(m.worktrees.create.branchTarget.Text(), m.worktrees.create.baseRef.Text(), outcome.SubmitKind)
			if err != nil {
				m.worktrees.create.errorText = err.Error()
				m.layout().syncViewport()
				return handledUIFeatureUpdate(m, nil)
			}
			createCmd := m.worktreeCreateCmd(req)
			return handledUIFeatureUpdate(m, tea.Batch(createCmd, m.reconcileSpinnerTicking(false)))
		}
		return handledUIFeatureUpdate(m, nil)
	}
	return uiFeatureUpdateResult{}
}
