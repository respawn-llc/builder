package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
)

func TestCommittedLocalEntrySteeringSerializesPersistProjectEmitOrder(t *testing.T) {
	t.Parallel()
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
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
	firstEntered, releaseFirst := gate.BlockNext()

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- eng.AppendCommittedEntry(t.Context(), "system", "first")
	}()
	select {
	case <-firstEntered:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for first append to enter persistence")
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- eng.AppendCommittedEntry(t.Context(), "system", "second")
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second append completed before first append released: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	releaseFirst()
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
	t.Parallel()
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
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
	stepID := runtimeTestStepID("cache-step")
	restoreStep := setTestActiveStep(eng, stepID)
	defer restoreStep()

	cachePersistEntered, releaseCachePersist := gate.BlockNext()

	cacheDone := make(chan error, 1)
	go func() {
		cacheDone <- eng.observePromptCacheResponse(stepID, preparedCacheRequestObservation{
			request: persistedCacheRequestObserved{
				DigestVersion: requestCacheDigestVersion,
				CacheKey:      "session-1/cache-key",
				Scope:         transcript.CacheWarningScopeConversation,
				ChunkCount:    1,
				TerminalHash:  "0000000000000000000000000000000000000000000000000000000000000000",
			},
			exactWarning: &transcript.CacheWarning{
				Scope:  transcript.CacheWarningScopeConversation,
				Reason: transcript.CacheWarningReasonNonPostfix,
			},
			previousCachedInputTokens: 10,
		}, llm.Usage{CachedInputTokens: textutil.Value(0)})
	}()
	select {
	case <-cachePersistEntered:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for cache warning observation to enter persistence")
	}

	appendDone := make(chan error, 1)
	go func() {
		appendDone <- eng.AppendCommittedEntry(t.Context(), "system", "feedback")
	}()
	select {
	case err := <-appendDone:
		t.Fatalf("committed feedback append completed before cache warning observation released: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	releaseCachePersist()
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
	persisted, err := collectTestEventRecords(store)
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
	t.Parallel()
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 4)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	stepID := runtimeTestStepID("step-1")
	restoreStep := setTestActiveStep(eng, stepID)
	defer restoreStep()

	if err := eng.observePromptCacheResponse(stepID, preparedCacheRequestObservation{
		request: persistedCacheRequestObserved{
			DigestVersion: requestCacheDigestVersion,
			CacheKey:      "session-1/cache-key",
			Scope:         transcript.CacheWarningScopeConversation,
			ChunkCount:    1,
			TerminalHash:  "0000000000000000000000000000000000000000000000000000000000000000",
		},
		exactWarning: &transcript.CacheWarning{
			Scope:  transcript.CacheWarningScopeConversation,
			Reason: transcript.CacheWarningReasonNonPostfix,
		},
		previousCachedInputTokens: 10,
	}, llm.Usage{CachedInputTokens: textutil.Value(0)}); err != nil {
		t.Fatalf("observe cache warning: %v", err)
	}

	assistant := llm.Message{
		Role:    llm.RoleAssistant,
		Content: textutil.Value("checking service"),
		Phase:   textutil.Value(llm.MessagePhaseCommentary),
		ToolCalls: []llm.ToolCall{
			{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"status"}`)},
			{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"ps"}`)},
		},
	}
	if err := eng.steer(runtimeTestStepID("step-1"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{assistant})); err != nil {
		t.Fatalf("persist assistant message: %v", err)
	}
	assistantEntries := VisibleChatEntriesFromMessage(assistant)
	assistantStart := eng.CommittedTranscriptEntryCount() - len(assistantEntries)
	if err := eng.steer(runtimeTestStepID("step-1"), steerCommittedAssistantMessageIntent(assistant, &committedAssistantCoordinate{start: assistantStart})); err != nil {
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
	t.Parallel()
	store := mustCreateTestSession(t)
	replacementEventEntered := make(chan struct{})
	releaseReplacementEvent := make(chan struct{})
	var replacementOnce sync.Once
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.Kind == EventLocalEntryAdded && evt.LocalEntry != nil && evt.LocalEntry.Text == "summary" {
				replacementOnce.Do(func() { close(replacementEventEntered) })
				<-releaseReplacementEvent
			}
		},
	})
	stepID := runtimeTestStepID("compact-step")
	restoreStep := setTestActiveStep(eng, stepID)
	defer restoreStep()

	replaceDone := make(chan error, 1)
	go func() {
		_, err := newCompactionPersistence(eng).replaceHistory(stepID, "local", compactionModeManual, llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			Content:     textutil.Value("summary"),
		}}))
		replaceDone <- err
	}()
	select {
	case <-replacementEventEntered:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for replacement projection event")
	}

	appendDone := make(chan error, 1)
	go func() {
		appendDone <- eng.AppendCommittedEntry(t.Context(), "system", "feedback")
	}()
	select {
	case err := <-appendDone:
		t.Fatalf("committed feedback append completed before history replacement finished emitting: %v", err)
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
	t.Parallel()
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 16)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	stepID := runtimeTestStepID("step-1")
	restoreStep := setTestActiveStep(eng, stepID)
	defer restoreStep()

	call := llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}
	if err := eng.steer(runtimeTestStepID("step-1"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}})); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}
	result := tools.Result{CallID: call.ID, Name: toolspec.ToolExecCommand, Output: json.RawMessage(`{"output":"/tmp","exit_code":0,"truncated":false}`)}
	if err := eng.steer(runtimeTestStepID("step-1"), steerToolCompletionIntent(result)); err != nil {
		t.Fatalf("persist tool completion: %v", err)
	}

	start := len(events)
	if err := eng.steer(runtimeTestStepID("step-1"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleTool, ToolCallID: textutil.Value(call.ID), Name: textutil.Value(string(result.Name)), Content: textutil.Value(string(result.Output))}})); err != nil {
		t.Fatalf("append tool mirror message: %v", err)
	}
	if got := events[start:]; len(got) != 0 {
		t.Fatalf("expected no generic committed advance for tool mirror message, got %+v", got)
	}
}

func TestVisibleToolMessageMutationPublishesCommittedEventBeforeLocalEntry(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 4)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	restoreStep := setTestActiveStep(eng, "step-1")
	defer restoreStep()

	toolMessage := llm.Message{
		Role:       llm.RoleTool,
		ToolCallID: textutil.Value("orphan-call"),
		Name:       textutil.Value(string(toolspec.ToolExecCommand)),
		Content:    textutil.Value(string(mustJSON(map[string]any{"output": "done", "exit_code": 0, "truncated": false}))),
	}
	if err := eng.steer(runtimeTestStepID("step-1"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{toolMessage})); err != nil {
		t.Fatalf("append visible tool message: %v", err)
	}
	if err := eng.steer(runtimeTestStepID("step-1"), steerLocalEntryIntent(storedLocalEntry{Role: "system", Text: "local note"})); err != nil {
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
	t.Parallel()
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 8)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	stepID := runtimeTestStepID("step-1")
	restoreStep := setTestActiveStep(eng, stepID)
	defer restoreStep()
	executor := defaultStepExecutor{
		engine: eng,
	}

	_, _, err := executor.materializeFinalAnswerToolCalls(
		context.Background(),
		stepID,
		acceptedResponseCalls{
			local: []llm.ToolCall{{
				ID:    "call-1",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"cmd":"pwd"}`),
			}},
			order: []acceptedResponseCallRef{{
				source: acceptedResponseCallLocal,
				index:  0,
			}},
		},
		stepLoopOptions{},
	)
	if err != nil {
		t.Fatalf("materialize final-answer tool calls: %v", err)
	}
	if err := eng.steer(runtimeTestStepID("step-1"), steerLocalEntryIntent(storedLocalEntry{Role: "reasoning", Text: "local note"})); err != nil {
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
	t.Parallel()
	toolCalls := []llm.ToolCall{
		{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
		{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
		{ID: "call-3", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
	}
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("commentary before tools"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: toolCalls,
			Reasoning: []llm.ReasoningEntry{
				{Role: textutil.Value("reasoning"), Text: "local note"},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	events := make([]Event, 0, 16)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	restoreStep := setTestActiveStep(eng, "step-1")
	defer restoreStep()

	if _, err := eng.runStepLoopWithOptions(context.Background(), runtimeTestStepID("step-1"), "off", nil, false); err != nil {
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

func TestStepLoopPersistsReasoningProgressAsDetailOnly(t *testing.T) {
	t.Parallel()
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("done"),
		},
		Reasoning: []llm.ReasoningEntry{{
			Role: textutil.Value("reasoning"),
			Text: "**Reviewing test flow for mode transitions**",
		}},
		Usage: llm.Usage{WindowTokens: 200000},
	}}}
	events := make([]Event, 0, 8)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t, tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: fakeTool{name: toolspec.ToolExecCommand},
	}), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	restoreStep := setTestActiveStep(eng, "step-1")
	defer restoreStep()

	if _, err := eng.runStepLoopWithOptions(context.Background(), runtimeTestStepID("step-1"), "off", nil, false); err != nil {
		t.Fatalf("run step loop: %v", err)
	}

	for _, evt := range committedTranscriptEventsWithEntries(events) {
		if evt.Kind != EventLocalEntryAdded || evt.LocalEntry == nil || evt.LocalEntry.Role != "reasoning" {
			continue
		}
		facts := TranscriptCommittedRowFactsFromEvent(evt)
		if len(facts) != 1 ||
			facts[0].Visibility != transcript.EntryVisibilityDetail ||
			facts[0].Integrity != transcript.RowIntegrityValid {
			t.Fatalf("reasoning progress facts = %+v, want one valid detail-only row", facts)
		}
		var hydration TranscriptHydrationSnapshot
		if err := eng.WithTranscriptHydrationSnapshot(func(snapshot TranscriptHydrationSnapshot) error {
			hydration = snapshot
			return nil
		}); err != nil {
			t.Fatalf("hydrate transcript: %v", err)
		}
		for _, row := range hydration.CommittedRows {
			if row.Kind == TranscriptCommittedRowFactReasoningTrace && row.ReasoningTrace != nil {
				if row.Visibility != transcript.EntryVisibilityDetail ||
					row.Integrity != transcript.RowIntegrityValid {
					t.Fatalf("hydrated reasoning row = %+v, want valid detail-only row", row)
				}
				return
			}
		}
		t.Fatalf("hydration rows = %+v, want persisted reasoning row", hydration.CommittedRows)
	}
	t.Fatal("reasoning progress was not committed")
}

func TestTranscriptHydrationSurvivesCommittedMessageWithoutProviderItems(t *testing.T) {
	t.Parallel()
	const (
		beforeStepID = "11111111-1111-4111-8111-111111111111"
		emptyStepID  = "22222222-2222-4222-8222-222222222222"
		afterStepID  = "33333333-3333-4333-8333-333333333333"
	)
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, beforeStepID, llm.Message{
		Role:    llm.RoleUser,
		Content: textutil.Value("before"),
	}); err != nil {
		t.Fatalf("append message before provider-empty assistant: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, emptyStepID, llm.Message{
		Role:  llm.RoleAssistant,
		Phase: textutil.Value(llm.MessagePhaseFinal),
	}); err != nil {
		t.Fatalf("append provider-empty assistant message: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, afterStepID, llm.Message{
		Role:    llm.RoleUser,
		Content: textutil.Value("after"),
	}); err != nil {
		t.Fatalf("append message after provider-empty assistant: %v", err)
	}

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	var hydration TranscriptHydrationSnapshot
	if err := eng.WithTranscriptHydrationSnapshot(func(snapshot TranscriptHydrationSnapshot) error {
		hydration = snapshot
		return nil
	}); err != nil {
		t.Fatalf("hydrate restored transcript: %v", err)
	}
	if len(hydration.CommittedRows) != 2 {
		t.Fatalf("hydrated rows = %+v, want only messages surrounding provider-empty assistant", hydration.CommittedRows)
	}
	if hydration.CommittedRows[0].StepID == nil ||
		*hydration.CommittedRows[0].StepID != beforeStepID ||
		hydration.CommittedRows[0].User == nil ||
		hydration.CommittedRows[0].User.Text != "before" {
		t.Fatalf("hydrated row before provider-empty assistant = %+v", hydration.CommittedRows[0])
	}
	if hydration.CommittedRows[1].StepID == nil ||
		*hydration.CommittedRows[1].StepID != afterStepID ||
		hydration.CommittedRows[1].User == nil ||
		hydration.CommittedRows[1].User.Text != "after" {
		t.Fatalf("hydrated row after provider-empty assistant = %+v", hydration.CommittedRows[1])
	}
}

func TestHistoryReplacementPublishesCompactionPreservedUserMessageBeforeLocalEntry(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 4)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	compactionStepID := runtimeTestStepID("compact-step")
	restoreStep := setTestActiveStep(eng, compactionStepID)
	defer restoreStep()

	carryover, ok := compactionPreservedUserMessage("keep the active requirement")
	if !ok {
		t.Fatal("expected non-empty compaction-preserved user message")
	}
	receipt, err := newCompactionPersistence(eng).replaceHistory(
		compactionStepID,
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{
			{Role: llm.RoleUser, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
			carryover,
		}),
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("replace history receipt=%+v error=%v", receipt, err)
	}
	if err := eng.steer(compactionStepID, steerLocalEntryIntent(storedLocalEntry{Role: "compaction_summary", Text: "summary"})); err != nil {
		t.Fatalf("append local entry: %v", err)
	}

	if len(events) != 4 {
		t.Fatalf("event count = %d, want 4 events=%+v", len(events), events)
	}
	if entries := TranscriptEntriesFromEvent(events[0]); events[0].Kind != EventLocalEntryAdded || len(entries) != 1 || entries[0].Role != string(transcript.EntryRoleCompactionSummary) {
		t.Fatalf("first replacement event = %+v, want compaction summary", events[0])
	}
	if !events[0].CommittedEntryStartSet || events[0].CommittedEntryStart != 0 || events[0].CommittedEntryCount != 1 {
		t.Fatalf("first event range = start_set:%t start:%d count:%d, want start 0 count 1", events[0].CommittedEntryStartSet, events[0].CommittedEntryStart, events[0].CommittedEntryCount)
	}
	if entries := TranscriptEntriesFromEvent(events[1]); events[1].Kind != EventLocalEntryAdded || len(entries) != 1 || entries[0].Role != string(transcript.EntryRoleCompactionPreservedUserMessage) {
		t.Fatalf("second replacement event = %+v, want compaction-preserved user message", events[1])
	}
	if !events[1].CommittedEntryStartSet || events[1].CommittedEntryStart != 1 || events[1].CommittedEntryCount != 2 {
		t.Fatalf("second event range = start_set:%t start:%d count:%d, want start 1 count 2", events[1].CommittedEntryStartSet, events[1].CommittedEntryStart, events[1].CommittedEntryCount)
	}
	if events[2].Kind != EventConversationUpdated || events[2].CommittedTranscriptChanged {
		t.Fatalf("third event = %+v, want replacement conversation update", events[2])
	}
	if events[3].Kind != EventLocalEntryAdded {
		t.Fatalf("fourth event kind = %s, want %s; events=%+v", events[3].Kind, EventLocalEntryAdded, events)
	}
	if !events[3].CommittedEntryStartSet || events[3].CommittedEntryStart != 2 || events[3].CommittedEntryCount != 3 {
		t.Fatalf("fourth event range = start_set:%t start:%d count:%d, want start 2 count 3", events[3].CommittedEntryStartSet, events[3].CommittedEntryStart, events[3].CommittedEntryCount)
	}
	assertRuntimeEventsAdvanceCommittedFrontierContiguously(t, events)
}
