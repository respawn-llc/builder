package app

import (
	"context"
	"core/cli/tui"
	"core/server/llm"
	"core/shared/clientui"
	"core/shared/serverapi"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type sessionActivityTestSubscription struct {
	events chan clientui.Event
}

func (s *sessionActivityTestSubscription) Next(ctx context.Context) (clientui.Event, error) {
	select {
	case <-ctx.Done():
		return clientui.Event{}, ctx.Err()
	case evt := <-s.events:
		return evt, nil
	}
}

func (s *sessionActivityTestSubscription) Close() error { return nil }

var _ serverapi.SessionActivitySubscription = (*sessionActivityTestSubscription)(nil)

func nextRuntimeEventBatch(t *testing.T, ch <-chan clientui.Event) runtimeEventBatchMsg {
	t.Helper()
	return runtimeEventBatchFromCmd(t, waitRuntimeEvent(ch), "runtime event wait")
}

func runtimeEventBatchFromCmd(t *testing.T, cmd tea.Cmd, description string) runtimeEventBatchMsg {
	t.Helper()
	if cmd == nil {
		t.Fatalf("expected %s command", description)
	}
	raw := cmd()
	msg, ok := raw.(runtimeEventBatchMsg)
	if !ok {
		t.Fatalf("expected runtimeEventBatchMsg for %s, got %T", description, raw)
	}
	return msg
}

func assertRuntimeEventBatchLen(t *testing.T, msg runtimeEventBatchMsg, want int) {
	t.Helper()
	if len(msg.events) != want {
		t.Fatalf("events len = %d, want %d", len(msg.events), want)
	}
}

func TestWaitRuntimeEventReturnsProjectedMessage(t *testing.T) {
	ch := make(chan clientui.Event, 1)
	ch <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "hello"}
	msg := nextRuntimeEventBatch(t, ch)
	assertRuntimeEventBatchLen(t, msg, 1)
	if msg.events[0].Kind != clientui.EventAssistantDelta || msg.events[0].AssistantDelta != "hello" {
		t.Fatalf("unexpected projected event: %+v", msg.events[0])
	}
}

func TestWaitRuntimeEventDrainsQueuedBatch(t *testing.T) {
	ch := make(chan clientui.Event, 3)
	ch <- clientui.Event{Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)}}
	ch <- clientui.Event{Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.IdleRunLifecycle()}}
	ch <- clientui.Event{Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)}}
	assertRuntimeEventBatchLen(t, nextRuntimeEventBatch(t, ch), 3)
}

func TestWaitRuntimeEventDoesNotCoalesceAssistantDeltas(t *testing.T) {
	ch := make(chan clientui.Event, 3)
	ch <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "hello"}
	ch <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: " world"}
	ch <- clientui.Event{Kind: clientui.EventRunStateChanged, RunState: &clientui.RunState{Lifecycle: clientui.MustRunLifecycle(clientui.RunLifecycleRunning, clientui.RunModeTurn)}}

	msg := nextRuntimeEventBatch(t, ch)
	assertRuntimeEventBatchLen(t, msg, 1)
	if msg.events[0].Kind != clientui.EventAssistantDelta || msg.events[0].AssistantDelta != "hello" {
		t.Fatalf("first streamed event = %+v, want first assistant delta", msg.events[0])
	}
	nextMsg := nextRuntimeEventBatch(t, ch)
	assertRuntimeEventBatchLen(t, nextMsg, 1)
	if nextMsg.events[0].Kind != clientui.EventAssistantDelta || nextMsg.events[0].AssistantDelta != " world" {
		t.Fatalf("second streamed event = %+v, want second assistant delta", nextMsg.events[0])
	}
}

func TestWaitRuntimeEventFencesTranscriptEventIntoCarry(t *testing.T) {
	ch := make(chan clientui.Event, 3)
	ch <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "hello"}
	ch <- clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged: true,
		UserMessage:                "steer now",
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "user",
			Text: "steer now",
		}},
	}
	ch <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "after"}

	msg := nextRuntimeEventBatch(t, ch)
	assertRuntimeEventBatchLen(t, msg, 1)
	if msg.events[0].Kind != clientui.EventAssistantDelta {
		t.Fatalf("first event kind = %q, want assistant_delta", msg.events[0].Kind)
	}
	if msg.carry != nil {
		t.Fatalf("did not expect carry when assistant deltas fence batching, got %+v", *msg.carry)
	}
	nextMsg := nextRuntimeEventBatch(t, ch)
	if len(nextMsg.events) != 1 || nextMsg.events[0].Kind != clientui.EventUserMessageFlushed {
		t.Fatalf("expected transcript event to be delivered on next wait, got %+v", nextMsg.events)
	}
}

func TestWaitRuntimeEventPrefersRichCommittedEventOverBareCommittedConversationUpdate(t *testing.T) {
	ch := make(chan clientui.Event, 2)
	ch <- clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
	}
	ch <- clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "final"}},
	}

	msg := nextRuntimeEventBatch(t, ch)
	assertRuntimeEventBatchLen(t, msg, 1)
	if msg.events[0].Kind != clientui.EventAssistantMessage {
		t.Fatalf("event kind = %q, want assistant_message", msg.events[0].Kind)
	}
	if msg.carry != nil {
		t.Fatalf("did not expect carry after rich committed event covered bare conversation update, got %+v", *msg.carry)
	}
}

func TestWaitRuntimeEventDoesNotSuppressCommittedConversationUpdateWhenNextEventAdvancesTailPastReplacement(t *testing.T) {
	ch := make(chan clientui.Event, 2)
	ch <- clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryCount:        3,
	}
	ch <- clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryCount:        4,
		CommittedEntryStart:        3,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "reviewer_status", Text: "Supervisor ran: no changes."}},
	}

	msg := nextRuntimeEventBatch(t, ch)
	assertRuntimeEventBatchLen(t, msg, 2)
	if msg.events[0].Kind != clientui.EventConversationUpdated {
		t.Fatalf("event kind = %q, want conversation_updated", msg.events[0].Kind)
	}
	if msg.events[1].Kind != clientui.EventLocalEntryAdded {
		t.Fatalf("second event kind = %q, want local_entry_added", msg.events[1].Kind)
	}
	if msg.carry != nil {
		t.Fatalf("did not expect carry when same-step committed tail should batch with replacement update, got %+v", msg.carry)
	}
}

func TestPlainConversationUpdateDoesNotDelayCompactionNoticeAppend(t *testing.T) {
	m := newProjectedTestUIModel(&runtimeControlFakeClient{}, closedProjectedRuntimeEvents(), nil)
	m.startupCmds = nil
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "seed", Committed: true}}
	m.transcriptBaseOffset = 0
	m.transcriptTotalEntries = 1
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	next, cmd := m.Update(runtimeEventBatchMsg{events: []clientui.Event{{
		Kind:               clientui.EventConversationUpdated,
		StepID:             "step-1",
		TranscriptRevision: 11,
	}}})
	updated := next.(*uiModel)
	if updated.waitRuntimeEventAfterHydration {
		t.Fatal("did not expect plain conversation update to arm hydration fence")
	}
	for _, msg := range collectCmdMessages(t, cmd) {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect plain conversation update to trigger hydration, got %+v", msg)
		}
	}

	next, _ = updated.Update(runtimeEventBatchMsg{events: []clientui.Event{{
		Kind:                       clientui.EventLocalEntryAdded,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryStart:        1,
		CommittedEntryStartSet:     true,
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "compaction_notice",
			Text: "context compacted for the 1st time",
		}},
	}}})
	updated = next.(*uiModel)
	if got := len(updated.transcriptEntries); got != 2 {
		t.Fatalf("expected compaction notice appended immediately, got %+v", updated.transcriptEntries)
	}
	if got := updated.transcriptEntries[1].Role; got != "compaction_notice" {
		t.Fatalf("second transcript role = %q, want compaction_notice", got)
	}
}

func TestWaitRuntimeEventReturnsFencedFirstEventWithoutCoalescing(t *testing.T) {
	ch := make(chan clientui.Event, 2)
	ch <- clientui.Event{
		Kind:                       clientui.EventUserMessageFlushed,
		CommittedTranscriptChanged: true,
		UserMessage:                "steer now",
		TranscriptEntries: []clientui.ChatEntry{{
			Role: "user",
			Text: "steer now",
		}},
	}
	ch <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "after"}

	msg := nextRuntimeEventBatch(t, ch)
	assertRuntimeEventBatchLen(t, msg, 1)
	if msg.events[0].Kind != clientui.EventUserMessageFlushed {
		t.Fatalf("first event kind = %q, want user_message_flushed", msg.events[0].Kind)
	}
	if msg.carry != nil {
		t.Fatalf("did not expect carry when first event is already fenced, got %+v", *msg.carry)
	}
	if remaining := (<-ch); remaining.Kind != clientui.EventAssistantDelta || remaining.AssistantDelta != "after" {
		t.Fatalf("expected later delta to remain unread, got %+v", remaining)
	}
}

func TestWaitRuntimeEventCmdPrefersPendingCarryBeforeRuntimeChannel(t *testing.T) {
	runtimeEvents := make(chan clientui.Event, 1)
	runtimeEvents <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "later"}
	m := newProjectedTestUIModel(nil, runtimeEvents, nil)
	m.pendingRuntimeEvents = []clientui.Event{{Kind: clientui.EventUserMessageFlushed, UserMessage: "steer now"}}

	firstMsg := runtimeEventBatchFromCmd(t, m.waitRuntimeEventCmd(), "wait command for pending carry event")
	if len(firstMsg.events) != 1 || firstMsg.events[0].Kind != clientui.EventUserMessageFlushed {
		t.Fatalf("unexpected pending carry batch: %+v", firstMsg.events)
	}
	if len(m.pendingRuntimeEvents) != 0 {
		t.Fatalf("expected pending carry queue drained, got %+v", m.pendingRuntimeEvents)
	}

	secondMsg := runtimeEventBatchFromCmd(t, m.waitRuntimeEventCmd(), "wait command for runtime channel after carry drain")
	if len(secondMsg.events) != 1 || secondMsg.events[0].AssistantDelta != "later" {
		t.Fatalf("unexpected runtime channel batch after carry drain: %+v", secondMsg.events)
	}
}

func TestWaitRuntimeEventCmdStaysPausedWhileHydrationFenceIsArmed(t *testing.T) {
	runtimeEvents := make(chan clientui.Event, 1)
	runtimeEvents <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "later"}
	m := newProjectedTestUIModel(nil, runtimeEvents, nil)
	m.waitRuntimeEventAfterHydration = true

	if cmd := m.waitRuntimeEventCmd(); cmd != nil {
		t.Fatalf("expected runtime wait to remain paused while hydration fence is armed, got %T", cmd())
	}
	if len(runtimeEvents) != 1 {
		t.Fatalf("expected runtime event to remain unread while hydration fence is armed, remaining=%d", len(runtimeEvents))
	}

	m.waitRuntimeEventAfterHydration = false
	msg := runtimeEventBatchFromCmd(t, m.waitRuntimeEventCmd(), "runtime wait after hydration fence clears")
	if len(msg.events) != 1 || msg.events[0].AssistantDelta != "later" {
		t.Fatalf("unexpected runtime event after hydration fence clears: %+v", msg.events)
	}
}

func TestConversationUpdateHydrationFencesLaterRuntimeEvents(t *testing.T) {
	client := &refreshingRuntimeClient{
		transcripts: []clientui.TranscriptPage{{
			SessionID: "session-1",
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "hydrated"}},
		}},
	}
	runtimeEvents := make(chan clientui.Event, 1)
	runtimeEvents <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "later"}
	m := newProjectedTestUIModel(client, runtimeEvents, nil)
	m.startupCmds = nil
	m.sawAssistantDelta = true
	m.reasoningLiveDirty = true
	m.forwardToView(tui.SetConversationMsg{Ongoing: "stale stream"})

	next, cmd := m.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventConversationUpdated, CommittedTranscriptChanged: true}})
	updated := next.(*uiModel)
	if updated.waitRuntimeEventAfterHydration {
		t.Fatal("deferred routine conversation update must not arm hydration fence")
	}
	if !updated.runtimeTranscriptPendingSet {
		t.Fatal("expected routine conversation update to defer transcript sync while streaming")
	}
	if !updated.sawAssistantDelta {
		t.Fatal("expected deferred conversation update to preserve assistant delta state")
	}
	if !updated.reasoningLiveDirty {
		t.Fatal("expected deferred conversation update to preserve reasoning live state")
	}
	if got := updated.view.OngoingStreamingText(); got != "stale stream" {
		t.Fatalf("expected deferred conversation update to preserve stale ongoing text, got %q", got)
	}
	msgs := collectCmdMessages(t, cmd)
	resumed := false
	for _, msg := range msgs {
		switch typed := msg.(type) {
		case runtimeTranscriptRefreshedMsg:
			t.Fatalf("did not expect routine refresh while streaming, got %+v", typed)
		case runtimeEventBatchMsg:
			if len(typed.events) == 1 && typed.events[0].AssistantDelta == "later" {
				resumed = true
			}
		}
	}
	if !resumed {
		t.Fatalf("expected runtime stream to continue without hydration fence, got %+v", msgs)
	}
}

func TestStreamGapInvalidatesTransientStateBeforeHydrationFence(t *testing.T) {
	client := &refreshingRuntimeClient{
		transcripts: []clientui.TranscriptPage{{
			SessionID: "session-1",
			Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "hydrated after gap"}},
		}},
	}
	runtimeEvents := make(chan clientui.Event, 1)
	runtimeEvents <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "later"}
	m := newProjectedTestUIModel(client, runtimeEvents, nil)
	m.startupCmds = nil
	m.sawAssistantDelta = true
	m.reasoningLiveDirty = true
	m.forwardToView(tui.SetConversationMsg{Ongoing: "stale stream"})

	next, cmd := m.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventStreamGap, RecoveryCause: clientui.TranscriptRecoveryCauseStreamGap}})
	updated := next.(*uiModel)
	if !updated.waitRuntimeEventAfterHydration {
		t.Fatal("expected stream gap recovery to arm hydration fence")
	}
	if !updated.sawAssistantDelta {
		t.Fatal("expected stream gap recovery to preserve assistant delta state before hydrate applies")
	}
	if !updated.reasoningLiveDirty {
		t.Fatal("expected stream gap recovery to preserve reasoning live state before hydrate applies")
	}
	if got := updated.view.OngoingStreamingText(); got != "stale stream" {
		t.Fatalf("expected stream gap recovery to preserve stale ongoing text before hydrate applies, got %q", got)
	}
	if len(runtimeEvents) != 1 {
		t.Fatalf("expected later runtime event to remain unread until hydration completes, remaining=%d", len(runtimeEvents))
	}
	msgs := collectCmdMessages(t, cmd)
	for _, msg := range msgs {
		if batch, ok := msg.(runtimeEventBatchMsg); ok {
			t.Fatalf("did not expect runtime stream to resume before stream-gap hydration completes, got %+v", batch)
		}
	}
}

func TestHydratingClientAndLiveClientConvergeWithoutDuplicateCommittedRows(t *testing.T) {
	baseline := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  10,
		Entries:   []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
	}
	authoritative := clientui.TranscriptPage{
		SessionID: "session-1",
		Revision:  11,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "seed"},
			{Role: "assistant", Text: "final", Phase: string(llm.MessagePhaseFinal)},
		},
	}
	committedFinal := clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryCount:        2,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "final", Phase: string(llm.MessagePhaseFinal)}},
	}

	hydratingEvents := make(chan clientui.Event, 1)
	hydratingClient := &runtimeControlFakeClient{transcript: authoritative}
	hydrating := newProjectedTestUIModel(hydratingClient, hydratingEvents, nil)
	hydrating.startupCmds = nil
	if cmd := hydrating.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	live := newProjectedTestUIModel(&runtimeControlFakeClient{}, closedProjectedRuntimeEvents(), nil)
	live.startupCmds = nil
	if cmd := live.runtimeAdapter().applyRuntimeTranscriptPageWithRecovery(clientui.TranscriptPageRequest{}, baseline, clientui.TranscriptRecoveryCauseNone); cmd != nil {
		_ = collectCmdMessages(t, cmd)
	}

	hydrating.waitRuntimeEventAfterHydration = true
	refreshMsg, ok := hydrating.requestRuntimeCommittedConversationSync()().(runtimeTranscriptRefreshedMsg)
	if !ok {
		t.Fatal("expected committed conversation sync to return runtimeTranscriptRefreshedMsg")
	}
	hydratingEvents <- committedFinal
	close(hydratingEvents)
	liveCmd := live.runtimeAdapter().applyProjectedRuntimeEvent(committedFinal).cmd
	for _, msg := range collectCmdMessages(t, liveCmd) {
		if _, ok := msg.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect live client committed event to require hydration, got %+v", msg)
		}
	}

	next, followCmd := hydrating.Update(refreshMsg)
	hydrating = next.(*uiModel)
	followMsgs := collectCmdMessages(t, followCmd)
	var resumed runtimeEventBatchMsg
	resumedFound := false
	for _, msg := range followMsgs {
		batch, ok := msg.(runtimeEventBatchMsg)
		if !ok {
			continue
		}
		resumed = batch
		resumedFound = true
	}
	if !resumedFound {
		t.Fatalf("expected hydrating client to resume queued live events after refresh, got %+v", followMsgs)
	}

	next, finalCmd := hydrating.Update(resumed)
	hydrating = next.(*uiModel)
	_ = finalCmd

	hydratingCommitted := stripANSIAndTrimRight(hydrating.view.CommittedOngoingProjection().Render(tui.TranscriptDivider))
	liveCommitted := stripANSIAndTrimRight(live.view.CommittedOngoingProjection().Render(tui.TranscriptDivider))
	if got := len(hydrating.transcriptEntries); got != 2 {
		t.Fatalf("hydrating client transcript entry count = %d, want 2", got)
	}
	if got := len(live.transcriptEntries); got != 2 {
		t.Fatalf("live client transcript entry count = %d, want 2", got)
	}
	if strings.Count(hydratingCommitted, "final") != 1 {
		t.Fatalf("expected hydrating client committed snapshot to contain final exactly once, got %q", hydratingCommitted)
	}
	if strings.Count(liveCommitted, "final") != 1 {
		t.Fatalf("expected live client committed snapshot to contain final exactly once, got %q", liveCommitted)
	}
	if hydratingCommitted != liveCommitted {
		t.Fatalf("expected hydrating and live clients to converge to same committed snapshot, hydrating=%q live=%q", hydratingCommitted, liveCommitted)
	}
}

func TestHydrationRetryErrorReleasesRuntimeEventFenceWhileRetryIsScheduled(t *testing.T) {
	previousRetryDelay := uiRuntimeHydrationRetryDelay
	uiRuntimeHydrationRetryDelay = time.Millisecond
	t.Cleanup(func() {
		uiRuntimeHydrationRetryDelay = previousRetryDelay
	})

	client := &refreshingRuntimeClient{}
	runtimeEvents := make(chan clientui.Event, 1)
	runtimeEvents <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "later"}
	m := newProjectedTestUIModel(client, runtimeEvents, nil)
	m.startupCmds = nil
	m.runtimeTranscriptBusy = true
	m.runtimeTranscriptToken = 7
	m.waitRuntimeEventAfterHydration = true

	next, cmd := m.Update(runtimeTranscriptRefreshedMsg{token: 7, err: errors.New("temporary refresh failure")})
	updated := next.(*uiModel)
	if updated.waitRuntimeEventAfterHydration {
		t.Fatal("expected hydration retry path to release runtime event fence after failure")
	}
	if updated.runtimeTranscriptBusy {
		t.Fatal("expected hydration retry path to clear in-flight busy flag after failure")
	}
	msgs := collectCmdMessages(t, cmd)
	retryFound := false
	resumed := false
	for _, msg := range msgs {
		switch typed := msg.(type) {
		case runtimeTranscriptRetryMsg:
			retryFound = true
		case runtimeEventBatchMsg:
			if len(typed.events) == 1 && typed.events[0].AssistantDelta == "later" {
				resumed = true
			}
		}
	}
	if !retryFound {
		t.Fatalf("expected hydration retry to remain scheduled after failure, got %+v", msgs)
	}
	if !resumed {
		t.Fatalf("expected runtime event consumption to resume while retry is pending, got %+v", msgs)
	}
	if len(runtimeEvents) != 0 {
		t.Fatalf("expected resumed runtime wait to consume pending event, remaining=%d", len(runtimeEvents))
	}
}

func TestProjectedRuntimeEventUpdateStreamsAssistantDelta(t *testing.T) {
	m := NewProjectedUIModel(nil, make(chan clientui.Event), make(chan askEvent)).(*uiModel)
	next, _ := m.Update(runtimeEventMsg{event: clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "hello"}})
	updated := next.(*uiModel)
	if got := updated.view.OngoingStreamingText(); got != "hello" {
		t.Fatalf("expected projected delta to reach view, got %q", got)
	}
}

func TestProjectedRuntimeEventSequentialAssistantDeltasStayIncremental(t *testing.T) {
	runtimeEvents := make(chan clientui.Event, 2)
	runtimeEvents <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: "hello"}
	runtimeEvents <- clientui.Event{Kind: clientui.EventAssistantDelta, AssistantDelta: " world"}
	m := newProjectedTestUIModel(nil, runtimeEvents, nil)
	m.startupCmds = nil

	first := m.waitRuntimeEventCmd()
	if first == nil {
		t.Fatal("expected first runtime wait command")
	}
	firstMsg, ok := first().(runtimeEventBatchMsg)
	if !ok {
		t.Fatalf("expected first runtimeEventBatchMsg, got %T", first())
	}
	if len(firstMsg.events) != 1 || firstMsg.events[0].AssistantDelta != "hello" {
		t.Fatalf("unexpected first batch: %+v", firstMsg.events)
	}
	next, _ := m.Update(firstMsg)
	updated := next.(*uiModel)
	if got := updated.view.OngoingStreamingText(); got != "hello" {
		t.Fatalf("stream after first delta = %q, want hello", got)
	}

	second := updated.waitRuntimeEventCmd()
	if second == nil {
		t.Fatal("expected second runtime wait command")
	}
	secondMsg, ok := second().(runtimeEventBatchMsg)
	if !ok {
		t.Fatalf("expected second runtimeEventBatchMsg, got %T", second())
	}
	if len(secondMsg.events) != 1 || secondMsg.events[0].AssistantDelta != " world" {
		t.Fatalf("unexpected second batch: %+v", secondMsg.events)
	}
	next, _ = updated.Update(secondMsg)
	updated = next.(*uiModel)
	if got := updated.view.OngoingStreamingText(); got != "hello world" {
		t.Fatalf("stream after second delta = %q, want hello world", got)
	}
}

func TestRuntimeModelSkipsBareCommittedConversationUpdateWhenRichCommittedEventImmediatelyFollows(t *testing.T) {
	runtimeEvents := make(chan clientui.Event, 2)
	runtimeEvents <- clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
	}
	runtimeEvents <- clientui.Event{
		Kind:                       clientui.EventAssistantMessage,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "assistant", Text: "final"}},
	}
	close(runtimeEvents)
	m := newProjectedTestUIModel(&runtimeControlFakeClient{}, runtimeEvents, nil)
	m.startupCmds = nil

	msg := runtimeEventBatchFromCmd(t, m.waitRuntimeEventCmd(), "runtime wait")
	next, cmd := m.Update(msg)
	updated := next.(*uiModel)
	for _, follow := range collectCmdMessages(t, cmd) {
		if _, ok := follow.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect hydration when rich committed event already covered bare committed conversation update, got %+v", follow)
		}
	}
	if updated.runtimeTranscriptBusy {
		t.Fatal("did not expect runtime transcript sync to start")
	}
	if got := len(updated.transcriptEntries); got != 1 {
		t.Fatalf("transcript entry count = %d, want 1", got)
	}
	if got := updated.transcriptEntries[0].Text; got != "final" {
		t.Fatalf("assistant transcript text = %q, want final", got)
	}
}

func TestRuntimeModelHiddenCommittedSkipDoesNotTriggerFollowUpCommittedConversationHydrate(t *testing.T) {
	client := &runtimeControlFakeClient{transcript: clientui.TranscriptPage{SessionID: "session-1"}}
	runtimeEvents := make(chan clientui.Event, 2)
	runtimeEvents <- clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         13,
		CommittedEntryCount:        8,
		CommittedEntryStart:        4,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "cache_warning", Text: "hidden-prefix-only"}},
	}
	runtimeEvents <- clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         13,
		CommittedEntryCount:        8,
	}
	close(runtimeEvents)

	m := newProjectedTestUIModel(client, runtimeEvents, nil)
	m.startupCmds = nil
	m.transcriptEntries = []tui.TranscriptEntry{
		{Role: "assistant", Text: "visible-a", Phase: llm.MessagePhaseFinal},
		{Role: "reviewer_status", Text: "visible-b"},
	}
	m.transcriptBaseOffset = 5
	m.transcriptTotalEntries = 7
	m.transcriptRevision = 12
	m.forwardToView(tui.SetConversationMsg{BaseOffset: m.transcriptBaseOffset, TotalEntries: m.transcriptTotalEntries, Entries: m.transcriptEntries})

	firstMsg := runtimeEventBatchFromCmd(t, m.waitRuntimeEventCmd(), "runtime wait command for hidden committed event")
	next, cmd := m.Update(firstMsg)
	updated := next.(*uiModel)
	followMsgs := collectCmdMessages(t, cmd)
	var secondMsg runtimeEventBatchMsg
	secondFound := false
	for _, follow := range followMsgs {
		if _, ok := follow.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect hidden committed event to trigger hydration, got %+v", follow)
		}
		typed, ok := follow.(runtimeEventBatchMsg)
		if ok {
			secondMsg = typed
			secondFound = true
		}
	}
	if updated.runtimeTranscriptBusy {
		t.Fatal("did not expect hidden committed event to start transcript hydration")
	}
	if !secondFound {
		t.Fatalf("expected follow-up committed conversation update to resume immediately, got %+v", followMsgs)
	}
	next, cmd = updated.Update(secondMsg)
	updated = next.(*uiModel)
	for _, follow := range collectCmdMessages(t, cmd) {
		if _, ok := follow.(runtimeTranscriptRefreshedMsg); ok {
			t.Fatalf("did not expect matching committed conversation_updated after hidden skip to trigger hydration, got %+v", follow)
		}
	}
	if updated.runtimeTranscriptBusy {
		t.Fatal("did not expect follow-up committed conversation_updated to start hydration after hidden skip")
	}
	if got := updated.transcriptRevision; got != 13 {
		t.Fatalf("transcript revision = %d, want 13", got)
	}
	if got := updated.transcriptTotalEntries; got != 8 {
		t.Fatalf("transcript total entries = %d, want 8", got)
	}
	if got := len(updated.transcriptEntries); got != 2 {
		t.Fatalf("visible transcript entry count = %d, want 2", got)
	}
}

func TestRuntimeModelReplacementAndSameStepTailConvergeWithoutDuplicateRows(t *testing.T) {
	client := &runtimeControlFakeClient{transcript: clientui.TranscriptPage{
		SessionID:    "session-1",
		Revision:     11,
		HasMoreAbove: true,
		Entries: []clientui.ChatEntry{
			{Role: "assistant", Text: "replacement summary"},
			{Role: "reviewer_status", Text: "Supervisor ran: no changes."},
		},
	}}
	runtimeEvents := make(chan clientui.Event, 2)
	runtimeEvents <- clientui.Event{
		Kind:                       clientui.EventConversationUpdated,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryCount:        3,
	}
	runtimeEvents <- clientui.Event{
		Kind:                       clientui.EventLocalEntryAdded,
		StepID:                     "step-1",
		CommittedTranscriptChanged: true,
		TranscriptRevision:         11,
		CommittedEntryCount:        4,
		CommittedEntryStart:        3,
		CommittedEntryStartSet:     true,
		TranscriptEntries:          []clientui.ChatEntry{{Role: "reviewer_status", Text: "Supervisor ran: no changes."}},
	}
	close(runtimeEvents)
	m := newProjectedTestUIModel(client, runtimeEvents, nil)
	m.startupCmds = nil
	m.transcriptEntries = []tui.TranscriptEntry{{Role: "assistant", Text: "stale before replace"}}
	m.transcriptTotalEntries = 1
	m.transcriptRevision = 10
	m.forwardToView(tui.SetConversationMsg{Entries: m.transcriptEntries})

	msg := runtimeEventBatchFromCmd(t, m.waitRuntimeEventCmd(), "runtime wait")
	if got := len(msg.events); got != 2 {
		t.Fatalf("expected replacement update and same-step tail in one batch, got %+v", msg.events)
	}
	next, cmd := m.Update(msg)
	updated := next.(*uiModel)
	if cmd == nil {
		t.Fatal("expected hydration command for committed replacement update")
	}
	refresh, ok := cmd().(runtimeTranscriptRefreshedMsg)
	if !ok {
		t.Fatalf("expected runtimeTranscriptRefreshedMsg, got %T", cmd())
	}
	next, follow := updated.Update(refresh)
	updated = next.(*uiModel)
	pending := collectCmdMessages(t, follow)
	for len(pending) > 0 {
		msg := pending[0]
		pending = pending[1:]
		refreshMsg, ok := msg.(runtimeTranscriptRefreshedMsg)
		if !ok {
			continue
		}
		next, nextCmd := updated.Update(refreshMsg)
		updated = next.(*uiModel)
		pending = append(pending, collectCmdMessages(t, nextCmd)...)
	}
	if got := len(updated.transcriptEntries); got != 2 {
		t.Fatalf("transcript entry count = %d, want 2", got)
	}
	if got := updated.transcriptEntries[0].Text; got != "replacement summary" {
		t.Fatalf("first transcript entry = %q, want replacement summary", got)
	}
	if got := updated.transcriptEntries[1].Text; got != "Supervisor ran: no changes." {
		t.Fatalf("second transcript entry = %q, want reviewer status", got)
	}
}

func TestRuntimeModelRefreshesOngoingErrorOnDedicatedUpdateEvent(t *testing.T) {
	client := &runtimeControlFakeClient{transcript: clientui.TranscriptPage{
		SessionID:      "session-1",
		Revision:       10,
		Entries:        []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
		StreamingError: "background continuation failed",
	}}
	runtimeEvents := make(chan clientui.Event, 1)
	runtimeEvents <- clientui.Event{Kind: clientui.EventStreamingErrorUpdated, StepID: "step-1"}
	close(runtimeEvents)
	m := newProjectedTestUIModel(client, runtimeEvents, nil)
	m.startupCmds = nil

	msg := runtimeEventBatchFromCmd(t, m.waitRuntimeEventCmd(), "runtime wait")
	next, cmd := m.Update(msg)
	updated := next.(*uiModel)
	refresh := runtimeTranscriptRefreshOrReleaseDeferredForTest(t, updated, cmd)
	next, _ = updated.Update(refresh)
	updated = next.(*uiModel)
	if got := updated.view.OngoingErrorText(); got != "background continuation failed" {
		t.Fatalf("ongoing error text = %q, want background continuation failed", got)
	}
}

func TestRuntimeModelStreamingErrorUpdatedSetsAndClearsBannerLifecycle(t *testing.T) {
	client := &refreshingRuntimeClient{
		transcripts: []clientui.TranscriptPage{
			{
				SessionID:      "session-1",
				Revision:       10,
				Entries:        []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
				StreamingError: "background continuation failed",
			},
			{
				SessionID:      "session-1",
				Revision:       10,
				Entries:        []clientui.ChatEntry{{Role: "assistant", Text: "seed"}},
				StreamingError: "",
			},
		},
	}
	runtimeEvents := make(chan clientui.Event, 2)
	runtimeEvents <- clientui.Event{Kind: clientui.EventStreamingErrorUpdated, StepID: "step-1"}
	runtimeEvents <- clientui.Event{Kind: clientui.EventStreamingErrorUpdated, StepID: "step-1"}
	close(runtimeEvents)
	m := newProjectedTestUIModel(client, runtimeEvents, nil)
	m.startupCmds = nil
	m.termWidth = 100
	m.termHeight = 20
	m.windowSizeKnown = true
	m.forwardToView(tui.SetViewportSizeMsg{Lines: 20, Width: 100})

	firstMsg := runtimeEventBatchFromCmd(t, m.waitRuntimeEventCmd(), "runtime wait command for ongoing error set")
	next, cmd := m.Update(firstMsg)
	updated := next.(*uiModel)
	refresh := runtimeTranscriptRefreshOrReleaseDeferredForTest(t, updated, cmd)
	next, _ = updated.Update(refresh)
	updated = next.(*uiModel)
	if got := updated.view.OngoingErrorText(); got != "background continuation failed" {
		t.Fatalf("ongoing error text after set = %q, want background continuation failed", got)
	}

	secondMsg := runtimeEventBatchFromCmd(t, updated.waitRuntimeEventCmd(), "runtime wait command for ongoing error clear")
	next, cmd = updated.Update(secondMsg)
	updated = next.(*uiModel)
	refresh = runtimeTranscriptRefreshOrReleaseDeferredForTest(t, updated, cmd)
	next, _ = updated.Update(refresh)
	updated = next.(*uiModel)
	if got := updated.view.OngoingErrorText(); got != "" {
		t.Fatalf("ongoing error text after clear = %q, want empty", got)
	}
}
