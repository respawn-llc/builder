package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/config"
	"core/shared/transcript"
)

func TestGenerateWithRetryClient_RestoreSkipsDigestVersionMismatch(t *testing.T) {
	store := mustCreateTestSession(t)
	legacyRequest := persistedCacheRequestObserved{
		DigestVersion: 999,
		CacheKey:      "cache-key-1",
		Scope:         transcript.CacheWarningScopeConversation,
		ChunkCount:    1,
		TerminalHash:  "legacy-hash",
	}
	legacyResponse := persistedCacheResponseObserved{
		DigestVersion:        999,
		CacheKey:             "cache-key-1",
		Scope:                transcript.CacheWarningScopeConversation,
		ChunkCount:           1,
		TerminalHash:         "legacy-hash",
		HasCachedInputTokens: true,
		CachedInputTokens:    42,
	}
	if _, _, err := store.AppendEvent("legacy-request", sessionEventCacheRequestObserved, legacyRequest); err != nil {
		t.Fatalf("append legacy request: %v", err)
	}
	if _, _, err := store.AppendEvent("legacy-response", sessionEventCacheResponseObserved, legacyResponse); err != nil {
		t.Fatalf("append legacy response: %v", err)
	}

	reopened, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{{Usage: llm.Usage{InputTokens: 12}}}}
	eng := mustNewTestEngine(t, reopened, client, tools.NewRegistry(), Config{Model: "gpt-5", CacheWarningMode: config.CacheWarningModeDefault})

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest("cache-key-1", "beta"), nil, nil, nil); err != nil {
		t.Fatalf("generate after reopen: %v", err)
	}

	warnings := persistedCacheWarnings(t, reopened)
	if len(warnings) != 0 {
		t.Fatalf("warning count = %d, want 0", len(warnings))
	}
}

func TestGenerateWithRetryClient_DoesNotInventCompactionCauseWithoutPriorLineageOnReopen(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := store.AppendEvent("legacy-compact", "history_replaced", historyReplacementPayload{
		Engine: "local",
		Mode:   string(compactionModeManual),
		Items:  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleAssistant, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}}),
	}); err != nil {
		t.Fatalf("append history_replaced: %v", err)
	}

	reopened, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{{Usage: llm.Usage{InputTokens: 12}}}}
	eng := mustNewTestEngine(t, reopened, client, tools.NewRegistry(), Config{Model: "gpt-5", CacheWarningMode: config.CacheWarningModeVerbose})

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest(reopened.Meta().SessionID, "beta"), nil, nil, nil); err != nil {
		t.Fatalf("generate after reopen: %v", err)
	}

	warnings := persistedCacheWarnings(t, reopened)
	if len(warnings) != 0 {
		t.Fatalf("warning count = %d, want 0", len(warnings))
	}
}

func testPromptCacheRequest(cacheKey string, messages ...string) llm.Request {
	items := make([]llm.ResponseItem, 0, len(messages))
	for _, message := range messages {
		items = append(items, llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: message}})...)
	}
	return llm.Request{ToolChoiceMode: llm.ToolChoiceModeAutomatic,
		Model:            "gpt-5",
		SystemPrompt:     "system",
		PromptCacheKey:   cacheKey,
		PromptCacheScope: transcript.CacheWarningScopeConversation,
		Items:            items,
	}
}

func testReviewerPromptCacheRequest(cacheKey string, messages ...string) llm.Request {
	request := testPromptCacheRequest(cacheKey, messages...)
	request.PromptCacheScope = transcript.CacheWarningScopeReviewer
	return request
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func persistedCacheWarnings(t *testing.T, store *session.Store) []transcript.CacheWarning {
	t.Helper()
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	warnings := make([]transcript.CacheWarning, 0, len(events))
	for _, evt := range events {
		if evt.Kind != sessionEventCacheWarning {
			continue
		}
		var warning transcript.CacheWarning
		if err := json.Unmarshal(evt.Payload, &warning); err != nil {
			t.Fatalf("decode warning: %v", err)
		}
		warnings = append(warnings, warning)
	}
	return warnings
}

func persistedCacheWarningEventCount(t *testing.T, store *session.Store) int {
	t.Helper()
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	count := 0
	for _, evt := range events {
		if evt.Kind == sessionEventCacheWarning {
			count++
		}
	}
	return count
}
