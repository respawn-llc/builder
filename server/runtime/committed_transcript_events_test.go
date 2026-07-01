package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"core/shared/toolspec"
	"core/shared/transcript"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
)

func TestCommittedTranscriptChangedMarksOnlyDurableTranscriptMutations(t *testing.T) {
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 16)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})

	start := len(events)
	if err := eng.AppendCommittedEntry("assistant", "committed local note"); err != nil {
		t.Fatalf("append committed entry: %v", err)
	}
	assertEventFlags(t, events[start:], []eventFlagExpectation{{kind: EventLocalEntryAdded, stepID: "", committedChanged: true}})

	start = len(events)
	eng.SetStreamingError("boom")
	assertEventFlags(t, events[start:], []eventFlagExpectation{{kind: EventStreamingErrorUpdated, stepID: "", committedChanged: false}})

	start = len(events)
	eng.ClearStreamingError()
	assertEventFlags(t, events[start:], []eventFlagExpectation{{kind: EventStreamingErrorUpdated, stepID: "", committedChanged: false}})

	start = len(events)
	if err := eng.steer("stream-step", steerClearStreamingStateIntent()); err != nil {
		t.Fatalf("clear streaming state: %v", err)
	}
	assertEventFlags(t, events[start:], []eventFlagExpectation{{kind: EventConversationUpdated, stepID: "stream-step", committedChanged: false}, {kind: EventAssistantDeltaReset, stepID: "stream-step", committedChanged: false}, {kind: EventReasoningDeltaReset, stepID: "stream-step", committedChanged: false}})

	start = len(events)
	if err := eng.steer("persist-step", steerLocalEntryIntent(storedLocalEntry{
		Visibility: transcript.EntryVisibilityAuto,
		Role:       "reviewer_status",
		Text:       "persisted local note",
	})); err != nil {
		t.Fatalf("append persisted local entry: %v", err)
	}
	assertEventFlags(t, events[start:], []eventFlagExpectation{{kind: EventLocalEntryAdded, stepID: "persist-step", committedChanged: true}})

	start = len(events)
	if err := newCompactionPersistence(eng).replaceHistory("compact-step", "local", compactionModeManual, llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}})); err != nil {
		t.Fatalf("replace history for compaction: %v", err)
	}
	assertEventFlags(t, events[start:], []eventFlagExpectation{{kind: EventLocalEntryAdded, stepID: "compact-step", committedChanged: true}, {kind: EventConversationUpdated, stepID: "compact-step", committedChanged: false}})

	start = len(events)
	if err := eng.steer("message-step", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, Content: "persisted assistant", Phase: llm.MessagePhaseFinal}})); err != nil {
		t.Fatalf("append persisted message: %v", err)
	}
	assertEventFlags(t, events[start:], []eventFlagExpectation{{kind: EventConversationUpdated, stepID: "message-step", committedChanged: true}})

	start = len(events)
	if err := eng.steer("goal-step", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{normalizeMessageForTranscript(llm.Message{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeGoal, Content: "Goal paused.", CompactContent: "Goal paused"}, eng.transcriptWorkingDir())})); err != nil {
		t.Fatalf("append goal feedback: %v", err)
	}
	assertEventFlags(t, events[start:], []eventFlagExpectation{{kind: EventConversationUpdated, stepID: "goal-step", committedChanged: true}})

	start = len(events)
	eng.QueueUserMessage("queued input")
	if _, err := eng.flushPendingUserInjections("flush-step", nil); err != nil {
		t.Fatalf("flush pending user injections: %v", err)
	}
	assertEventFlags(t, events[start:], []eventFlagExpectation{
		{kind: EventQueuedUserMessageStatus, stepID: "", committedChanged: false},
		{kind: EventUserMessageFlushed, stepID: "flush-step", committedChanged: true},
		{kind: EventQueuedUserMessageStatus, stepID: "", committedChanged: false},
	})

	eng.ensureOrchestrationCollaborators()
	start = len(events)
	if err := eng.observePromptCacheResponse("cache-step", preparedCacheRequestObservation{
		request: persistedCacheRequestObserved{
			DigestVersion: requestCacheDigestVersion,
			CacheKey:      "session-1/cache-key",
			Scope:         transcript.CacheWarningScopeConversation,
		},
		exactWarning: &transcript.CacheWarning{
			Scope:  transcript.CacheWarningScopeConversation,
			Reason: transcript.CacheWarningReasonNonPostfix,
		},
		previousCachedInputTokens: 10,
	}, llm.Usage{HasCachedInputTokens: true, CachedInputTokens: 0}); err != nil {
		t.Fatalf("observe prompt cache response: %v", err)
	}
	assertEventFlags(t, events[start:], []eventFlagExpectation{{kind: EventCacheWarning, stepID: "cache-step", committedChanged: true}})

	start = len(events)
	if _, err := eng.executeToolCalls(context.Background(), "tool-step", []llm.ToolCall{{
		ID:    "call-1",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd"}`),
	}}); err != nil {
		t.Fatalf("execute tool calls: %v", err)
	}
	assertEventFlags(t, events[start:], []eventFlagExpectation{{kind: EventToolCallStarted, stepID: "tool-step", committedChanged: true}, {kind: EventToolCallCompleted, stepID: "tool-step", committedChanged: true}})
}

func TestCommittedLocalEntrySteeringSerializesPersistProjectEmitOrder(t *testing.T) {
	store := mustCreateTestSession(t)
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	var firstOnce sync.Once
	var secondOnce sync.Once
	eng.beforePersistLocalEntry = func(entry storedLocalEntry) error {
		switch entry.Text {
		case "first":
			firstOnce.Do(func() { close(firstEntered) })
			<-releaseFirst
		case "second":
			secondOnce.Do(func() { close(secondEntered) })
		}
		return nil
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- eng.AppendCommittedEntry("system", "first")
	}()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first append to enter persistence")
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- eng.AppendCommittedEntry("system", "second")
	}()
	select {
	case <-secondEntered:
		t.Fatal("second append entered persistence before first append completed")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second append: %v", err)
	}

	snapshot := eng.ChatSnapshot()
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Text != "first" || snapshot.Entries[1].Text != "second" {
		t.Fatalf("committed chat order = %+v, want first then second", snapshot.Entries)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2 events=%+v", len(events), events)
	}
	if events[0].LocalEntry == nil || events[0].LocalEntry.Text != "first" || events[1].LocalEntry == nil || events[1].LocalEntry.Text != "second" {
		t.Fatalf("event order = %+v, want first then second", events)
	}
	if events[0].CommittedEntryStart > events[1].CommittedEntryStart {
		t.Fatalf("event committed ranges out of order: first=%d second=%d", events[0].CommittedEntryStart, events[1].CommittedEntryStart)
	}
}

func TestCacheWarningObservationSerializesPersistProjectEmitOrder(t *testing.T) {
	store := mustCreateTestSession(t)
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})
	eng.ensureOrchestrationCollaborators()

	cachePersistEntered := make(chan struct{})
	releaseCachePersist := make(chan struct{})
	appendEntered := make(chan struct{})
	var cacheOnce sync.Once
	var appendOnce sync.Once
	eng.beforePersistCacheObservation = func(events []session.EventInput) error {
		for _, event := range events {
			if event.Kind == sessionEventCacheWarning {
				cacheOnce.Do(func() { close(cachePersistEntered) })
				<-releaseCachePersist
				return nil
			}
		}
		return nil
	}
	eng.beforePersistLocalEntry = func(entry storedLocalEntry) error {
		if entry.Text == "feedback" {
			appendOnce.Do(func() { close(appendEntered) })
		}
		return nil
	}

	cacheDone := make(chan error, 1)
	go func() {
		cacheDone <- eng.observePromptCacheResponse("cache-step", preparedCacheRequestObservation{
			request: persistedCacheRequestObserved{
				DigestVersion: requestCacheDigestVersion,
				CacheKey:      "session-1/cache-key",
				Scope:         transcript.CacheWarningScopeConversation,
			},
			exactWarning: &transcript.CacheWarning{
				Scope:  transcript.CacheWarningScopeConversation,
				Reason: transcript.CacheWarningReasonNonPostfix,
			},
			previousCachedInputTokens: 10,
		}, llm.Usage{HasCachedInputTokens: true, CachedInputTokens: 0})
	}()
	select {
	case <-cachePersistEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cache warning observation to enter persistence")
	}

	appendDone := make(chan error, 1)
	go func() {
		appendDone <- eng.AppendCommittedEntry("system", "feedback")
	}()
	select {
	case <-appendEntered:
		t.Fatal("committed feedback append entered persistence before cache warning observation completed")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseCachePersist)
	if err := <-cacheDone; err != nil {
		t.Fatalf("cache warning observation: %v", err)
	}
	if err := <-appendDone; err != nil {
		t.Fatalf("append feedback: %v", err)
	}

	snapshot := eng.ChatSnapshot()
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Role != cacheWarningTranscriptRole || snapshot.Entries[1].Text != "feedback" {
		t.Fatalf("committed chat order = %+v, want cache warning then feedback", snapshot.Entries)
	}
	persisted, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(persisted) < 3 {
		t.Fatalf("persisted event count = %d, want at least 3 events=%+v", len(persisted), persisted)
	}
	if persisted[0].Kind != sessionEventCacheWarning || persisted[1].Kind != sessionEventCacheResponseObserved || persisted[2].Kind != "local_entry" {
		t.Fatalf("persisted event order = %s, %s, %s; want cache_warning, cache_response_observed, local_entry", persisted[0].Kind, persisted[1].Kind, persisted[2].Kind)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2 events=%+v", len(events), events)
	}
	if events[0].Kind != EventCacheWarning || events[1].LocalEntry == nil || events[1].LocalEntry.Text != "feedback" {
		t.Fatalf("live event order = %+v, want cache warning then feedback", events)
	}
}

func TestHistoryReplacementSerializesAgainstCommittedLocalEntryAppend(t *testing.T) {
	store := mustCreateTestSession(t)
	replacementEventEntered := make(chan struct{})
	releaseReplacementEvent := make(chan struct{})
	appendEntered := make(chan struct{})
	var replacementOnce sync.Once
	var appendOnce sync.Once
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.Kind == EventLocalEntryAdded && evt.LocalEntry != nil && evt.LocalEntry.Text == "summary" {
				replacementOnce.Do(func() { close(replacementEventEntered) })
				<-releaseReplacementEvent
			}
		},
	})
	eng.beforePersistLocalEntry = func(entry storedLocalEntry) error {
		if entry.Text == "feedback" {
			appendOnce.Do(func() { close(appendEntered) })
		}
		return nil
	}

	replaceDone := make(chan error, 1)
	go func() {
		replaceDone <- newCompactionPersistence(eng).replaceHistory("compact-step", "local", compactionModeManual, llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: llm.MessageTypeCompactionSummary,
			Content:     "summary",
		}}))
	}()
	select {
	case <-replacementEventEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for replacement projection event")
	}

	appendDone := make(chan error, 1)
	go func() {
		appendDone <- eng.AppendCommittedEntry("system", "feedback")
	}()
	select {
	case <-appendEntered:
		t.Fatal("committed feedback append entered persistence before history replacement finished emitting")
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseReplacementEvent)
	if err := <-replaceDone; err != nil {
		t.Fatalf("replace history: %v", err)
	}
	if err := <-appendDone; err != nil {
		t.Fatalf("append feedback: %v", err)
	}
	snapshot := eng.ChatSnapshot()
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Text != "summary" || snapshot.Entries[1].Text != "feedback" {
		t.Fatalf("expected replacement summary then feedback after serialized append, got %+v", snapshot.Entries)
	}
}

func TestToolResultMirrorMessageDoesNotEmitGenericCommittedAdvance(t *testing.T) {
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 16)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})

	call := llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}
	if err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}})); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	result := tools.Result{CallID: call.ID, Name: toolspec.ToolExecCommand, Output: json.RawMessage(`{"output":"/tmp","exit_code":0,"truncated":false}`)}
	if err := eng.steer("step-1", steerToolCompletionIntent(result)); err != nil {
		t.Fatalf("persist tool completion: %v", err)
	}

	start := len(events)
	if err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleTool, ToolCallID: call.ID, Name: string(result.Name), Content: string(result.Output)}})); err != nil {
		t.Fatalf("append tool mirror message: %v", err)
	}
	if got := events[start:]; len(got) != 0 {
		t.Fatalf("expected no generic committed advance for tool mirror message, got %+v", got)
	}
}

func TestVisibleToolMessageMutationPublishesCommittedEventBeforeLocalEntry(t *testing.T) {
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 4)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})

	toolMessage := llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: "orphan-call",
		Name:       string(toolspec.ToolExecCommand),
		Content:    string(mustJSON(map[string]any{"output": "done", "exit_code": 0, "truncated": false})),
	}
	if err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{toolMessage})); err != nil {
		t.Fatalf("append visible tool message: %v", err)
	}
	if err := eng.steer("step-1", steerLocalEntryIntent(storedLocalEntry{Role: "system", Text: "local note"})); err != nil {
		t.Fatalf("append local entry: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2 events=%+v", len(events), events)
	}
	if events[0].Kind != EventConversationUpdated {
		t.Fatalf("first event kind = %s, want %s; events=%+v", events[0].Kind, EventConversationUpdated, events)
	}
	if entries := TranscriptEntriesFromEvent(events[0]); len(entries) != 1 || entries[0].Role != "tool_result_ok" {
		t.Fatalf("first event entries = %+v, want one tool result", entries)
	}
	if !events[0].CommittedEntryStartSet || events[0].CommittedEntryStart != 0 || events[0].CommittedEntryCount != 1 {
		t.Fatalf("first event range = start_set:%t start:%d count:%d, want start 0 count 1", events[0].CommittedEntryStartSet, events[0].CommittedEntryStart, events[0].CommittedEntryCount)
	}
	if events[1].Kind != EventLocalEntryAdded {
		t.Fatalf("second event kind = %s, want %s; events=%+v", events[1].Kind, EventLocalEntryAdded, events)
	}
	if !events[1].CommittedEntryStartSet || events[1].CommittedEntryStart != 1 || events[1].CommittedEntryCount != 2 {
		t.Fatalf("second event range = start_set:%t start:%d count:%d, want start 1 count 2", events[1].CommittedEntryStartSet, events[1].CommittedEntryStart, events[1].CommittedEntryCount)
	}
	assertRuntimeEventsAdvanceCommittedFrontierContiguously(t, events)
}

func TestManualCompactionCarryoverPublishesCommittedEventBeforeLocalEntry(t *testing.T) {
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 4)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})

	messages := []postCompactionMessage{{message: manualCompactionCarryoverMessage("keep the active requirement")}}
	if err := newCompactionCarryoverCoordinator(eng).appendPostCompactionMessages("compact-step", messages); err != nil {
		t.Fatalf("append manual carryover: %v", err)
	}
	if err := eng.steer("compact-step", steerLocalEntryIntent(storedLocalEntry{Role: "compaction_summary", Text: "summary"})); err != nil {
		t.Fatalf("append local entry: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("event count = %d, want 2 events=%+v", len(events), events)
	}
	if events[0].Kind != EventConversationUpdated {
		t.Fatalf("first event kind = %s, want %s; events=%+v", events[0].Kind, EventConversationUpdated, events)
	}
	if entries := TranscriptEntriesFromEvent(events[0]); len(entries) != 1 || entries[0].Role != string(transcript.EntryRoleManualCompactionCarryover) {
		t.Fatalf("first event entries = %+v, want one manual compaction carryover", entries)
	}
	if !events[0].CommittedEntryStartSet || events[0].CommittedEntryStart != 0 || events[0].CommittedEntryCount != 1 {
		t.Fatalf("first event range = start_set:%t start:%d count:%d, want start 0 count 1", events[0].CommittedEntryStartSet, events[0].CommittedEntryStart, events[0].CommittedEntryCount)
	}
	if events[1].Kind != EventLocalEntryAdded {
		t.Fatalf("second event kind = %s, want %s; events=%+v", events[1].Kind, EventLocalEntryAdded, events)
	}
	if !events[1].CommittedEntryStartSet || events[1].CommittedEntryStart != 1 || events[1].CommittedEntryCount != 2 {
		t.Fatalf("second event range = start_set:%t start:%d count:%d, want start 1 count 2", events[1].CommittedEntryStartSet, events[1].CommittedEntryStart, events[1].CommittedEntryCount)
	}
	assertRuntimeEventsAdvanceCommittedFrontierContiguously(t, events)
}

type eventFlagExpectation struct {
	kind             EventKind
	stepID           string
	committedChanged bool
}

func assertEventFlags(t *testing.T, events []Event, expected []eventFlagExpectation) {
	t.Helper()
	if len(events) != len(expected) {
		t.Fatalf("event count = %d, want %d; events=%+v", len(events), len(expected), events)
	}
	for idx, want := range expected {
		got := events[idx]
		if got.Kind != want.kind || got.StepID != want.stepID || got.CommittedTranscriptChanged != want.committedChanged {
			t.Fatalf("event[%d] = {Kind:%s StepID:%q CommittedTranscriptChanged:%t}, want {Kind:%s StepID:%q CommittedTranscriptChanged:%t}", idx, got.Kind, got.StepID, got.CommittedTranscriptChanged, want.kind, want.stepID, want.committedChanged)
		}
	}
}
