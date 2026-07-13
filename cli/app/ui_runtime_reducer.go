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
		return handledUIFeatureUpdate(m, m.batchWithNativeOngoingRepaint(sequenceCmds(cmd, waitRuntimeReconnectWarning(m.runtimeReconnectWarning))))
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
	pending, ok := m.takePendingDetailTranscriptRequest(msg.requestID)
	if !ok {
		return nil
	}
	if msg.err == nil {
		msg.err = validateDetailTranscriptPageResponse(pending.sessionID, msg.page)
	}
	if msg.err != nil {
		m.rollbackDetailPageLoadFailed(pending.request)
		return m.sendTransientStatusWithNoticeID(msg.err.Error(), uiStatusNoticeError, transientStatusDuration, uiStatusNoticeReplace, "")
	}
	clearLoadingCmd := m.clearDetailTranscriptLoadingNotice(msg.requestID)
	if m.rollbackNavigationDeadlineExceeded(pending.request) {
		m.rollback.pendingNavigation = nil
		return m.sendTransientStatusWithNoticeID(
			errRollbackNavigationTimedOut.Error(),
			uiStatusNoticeError,
			transientStatusDuration,
			uiStatusNoticeReplace,
			"",
		)
	}
	if continuationCmd, consumed := m.continueRollbackNavigationAcrossCandidateFreePage(
		pending.request,
		msg.page,
	); consumed {
		if continuationCmd != nil {
			return continuationCmd
		}
		return clearLoadingCmd
	}
	m.applyDetailTranscriptLoad(pending.sessionID.String(), pending.request, msg.page)
	rollbackCmd := m.reconcileRollbackDetailPageLoad(pending.request)
	return sequenceCmds(clearLoadingCmd, rollbackCmd)
}

func (m *uiModel) applyDetailTranscriptLoad(requestSessionID string, request clientui.TranscriptPageRequest, responsePage clientui.TranscriptPage) {
	if !m.detailTranscriptResponseCurrent(requestSessionID, responsePage.SessionID) {
		return
	}
	if pageRequestEqual(m.detailTranscript.lastRequest, request) && m.detailTranscript.matchesPage(responsePage) {
		m.detailTranscript.refreshEdgeCursors(responsePage)
		return
	}
	anchor := tui.DetailTranscriptAnchorDefault
	prependedEntries := 0
	var trimmedFrontEntries []clientui.TranscriptCommittedRow
	if isolatedAnchor, ok := m.rollbackIsolatedPageAnchor(request); ok {
		m.detailTranscript.replace(responsePage)
		anchor = isolatedAnchor
	} else if request.NewerCursor != nil {
		result := m.detailTranscript.appendCursorPage(responsePage)
		trimmedFrontEntries = result.trimmedFrontEntries
		anchor = tui.DetailTranscriptAnchorPreserve
	} else if request.Cursor != nil {
		result := m.detailTranscript.prependCursorPage(responsePage)
		prependedEntries = result.addedEntries
		trimmedFrontEntries = result.trimmedFrontEntries
		anchor = tui.DetailTranscriptAnchorPreserve
	} else {
		preserveCachedPosition := m.detailTranscript.loaded &&
			!transcriptPageSessionChanged(m.detailTranscript.sessionID, responsePage.SessionID) &&
			!m.detailTranscript.hasMoreBelow &&
			m.detailTranscript.newerCursor == nil
		m.detailTranscript.replace(responsePage)
		if preserveCachedPosition {
			anchor = tui.DetailTranscriptAnchorRefresh
		} else {
			anchor = tui.DetailTranscriptAnchorBottom
		}
	}
	m.detailTranscript.lastRequest = request
	page := m.detailTranscript.page()
	page.SessionID = responsePage.SessionID
	page.SessionName = responsePage.SessionName
	page.ConversationFreshness = responsePage.ConversationFreshness
	m.forwardToView(tui.SetDetailTranscriptPageMsg{
		Page:                  page,
		Anchor:                anchor,
		PrependedEntriesCount: prependedEntries,
		TrimmedFrontEntries:   trimmedFrontEntries,
	})
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
