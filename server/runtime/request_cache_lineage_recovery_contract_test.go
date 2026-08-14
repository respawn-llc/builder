package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/config"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestCommittedCacheResponseObserverFailureRetainsLineage(t *testing.T) {
	t.Parallel()
	observerErr := errors.New("cache response observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	client := &fakeClient{responses: []llm.Response{
		{Usage: llm.Usage{CachedInputTokens: textutil.Value(7)}},
		{Usage: llm.Usage{CachedInputTokens: textutil.Value(0)}},
	}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:            "gpt-5",
		CacheWarningMode: config.CacheWarningModeDefault,
	})
	gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.LastSequence == 2
	}, observerErr)

	if _, err := generateTestActiveStep(
		context.Background(),
		engine,
		"first",
		client,
		cacheLineageRequest("conversation", transcript.CacheWarningScopeConversation, "alpha"),
	); !errors.Is(err, observerErr) {
		t.Fatalf("first cache observation error = %v, want observer error", err)
	}
	if _, err := generateTestActiveStep(
		context.Background(),
		engine,
		"second",
		client,
		cacheLineageRequest("conversation", transcript.CacheWarningScopeConversation, "beta"),
	); err != nil {
		t.Fatalf("second cache observation: %v", err)
	}

	assertBoundedCacheWarning(t, store, session.CacheScopeConversation, session.CacheWarningReasonNonPostfix, 7)
}

func TestVerboseCacheReuseDropPersistsTypedWarning(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		{Usage: llm.Usage{CachedInputTokens: textutil.Value(4)}},
		{Usage: llm.Usage{CachedInputTokens: textutil.Value(0)}},
	}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:            "gpt-5",
		CacheWarningMode: config.CacheWarningModeVerbose,
	})

	if _, err := generateTestActiveStep(
		context.Background(),
		engine,
		"first",
		client,
		cacheLineageRequest("conversation", transcript.CacheWarningScopeConversation, "alpha"),
	); err != nil {
		t.Fatalf("first cache observation: %v", err)
	}
	if _, err := generateTestActiveStep(
		context.Background(),
		engine,
		"second",
		client,
		cacheLineageRequest("conversation", transcript.CacheWarningScopeConversation, "alpha", "omega"),
	); err != nil {
		t.Fatalf("second cache observation: %v", err)
	}

	assertBoundedCacheWarning(t, store, session.CacheScopeConversation, session.CacheWarningReasonReuseDropped, 4)
}

func TestReviewerCacheLineagePersistsScopedWarning(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		{Usage: llm.Usage{CachedInputTokens: textutil.Value(8)}},
		{Usage: llm.Usage{CachedInputTokens: textutil.Value(6)}},
		{Usage: llm.Usage{CachedInputTokens: textutil.Value(10)}},
		{Usage: llm.Usage{CachedInputTokens: textutil.Value(0)}},
	}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:            "gpt-5",
		CacheWarningMode: config.CacheWarningModeVerbose,
	})
	requests := []llm.Request{
		cacheLineageRequest("conversation", transcript.CacheWarningScopeConversation, "alpha"),
		cacheLineageRequest("reviewer", transcript.CacheWarningScopeReviewer, "beta"),
		cacheLineageRequest("conversation", transcript.CacheWarningScopeConversation, "alpha", "omega"),
		cacheLineageRequest("reviewer", transcript.CacheWarningScopeReviewer, "gamma"),
	}
	for index, request := range requests {
		if _, err := generateTestActiveStep(context.Background(), engine, "cache-lineage", client, request); err != nil {
			t.Fatalf("cache observation %d: %v", index, err)
		}
	}

	assertBoundedCacheWarning(t, store, session.CacheScopeReviewer, session.CacheWarningReasonNonPostfix, 6)
}

func cacheLineageRequest(cacheKey string, scope transcript.CacheWarningScope, contents ...string) llm.Request {
	items := make([]llm.ResponseItem, 0, len(contents))
	for _, content := range contents {
		items = append(items, llm.ItemsFromMessages([]llm.Message{{
			Role:    llm.RoleUser,
			Content: textutil.Value(content),
		}})...)
	}
	return llm.Request{
		Model:            "gpt-5",
		PromptCacheKey:   cacheKey,
		PromptCacheScope: scope,
		Items:            items,
	}
}

func assertBoundedCacheWarning(
	t *testing.T,
	store *session.Store,
	scope session.CacheScope,
	reason session.CacheWarningReason,
	lostInputTokens int,
) {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(8)
	if err != nil {
		t.Fatalf("read bounded cache-lineage records: %v", err)
	}
	var warnings []session.CacheWarningRecord
	for _, record := range window.Records {
		warning, ok := mustSessionEventPayload(record).(session.CacheWarningRecord)
		if ok {
			warnings = append(warnings, warning)
		}
	}
	if len(warnings) != 1 {
		t.Fatalf("typed cache warnings = %+v, want one", warnings)
	}
	warning := warnings[0]
	if warning.Scope != scope || warning.Reason != reason ||
		warning.LostInputTokens == nil || *warning.LostInputTokens != lostInputTokens {
		t.Fatalf("typed cache warning = %+v", warning)
	}
}
