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

	committed := make([]Event, 0, 3)
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
	for index, event := range committed {
		if !event.CommittedEntryStartSet ||
			event.CommittedEntryCount < event.CommittedEntryStart {
			t.Fatalf("committed materialization event[%d] has invalid range: %+v", index, event)
		}
		if index > 0 && event.CommittedEntryStart != committed[index-1].CommittedEntryCount {
			t.Fatalf(
				"committed materialization range discontinuity at %d: previous=%+v current=%+v",
				index,
				committed[index-1],
				event,
			)
		}
	}
}
