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

type runtimeEventApplyResult struct {
	cmd             tea.Cmd
	awaitsHydration bool
	fatal           bool
}

func (a uiRuntimeAdapter) applyProjectedRuntimeEventsBatch(events []clientui.Event) runtimeEventApplyResult {
	cmds := make([]tea.Cmd, 0, len(events))
	fatal := false
	for _, evt := range events {
		result := a.applyProjectedRuntimeEvent(evt)
		cmds = append(cmds, result.cmd)
		if result.fatal {
			fatal = true
			break
		}
	}
	return runtimeEventApplyResult{cmd: batchCmds(cmds...), fatal: fatal}
}

func (a uiRuntimeAdapter) applyProjectedRuntimeEvent(evt clientui.Event) runtimeEventApplyResult {
	m := a.model
	if m == nil {
		return runtimeEventApplyResult{}
	}
	if runtimeEventHasReadModelPayload(evt) && evt.ReadModelVersion.Validate() != nil {
		decision := m.startRuntimeMainViewRefreshRequest(runtimeReadModelResetMainViewRefreshRequest())
		return runtimeEventApplyResult{cmd: tea.Batch(decision.cmd, m.sendTransientStatusWithNoticeID("invalid runtime read-model update ignored; refreshing session view", uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, ""))}
	}
	if runtimeEventHasReadModelPayload(evt) {
		switch m.acceptRuntimeReadModelVersion(evt.ReadModelVersion, false) {
		case runtimeReadModelVersionIgnore:
			return runtimeEventApplyResult{cmd: m.runtimeReadModelConflictDiagnosticCmd(evt)}
		case runtimeReadModelVersionRefresh:
			decision := m.startRuntimeMainViewRefreshRequest(runtimeReadModelResetMainViewRefreshRequest())
			return runtimeEventApplyResult{cmd: decision.cmd}
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
	}
	return runtimeEventApplyResult{cmd: batchCmds(cmds...)}
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
	if previousSessionID != "" && nextSessionID != "" && previousSessionID != nextSessionID && a.model.view.Mode() == tui.ModeDetail {
		a.model.detailTranscript.reset()
		return a.model.loadDetailTranscriptPageCmd(a.model.detailTranscript.requestedPageForDetailEntry())
	}
	return nil
}
