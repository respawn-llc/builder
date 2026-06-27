package app

import (
	"context"
	"io"
	"strings"
	"testing"

	"core/shared/clientui"
	"core/shared/serverapi"
)

func TestSessionActivityStreamGapHydratesThenRearmsOngoingRuntimeWait(t *testing.T) {
	useFastStreamResubscribeDelays(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{
		{evt: clientui.Event{Sequence: 41, Kind: clientui.EventAssistantDelta, AssistantDelta: "before restart"}},
		{err: io.EOF},
	}}
	recovered := &stubSessionActivitySubscription{steps: []stubSessionActivityStep{
		{evt: clientui.Event{Sequence: 1, Kind: clientui.EventAssistantDelta, AssistantDelta: "after restart"}},
	}}
	var requestedAfter []uint64
	runtimeEvents, stop := startSessionActivityEvents(ctx, initial, func(_ context.Context, afterSequence uint64) (serverapi.SessionActivitySubscription, error) {
		requestedAfter = append(requestedAfter, afterSequence)
		if afterSequence > 0 {
			return nil, serverapi.ErrStreamGap
		}
		return recovered, nil
	}, func() bool { return false }, nil)
	defer stop()

	client := &refreshingRuntimeClient{transcripts: []clientui.TranscriptPage{{
		SessionID: "session-1",
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "hydrated after restart"}},
	}}}
	m := newProjectedRuntimeEventsUIModel(client, runtimeEvents)
	m.startupCmds = nil
	m.termWidth = 90
	m.termHeight = 16
	m.windowSizeKnown = true
	m.layout().syncViewport()

	firstMsg, ok := m.waitRuntimeEventCmd()().(runtimeEventBatchMsg)
	if !ok {
		t.Fatalf("expected first runtime event batch")
	}
	next, followCmd := m.Update(firstMsg)
	m = next.(*uiModel)

	var gapMsg runtimeEventBatchMsg
	for _, msg := range collectCmdMessages(t, followCmd) {
		if typed, ok := msg.(runtimeEventBatchMsg); ok {
			gapMsg = typed
		}
	}
	if len(gapMsg.events) != 1 || gapMsg.events[0].Kind != clientui.EventStreamGap {
		t.Fatalf("expected stream-gap event from rearmed update command, got %+v", gapMsg.events)
	}
	next, hydrateCmd := m.Update(gapMsg)
	m = next.(*uiModel)
	if !m.waitRuntimeEventAfterHydration {
		t.Fatal("expected stream gap to pause runtime event consumption until hydration")
	}

	var refresh runtimeTranscriptRefreshedMsg
	for _, msg := range collectCmdMessages(t, hydrateCmd) {
		if typed, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			refresh = typed
		}
	}
	if refresh.token == 0 {
		t.Fatal("expected stream-gap hydration response")
	}
	next, resumeCmd := m.Update(refresh)
	m = next.(*uiModel)
	if m.waitRuntimeEventAfterHydration {
		t.Fatal("expected hydration to release runtime event wait fence")
	}

	var liveMsg runtimeEventBatchMsg
	for _, msg := range collectCmdMessages(t, resumeCmd) {
		if typed, ok := msg.(runtimeEventBatchMsg); ok {
			liveMsg = typed
		}
	}
	if len(liveMsg.events) != 1 || liveMsg.events[0].AssistantDelta != "after restart" {
		t.Fatalf("expected live event after hydration rearm, got %+v", liveMsg.events)
	}
	next, _ = m.Update(liveMsg)
	m = next.(*uiModel)
	if got := m.view.OngoingStreamingText(); !strings.Contains(got, "after restart") {
		t.Fatalf("expected live post-restart event to render in ongoing stream, got %q", got)
	}
	if len(requestedAfter) != 2 || requestedAfter[0] != 41 || requestedAfter[1] != 0 {
		t.Fatalf("resubscribe cursors = %+v, want [41 0]", requestedAfter)
	}
}
