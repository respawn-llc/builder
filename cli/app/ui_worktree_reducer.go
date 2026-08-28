package app

import (
	"errors"

	"core/cli/app/internal/runtimeattach"
	"core/cli/app/internal/worktreeui"
	"core/shared/clientui"
	"core/shared/invariant"
	"core/shared/runtimeinput"
	"core/shared/serverapi"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *uiModel) reconcileTranscriptWorktreeTransitionOutcome(outcome clientui.TranscriptWorktreeTransitionOutcome) tea.Cmd {
	if m == nil {
		return nil
	}
	var statusCmd tea.Cmd
	if outcome.State == clientui.WorktreeTransitionFailed {
		statusCmd = m.sendTransientStatusWithNoticeID(
			outcome.Failure.Detail,
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
	} else {
		statusCmd = m.sendTransientStatusWithNoticeID(
			"Worktree "+string(outcome.Transition)+" completed",
			uiStatusNoticeSuccess,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
	}
	refresh := m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseWorktreeMutation)).cmd
	if m.worktrees.open {
		return tea.Batch(statusCmd, refresh, m.requestWorktreeListCmd())
	}
	return tea.Batch(statusCmd, refresh)
}

func (m *uiModel) reduceWorktreeMessage(msg tea.Msg) uiFeatureUpdateResult {
	switch msg := msg.(type) {
	case worktreeListDoneMsg:
		if !m.worktrees.open || msg.token != m.worktreeListGeneration {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.worktrees.listPending = false
		if msg.err != nil {
			m.worktrees.errorText = runtimeattach.FormatSubmissionError(msg.err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
		}
		m.worktrees.errorText = ""
		if err := m.applyWorktreeListResponse(msg.resp); err != nil {
			m.worktrees.errorText = runtimeattach.FormatSubmissionError(err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
		}
		cmd := m.applyWorktreeIntent()
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, tea.Batch(cmd, m.reconcileSpinnerTicking(false)))
	case worktreeDeleteTargetResolvedMsg:
		if !m.worktrees.open ||
			m.worktrees.phase != uiWorktreeOverlayPhaseList ||
			msg.generation != m.deleteTargetResolutionGeneration {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.worktrees.deleteTargetResolutionPending = false
		if msg.err != nil {
			m.worktrees.errorText = runtimeattach.FormatSubmissionError(msg.err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
		}
		target, err := worktreeui.ProjectSelectorPreview(msg.resp)
		if err != nil {
			m.worktrees.errorText = runtimeattach.FormatSubmissionError(err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
		}
		if err := worktreeui.ValidateDeletionTarget(target); err != nil {
			m.worktrees.errorText = runtimeattach.FormatSubmissionError(err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
		}
		targetIdentity, err := worktreeui.SelectionIdentityForItem(target)
		if err != nil {
			m.worktrees.errorText = runtimeattach.FormatSubmissionError(err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
		}
		listedTarget, idx, ok, err := worktreeui.FindByIdentity(m.worktrees.entries, targetIdentity)
		if err != nil {
			m.worktrees.errorText = runtimeattach.FormatSubmissionError(err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
		}
		if ok {
			target.IsCurrent = listedTarget.IsCurrent
			target.Entry.Projection.IsCurrent = listedTarget.Entry.Projection.IsCurrent
			m.worktrees.entries[idx] = target
			m.worktrees.selection = idx + 1
			if err := m.recordWorktreeSelection(); err != nil {
				m.worktrees.errorText = runtimeattach.FormatSubmissionError(err)
				m.layout().syncViewport()
				return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
			}
		}
		m.openDeleteWorktreeDialog(
			target,
			msg.preferDeleteBranch,
			uiWorktreeDeleteTargetAuthorityResolvedSelector,
		)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
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
			m.applyWorktreeCreateError(msg.err)
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
		}
		var overlayCmd tea.Cmd
		if m.worktrees.open {
			overlayCmd = m.restoreTranscriptSurface()
			m.closeWorktreeOverlay()
		}
		created, err := worktreeui.ProjectItem(msg.resp.Worktree)
		if err != nil {
			status := "invalid created worktree response: " + err.Error()
			feedbackCmd := m.sendTransientStatusWithNoticeID(status, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, tea.Batch(overlayCmd, feedbackCmd, m.reconcileSpinnerTicking(false)))
		}
		status := "Created worktree " + worktreeui.DisplayName(created)
		feedbackCmd := m.sendTransientStatusWithNoticeID(status, uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeReplace, "")
		targetToken, err := worktreeui.StableMutationSelector(created)
		if err != nil {
			status = "Created worktree " + worktreeui.DisplayName(created) + " but could not select it: " + err.Error()
			feedbackCmd = m.sendTransientStatusWithNoticeID(status, uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, tea.Batch(overlayCmd, feedbackCmd, m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseWorktreeMutation)).cmd, m.reconcileSpinnerTicking(false)))
		}
		enterCmd := m.worktreeSwitchCommandForTarget(targetToken)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, tea.Batch(overlayCmd, feedbackCmd, enterCmd, m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseWorktreeMutation)).cmd, m.reconcileSpinnerTicking(false)))
	case worktreeSetupEventMsg:
		if msg.token != m.worktrees.mutationToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		if msg.err != nil {
			m.applyWorktreeCreateError(msg.err)
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
			followUp = m.takeQueuedWorktreeTransitionCmd()
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
		status := "Scheduled worktree leave"
		if msg.transition.Transition == runtimeinput.PendingWorkWorktreeTransitionEnter {
			status = "Scheduled worktree switch to " + *msg.transition.Selector
		}
		feedbackCmd := m.sendTransientStatusWithNoticeID(status, uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeReplace, "")
		followUp = m.takeQueuedWorktreeTransitionCmd()
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, tea.Batch(overlayCmd, feedbackCmd, m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequestForCause(runtimeMainViewRefreshCauseWorktreeMutation)).cmd, followUp, m.reconcileSpinnerTicking(false)))
	case worktreeDeleteDoneMsg:
		if msg.token != m.worktrees.mutationToken {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		m.worktrees.deleteConfirm.submitting = false
		if msg.err != nil {
			var precondition *serverapi.WorktreeDeletePreconditionError
			if errors.As(msg.err, &precondition) {
				m.worktrees.deleteConfirm.forceFolderRemoval = true
				m.worktrees.deleteConfirm.errorText = worktreeDeleteForceConfirmation(precondition.DirtyState)
				m.layout().syncViewport()
				return handledUIFeatureUpdate(m, m.reconcileSpinnerTicking(false))
			}
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
			m.worktrees.selectedIdentity = worktreeui.SelectionIdentity{
				Kind: worktreeui.SelectionIdentityKindCreateRow,
			}
			listCmd = m.requestWorktreeListCmd()
		}
		feedbackCmd := m.sendTransientStatusWithNoticeID(worktreeDeleteSuccessStatus(msg.target, msg.resp), uiStatusNoticeSuccess, transientStatusDuration, uiStatusNoticeReplace, "")
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
				m.applyWorktreeCreateError(err)
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

type worktreeCreateErrorPlacement struct {
	owner      serverapi.WorktreeCreateErrorOwner
	diagnostic string
}

func (m *uiModel) applyWorktreeCreateError(err error) {
	if m == nil {
		return
	}
	placement := classifyWorktreeCreateError(err, worktreeCreateInvariantPolicy(m.debugMode))
	m.worktrees.create.baseRefErrorText = ""
	m.worktrees.create.errorText = ""
	if placement == nil {
		return
	}
	switch placement.owner {
	case serverapi.WorktreeCreateErrorOwnerBaseRef:
		m.worktrees.create.baseRefErrorText = placement.diagnostic
	case serverapi.WorktreeCreateErrorOwnerForm:
		m.worktrees.create.errorText = placement.diagnostic
	default:
		m.worktrees.create.errorText = placement.diagnostic
	}
}

func classifyWorktreeCreateError(err error, policy invariant.Policy) *worktreeCreateErrorPlacement {
	if err == nil {
		return nil
	}
	if contractErr := serverapi.ValidateWorktreeCreateErrorBoundary(err, "cli.worktree.create", policy); contractErr != nil {
		return &worktreeCreateErrorPlacement{
			owner:      serverapi.WorktreeCreateErrorOwnerForm,
			diagnostic: runtimeattach.FormatSubmissionError(contractErr),
		}
	}
	var typed *serverapi.WorktreeCreateError
	if errors.As(err, &typed) {
		if typed == nil {
			return &worktreeCreateErrorPlacement{
				owner:      serverapi.WorktreeCreateErrorOwnerForm,
				diagnostic: runtimeattach.FormatSubmissionError(err),
			}
		}
		switch typed.Owner {
		case serverapi.WorktreeCreateErrorOwnerBaseRef:
			return &worktreeCreateErrorPlacement{
				owner:      serverapi.WorktreeCreateErrorOwnerBaseRef,
				diagnostic: typed.Diagnostic,
			}
		case serverapi.WorktreeCreateErrorOwnerForm:
			return &worktreeCreateErrorPlacement{
				owner:      serverapi.WorktreeCreateErrorOwnerForm,
				diagnostic: typed.Diagnostic,
			}
		}
	}
	return &worktreeCreateErrorPlacement{
		owner:      serverapi.WorktreeCreateErrorOwnerForm,
		diagnostic: runtimeattach.FormatSubmissionError(err),
	}
}

func worktreeCreateInvariantPolicy(debugMode bool) invariant.Policy {
	mode := invariant.ModeDiagnostic
	if debugMode {
		mode = invariant.ModePanic
	}
	return invariant.NewPolicy(invariant.WithMode(mode))
}
