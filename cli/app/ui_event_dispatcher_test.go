package app

import (
	"testing"

	"core/shared/clientui"

	tea "github.com/charmbracelet/bubbletea"
)

func TestUIEventDispatcherDispatchesReadyRuntimeBatch(t *testing.T) {
	runtimeEvents := make(chan clientui.Event, 1)
	runtimeEvents <- clientui.Event{Kind: clientui.EventRunStateChanged}

	dispatcher := newUIEventDispatcher(runtimeEvents, nil, nil)
	message := dispatcher.wait()()
	dispatched, ok := message.(uiDispatchedEventMsg)
	if !ok {
		t.Fatalf("message type = %T, want uiDispatchedEventMsg", message)
	}
	batch, ok := dispatched.event.(uiDispatchedRuntimeBatch)
	if !ok {
		t.Fatalf("dispatched event type = %T, want uiDispatchedRuntimeBatch", dispatched.event)
	}
	if len(batch.events) != 1 || batch.events[0].Kind != clientui.EventRunStateChanged {
		t.Fatalf("runtime batch = %#v, want one run-state event", batch.events)
	}
}

func TestUIEventDispatcherPreservesRuntimeBatchFence(t *testing.T) {
	runtimeEvents := make(chan clientui.Event, 3)
	runtimeEvents <- clientui.Event{Kind: clientui.EventRunStateChanged}
	runtimeEvents <- clientui.Event{Kind: clientui.EventToolCallStarted}
	runtimeEvents <- clientui.Event{Kind: clientui.EventAssistantDelta}

	dispatcher := newUIEventDispatcher(runtimeEvents, nil, nil)
	first := dispatcher.wait()().(uiDispatchedEventMsg).event.(uiDispatchedRuntimeBatch)
	if len(first.events) != 2 {
		t.Fatalf("first runtime batch length = %d, want 2", len(first.events))
	}
	if first.events[0].Kind != clientui.EventRunStateChanged || first.events[1].Kind != clientui.EventToolCallStarted {
		t.Fatalf("first runtime batch = %#v, want non-fence events in source order", first.events)
	}

	second := dispatcher.wait()().(uiDispatchedEventMsg).event.(uiDispatchedRuntimeBatch)
	if len(second.events) != 1 || second.events[0].Kind != clientui.EventAssistantDelta {
		t.Fatalf("second runtime batch = %#v, want the prefetched fence", second.events)
	}
}

func TestUIEventDispatcherDispatchesPromptAndTranscriptVariants(t *testing.T) {
	t.Run("prompt", func(t *testing.T) {
		promptEvents := make(chan askEvent, 1)
		promptEvents <- askEvent{resolvedPromptID: "prompt-1"}

		message := newUIEventDispatcher(nil, nil, promptEvents).wait()()
		dispatched := message.(uiDispatchedEventMsg)
		prompt := dispatched.event.(uiDispatchedPromptEvent)
		if prompt.event.resolvedPromptID != "prompt-1" {
			t.Fatalf("prompt ID = %q, want prompt-1", prompt.event.resolvedPromptID)
		}
	})

	t.Run("transcript", func(t *testing.T) {
		transcriptEvents := make(chan ongoingTranscriptEvent, 1)
		transcriptEvents <- ongoingTranscriptEvent{Kind: ongoingTranscriptEventLoss}

		message := newUIEventDispatcher(nil, transcriptEvents, nil).wait()()
		dispatched := message.(uiDispatchedEventMsg)
		transcript := dispatched.event.(uiDispatchedTranscriptEvent)
		if transcript.event.Kind != ongoingTranscriptEventLoss {
			t.Fatalf("transcript event kind = %q, want %q", transcript.event.Kind, ongoingTranscriptEventLoss)
		}
	})
}

func TestUIEventDispatcherIgnoresClosedSourcesUntilAllSourcesClose(t *testing.T) {
	runtimeEvents := make(chan clientui.Event)
	close(runtimeEvents)
	transcriptEvents := make(chan ongoingTranscriptEvent)
	close(transcriptEvents)
	promptEvents := make(chan askEvent, 1)
	promptEvents <- askEvent{resolvedPromptID: "prompt-1"}

	dispatcher := newUIEventDispatcher(runtimeEvents, transcriptEvents, promptEvents)
	message := dispatcher.wait()()
	dispatched, ok := message.(uiDispatchedEventMsg)
	if !ok {
		t.Fatalf("message type = %T, want uiDispatchedEventMsg", message)
	}
	if _, ok := dispatched.event.(uiDispatchedPromptEvent); !ok {
		t.Fatalf("dispatched event type = %T, want uiDispatchedPromptEvent", dispatched.event)
	}

	close(promptEvents)
	if message := dispatcher.wait()(); message != nil {
		t.Fatalf("message after all sources closed = %T, want nil", message)
	}
}

func TestUIEventDispatcherBoundsRuntimeBatches(t *testing.T) {
	runtimeEvents := make(chan clientui.Event, 65)
	for index := 0; index < 65; index++ {
		runtimeEvents <- clientui.Event{Sequence: uint64(index + 1), Kind: clientui.EventRunStateChanged}
	}

	dispatcher := newUIEventDispatcher(runtimeEvents, nil, nil)
	first := dispatcher.wait()().(uiDispatchedEventMsg).event.(uiDispatchedRuntimeBatch)
	if len(first.events) != 64 {
		t.Fatalf("first runtime batch length = %d, want 64", len(first.events))
	}
	second := dispatcher.wait()().(uiDispatchedEventMsg).event.(uiDispatchedRuntimeBatch)
	if len(second.events) != 1 || second.events[0].Sequence != 65 {
		t.Fatalf("second runtime batch = %#v, want sequence 65", second.events)
	}
}

func TestUIModelInitConsumesRuntimeSourceThroughDispatcher(t *testing.T) {
	runtimeEvents := make(chan clientui.Event, 1)
	runtimeEvents <- clientui.Event{Kind: clientui.EventRunStateChanged}
	model := newUIModelDefaults(nil, runtimeEvents, nil)

	batch, ok := model.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatalf("Init message type is not tea.BatchMsg")
	}
	dispatches := 0
	for _, command := range batch {
		if command == nil {
			continue
		}
		if _, ok := command().(uiDispatchedEventMsg); ok {
			dispatches++
		}
	}
	if dispatches != 1 {
		t.Fatalf("dispatcher messages = %d, want 1", dispatches)
	}
	if len(runtimeEvents) != 0 {
		t.Fatalf("runtime source still contains %d events, want 0", len(runtimeEvents))
	}
}

func TestUIEventDispatcherReducerRearmsOneWaitForEveryVariant(t *testing.T) {
	t.Run("runtime", func(t *testing.T) {
		runtimeEvents := make(chan clientui.Event, 1)
		runtimeEvents <- clientui.Event{Kind: clientui.EventRunStateChanged}
		model := newUIModelDefaults(nil, runtimeEvents, nil)

		_, command := model.Update(uiDispatchedEventMsg{
			event: uiDispatchedRuntimeBatch{},
		})
		assertOneRearmedDispatcherMessage(t, command)
		if len(runtimeEvents) != 0 {
			t.Fatalf("runtime source still contains %d events, want 0", len(runtimeEvents))
		}
	})

	t.Run("prompt", func(t *testing.T) {
		promptEvents := make(chan askEvent, 1)
		promptEvents <- askEvent{resolvedPromptID: "next-prompt"}
		model := newUIModelDefaults(nil, nil, promptEvents)

		_, command := model.Update(uiDispatchedEventMsg{
			event: uiDispatchedPromptEvent{event: askEvent{resolvedPromptID: "current-prompt"}},
		})
		assertOneRearmedDispatcherMessage(t, command)
		if len(promptEvents) != 0 {
			t.Fatalf("prompt source still contains %d events, want 0", len(promptEvents))
		}
	})

	t.Run("transcript", func(t *testing.T) {
		transcriptEvents := make(chan ongoingTranscriptEvent, 1)
		transcriptEvents <- ongoingTranscriptEvent{Kind: ongoingTranscriptEventLoss}
		model := newUIModelDefaults(nil, nil, nil)
		model.eventDispatcher.transcriptEvents = transcriptEvents

		_, command := model.Update(uiDispatchedEventMsg{
			event: uiDispatchedTranscriptEvent{event: ongoingTranscriptEvent{Kind: ongoingTranscriptEventLoss}},
		})
		assertOneRearmedDispatcherMessage(t, command)
		if len(transcriptEvents) != 0 {
			t.Fatalf("transcript source still contains %d events, want 0", len(transcriptEvents))
		}
	})
}

func assertOneRearmedDispatcherMessage(t *testing.T, command tea.Cmd) {
	t.Helper()
	messages := collectCmdMessages(t, command)
	dispatches := 0
	for _, message := range messages {
		if _, ok := message.(uiDispatchedEventMsg); ok {
			dispatches++
		}
	}
	if dispatches != 1 {
		t.Fatalf("rearmed dispatcher messages = %d, want 1; messages = %#v", dispatches, messages)
	}
}
