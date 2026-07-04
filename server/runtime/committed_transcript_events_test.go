package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	if err := eng.AppendCommittedEntry("system", "committed local note"); err != nil {
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

func TestAssistantMessageAfterCacheWarningDoesNotOwnCacheWarningRange(t *testing.T) {
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 4)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})

	if err := eng.observePromptCacheResponse("step-1", preparedCacheRequestObservation{
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
		t.Fatalf("observe cache warning: %v", err)
	}

	assistant := llm.Message{
		Role:    llm.RoleAssistant,
		Content: "checking service",
		Phase:   llm.MessagePhaseCommentary,
		ToolCalls: []llm.ToolCall{
			{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"status"}`)},
			{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"ps"}`)},
		},
	}
	if err := eng.steer("step-1", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{assistant})); err != nil {
		t.Fatalf("persist assistant message: %v", err)
	}
	assistantEntries := VisibleChatEntriesFromMessage(assistant)
	assistantStart := eng.CommittedTranscriptEntryCount() - len(assistantEntries)
	if err := eng.steer("step-1", steerCommittedAssistantMessageIntent(assistant, assistantStart, true)); err != nil {
		t.Fatalf("publish assistant message: %v", err)
	}

	committed := make([]Event, 0, 2)
	for _, evt := range events {
		if evt.CommittedTranscriptChanged && len(TranscriptEntriesFromEvent(evt)) > 0 {
			committed = append(committed, evt)
		}
	}
	if len(committed) != 2 {
		t.Fatalf("events = %+v, want cache warning then assistant message committed events", events)
	}
	cacheWarning := committed[0]
	if cacheWarning.Kind != EventCacheWarning {
		t.Fatalf("first event = %+v, want cache warning", cacheWarning)
	}
	cacheEntries := TranscriptEntriesFromEvent(cacheWarning)
	if len(cacheEntries) != 1 {
		t.Fatalf("cache warning entries = %+v, want one row", cacheEntries)
	}
	assistantEvent := committed[1]
	if assistantEvent.Kind != EventAssistantMessage {
		t.Fatalf("second event = %+v, want assistant message", assistantEvent)
	}
	if got := len(TranscriptEntriesFromEvent(assistantEvent)); got != len(assistantEntries) {
		t.Fatalf("assistant event entries = %d, want %d", got, len(assistantEntries))
	}
	wantAssistantStart := cacheWarning.CommittedEntryStart + len(cacheEntries)
	if assistantEvent.CommittedEntryStart != wantAssistantStart {
		t.Fatalf("assistant start = %d, want %d after cache warning; events=%+v", assistantEvent.CommittedEntryStart, wantAssistantStart, events)
	}
	if assistantEvent.CommittedEntryStart+len(assistantEntries) != assistantEvent.CommittedEntryCount {
		t.Fatalf("assistant event range owns rows outside its payload: event=%+v entries=%+v", assistantEvent, assistantEntries)
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

func TestFinalAnswerToolCallMaterializationPublishesToolCallRowsBeforeLocalEntry(t *testing.T) {
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 8)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	executor := defaultStepExecutor{
		engine: eng,
		tools:  &defaultToolExecutor{engine: eng},
	}

	_, _, err := executor.materializeFinalAnswerToolCalls(context.Background(), "step-1", []llm.ToolCall{{
		ID:    "call-1",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"cmd":"pwd"}`),
	}}, nil)
	if err != nil {
		t.Fatalf("materialize final-answer tool calls: %v", err)
	}
	if err := eng.steer("step-1", steerLocalEntryIntent(storedLocalEntry{Role: "reasoning", Text: "local note"})); err != nil {
		t.Fatalf("append local entry: %v", err)
	}

	committed := committedTranscriptEventsWithEntries(events)
	if len(committed) < 3 {
		t.Fatalf("committed events = %+v, want assistant tool-call row, tool result, and local entry", committed)
	}
	if committed[0].Kind != EventAssistantMessage {
		t.Fatalf("first committed event kind = %s, want %s; events=%+v", committed[0].Kind, EventAssistantMessage, committed)
	}
	if entries := TranscriptEntriesFromEvent(committed[0]); len(entries) != 1 || entries[0].Role != "tool_call" || entries[0].ToolCallID != "call-1" {
		t.Fatalf("first committed entries = %+v, want materialized tool-call row", entries)
	}
	assertRuntimeEventsAdvanceCommittedFrontierContiguously(t, committed)
}

func TestStepLoopPublishesCommentaryAssistantWithToolCallsBeforeReasoningAndToolResults(t *testing.T) {
	toolCalls := []llm.ToolCall{
		{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
		{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
		{ID: "call-3", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
	}
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "commentary before tools", Phase: llm.MessagePhaseCommentary},
			ToolCalls: toolCalls,
			Reasoning: []llm.ReasoningEntry{
				{Role: "reasoning", Text: "local note"},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	events := make([]Event, 0, 16)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})

	if _, err := eng.runStepLoopWithOptions(context.Background(), "step-1", "off", nil, false); err != nil {
		t.Fatalf("run step loop: %v", err)
	}

	committed := committedTranscriptEventsWithEntries(events)
	if len(committed) < 6 {
		t.Fatalf("committed events = %+v, want assistant, reasoning, tool rows, and final assistant", committed)
	}
	assistant := committed[0]
	if assistant.Kind != EventAssistantMessage {
		t.Fatalf("first committed event kind = %s, want %s; events=%+v", assistant.Kind, EventAssistantMessage, committed)
	}
	assistantEntries := TranscriptEntriesFromEvent(assistant)
	if len(assistantEntries) != 4 {
		t.Fatalf("first committed entries = %+v, want assistant text plus three tool-call rows", assistantEntries)
	}
	if assistantEntries[0].Role != "assistant" {
		t.Fatalf("first committed entry role = %q, want assistant; entries=%+v", assistantEntries[0].Role, assistantEntries)
	}
	for idx, call := range toolCalls {
		entry := assistantEntries[idx+1]
		if entry.Role != "tool_call" || entry.ToolCallID != call.ID {
			t.Fatalf("assistant entry[%d] = %+v, want tool_call %s; entries=%+v", idx+1, entry, call.ID, assistantEntries)
		}
	}
	reasoning := committed[1]
	if reasoning.Kind != EventLocalEntryAdded || reasoning.LocalEntry == nil || reasoning.LocalEntry.Role != "reasoning" {
		t.Fatalf("second committed event = %+v, want reasoning local entry after assistant/tool-call rows; events=%+v", reasoning, committed)
	}
	if reasoning.CommittedEntryStart != assistant.CommittedEntryStart+len(assistantEntries) {
		t.Fatalf("reasoning start = %d, want %d after assistant/tool-call rows; assistant=%+v reasoning=%+v", reasoning.CommittedEntryStart, assistant.CommittedEntryStart+len(assistantEntries), assistant, reasoning)
	}
	assertRuntimeEventsAdvanceCommittedFrontierContiguously(t, committed)
}

func TestStreamingToolLoopAssistantCommitClearsServerStreamingBeforeNextTurn(t *testing.T) {
	client := &streamingToolLoopClient{}
	events := make([]Event, 0, 16)
	assistantIdx := -1
	resetIdx := -1
	firstToolStartIdx := -1
	finalDeltaStreaming := ""
	streamingDuringCommittedAssistant := ""
	var resetMetadata *AssistantStreamMetadata
	var eng *Engine
	eng = mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			idx := len(events)
			events = append(events, evt)
			if evt.Kind == EventAssistantMessage && evt.Message.Content == "ab" {
				assistantIdx = idx
				streamingDuringCommittedAssistant = eng.ChatSnapshot().Streaming
			}
			if evt.Kind == EventAssistantDeltaReset && assistantIdx >= 0 && resetIdx < 0 {
				resetIdx = idx
				resetMetadata = cloneAssistantStreamMetadata(evt.AssistantStreamMetadata)
			}
			if evt.Kind == EventToolCallStarted && firstToolStartIdx < 0 {
				firstToolStartIdx = idx
			}
			if evt.Kind == EventAssistantDelta && evt.AssistantDelta == "done" && eng != nil {
				finalDeltaStreaming = eng.ChatSnapshot().Streaming
			}
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "stream then tool")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}
	if assistantIdx < 0 {
		t.Fatalf("missing committed commentary assistant event; events=%+v", events)
	}
	if streamingDuringCommittedAssistant != "" {
		t.Fatalf("streaming snapshot during committed assistant event = %q, want cleared before publish", streamingDuringCommittedAssistant)
	}
	if resetIdx < 0 {
		t.Fatalf("missing assistant delta reset after committed commentary assistant; events=%+v", events)
	}
	if resetMetadata == nil || resetMetadata.StepID == "" || resetMetadata.BaseRevision <= 0 || resetMetadata.BaseCommittedEntryCount <= 0 {
		t.Fatalf("assistant delta reset metadata = %+v, want cleared stream identity", resetMetadata)
	}
	if firstToolStartIdx < 0 {
		t.Fatalf("missing tool start event; events=%+v", events)
	}
	if !(assistantIdx < resetIdx && resetIdx < firstToolStartIdx) {
		t.Fatalf("stream reset order assistant=%d reset=%d tool_start=%d events=%+v", assistantIdx, resetIdx, firstToolStartIdx, events)
	}
	if finalDeltaStreaming != "done" {
		t.Fatalf("streaming during final delta = %q, want only the new turn text; events=%+v", finalDeltaStreaming, events)
	}
	if ongoing := strings.TrimSpace(eng.ChatSnapshot().Streaming); ongoing != "" {
		t.Fatalf("expected ongoing cleared after final commit, got %q", ongoing)
	}
}

func TestRestoredCompactedRuntimePublishesCommittedRangesInVisibleTranscriptCoordinates(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	if err := eng.AppendCommittedEntry("system", "before-1"); err != nil {
		t.Fatalf("append before-1: %v", err)
	}
	if err := eng.AppendCommittedEntry("system", "before-2"); err != nil {
		t.Fatalf("append before-2: %v", err)
	}
	if err := eng.steer("compact", steerHistoryReplacementIntent("local", compactionModeAuto, "", 1, "", "", llm.ItemsFromMessages([]llm.Message{{
		Role:        llm.RoleUser,
		MessageType: llm.MessageTypeCompactionSummary,
		Content:     "summary",
	}}))); err != nil {
		t.Fatalf("replace history: %v", err)
	}
	if got := eng.CommittedTranscriptEntryCount(); got != 3 {
		t.Fatalf("live committed entry count after compaction = %d, want 3", got)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}

	events := make([]Event, 0, 2)
	reopened := mustNewTestEngine(t, mustOpenTestSession(t, store.Dir()), &fakeClient{}, tools.NewRegistry(), Config{
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	if got := reopened.CommittedTranscriptEntryCount(); got != 3 {
		t.Fatalf("restored committed entry count after compaction = %d, want 3", got)
	}
	if err := reopened.AppendCommittedEntry("system", "after"); err != nil {
		t.Fatalf("append after: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v, want one committed append event", events)
	}
	if events[0].CommittedEntryStart != 3 || events[0].CommittedEntryCount != 4 {
		t.Fatalf("committed range = start %d count %d, want start 3 count 4; event=%+v", events[0].CommittedEntryStart, events[0].CommittedEntryCount, events[0])
	}
}

func TestRestoredCompactedRuntimeNextCommittedEventFollowsHistoryReplacementSeed(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	for _, text := range []string{"before-1", "before-2", "before-3"} {
		if err := eng.AppendCommittedEntry("system", text); err != nil {
			t.Fatalf("append %s: %v", text, err)
		}
	}
	if err := eng.steer("compact", steerHistoryReplacementIntent("local", compactionModeAuto, "", 1, "", "", llm.ItemsFromMessages([]llm.Message{{
		Role:        llm.RoleUser,
		MessageType: llm.MessageTypeCompactionSummary,
		Content:     "summary",
	}}))); err != nil {
		t.Fatalf("replace history: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}

	events := make([]Event, 0, 2)
	reopened := mustNewTestEngine(t, mustOpenTestSession(t, store.Dir()), &fakeClient{}, tools.NewRegistry(), Config{
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	if err := reopened.AppendCommittedEntry("system", "after-1"); err != nil {
		t.Fatalf("append after-1: %v", err)
	}
	if err := reopened.AppendCommittedEntry("system", "after-2"); err != nil {
		t.Fatalf("append after-2: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want two committed append events", events)
	}
	if events[0].CommittedEntryStart != 4 || events[0].CommittedEntryCount != 5 {
		t.Fatalf("first restored event range = start %d count %d, want start 4 count 5; event=%+v", events[0].CommittedEntryStart, events[0].CommittedEntryCount, events[0])
	}
	if events[1].CommittedEntryStart != 5 || events[1].CommittedEntryCount != 6 {
		t.Fatalf("second restored event range = start %d count %d, want start 5 count 6; event=%+v", events[1].CommittedEntryStart, events[1].CommittedEntryCount, events[1])
	}
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

type streamingToolLoopClient struct {
	calls int
}

func (c *streamingToolLoopClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("streamingToolLoopClient requires GenerateStream")
}

func (c *streamingToolLoopClient) GenerateStream(_ context.Context, _ llm.Request, onDelta func(string)) (llm.Response, error) {
	c.calls++
	switch c.calls {
	case 1:
		if onDelta != nil {
			onDelta("a")
			onDelta("b")
		}
		call := llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}
		return llm.Response{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ab", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{call},
			Usage:     llm.Usage{WindowTokens: 200000},
		}, nil
	default:
		if onDelta != nil {
			onDelta("done")
		}
		return llm.Response{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		}, nil
	}
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
