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
	submission := runtimestate.InputSubmissionUnlocked
	if m.isInputSubmitLocked() {
		submission = runtimestate.InputSubmissionLocked
	}
	return runtimestate.PendingInputState{
		Input:            m.input,
		PendingInjected:  m.pendingInjected,
		LockedInjectText: m.lockedInjectText,
		LockedInjectID:   m.lockedInjectID,
		Submission:       submission,
	}
}

func (a uiRuntimeAdapter) applyRuntimeEventReduction(reduction runtimestate.RuntimeEventReduction) tea.Cmd {
	m := a.model
	var cmd tea.Cmd
	if reduction.RunState.Err != nil {
		m.activity = uiActivityError
		cmd = m.sendTransientStatusWithNoticeID("invalid runtime lifecycle: "+reduction.RunState.Err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	} else if err := m.setRunLifecycle(reduction.RunState.State.Run); err != nil {
		m.activity = uiActivityError
		cmd = m.sendTransientStatusWithNoticeID("invalid runtime lifecycle: "+err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	m.setCompacting(reduction.RunState.State.Compaction.IsRunning())
	m.setReviewerRunning(reduction.RunState.State.Reviewer.IsRunning())
	m.setReviewerBlocking(reduction.RunState.State.Reviewer.IsBlocking())
	if reduction.RunState.ExternalRuntime != nil {
		m.setExternalRuntimeStatus(reduction.RunState.ExternalRuntime)
	}
	m.conversationFreshness = reduction.Conversation.State.Freshness
	m.reasoningStatusHeader = reduction.Reasoning.State.StatusHeader
	m.pendingInjected = reduction.PendingInput.State.PendingInjected
	for _, answer := range m.removeInjectedQueueItemsByIDs(reduction.PendingInput.ConsumedQueueItemIDs) {
		cmd = tea.Batch(cmd, m.answerQueuedApprovalCommentary(answer))
	}
	m.lockedInjectText = reduction.PendingInput.State.LockedInjectText
	m.lockedInjectID = reduction.PendingInput.State.LockedInjectID
	m.setInputSubmitLocked(reduction.PendingInput.State.Submission == runtimestate.InputSubmissionLocked)
	if reduction.PendingInput.RestoredText != "" {
		m.inputController().restoreInjectedTextIntoInput(reduction.PendingInput.RestoredText)
		cmd = tea.Batch(cmd, m.sendTransientStatusWithNoticeID("queued message was not submitted; restored to input", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	}
	switch reduction.PendingInput.DraftCommand {
	case runtimestate.RuntimePendingInputClearDraft:
		m.clearInput()
	}
	switch reduction.RunState.Activity {
	case runtimestate.RuntimeActivityRunning:
		m.activity = uiActivityRunning
		m.setBusy(true)
	case runtimestate.RuntimeActivityIdle:
		if m.externalRuntimeBusy() {
			m.activity = uiActivityRunning
			m.setBusy(true)
		} else {
			m.activity = uiActivityIdle
			m.setBusy(false)
			cmd = tea.Batch(cmd, m.releaseDeferredRuntimeSyncs())
		}
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
	m := a.model
	if m == nil || evt.Kind != clientui.EventRunStateChanged || evt.RunState == nil || evt.RunState.Lifecycle.IsRunning() {
		return nil
	}
	if evt.RunState.Status != clientui.RunStatusInterrupted {
		m.setPendingInterrupt(false)
		return nil
	}
	if m.hasPendingInterrupt() {
		return m.acknowledgePendingInterrupt()
	}
	m.activity = uiActivityInterrupted
	m.clearReviewerState()
	return nil
}

func (m *uiModel) acknowledgePendingInterrupt() tea.Cmd {
	if m == nil || !m.hasPendingInterrupt() {
		return nil
	}
	var cmd tea.Cmd
	if m.activeSubmit.restoreOnInterrupt && !m.activeSubmit.flushed {
		c := uiInputController{model: m}
		c.restoreSubmittedTextIntoInput(m.activeSubmit.text)
	}
	m.activeSubmit = activeSubmitState{}
	c := uiInputController{model: m}
	cmd = tea.Batch(c.releaseLockedInjectedInput(true), c.restorePendingInjectedIntoInput())
	c.restoreQueuedMessagesIntoInput()
	m.setPendingInterrupt(false)
	m.setBusy(false)
	m.activity = uiActivityInterrupted
	m.clearReviewerState()
	return cmd
}

func (a uiRuntimeAdapter) effectiveRuntimeTranscriptSync(evt clientui.Event, proposed runtimestate.RuntimeTranscriptSyncCommand) runtimestate.RuntimeTranscriptSyncCommand {
	if evt.Kind != clientui.EventConversationUpdated {
		return proposed
	}
	if !shouldRecoverCommittedTranscriptFromConversationUpdate(a.model, evt) {
		return runtimestate.RuntimeTranscriptSyncCommand{}
	}
	if proposed.Reason != runtimestate.RuntimeTranscriptSyncNone {
		return proposed
	}
	return runtimestate.RuntimeTranscriptSyncCommand{Reason: runtimestate.RuntimeTranscriptSyncCommittedAdvance}
}
