package app

import (
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

type uiDispatchedEvent interface {
	reduce(*uiModel) uiFeatureUpdateResult
}

type uiDispatchedRuntimeBatch struct {
	events []clientui.Event
}

func (event uiDispatchedRuntimeBatch) reduce(model *uiModel) uiFeatureUpdateResult {
	return model.runtimeReducer().Update(runtimeEventBatchMsg{events: event.events})
}

type uiDispatchedPromptEvent struct {
	event askEvent
}

func (event uiDispatchedPromptEvent) reduce(model *uiModel) uiFeatureUpdateResult {
	return model.askReducer().Update(askEventMsg{event: event.event})
}

type uiDispatchedTranscriptEvent struct {
	event ongoingTranscriptEvent
}

func (event uiDispatchedTranscriptEvent) reduce(model *uiModel) uiFeatureUpdateResult {
	return model.ongoingReducer().Update(event.event)
}

type uiDispatchedEventMsg struct {
	event uiDispatchedEvent
}

type uiEventDispatcher struct {
	runtimeEvents          <-chan clientui.Event
	transcriptEvents       <-chan ongoingTranscriptEvent
	promptEvents           <-chan askEvent
	prefetchedRuntimeEvent *clientui.Event
}

func newUIEventDispatcher(
	runtimeEvents <-chan clientui.Event,
	transcriptEvents <-chan ongoingTranscriptEvent,
	promptEvents <-chan askEvent,
) *uiEventDispatcher {
	return &uiEventDispatcher{
		runtimeEvents:    runtimeEvents,
		transcriptEvents: transcriptEvents,
		promptEvents:     promptEvents,
	}
}

func (d *uiEventDispatcher) wait() tea.Cmd {
	if d == nil {
		return nil
	}
	return func() tea.Msg {
		if d.prefetchedRuntimeEvent != nil {
			event := *d.prefetchedRuntimeEvent
			d.prefetchedRuntimeEvent = nil
			return uiDispatchedEventMsg{event: uiDispatchedRuntimeBatch{events: []clientui.Event{event}}}
		}
		runtimeEvents := d.runtimeEvents
		transcriptEvents := d.transcriptEvents
		promptEvents := d.promptEvents
		for runtimeEvents != nil || transcriptEvents != nil || promptEvents != nil {
			select {
			case event, ok := <-runtimeEvents:
				if !ok {
					runtimeEvents = nil
					continue
				}
				return uiDispatchedEventMsg{event: uiDispatchedRuntimeBatch{events: d.collectRuntimeBatch(event)}}
			case event, ok := <-transcriptEvents:
				if !ok {
					transcriptEvents = nil
					continue
				}
				return uiDispatchedEventMsg{event: uiDispatchedTranscriptEvent{event: event}}
			case event, ok := <-promptEvents:
				if !ok {
					promptEvents = nil
					continue
				}
				return uiDispatchedEventMsg{event: uiDispatchedPromptEvent{event: event}}
			}
		}
		return nil
	}
}

func (d *uiEventDispatcher) collectRuntimeBatch(first clientui.Event) []clientui.Event {
	events := []clientui.Event{first}
	if runtimeEventBatchFence(first) {
		return events
	}
	for len(events) < 64 {
		select {
		case next, ok := <-d.runtimeEvents:
			if !ok {
				return events
			}
			if runtimeEventBatchFence(next) {
				d.prefetchedRuntimeEvent = &next
				return events
			}
			events = append(events, next)
		default:
			return events
		}
	}
	return events
}

func runtimeEventBatchFence(event clientui.Event) bool {
	switch event.Kind {
	case clientui.EventStreamGap,
		clientui.EventConversationUpdated,
		clientui.EventAssistantDelta,
		clientui.EventReasoningDelta,
		clientui.EventStreamingErrorUpdated,
		clientui.EventAssistantDeltaReset,
		clientui.EventReasoningDeltaReset:
		return true
	default:
		return false
	}
}

type uiEventDispatcherReducer struct {
	model *uiModel
}

func (m *uiModel) eventDispatcherReducer() uiEventDispatcherReducer {
	return uiEventDispatcherReducer{model: m}
}

func (r uiEventDispatcherReducer) Update(message tea.Msg) uiFeatureUpdateResult {
	dispatched, ok := message.(uiDispatchedEventMsg)
	if !ok {
		return uiFeatureUpdateResult{}
	}
	result := dispatched.event.reduce(r.model)
	return handledUIFeatureUpdate(result.model, tea.Batch(result.cmd, result.model.eventDispatcher.wait()))
}
