package runtime

import (
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
