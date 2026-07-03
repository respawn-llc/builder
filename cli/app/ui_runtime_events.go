package app

import (
	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func waitRuntimeEvent(ch <-chan clientui.Event) tea.Cmd {
	return func() tea.Msg {
		evt, ok := <-ch
		if !ok {
			return nil
		}
		events := []clientui.Event{evt}
		if runtimeEventBatchFence(evt) {
			return runtimeEventBatchMsg{events: events}
		}
		for len(events) < 64 {
			select {
			case next, ok := <-ch:
				if !ok {
					return runtimeEventBatchMsg{events: events}
				}
				if runtimeEventBatchFence(next) {
					carry := next
					return runtimeEventBatchMsg{events: events, carry: &carry}
				}
				events = append(events, next)
			default:
				return runtimeEventBatchMsg{events: events}
			}
		}
		return runtimeEventBatchMsg{events: events}
	}
}

func runtimeEventBatchFence(evt clientui.Event) bool {
	switch evt.Kind {
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
