package app

import (
	tea "github.com/charmbracelet/bubbletea"
)

type uiDispatchedEventMsg struct {
	event ongoingTranscriptEvent
}

type uiEventDispatcher struct {
	transcriptEvents <-chan ongoingTranscriptEvent
}

func newUIEventDispatcher(transcriptEvents <-chan ongoingTranscriptEvent) *uiEventDispatcher {
	return &uiEventDispatcher{transcriptEvents: transcriptEvents}
}

func (d *uiEventDispatcher) wait() tea.Cmd {
	if d == nil || d.transcriptEvents == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-d.transcriptEvents
		if !ok {
			return nil
		}
		return uiDispatchedEventMsg{event: event}
	}
}

func (m *uiModel) reduceDispatchedEvent(message tea.Msg) uiFeatureUpdateResult {
	dispatched, ok := message.(uiDispatchedEventMsg)
	if !ok {
		return uiFeatureUpdateResult{}
	}
	result := m.reduceOngoingMessage(dispatched.event)
	return handledUIFeatureUpdate(result.model, tea.Batch(result.cmd, result.model.eventDispatcher.wait()))
}
