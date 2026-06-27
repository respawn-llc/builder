package app

import (
	"strconv"
	"strings"

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
	case runtimeTranscriptRefreshedMsg:
		cmd := m.handleRuntimeTranscriptRefreshed(msg)
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case runtimeTranscriptRetryMsg:
		if msg.token != m.runtimeTranscriptRetry {
			m.layout().syncViewport()
			return handledUIFeatureUpdate(m, nil)
		}
		req := msg.req
		if req == (runtimeTranscriptSyncRequest{}) {
			req = runtimeTranscriptSyncRequestForPage(m.transcriptRequestForCurrentMode(), false, msg.syncCause, msg.recoveryCause)
		}
		cmd := m.startRuntimeTranscriptSyncRequest(req).cmd
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	case detailTranscriptLoadMsg:
		cmd := m.startRuntimeTranscriptSyncRequest(runtimeTranscriptSyncRequestForPage(m.transcriptRequestForCurrentMode(), true, runtimeTranscriptSyncCauseManualTranscriptRefresh, clientui.TranscriptRecoveryCauseNone)).cmd
		m.layout().syncViewport()
		return handledUIFeatureUpdate(m, cmd)
	}
	return uiFeatureUpdateResult{}
}

func splitRuntimeBatchAtAssistantDelta(events []clientui.Event) ([]clientui.Event, []clientui.Event, bool) {
	return events, nil, false
}
