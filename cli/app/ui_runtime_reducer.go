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
		cmd := m.sendTransientStatusWithNoticeID(msg.text, uiStatusNoticeWarning, transientStatusDuration, uiStatusNoticeReplace, "")
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
		var (
			request clientui.TranscriptPageRequest
			ok      bool
		)
		switch msg.Direction {
		case tui.DetailTranscriptPageOlder:
			request, ok = m.detailTranscript.pageBefore()
		case tui.DetailTranscriptPageNewer:
			request, ok = m.detailTranscript.pageAfter()
		}
		if !ok {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		cmd := m.loadDetailTranscriptPageCmd(request)
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
	if msg.request.Cursor == nil && msg.request.NewerCursor == nil && m.detailTranscript.loaded && !transcriptPageSessionChanged(m.detailTranscript.sessionID, msg.page.SessionID) {
		m.detailTranscript.lastRequest = msg.request
		return nil
	}
	anchor := tui.DetailTranscriptAnchorDefault
	prependedEntries := 0
	var trimmedFrontEntries []clientui.ChatEntry
	if msg.request.NewerCursor != nil {
		result := m.detailTranscript.appendCursorPage(msg.page)
		trimmedFrontEntries = result.trimmedFrontEntries
		anchor = tui.DetailTranscriptAnchorPreserve
	} else if msg.request.Cursor != nil {
		result := m.detailTranscript.prependCursorPage(msg.page)
		prependedEntries = result.addedEntries
		trimmedFrontEntries = result.trimmedFrontEntries
		anchor = tui.DetailTranscriptAnchorPreserve
	} else {
		m.detailTranscript.apply(msg.page)
	}
	m.detailTranscript.lastRequest = msg.request
	page := m.detailTranscript.page()
	page.SessionID = msg.page.SessionID
	page.SessionName = msg.page.SessionName
	page.ConversationFreshness = msg.page.ConversationFreshness
	m.forwardToView(tui.SetDetailTranscriptPageMsg{
		Page:                  page,
		Anchor:                anchor,
		PrependedEntriesCount: prependedEntries,
		TrimmedFrontEntries:   trimmedFrontEntries,
	})
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
	if requestSessionID != "" && responseSessionID != "" && requestSessionID != responseSessionID {
		return false
	}
	return true
}

func splitRuntimeBatchAtAssistantDelta(events []clientui.Event) ([]clientui.Event, []clientui.Event, bool) {
	return events, nil, false
}
