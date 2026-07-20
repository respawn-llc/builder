package app

import (
	tea "github.com/charmbracelet/bubbletea"
)

type uiAcceptedExternalEvent interface {
	uiAcceptedExternalEvent()
}

type uiAcceptedTranscriptEvent struct {
	event ongoingTranscriptEvent
}

func (uiAcceptedTranscriptEvent) uiAcceptedExternalEvent() {}

type uiAcceptedAttentionEvent struct {
	outcome attentionStreamOutcome
}

func (uiAcceptedAttentionEvent) uiAcceptedExternalEvent() {}

type uiDispatchedEventMsg struct {
	event uiAcceptedExternalEvent
}

type uiEventDispatcher struct {
	transcriptEvents       <-chan ongoingTranscriptEvent
	attentionEvents        <-chan attentionStreamOutcome
	requestAttentionReopen func()
}

func newUIEventDispatcher(transcriptEvents <-chan ongoingTranscriptEvent) *uiEventDispatcher {
	return &uiEventDispatcher{transcriptEvents: transcriptEvents}
}

func WithUIAttentionEvents(events <-chan attentionStreamOutcome, requestReopen func()) UIOption {
	return func(m *uiModelConstruction) {
		m.eventDispatcher.attentionEvents = events
		m.eventDispatcher.requestAttentionReopen = requestReopen
	}
}

func (d *uiEventDispatcher) wait() tea.Cmd {
	if d == nil || (d.transcriptEvents == nil && d.attentionEvents == nil) {
		return nil
	}
	return func() tea.Msg {
		transcriptEvents := d.transcriptEvents
		attentionEvents := d.attentionEvents
		for transcriptEvents != nil || attentionEvents != nil {
			select {
			case event, ok := <-transcriptEvents:
				if !ok {
					transcriptEvents = nil
					continue
				}
				return uiDispatchedEventMsg{event: uiAcceptedTranscriptEvent{event: event}}
			case outcome, ok := <-attentionEvents:
				if !ok {
					attentionEvents = nil
					continue
				}
				return uiDispatchedEventMsg{event: uiAcceptedAttentionEvent{outcome: outcome}}
			}
		}
		return nil
	}
}

func (m *uiModel) reduceDispatchedEvent(message tea.Msg) uiFeatureUpdateResult {
	dispatched, ok := message.(uiDispatchedEventMsg)
	if !ok {
		return uiFeatureUpdateResult{}
	}
	switch event := dispatched.event.(type) {
	case uiAcceptedTranscriptEvent:
		result := m.reduceOngoingMessage(event.event)
		return handledUIFeatureUpdate(result.model, tea.Batch(result.cmd, result.model.eventDispatcher.wait()))
	case uiAcceptedAttentionEvent:
		m.reduceAcceptedAttentionEvent(event.outcome)
		return handledUIFeatureUpdate(m, m.eventDispatcher.wait())
	default:
		if m.debugMode {
			panic("unknown accepted external event")
		}
		return handledUIFeatureUpdate(m, m.eventDispatcher.wait())
	}
}

func (m *uiModel) reduceAcceptedAttentionEvent(outcome attentionStreamOutcome) {
	m.reduceAcceptedAttentionEventWithNative(outcome, m.turnQueueHook)
}

func (m *uiModel) reduceAcceptedAttentionEventWithNative(outcome attentionStreamOutcome, native *bellHooks) {
	if m == nil {
		return
	}
	switch outcome := outcome.(type) {
	case *attentionFact:
		m.fanOutAcceptedAttentionFact(*outcome, native)
	case attentionStreamDiscontinuity:
		if reopen := m.eventDispatcher.requestAttentionReopen; reopen != nil {
			reopen()
		}
	}
}

type lifecycleAttentionFactSink interface {
	AcceptAttentionFact(attentionFact)
}

func (m *uiModel) fanOutAcceptedAttentionFact(fact attentionFact, native *bellHooks) {
	if native != nil {
		native.OnAttentionFact(fact)
	}
	if m.lifecycleAttention != nil {
		m.lifecycleAttention.AcceptAttentionFact(fact)
	}
}
