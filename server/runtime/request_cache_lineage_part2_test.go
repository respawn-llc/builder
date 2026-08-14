package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestCurrentEventLogRejectsUnsupportedCacheDigestVersion(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	_, _, err := appendTestEvent(t, store, "legacy-request", persistedCacheRequestObserved{
		DigestVersion: 999,
		CacheKey:      "cache-key-1",
		Scope:         transcript.CacheWarningScopeConversation,
		ChunkCount:    1,
		TerminalHash:  "0000000000000000000000000000000000000000000000000000000000000000",
	})
	if err == nil {
		t.Fatal("unsupported cache digest version was accepted by the strict v1 contract")
	}
}

func TestGenerateWithRetryClient_DoesNotInventCompactionCauseWithoutPriorLineageOnReopen(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "legacy-compact", historyReplacementPayload{
		Engine: "local",
		Mode:   string(compactionModeManual),
		Items:  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleAssistant, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")}}),
	}); err != nil {
		t.Fatalf("append history_replaced: %v", err)
	}

	reopened, err := runtimeTestSessionPersistence.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{{Usage: llm.Usage{InputTokens: 12}}}}
	eng := mustNewTestEngine(t, reopened, client, tools.NewRegistry(), Config{Model: "gpt-5", CacheWarningMode: config.CacheWarningModeVerbose})

	if _, err := generateTestActiveStep(context.Background(), eng, "step-1", client, testPromptCacheRequest(reopened.Meta().SessionID, "beta")); err != nil {
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
		items = append(items, llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: textutil.Value(message)}})...)
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
	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	warnings := make([]transcript.CacheWarning, 0, len(events))
	for _, evt := range events {
		if evt.Kind != sessionEventCacheWarning {
			continue
		}
		record, ok := mustSessionEventPayload(evt.Record).(session.CacheWarningRecord)
		if !ok {
			t.Fatalf("warning payload type = %T", mustSessionEventPayload(evt.Record))
		}
		warning := cacheWarningFromSessionRecord(record)
		warnings = append(warnings, warning)
	}
	return warnings
}

func persistedCacheWarningEventCount(t *testing.T, store *session.Store) int {
	t.Helper()
	events, err := collectTestEventRecords(store)
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
