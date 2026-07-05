package app

import (
	"core/cli/tui"
	"core/cli/tui/ongoing"

	tea "github.com/charmbracelet/bubbletea"
)

type uiOngoingFeatureReducer struct {
	model *uiModel
}

func (m *uiModel) ongoingReducer() uiOngoingFeatureReducer {
	return uiOngoingFeatureReducer{model: m}
}

func (r uiOngoingFeatureReducer) Update(msg tea.Msg) uiFeatureUpdateResult {
	m := r.model
	switch msg := msg.(type) {
	case ongoingTranscriptEvent:
		cmd := m.handleOngoingTranscriptEvent(msg)
		return handledUIFeatureUpdate(m, tea.Batch(cmd, waitOngoingTranscriptEvent(m.ongoingEvents)))
	case ongoingNormalBufferOwnedMsg:
		return handledUIFeatureUpdate(m, m.setOngoingNormalBufferOwned(msg.owned))
	case ongoingWidthRehydrationDebounceMsg:
		if msg.token != m.ongoingWidthToken {
			return handledUIFeatureUpdate(m, nil)
		}
		result := ongoing.Result{Action: ongoing.ResultRequestScratchRehydration, Reason: ongoing.RehydrateReasonWidthChange}
		return handledUIFeatureUpdate(m, m.handleOngoingResult(result))
	}
	return uiFeatureUpdateResult{}
}

func (m *uiModel) handleOngoingTranscriptEvent(event ongoingTranscriptEvent) tea.Cmd {
	if m == nil || m.ongoingTranscript == nil {
		return nil
	}
	var (
		result ongoing.Result
		err    error
	)
	switch event.Kind {
	case ongoingTranscriptEventMessage:
		result, err = m.ongoingTranscript.Accept(event.Message)
	case ongoingTranscriptEventLoss:
		result = m.ongoingTranscript.HandleSubscriptionLoss()
	default:
		if m.debugMode {
			panic("unknown ongoing transcript event kind")
		}
		return nil
	}
	if err != nil {
		return m.handleOngoingSurfaceError(err)
	}
	if event.Kind == ongoingTranscriptEventMessage && result.Action != ongoing.ResultRequestScratchRehydration {
		m.forwardToView(tui.ApplyTranscriptMessageMsg{Message: event.Message})
	}
	return m.handleOngoingResult(result)
}
