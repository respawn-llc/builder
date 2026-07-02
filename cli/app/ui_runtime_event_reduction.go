package app

import (
	"core/cli/app/internal/runtimestate"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func shouldRefreshDeferredCommittedTailOnRunEnd(m *uiModel, evt clientui.Event) bool {
	if m == nil || !m.hasRuntimeClient() || len(m.deferredCommittedTail) == 0 {
		return false
	}
	if evt.Kind != clientui.EventRunStateChanged || evt.RunState == nil {
		return false
	}
	return !evt.RunState.Lifecycle.IsRunning()
}

func (a uiRuntimeAdapter) runtimeRunState() runtimestate.RuntimeRunState {
	m := a.model
	if err := m.runtimeLifecycle.Run.Validate(); err != nil {
		panic(err)
	}
	if err := m.runtimeLifecycle.Reviewer.Validate(); err != nil {
		panic(err)
	}
	return runtimestate.RuntimeRunState{
		Run:        m.runtimeLifecycle.Run,
		Compaction: m.runtimeLifecycle.Compaction,
		Reviewer:   m.runtimeLifecycle.Reviewer,
	}
}

func (a uiRuntimeAdapter) runtimeConversationState() runtimestate.RuntimeConversationState {
	return runtimestate.RuntimeConversationState{Freshness: a.model.conversationFreshness}
}

func (a uiRuntimeAdapter) runtimeReasoningState() runtimestate.RuntimeReasoningState {
	return runtimestate.RuntimeReasoningState{StatusHeader: a.model.reasoningStatusHeader}
}

func (a uiRuntimeAdapter) pendingInputState() runtimestate.PendingInputState {
	m := a.model
	return runtimestate.PendingInputState{
		PendingInjected: m.pendingInjected,
	}
}

func (a uiRuntimeAdapter) applyRuntimeEventReduction(reduction runtimestate.RuntimeEventReduction) tea.Cmd {
	m := a.model
	var cmd tea.Cmd
	if reduction.RunState.Err != nil {
		m.activity = uiActivityError
		cmd = m.sendTransientStatusWithNoticeID("invalid runtime lifecycle: "+reduction.RunState.Err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	} else if reduction.RunState.Activity != runtimestate.RuntimeActivityUnchanged {
		if reduction.RunState.RuntimeActivity == nil {
			m.runtimeLifecycle.Run = reduction.RunState.State.Run
		} else if err := m.applyRuntimeActivityProjection(*reduction.RunState.RuntimeActivity); err != nil {
			m.activity = uiActivityError
			cmd = m.sendTransientStatusWithNoticeID("invalid runtime activity: "+err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
		}
	}
	m.setCompacting(reduction.RunState.State.Compaction.IsRunning())
	m.setReviewerRunning(reduction.RunState.State.Reviewer.IsRunning())
	m.setReviewerBlocking(reduction.RunState.State.Reviewer.IsBlocking())
	m.conversationFreshness = reduction.Conversation.State.Freshness
	m.reasoningStatusHeader = reduction.Reasoning.State.StatusHeader
	m.pendingInjected = reduction.PendingInput.State.PendingInjected
	for _, answer := range m.removeInjectedQueueItemsByIDs(reduction.PendingInput.ConsumedQueueItemIDs) {
		cmd = tea.Batch(cmd, m.answerQueuedApprovalCommentary(answer))
	}
	if reduction.PendingInput.RestoredText != "" {
		m.inputController().restoreInjectedTextIntoInput(reduction.PendingInput.RestoredText)
		cmd = tea.Batch(cmd, m.sendTransientStatusWithNoticeID("queued message was not submitted; restored to input", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	}
	switch reduction.RunState.Activity {
	case runtimestate.RuntimeActivityRunning:
		if reduction.RunState.RuntimeActivity == nil && m.activity != uiActivityQuestion {
			m.activity = uiActivityRunning
		}
	case runtimestate.RuntimeActivityIdle:
		if reduction.RunState.RuntimeActivity == nil {
			m.activity = uiActivityIdle
		}
		cmd = tea.Batch(cmd, m.releaseDeferredRuntimeSyncs())
	}
	switch reduction.BackgroundProcesses.Command {
	case runtimestate.RuntimeBackgroundProcessRefresh:
		if m.processList.open {
			cmd = tea.Batch(cmd, m.requestProcessListRefresh())
		}
	}
	return cmd
}

func (a uiRuntimeAdapter) reconcileInterruptFromRunState(evt clientui.Event) tea.Cmd {
	return nil
}

func (a uiRuntimeAdapter) reconcileInterruptFromRuntimeActivity(evt clientui.Event) tea.Cmd {
	m := a.model
	if m == nil || evt.Kind != clientui.EventRuntimeActivityChanged || evt.RuntimeActivity == nil || evt.RuntimeActivity.ActiveForControl() {
		return nil
	}
	if m.hasPendingInterrupt() {
		if m.pendingInterruptNeedsInputReconciliation() {
			return m.requestInputReconciliationRefresh()
		}
		return m.acknowledgePendingInterrupt()
	}
	return nil
}

func (m *uiModel) acknowledgePendingInterrupt() tea.Cmd {
	if m == nil || !m.hasPendingInterrupt() {
		return nil
	}
	var cmd tea.Cmd
	restoreActiveSubmit, ambiguous := m.shouldRestoreActiveSubmitAfterInterrupt()
	if restoreActiveSubmit {
		c := uiInputController{model: m}
		c.restoreSubmittedTextIntoInput(m.activeSubmit.text)
	}
	m.activeSubmit = activeSubmitState{}
	m.clearPendingRuntimeOperations(
		clientui.RuntimeOperationKindSubmit,
		clientui.RuntimeOperationKindPreSubmitCompact,
		clientui.RuntimeOperationKindUserShell,
		clientui.RuntimeOperationKindCompact,
		clientui.RuntimeOperationKindSubmitQueued,
	)
	c := uiInputController{model: m}
	cmd = c.restorePendingInjectedIntoInput()
	c.restoreQueuedMessagesIntoInput()
	m.setPendingInterrupt(false)
	m.activity = uiActivityInterrupted
	m.clearReviewerState()
	if ambiguous {
		cmd = tea.Batch(cmd, m.sendTransientStatusWithNoticeID("runtime input state is unknown; restored local text for review", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	}
	return cmd
}

func (m *uiModel) shouldRestoreActiveSubmitAfterInterrupt() (bool, bool) {
	if m == nil || !m.activeSubmit.restoreOnInterrupt {
		return false, false
	}
	ref := m.activeSubmit.operationRef
	if err := ref.Validate(); err != nil {
		return true, true
	}
	view := m.cachedRuntimeMainView()
	for _, record := range view.InputReconciliation.Operations {
		if record.OperationRef != ref {
			continue
		}
		switch record.State {
		case clientui.RuntimeInputReconciliationCommitted, clientui.RuntimeInputReconciliationSubmitted:
			return false, false
		case clientui.RuntimeInputReconciliationCanceledNotCommitted, clientui.RuntimeInputReconciliationFailedWithRestore:
			return true, false
		case clientui.RuntimeInputReconciliationUnknown, clientui.RuntimeInputReconciliationEvicted:
			if m.activeSubmit.flushed {
				return false, false
			}
			return true, true
		}
	}
	if m.activeSubmit.flushed {
		return false, false
	}
	return true, true
}

func (m *uiModel) pendingInterruptNeedsInputReconciliation() bool {
	if !m.pendingInterruptBlocksRawInputCleanup() {
		return false
	}
	return m.canRequestInputReconciliationRefresh()
}

func (m *uiModel) pendingInterruptBlocksRawInputCleanup() bool {
	if m == nil || !m.hasPendingInterrupt() {
		return false
	}
	return m.pendingInterruptMissingInputReconciliation(m.cachedRuntimeMainView())
}

func (m *uiModel) pendingInterruptMissingInputReconciliation(view clientui.RuntimeMainView) bool {
	if m == nil {
		return false
	}
	ref := m.activeSubmit.operationRef
	if err := ref.Validate(); err != nil {
		return false
	}
	for _, record := range view.InputReconciliation.Operations {
		if record.OperationRef == ref {
			return false
		}
	}
	return true
}

func (m *uiModel) canRequestInputReconciliationRefresh() bool {
	if m == nil {
		return false
	}
	client := m.runtimeClient()
	if _, ok := client.(interface{ sessionRuntimeBoundary() }); !ok {
		return false
	}
	_, canRefresh := client.(runtimeMainViewReconciliationClient)
	_, canInterrupt := client.(runtimeInterruptReconciliationClient)
	return canRefresh && canInterrupt
}

func (m *uiModel) requestInputReconciliationRefresh() tea.Cmd {
	if !m.canRequestInputReconciliationRefresh() {
		return nil
	}
	return m.startRuntimeMainViewRefreshRequest(runtimeMainViewRefreshRequest{
		cause:    runtimeMainViewRefreshCauseManual,
		class:    runtimeSyncPolicyClassAllowed,
		priority: 100,
	}).cmd
}

func (a uiRuntimeAdapter) effectiveRuntimeTranscriptSync(evt clientui.Event, proposed runtimestate.RuntimeTranscriptSyncCommand) runtimestate.RuntimeTranscriptSyncCommand {
	if evt.Kind != clientui.EventConversationUpdated {
		return proposed
	}
	if a.model.nativeCommittedAdvanceRequiresContinuityBarrier(evt) {
		return runtimestate.RuntimeTranscriptSyncCommand{Reason: runtimestate.RuntimeTranscriptSyncCommittedAdvance}
	}
	if !shouldRecoverCommittedTranscriptFromConversationUpdate(a.model, evt) {
		return runtimestate.RuntimeTranscriptSyncCommand{}
	}
	if proposed.Reason != runtimestate.RuntimeTranscriptSyncNone {
		return proposed
	}
	return runtimestate.RuntimeTranscriptSyncCommand{Reason: runtimestate.RuntimeTranscriptSyncCommittedAdvance}
}
