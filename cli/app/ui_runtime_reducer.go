package app

import (
	"strconv"
	"strings"

	"core/cli/tui"
	"core/shared/clientui"
	"core/shared/transcriptdiag"

	tea "github.com/charmbracelet/bubbletea"
)

type uiRuntimeFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) runtimeReducer() uiRuntimeFeatureReducer {
	return uiRuntimeFeatureReducer{model: m}
}

func (r uiRuntimeFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
	switch msg := msg.(type) {
	case runtimeEventMsg:
		next, cmd := m.handleRuntimeEventBatch([]clientui.Event{msg.event})
		return handledUIFeatureUpdate(next, cmd)
	case runtimeEventBatchMsg:
		if msg.carry != nil {
			m.logTranscriptDiag(transcriptdiag.FormatLine("transcript.diag.client.runtime_batch_carry", map[string]string{
				"session_id":             strings.TrimSpace(m.sessionID),
				"mode":                   string(m.view.Mode()),
				"kind":                   string(msg.carry.Kind),
				"pending_runtime_events": strconv.Itoa(len(m.pendingRuntimeEvents) + 1),
			}))
			m.pendingRuntimeEvents = append([]clientui.Event{*msg.carry}, m.pendingRuntimeEvents...)
		}
		if head, tail, split := splitRuntimeBatchAtAssistantDelta(msg.events); split {
			m.pendingRuntimeEvents = append(append([]clientui.Event(nil), tail...), m.pendingRuntimeEvents...)
			msg.events = head
		}
		next, cmd := m.handleRuntimeEventBatch(msg.events)
		return handledUIFeatureUpdate(next, cmd)
	case runtimeConnectionStateChangedMsg:
		m.observeRuntimeRequestResult(msg.err)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, waitRuntimeConnectionStateChange(m.runtimeConnectionEvents))
	case runtimeReconnectWarningMsg:
		cmd := m.sendTransientStatusWithNoticeID(msg.text, uiStatusNoticeNeutral, transientStatusDuration, uiStatusNoticeReplace, "")
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, sequenceCmds(cmd, waitRuntimeReconnectWarning(m.runtimeReconnectWarning)))
	case runtimeMainViewRefreshedMsg:
		cmd := m.handleRuntimeMainViewRefreshed(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case detailTranscriptLoadMsg:
		cmd := m.handleDetailTranscriptLoad(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case tui.RequestDetailTranscriptPageMsg:
		cmd := m.loadDetailTranscriptPageCmd(msg.Request)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	}
	return uiFeatureUpdateResult{}
}

func (m *uiModel) handleDetailTranscriptLoad(msg detailTranscriptLoadMsg) tea.Cmd {
	if msg.err != nil {
		return m.sendTransientStatusWithNoticeID(msg.err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	if !m.detailTranscriptResponseCurrent(msg.sessionID, msg.page.SessionID) {
		return nil
	}
	if pageRequestEqual(m.detailTranscript.lastRequest, msg.request) && m.detailTranscript.matchesPage(msg.page) {
		m.detailTranscript.refreshEdgeCursors(msg.page)
		return nil
	}
	anchor := tui.DetailTranscriptAnchorDefault
	if msg.request.NewerCursor != nil {
		m.detailTranscript.appendCursorPage(msg.page)
		anchor = tui.DetailTranscriptAnchorBottom
	} else if msg.request.Cursor != nil {
		m.detailTranscript.prependCursorPage(msg.page)
		anchor = tui.DetailTranscriptAnchorTop
	} else {
		m.detailTranscript.apply(msg.page)
	}
	m.detailTranscript.lastRequest = msg.request
	page := m.detailTranscript.page()
	page.SessionID = msg.page.SessionID
	page.SessionName = msg.page.SessionName
	page.ConversationFreshness = msg.page.ConversationFreshness
	m.forwardToView(tui.SetDetailTranscriptPageMsg{Page: page, Anchor: anchor})
	return nil
}

func (m *uiModel) detailTranscriptResponseCurrent(requestSessionID, responseSessionID string) bool {
	currentSessionID := strings.TrimSpace(m.currentRuntimeSessionID())
	requestSessionID = strings.TrimSpace(requestSessionID)
	responseSessionID = strings.TrimSpace(responseSessionID)
	if currentSessionID != "" && requestSessionID != "" && currentSessionID != requestSessionID {
		return false
	}
	if currentSessionID != "" && responseSessionID != "" && currentSessionID != responseSessionID {
		return false
	}
	return true
}

func splitRuntimeBatchAtAssistantDelta(events []clientui.Event) ([]clientui.Event, []clientui.Event, bool) {
	return events, nil, false
}
