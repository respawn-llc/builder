package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestAssistantMessageAfterCacheWarningOwnsOnlyAssistantRange(t *testing.T) {
	var events []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:            "gpt-5",
			CacheWarningMode: config.CacheWarningModeVerbose,
			OnEvent:          func(event Event) { events = append(events, event) },
		},
	)
	if err := engine.observePromptCacheResponse("step", preparedCacheRequestObservation{
		request: persistedCacheRequestObserved{
			DigestVersion: requestCacheDigestVersion,
			CacheKey:      "cache-key",
			Scope:         transcript.CacheWarningScopeConversation,
			ChunkCount:    1,
			TerminalHash:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
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
		Content: textutil.Value("commentary"),
		Phase:   textutil.Value(llm.MessagePhaseCommentary),
		ToolCalls: []llm.ToolCall{
			{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
			{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
		},
	}
	if err := engine.steer(
		"step",
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{assistant},
		),
	); err != nil {
		t.Fatalf("persist assistant message: %v", err)
	}
	assistantEntries := TranscriptEntriesFromEvent(Event{Kind: EventAssistantMessage, Message: assistant})
	assistantStart := engine.CommittedTranscriptEntryCount() - len(assistantEntries)
	if err := engine.steer(
		"step",
		steerCommittedAssistantMessageIntent(assistant, &committedAssistantCoordinate{start: assistantStart}),
	); err != nil {
		t.Fatalf("publish assistant message: %v", err)
	}

	committed := make([]Event, 0, 2)
	for _, event := range events {
		if event.CommittedTranscriptChanged && len(TranscriptEntriesFromEvent(event)) > 0 {
			committed = append(committed, event)
		}
	}
	if len(committed) != 2 || committed[0].Kind != EventCacheWarning || committed[1].Kind != EventAssistantMessage {
		t.Fatalf("committed event sequence = %+v", committed)
	}
	cacheWarning, assistantEvent := committed[0], committed[1]
	cacheEntries := TranscriptEntriesFromEvent(cacheWarning)
	if !cacheWarning.CommittedEntryStartSet ||
		len(cacheEntries) != 1 ||
		assistantEvent.CommittedEntryStart != cacheWarning.CommittedEntryStart+len(cacheEntries) ||
		assistantEvent.CommittedEntryStart+len(assistantEntries) != assistantEvent.CommittedEntryCount {
		t.Fatalf(
			"cache/assistant committed ranges = cache:%+v assistant:%+v",
			cacheWarning,
			assistantEvent,
		)
	}
}

func TestFinalAnswerToolMaterializationPublishesToolCallBeforeLocalEntry(t *testing.T) {
	var events []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		&fakeClient{},
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	executor := defaultStepExecutor{
		engine: engine,
		tools:  &defaultToolExecutor{engine: engine},
	}
	call := llm.ToolCall{
		ID:    "call-1",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{}`),
	}
	if _, _, err := executor.materializeFinalAnswerToolCalls(
		context.Background(),
		"step",
		[]llm.ToolCall{call},
		nil,
	); err != nil {
		t.Fatalf("materialize final-answer tool call: %v", err)
	}
	if err := engine.steer(
		"step",
		steerLocalEntryIntent(storedLocalEntry{
			Role: string(transcript.EntryRoleReasoning),
			Text: "reasoning",
		}),
	); err != nil {
		t.Fatalf("append local entry: %v", err)
	}

	committed := durableTranscriptProjectionEvents(events)
	if len(committed) < 3 ||
		committed[0].Kind != EventAssistantMessage ||
		committed[len(committed)-1].Kind != EventLocalEntryAdded {
		t.Fatalf("committed materialization sequence = %+v", committed)
	}
	toolRows := TranscriptEntriesFromEvent(committed[0])
	if len(toolRows) != 1 ||
		toolRows[0].Role != "tool_call" ||
		toolRows[0].ToolCallID != call.ID {
		t.Fatalf("materialized tool-call rows = %+v", toolRows)
	}
	assertDurableTranscriptProjectionRangesContiguous(t, committed)
}

func TestStepLoopPublishesCommentaryToolEnvelopeBeforeReasoningAndToolResults(t *testing.T) {
	toolCalls := []llm.ToolCall{
		{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
		{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
		{ID: "call-3", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{}`)},
	}
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("commentary"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: toolCalls,
			Reasoning: []llm.ReasoningEntry{{
				Role: textutil.Value(string(transcript.EntryRoleReasoning)),
				Text: "reasoning",
			}},
			Usage: llm.Usage{WindowTokens: 200_000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("final"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200_000},
		},
	}}
	var events []Event
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	if _, err := engine.runStepLoopWithOptions(context.Background(), "step", "off", nil, false); err != nil {
		t.Fatalf("run step loop: %v", err)
	}

	committed := durableTranscriptProjectionEvents(events)
	if len(committed) < len(toolCalls)+3 || committed[0].Kind != EventAssistantMessage {
		t.Fatalf("committed step-loop events = %+v", committed)
	}
	envelope := TranscriptEntriesFromEvent(committed[0])
	if len(envelope) != len(toolCalls)+1 || envelope[0].Role != "assistant" {
		t.Fatalf("commentary tool envelope = %+v", envelope)
	}
	for index, call := range toolCalls {
		entry := envelope[index+1]
		if entry.Role != "tool_call" || entry.ToolCallID != call.ID {
			t.Fatalf("commentary tool envelope entry[%d] = %+v", index+1, entry)
		}
	}

	reasoningIndex, firstToolResultIndex := -1, -1
	for index, event := range committed {
		switch event.Kind {
		case EventLocalEntryAdded:
			if event.LocalEntry != nil && event.LocalEntry.Role == string(transcript.EntryRoleReasoning) {
				reasoningIndex = index
			}
		case EventToolCallCompleted:
			if firstToolResultIndex < 0 {
				firstToolResultIndex = index
			}
		}
	}
	if reasoningIndex != 1 || firstToolResultIndex <= reasoningIndex {
		t.Fatalf(
			"commentary/tool/reasoning ordering = reasoning_index:%d first_tool_result_index:%d events:%+v",
			reasoningIndex,
			firstToolResultIndex,
			committed,
		)
	}
	assertDurableTranscriptProjectionRangesContiguous(t, committed)
}

func TestTranscriptHydrationRetainsAdjacentRowsAroundProviderEmptyAssistant(t *testing.T) {
	const (
		beforeStepID = "11111111-1111-4111-8111-111111111111"
		emptyStepID  = "22222222-2222-4222-8222-222222222222"
		afterStepID  = "33333333-3333-4333-8333-333333333333"
	)
	store := mustCreateTestSession(t)
	for _, testCase := range []struct {
		stepID  string
		message llm.Message
	}{
		{
			stepID:  beforeStepID,
			message: llm.Message{Role: llm.RoleUser, Content: textutil.Value("before")},
		},
		{
			stepID: emptyStepID,
			message: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal)},
		},
		{
			stepID:  afterStepID,
			message: llm.Message{Role: llm.RoleUser, Content: textutil.Value("after")},
		},
	} {
		if _, _, err := appendTestEvent(t, store, testCase.stepID, testCase.message); err != nil {
			t.Fatalf("append persisted message for step %q: %v", testCase.stepID, err)
		}
	}

	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	var hydration TranscriptHydrationSnapshot
	if err := engine.WithTranscriptHydrationSnapshot(func(snapshot TranscriptHydrationSnapshot) error {
		hydration = snapshot
		return nil
	}); err != nil {
		t.Fatalf("hydrate restored transcript: %v", err)
	}
	if len(hydration.CommittedRows) != 2 {
		t.Fatalf("hydrated rows = %+v", hydration.CommittedRows)
	}
	for index, stepID := range []string{beforeStepID, afterStepID} {
		row := hydration.CommittedRows[index]
		if row.StepID != stepID || row.Kind != TranscriptCommittedRowFactUser || row.User == nil {
			t.Fatalf("hydrated row[%d] = %+v", index, row)
		}
	}
}

func durableTranscriptProjectionEvents(events []Event) []Event {
	committed := make([]Event, 0, len(events))
	for _, event := range events {
		switch event.Kind {
		case EventAssistantMessage, EventToolCallCompleted, EventLocalEntryAdded:
		default:
			continue
		}
		if event.CommittedTranscriptChanged && len(TranscriptEntriesFromEvent(event)) > 0 {
			committed = append(committed, event)
		}
	}
	return committed
}

func assertDurableTranscriptProjectionRangesContiguous(t *testing.T, events []Event) {
	t.Helper()
	for index, event := range events {
		if !event.CommittedEntryStartSet ||
			event.CommittedEntryCount < event.CommittedEntryStart {
			t.Fatalf("durable transcript event[%d] has invalid range: %+v", index, event)
		}
		if index > 0 && event.CommittedEntryStart != events[index-1].CommittedEntryCount {
			t.Fatalf(
				"durable transcript range discontinuity at %d: previous=%+v current=%+v",
				index,
				events[index-1],
				event,
			)
		}
	}
}
