package app

import (
	"strings"

	"core/cli/app/internal/runtimestate"
	"core/cli/tui"
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

type uiRuntimeAdapter struct {
	model *uiModel
}

func (a uiRuntimeAdapter) applyProjectedRuntimeEventsBatch(events []clientui.Event) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(events))
	for _, evt := range events {
		cmds = append(cmds, a.applyProjectedRuntimeEvent(evt))
	}
	return batchCmds(cmds...)
}

func (a uiRuntimeAdapter) applyProjectedRuntimeEvent(evt clientui.Event) tea.Cmd {
	m := a.model
	if m == nil {
		return nil
	}
	if runtimeEventHasReadModelPayload(evt) && evt.ReadModelVersion.Validate() != nil {
		decision := m.startRuntimeMainViewRefreshRequest(runtimeReadModelResetMainViewRefreshRequest())
		return tea.Batch(decision.cmd, m.sendTransientStatusWithNoticeID("invalid runtime read-model update ignored; refreshing session view", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))
	}
	if runtimeEventHasReadModelPayload(evt) {
		switch m.acceptRuntimeReadModelVersion(evt.ReadModelVersion, false) {
		case runtimeReadModelVersionIgnore:
			return m.runtimeReadModelConflictDiagnosticCmd(evt)
		case runtimeReadModelVersionRefresh:
			decision := m.startRuntimeMainViewRefreshRequest(runtimeReadModelResetMainViewRefreshRequest())
			return decision.cmd
		}
	}
	if m.turnQueueHook != nil {
		m.turnQueueHook.OnProjectedRuntimeEvent(evt)
	}
	reduction := runtimestate.ReduceRuntimeEvent(
		a.runtimeRunState(),
		a.runtimeConversationState(),
		a.pendingInputState(),
		a.runtimeReasoningState(),
		m.activity == uiActivityRunning,
		evt,
	)
	m.markActiveSubmitFlushed(evt)
	m.trackRuntimeActivityToken(evt)
	m.applyRuntimeEventStatus(evt)
	if !m.processList.open {
		m.applyBackgroundProcessEventToCache(evt.Background)
	}
	cmds := []tea.Cmd{
		a.applyRuntimeEventReduction(reduction),
		a.reconcileInterruptFromRunState(evt),
		a.reconcileInterruptFromRuntimeActivity(evt),
		a.reconcileWorktreeTransitionOutcome(evt),
	}
	return batchCmds(cmds...)
}

func (a uiRuntimeAdapter) applyProjectedSessionMetadata(session clientui.RuntimeSessionView) tea.Cmd {
	if a.model == nil {
		return nil
	}
	previousSessionID := strings.TrimSpace(a.model.sessionID)
	nextSessionID := strings.TrimSpace(session.SessionID)
	if nextSessionID != "" {
		a.model.sessionID = nextSessionID
	}
	if strings.TrimSpace(session.SessionName) != "" {
		a.model.sessionName = strings.TrimSpace(session.SessionName)
	}
	a.model.conversationFreshness = session.ConversationFreshness
	if previousSessionID != "" && nextSessionID != "" && previousSessionID != nextSessionID {
		rollbackCmd := a.model.discardRollbackStateForSessionReplacement()
		cancelCmd := a.model.cancelPendingDetailTranscriptRequest()
		a.model.detailTranscript.reset()
		resetCmd := a.model.forwardToView(tui.ResetDetailTranscriptMsg{})
		loadCmd := a.model.loadDetailTranscriptPageCmd(a.model.detailTranscript.requestedPageForDetailEntry())
		return sequenceCmds(rollbackCmd, cancelCmd, resetCmd, loadCmd)
	}
	return nil
}
