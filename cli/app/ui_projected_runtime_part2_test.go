package app

import (
	"context"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/shared/clientui"
	"core/shared/serverapi"
	"testing"
	"time"
)

func TestProjectRuntimeEventChannelStopsWhenRequested(t *testing.T) {
	src := make(chan runtime.Event, 1)
	stop := make(chan struct{})
	out := projectRuntimeEventChannel(src, nil, stop)

	src <- runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "first"}

	deadline := time.After(2 * time.Second)
	for len(out) == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for first projected event")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	first, ok := <-out
	if !ok {
		t.Fatal("projected runtime channel closed before first event")
	}
	if first.AssistantDelta != "first" {
		t.Fatalf("first projected delta = %q, want first", first.AssistantDelta)
	}

	sentSecond := make(chan struct{})
	go func() {
		src <- runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "second"}
		close(sentSecond)
	}()
	select {
	case <-sentSecond:
	case <-deadline:
		t.Fatal("timed out queueing second projected event")
	}

	close(stop)

	for {
		select {
		case _, ok := <-out:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for projected runtime channel to stop")
		}
	}
}

func TestProjectRuntimeEventChannelPublishesExplicitStreamGapAfterBridgeGap(t *testing.T) {
	bridge := runtimewire.NewEventBridge(1, nil)
	bridge.Publish(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "first"})
	bridge.Publish(runtime.Event{Kind: runtime.EventToolCallStarted, StepID: "step-1"})

	stop := make(chan struct{})
	out := projectRuntimeEventChannel(bridge.Events, bridge.GapEvents, stop)
	t.Cleanup(func() { close(stop) })

	deadline := time.After(2 * time.Second)
	events := make([]clientui.Event, 0, 2)
	for len(events) < 2 {
		select {
		case evt, ok := <-out:
			if !ok {
				t.Fatalf("projected runtime channel closed early after %d events", len(events))
			}
			events = append(events, evt)
		case <-deadline:
			t.Fatalf("timed out waiting for projected runtime events, got %+v", events)
		}
	}

	sawAssistantDelta := false
	sawRecovery := false
	for _, evt := range events {
		if evt.AssistantDelta == "first" {
			sawAssistantDelta = true
		}
		if evt.Kind == clientui.EventStreamGap {
			if evt.RecoveryCause != clientui.TranscriptRecoveryCauseStreamGap {
				t.Fatalf("expected bridge-gap recovery cause, got %+v", evt)
			}
			sawRecovery = true
		}
	}
	if !sawAssistantDelta || !sawRecovery {
		t.Fatalf("expected projected runtime channel to emit surviving event and recovery signal, got %+v", events)
	}
}

func TestWaitRuntimeEventTreatsStreamGapAsBatchFence(t *testing.T) {
	ch := make(chan clientui.Event, 2)
	ch <- clientui.Event{Kind: clientui.EventStreamGap, RecoveryCause: clientui.TranscriptRecoveryCauseStreamGap}
	ch <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "after"}

	raw := waitRuntimeEvent(ch)()
	msg, ok := raw.(runtimeEventBatchMsg)
	if !ok {
		t.Fatalf("expected runtimeEventBatchMsg, got %T", raw)
	}
	if len(msg.events) != 1 || msg.events[0].Kind != clientui.EventStreamGap {
		t.Fatalf("first batch = %+v, want stream gap only", msg.events)
	}
	if msg.carry != nil {
		t.Fatalf("did not expect stream gap to carry later event, got %+v", *msg.carry)
	}

	raw = waitRuntimeEvent(ch)()
	next, ok := raw.(runtimeEventBatchMsg)
	if !ok {
		t.Fatalf("expected second runtimeEventBatchMsg, got %T", raw)
	}
	if len(next.events) != 1 || next.events[0].AssistantDelta != "after" {
		t.Fatalf("second batch = %+v, want assistant delta", next.events)
	}
}

func TestSessionActivityEventsDoNotLogDiagnosticsWhenDisabled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := &sessionActivityTestSubscription{events: make(chan clientui.Event, 1)}
	lines := make([]string, 0, 1)
	out, stop := startSessionActivityEvents(ctx, sub, func(context.Context, uint64) (serverapi.SessionActivitySubscription, error) {
		return sub, nil
	}, func() bool { return false }, func(line string) {
		lines = append(lines, line)
	})
	defer stop()

	sub.events <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "hello"}
	select {
	case evt := <-out:
		if evt.AssistantDelta != "hello" {
			t.Fatalf("assistant delta = %q, want hello", evt.AssistantDelta)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for session activity event")
	}
	stop()
	if len(lines) != 0 {
		t.Fatalf("expected no diagnostics when disabled, got %q", lines)
	}
}

func TestBridgeGapHydratesTranscriptStateInProjectedUI(t *testing.T) {
	client := &refreshingRuntimeClient{
		transcripts: []clientui.TranscriptPage{{
			SessionID: "session-1",
			Revision:  7,
			Entries: []clientui.ChatEntry{
				{Role: "tool_call", Text: "pwd", ToolCallID: "call-1", ToolCall: &clientui.ToolCallMeta{ToolName: "shell", IsShell: true, Command: "pwd"}},
				{Role: "tool_result_ok", Text: "/tmp", ToolCallID: "call-1"},
			},
		}},
	}
	bridge := runtimewire.NewEventBridge(1, nil)
	// Overflow the bridge before starting the projector so recovery is deterministic.
	bridge.Publish(runtime.Event{Kind: runtime.EventAssistantDelta, AssistantDelta: "partial"})
	bridge.Publish(runtime.Event{Kind: runtime.EventToolCallStarted, StepID: "step-1"})

	stop := make(chan struct{})
	runtimeEvents := projectRuntimeEventChannel(bridge.Events, bridge.GapEvents, stop)
	t.Cleanup(func() { close(stop) })

	events := make([]clientui.Event, 0, 2)
	deadline := time.After(2 * time.Second)
	for len(events) < 2 {
		select {
		case evt, ok := <-runtimeEvents:
			if !ok {
				t.Fatalf("projected runtime channel closed early after %d events", len(events))
			}
			events = append(events, evt)
		case <-deadline:
			t.Fatalf("timed out waiting for projected runtime events, got %+v", events)
		}
	}

	m := newProjectedTestUIModel(client, closedProjectedRuntimeEvents(), nil)
	m.startupCmds = nil
	for _, evt := range events {
		next, cmd := m.Update(runtimeEventMsg{event: evt})
		m = next.(*uiModel)
		msgs := collectCmdMessages(t, cmd)
		for _, msg := range msgs {
			refresh, ok := msg.(runtimeTranscriptRefreshedMsg)
			if !ok {
				continue
			}
			if refresh.syncCause != runtimeTranscriptSyncCauseContinuityRecovery {
				t.Fatalf("bridge-gap sync cause = %q, want %q", refresh.syncCause, runtimeTranscriptSyncCauseContinuityRecovery)
			}
			if refresh.recoveryCause != clientui.TranscriptRecoveryCauseStreamGap {
				t.Fatalf("bridge-gap recovery cause = %q, want %q", refresh.recoveryCause, clientui.TranscriptRecoveryCauseStreamGap)
			}
			next, follow := m.Update(refresh)
			m = next.(*uiModel)
			_ = collectCmdMessages(t, follow)
		}
	}

	if got := client.calls; got != 1 {
		t.Fatalf("transcript refresh calls = %d, want 1", got)
	}
	if got := len(m.transcriptEntries); got != 2 {
		t.Fatalf("transcript entry count after recovery hydrate = %d, want 2", got)
	}
	if got := m.transcriptEntries[0].Role; got != "tool_call" {
		t.Fatalf("first transcript role after recovery hydrate = %q, want tool_call", got)
	}
	if got := m.transcriptEntries[0].ToolCallID; got != "call-1" {
		t.Fatalf("first transcript tool call id after recovery hydrate = %q, want call-1", got)
	}
	if got := m.transcriptEntries[1].Role; got != "tool_result_ok" {
		t.Fatalf("second transcript role after recovery hydrate = %q, want tool_result_ok", got)
	}
	loaded := m.view.LoadedTranscriptEntries()
	if len(loaded) != 2 || loaded[0].Role != "tool_call" || loaded[1].Role != "tool_result_ok" {
		t.Fatalf("expected hydrated tool transcript visible in view, got %+v", loaded)
	}
}
