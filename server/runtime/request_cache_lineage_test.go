package runtime

import (
	"bytes"
	"context"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/server/workflow"
	"core/shared/config"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWorkflowCacheFriendlyCompletionModesKeepRequestMetadataStableAcrossContracts(t *testing.T) {
	tests := []struct {
		name string
		mode config.WorkflowCompletionMode
	}{
		{name: "shell command", mode: config.WorkflowCompletionModeShellCommand},
		{name: "unstructured output", mode: config.WorkflowCompletionModeUnstructured},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			workflowCfg := testWorkflowConfig(&fakeWorkflowController{}, tt.mode)
			eng := mustNewWorkflowTestEngine(t, store, &fakeClient{}, workflowCfg, Config{
				EnabledTools: []toolspec.ID{toolspec.ToolExecCommand},
			})

			reqBefore, err := eng.buildRequest(context.Background(), "step-before", true)
			if err != nil {
				t.Fatalf("build before request: %v", err)
			}
			eng.cfg.WorkflowRun.Contract.Transitions[0].Parameters = []workflow.Parameter{{Key: "different", Description: "Changed transition output."}}
			reqAfter, err := eng.buildRequest(context.Background(), "step-after", true)
			if err != nil {
				t.Fatalf("build after request: %v", err)
			}
			beforeChunks, err := promptCacheChunks(reqBefore)
			if err != nil {
				t.Fatalf("before prompt cache chunks: %v", err)
			}
			afterChunks, err := promptCacheChunks(reqAfter)
			if err != nil {
				t.Fatalf("after prompt cache chunks: %v", err)
			}
			if len(beforeChunks) == 0 || len(afterChunks) == 0 {
				t.Fatalf("metadata chunks missing: before=%d after=%d", len(beforeChunks), len(afterChunks))
			}
			if !bytes.Equal(beforeChunks[0], afterChunks[0]) {
				t.Fatalf("%s metadata chunk changed across contract drift:\nbefore=%s\nafter=%s", tt.name, beforeChunks[0], afterChunks[0])
			}
		})
	}
}

func TestCacheWarningSteeringUsesCacheWarningModeVisibility(t *testing.T) {
	tests := []struct {
		name string
		mode config.CacheWarningMode
		want transcript.EntryVisibility
	}{
		{name: "default", mode: config.CacheWarningModeDefault, want: transcript.EntryVisibilityVerbose},
		{name: "verbose", mode: config.CacheWarningModeVerbose, want: transcript.EntryVisibilityAll},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := make([]Event, 0, 1)
			store := mustCreateTestSession(t)
			eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
				CacheWarningMode: tt.mode,
				OnEvent: func(evt Event) {
					events = append(events, evt)
				},
			})
			if err := eng.steer("cache-step", steerCacheWarningIntent(transcript.CacheWarning{Scope: transcript.CacheWarningScopeConversation, Reason: transcript.CacheWarningReasonReuseDropped}, cacheWarningEntryVisibility(tt.mode), true)); err != nil {
				t.Fatalf("steer cache warning: %v", err)
			}
			snapshot := eng.ChatSnapshot()
			if len(snapshot.Entries) != 1 {
				t.Fatalf("expected one cache warning entry, got %d", len(snapshot.Entries))
			}
			if got := snapshot.Entries[0].Visibility; got != tt.want {
				t.Fatalf("cache warning visibility = %q, want %q", got, tt.want)
			}
			if len(events) != 1 || events[0].Kind != EventCacheWarning {
				t.Fatalf("events = %+v, want one cache warning event", events)
			}
			if events[0].CacheWarningVisibility != tt.want {
				t.Fatalf("cache warning event visibility = %q, want %q", events[0].CacheWarningVisibility, tt.want)
			}
		})
	}
}

type transportStaticAuth struct{}

func (transportStaticAuth) AuthorizationHeader(context.Context) (string, error) {
	return "Bearer token", nil
}

func newCacheWarningTestEngine(t *testing.T, client llm.Client, mode config.CacheWarningMode) (*session.Store, *Engine) {
	t.Helper()
	store := mustCreateTestSession(t)
	return store, mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{CacheWarningMode: mode})
}

func TestGenerateWithRetryClient_PersistsExactNonPostfixCacheWarningInDefaultMode(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{Usage: llm.Usage{InputTokens: 10, HasCachedInputTokens: true, CachedInputTokens: 7}}, {Usage: llm.Usage{InputTokens: 12, HasCachedInputTokens: true, CachedInputTokens: 0}}}}
	store, eng := newCacheWarningTestEngine(t, client, config.CacheWarningModeDefault)

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest("cache-key-1", "alpha"), nil, nil, nil); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if warnings := persistedCacheWarnings(t, store); len(warnings) != 0 {
		t.Fatalf("warning count after baseline success = %d, want 0", len(warnings))
	}
	if _, err := eng.generateWithRetryClient(context.Background(), "step-2", client, testPromptCacheRequest("cache-key-1", "beta"), nil, nil, nil); err != nil {
		t.Fatalf("second generate: %v", err)
	}

	warnings := persistedCacheWarnings(t, store)
	if len(warnings) != 1 {
		t.Fatalf("warning count = %d, want 1", len(warnings))
	}
	if warnings[0].Reason != transcript.CacheWarningReasonNonPostfix {
		t.Fatalf("warning reason = %q, want %q", warnings[0].Reason, transcript.CacheWarningReasonNonPostfix)
	}
	if warnings[0].LostInputTokens != 7 {
		t.Fatalf("warning lost input tokens = %d, want 7", warnings[0].LostInputTokens)
	}
}

func TestGenerateWithRetryClient_SuppressesExactNonPostfixWarningWhenProviderReuseIncreases(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Usage: llm.Usage{InputTokens: 10, HasCachedInputTokens: true, CachedInputTokens: 2_432}},
		{Usage: llm.Usage{InputTokens: 12, HasCachedInputTokens: true, CachedInputTokens: 12_160}},
	}}
	store, eng := newCacheWarningTestEngine(t, client, config.CacheWarningModeDefault)

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest("cache-key-1", "alpha"), nil, nil, nil); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if _, err := eng.generateWithRetryClient(context.Background(), "step-2", client, testPromptCacheRequest("cache-key-1", "beta"), nil, nil, nil); err != nil {
		t.Fatalf("second generate: %v", err)
	}

	if warnings := persistedCacheWarnings(t, store); len(warnings) != 0 {
		t.Fatalf("warning count = %d, want 0: %+v", len(warnings), warnings)
	}
	if got := persistedCacheWarningEventCount(t, store); got != 0 {
		t.Fatalf("cache_warning event count = %d, want 0", got)
	}
}

func TestGenerateWithRetryClient_SuppressesExactNonPostfixWarningWithoutProviderCacheMetadata(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Usage: llm.Usage{InputTokens: 10}},
		{Usage: llm.Usage{InputTokens: 12}},
	}}
	store, eng := newCacheWarningTestEngine(t, client, config.CacheWarningModeDefault)

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest("cache-key-1", "alpha"), nil, nil, nil); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if _, err := eng.generateWithRetryClient(context.Background(), "step-2", client, testPromptCacheRequest("cache-key-1", "beta"), nil, nil, nil); err != nil {
		t.Fatalf("second generate: %v", err)
	}

	if got := persistedCacheWarningEventCount(t, store); got != 0 {
		t.Fatalf("cache_warning event count = %d, want 0", got)
	}
}

func TestNew_RejectsInvalidCacheWarningMode(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, err := New(store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5", CacheWarningMode: config.CacheWarningMode("bogus")}); err == nil {
		t.Fatal("expected invalid cache_warning_mode to fail")
	}
}

func TestGenerateWithRetryClient_OffModeSuppressesExactNonPostfixWarning(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{Usage: llm.Usage{InputTokens: 10}}, {Usage: llm.Usage{InputTokens: 12}}}}
	store, eng := newCacheWarningTestEngine(t, client, config.CacheWarningModeOff)

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest("cache-key-1", "alpha"), nil, nil, nil); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if _, err := eng.generateWithRetryClient(context.Background(), "step-2", client, testPromptCacheRequest("cache-key-1", "beta"), nil, nil, nil); err != nil {
		t.Fatalf("second generate: %v", err)
	}

	warnings := persistedCacheWarnings(t, store)
	if len(warnings) != 0 {
		t.Fatalf("warning count = %d, want 0", len(warnings))
	}
}

func TestGenerateWithRetryClient_FailedRequestDoesNotAdvanceLineage(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond})

	client := &fakeClient{responses: []llm.Response{{Usage: llm.Usage{InputTokens: 10}}, {Usage: llm.Usage{InputTokens: 12}}}}
	store, eng := newCacheWarningTestEngine(t, client, config.CacheWarningModeDefault)
	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest("cache-key-1", "alpha"), nil, nil, nil); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	failingClient := failingCacheClient{caps: llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, SupportsPromptCacheKey: true, IsOpenAIFirstParty: true}}
	if _, err := eng.generateWithRetryClient(context.Background(), "step-2", &failingClient, testPromptCacheRequest("cache-key-1", "beta"), nil, nil, nil); err == nil {
		t.Fatal("expected failed generate")
	}
	if _, err := eng.generateWithRetryClient(context.Background(), "step-3", client, testPromptCacheRequest("cache-key-1", "alpha", "omega"), nil, nil, nil); err != nil {
		t.Fatalf("third generate: %v", err)
	}
	warnings := persistedCacheWarnings(t, store)
	if len(warnings) != 0 {
		t.Fatalf("warning count = %d, want 0", len(warnings))
	}
}

func TestGenerateWithRetryClient_PersistsVerboseReuseDropWarning(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{Usage: llm.Usage{InputTokens: 10, HasCachedInputTokens: true, CachedInputTokens: 4}}, {Usage: llm.Usage{InputTokens: 12, HasCachedInputTokens: true, CachedInputTokens: 0}}}}
	store, eng := newCacheWarningTestEngine(t, client, config.CacheWarningModeVerbose)

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest("cache-key-1", "alpha"), nil, nil, nil); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if _, err := eng.generateWithRetryClient(context.Background(), "step-2", client, testPromptCacheRequest("cache-key-1", "alpha", "omega"), nil, nil, nil); err != nil {
		t.Fatalf("second generate: %v", err)
	}

	warnings := persistedCacheWarnings(t, store)
	if len(warnings) != 1 {
		t.Fatalf("warning count = %d, want 1", len(warnings))
	}
	if warnings[0].Reason != transcript.CacheWarningReasonReuseDropped {
		t.Fatalf("warning reason = %q, want %q", warnings[0].Reason, transcript.CacheWarningReasonReuseDropped)
	}
	if warnings[0].LostInputTokens != 4 {
		t.Fatalf("warning lost input tokens = %d, want 4", warnings[0].LostInputTokens)
	}
}

func TestGenerateWithRetryClient_DoesNotWarnAcrossDistinctCacheKeys(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{Usage: llm.Usage{InputTokens: 10}}, {Usage: llm.Usage{InputTokens: 12}}}}
	store, eng := newCacheWarningTestEngine(t, client, config.CacheWarningModeVerbose)

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest("cache-key-1", "alpha"), nil, nil, nil); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if _, err := eng.generateWithRetryClient(context.Background(), "step-2", client, testPromptCacheRequest("cache-key-2", "beta"), nil, nil, nil); err != nil {
		t.Fatalf("second generate: %v", err)
	}

	warnings := persistedCacheWarnings(t, store)
	if len(warnings) != 0 {
		t.Fatalf("warning count = %d, want 0", len(warnings))
	}
}

func TestBuildRequest_SkipsPromptCacheKeyForUnsupportedProvider(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	req, err := eng.buildRequestWithExtraItems(context.Background(), "", []llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "hello"}}, true)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if req.PromptCacheKey != "" {
		t.Fatalf("PromptCacheKey = %q, want empty", req.PromptCacheKey)
	}
	if req.PromptCacheScope != "" {
		t.Fatalf("PromptCacheScope = %q, want empty", req.PromptCacheScope)
	}
}

func TestBuildRequest_UsesBasePromptCacheKeyBeforeFirstCompactionWhenProviderSupportsIt(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, SupportsPromptCacheKey: true}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	req, err := eng.buildRequestWithExtraItems(context.Background(), "", []llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "hello"}}, true)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if got, want := req.SessionID, eng.SessionID(); got != want {
		t.Fatalf("SessionID = %q, want %q", got, want)
	}
	if got, want := req.PromptCacheKey, conversationPromptCacheKey(eng.SessionID(), eng.compactionRuntimeState().Count()); got != want {
		t.Fatalf("PromptCacheKey = %q, want %q", got, want)
	}
	if req.PromptCacheScope != transcript.CacheWarningScopeConversation {
		t.Fatalf("PromptCacheScope = %q, want %q", req.PromptCacheScope, transcript.CacheWarningScopeConversation)
	}
}

func TestBuildRequest_RotatesPromptCacheKeyWithRequestSessionIDAfterCompaction(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, SupportsPromptCacheKey: true}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	eng.compactionRuntimeState().SetCount(1)
	req, err := eng.buildRequestWithExtraItems(context.Background(), "", []llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "hello"}}, true)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if got, want := req.SessionID, eng.SessionID(); got != want {
		t.Fatalf("SessionID = %q, want %q", got, want)
	}
	if got, want := req.PromptCacheKey, conversationPromptCacheKey(eng.SessionID(), eng.compactionRuntimeState().Count()); got != want {
		t.Fatalf("PromptCacheKey = %q, want %q", got, want)
	}
}

func TestBuildRequest_RotatesPromptCacheKeyFromPersistedCompactionOnReopen(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := store.AppendEvent("legacy-compact", "history_replaced", historyReplacementPayload{
		Engine: "local",
		Mode:   string(compactionModeManual),
		Items:  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleAssistant, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}}),
	}); err != nil {
		t.Fatalf("append history_replaced: %v", err)
	}

	reopened, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	client := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, SupportsPromptCacheKey: true}}
	eng := mustNewTestEngine(t, reopened, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	req, err := eng.buildRequestWithExtraItems(context.Background(), "", []llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "hello"}}, true)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if got, want := req.SessionID, eng.SessionID(); got != want {
		t.Fatalf("SessionID = %q, want %q", got, want)
	}
	if got, want := req.PromptCacheKey, conversationPromptCacheKey(eng.SessionID(), eng.compactionRuntimeState().Count()); got != want {
		t.Fatalf("PromptCacheKey = %q, want %q", got, want)
	}
}

func TestLocalCompactionSummary_UsesMainConversationRequestIdentityAndPrompt(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{
		caps:      llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, SupportsPromptCacheKey: true},
		responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: "summary"}}},
	}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", EnabledTools: []toolspec.ID{toolspec.ToolExecCommand}})
	eng.compactionRuntimeState().SetCount(1)
	input := llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: "alpha"}, {Role: llm.RoleAssistant, Content: "beta"}})
	if _, err := eng.localCompactionSummary(context.Background(), input, compactionInstructions("keep API details"), compactionModeManual); err != nil {
		t.Fatalf("local compaction summary: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("model calls = %d, want 1", len(client.calls))
	}
	req := client.calls[0]
	locked, err := eng.ensureLocked()
	if err != nil {
		t.Fatalf("ensure locked: %v", err)
	}
	if got, want := req.SessionID, eng.SessionID(); got != want {
		t.Fatalf("SessionID = %q, want %q", got, want)
	}
	if got, want := req.PromptCacheKey, conversationPromptCacheKey(eng.SessionID(), eng.compactionRuntimeState().Count()); got != want {
		t.Fatalf("PromptCacheKey = %q, want %q", got, want)
	}
	if got, want := req.PromptCacheScope, transcript.CacheWarningScopeConversation; got != want {
		t.Fatalf("PromptCacheScope = %q, want %q", got, want)
	}
	want, err := eng.systemPrompt(locked)
	if err != nil {
		t.Fatalf("systemPrompt: %v", err)
	}
	if got := req.SystemPrompt; got != want {
		t.Fatalf("SystemPrompt mismatch\ngot: %q\nwant: %q", got, want)
	}
	if got, want := req.ReasoningEffort, eng.ThinkingLevel(); got != want {
		t.Fatalf("ReasoningEffort = %q, want %q", got, want)
	}
	if got, want := req.FastMode, eng.FastModeEnabled(); got != want {
		t.Fatalf("FastMode = %v, want %v", got, want)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != string(toolspec.ToolExecCommand) {
		t.Fatalf("Tools = %+v, want exec_command tool contract", req.Tools)
	}
}

func TestOpenAITransport_UsesExpectedSessionHeadersAndPromptCacheKeysAcrossConversationSupervisorAndReopen(t *testing.T) {
	type capturedRequest struct {
		path      string
		sessionID string
		payload   map[string]any
	}
	var capturedRequests []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		captured := capturedRequest{
			path:      r.URL.Path,
			sessionID: r.Header.Get("session_id"),
			payload:   payload,
		}
		capturedRequests = append(capturedRequests, captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	transport := llm.NewHTTPTransport(transportStaticAuth{})
	transport.BaseURL = server.URL + "/v1"
	transport.Client = server.Client()
	openAIClient := llm.NewOpenAIClient(transport)

	store := mustCreateTestSession(t)
	engineClient := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, SupportsPromptCacheKey: true, IsOpenAIFirstParty: true}}
	eng := mustNewTestEngine(t, store, engineClient, tools.NewRegistry(), Config{Model: "gpt-5", Reviewer: ReviewerConfig{Model: "gpt-5"}})
	send := func(req llm.Request) capturedRequest {
		t.Helper()
		before := len(capturedRequests)
		if _, err := openAIClient.Generate(context.Background(), req); err != nil {
			t.Fatalf("transport generate: %v", err)
		}
		if len(capturedRequests) != before+1 {
			t.Fatalf("captured requests = %d, want %d", len(capturedRequests), before+1)
		}
		return capturedRequests[len(capturedRequests)-1]
	}

	mainBeforeReq, err := eng.buildRequestWithExtraItems(context.Background(), "", []llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "before"}}, true)
	if err != nil {
		t.Fatalf("build main before request: %v", err)
	}
	mainBefore := send(mainBeforeReq)
	if got, want := mainBefore.path, "/v1/responses"; got != want {
		t.Fatalf("main before path = %q, want %q", got, want)
	}
	if got, want := mainBefore.sessionID, store.Meta().SessionID; got != want {
		t.Fatalf("main before session_id header = %q, want %q", got, want)
	}
	if got, want := stringValue(mainBefore.payload["prompt_cache_key"]), store.Meta().SessionID; got != want {
		t.Fatalf("main before prompt_cache_key = %q, want %q", got, want)
	}

	reviewerBeforeReq, err := eng.buildReviewerRequest(context.Background(), engineClient)
	if err != nil {
		t.Fatalf("build reviewer before request: %v", err)
	}
	reviewerBefore := send(reviewerBeforeReq)
	if got, want := reviewerBefore.sessionID, reviewerSessionID(store.Meta().SessionID); got != want {
		t.Fatalf("reviewer before session_id header = %q, want %q", got, want)
	}
	if got, want := stringValue(reviewerBefore.payload["prompt_cache_key"]), conversationPromptCacheKey(reviewerSessionID(store.Meta().SessionID), 0); got != want {
		t.Fatalf("reviewer before prompt_cache_key = %q, want %q", got, want)
	}

	eng.compactionRuntimeState().SetCount(1)
	mainAfterReq, err := eng.buildRequestWithExtraItems(context.Background(), "", []llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "after"}}, true)
	if err != nil {
		t.Fatalf("build main after request: %v", err)
	}
	mainAfter := send(mainAfterReq)
	if got, want := mainAfter.sessionID, store.Meta().SessionID; got != want {
		t.Fatalf("main after session_id header = %q, want %q", got, want)
	}
	if got, want := stringValue(mainAfter.payload["prompt_cache_key"]), conversationPromptCacheKey(store.Meta().SessionID, 1); got != want {
		t.Fatalf("main after prompt_cache_key = %q, want %q", got, want)
	}

	reviewerAfterReq, err := eng.buildReviewerRequest(context.Background(), engineClient)
	if err != nil {
		t.Fatalf("build reviewer after request: %v", err)
	}
	reviewerAfter := send(reviewerAfterReq)
	if got, want := reviewerAfter.sessionID, reviewerSessionID(store.Meta().SessionID); got != want {
		t.Fatalf("reviewer after session_id header = %q, want %q", got, want)
	}
	if got, want := stringValue(reviewerAfter.payload["prompt_cache_key"]), conversationPromptCacheKey(reviewerSessionID(store.Meta().SessionID), 1); got != want {
		t.Fatalf("reviewer after prompt_cache_key = %q, want %q", got, want)
	}

	if _, _, err := store.AppendEvent("legacy-compact", "history_replaced", historyReplacementPayload{
		Engine: "local",
		Mode:   string(compactionModeManual),
		Items:  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleAssistant, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}}),
	}); err != nil {
		t.Fatalf("append history_replaced: %v", err)
	}
	reopened, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	reopenedEng := mustNewTestEngine(t, reopened, engineClient, tools.NewRegistry(), Config{Model: "gpt-5", Reviewer: ReviewerConfig{Model: "gpt-5"}})
	reopenedMainReq, err := reopenedEng.buildRequestWithExtraItems(context.Background(), "", []llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "reopened"}}, true)
	if err != nil {
		t.Fatalf("build reopened main request: %v", err)
	}
	reopenedMain := send(reopenedMainReq)
	if got, want := reopenedMain.sessionID, reopened.Meta().SessionID; got != want {
		t.Fatalf("reopened main session_id header = %q, want %q", got, want)
	}
	if got, want := stringValue(reopenedMain.payload["prompt_cache_key"]), conversationPromptCacheKey(reopened.Meta().SessionID, reopenedEng.compactionRuntimeState().Count()); got != want {
		t.Fatalf("reopened main prompt_cache_key = %q, want %q", got, want)
	}

	reopenedReviewerReq, err := reopenedEng.buildReviewerRequest(context.Background(), engineClient)
	if err != nil {
		t.Fatalf("build reopened reviewer request: %v", err)
	}
	reopenedReviewer := send(reopenedReviewerReq)
	if got, want := reopenedReviewer.sessionID, reviewerSessionID(reopened.Meta().SessionID); got != want {
		t.Fatalf("reopened reviewer session_id header = %q, want %q", got, want)
	}
	if got, want := stringValue(reopenedReviewer.payload["prompt_cache_key"]), conversationPromptCacheKey(reviewerSessionID(reopened.Meta().SessionID), reopenedEng.compactionRuntimeState().Count()); got != want {
		t.Fatalf("reopened reviewer prompt_cache_key = %q, want %q", got, want)
	}
}

func TestReviewerSuggestions_SkipsPromptCacheKeyForUnsupportedProvider(t *testing.T) {
	store := mustCreateTestSession(t)
	engineClient := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, SupportsPromptCacheKey: true, IsOpenAIFirstParty: true}}
	reviewerClient := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true}, responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`}}}}
	eng := mustNewTestEngine(t, store, engineClient, tools.NewRegistry(), Config{Model: "gpt-5", Reviewer: ReviewerConfig{Model: "gpt-5"}})
	if _, err := eng.runReviewerSuggestions(context.Background(), "step-1", reviewerClient); err != nil {
		t.Fatalf("run reviewer suggestions: %v", err)
	}
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("reviewer client calls = %d, want 1", len(reviewerClient.calls))
	}
	if got, want := reviewerClient.calls[0].SessionID, reviewerSessionID(store.Meta().SessionID); got != want {
		t.Fatalf("reviewer SessionID = %q, want %q", got, want)
	}
	if reviewerClient.calls[0].PromptCacheKey != "" {
		t.Fatalf("reviewer PromptCacheKey = %q, want empty", reviewerClient.calls[0].PromptCacheKey)
	}
	if reviewerClient.calls[0].PromptCacheScope != "" {
		t.Fatalf("reviewer PromptCacheScope = %q, want empty", reviewerClient.calls[0].PromptCacheScope)
	}
}

func TestReviewerSuggestions_UsesReviewerClientPromptCacheCapability(t *testing.T) {
	store := mustCreateTestSession(t)
	engineClient := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true}}
	reviewerClient := &fakeClient{
		caps:      llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, SupportsPromptCacheKey: true},
		responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`}}},
	}
	eng := mustNewTestEngine(t, store, engineClient, tools.NewRegistry(), Config{Model: "gpt-5", Reviewer: ReviewerConfig{Model: "gpt-5"}})
	eng.compactionRuntimeState().SetCount(1)
	if _, err := eng.runReviewerSuggestions(context.Background(), "step-1", reviewerClient); err != nil {
		t.Fatalf("run reviewer suggestions: %v", err)
	}
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("reviewer client calls = %d, want 1", len(reviewerClient.calls))
	}
	if got, want := reviewerClient.calls[0].SessionID, reviewerSessionID(store.Meta().SessionID); got != want {
		t.Fatalf("reviewer SessionID = %q, want %q", got, want)
	}
	if got, want := reviewerClient.calls[0].PromptCacheKey, conversationPromptCacheKey(reviewerSessionID(store.Meta().SessionID), eng.compactionRuntimeState().Count()); got != want {
		t.Fatalf("reviewer PromptCacheKey = %q, want %q", got, want)
	}
	if reviewerClient.calls[0].PromptCacheScope != transcript.CacheWarningScopeReviewer {
		t.Fatalf("reviewer PromptCacheScope = %q, want %q", reviewerClient.calls[0].PromptCacheScope, transcript.CacheWarningScopeReviewer)
	}
}

func TestReviewerSuggestions_PromptCacheKeyStaysOnReviewerSessionAfterConversationCompaction(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := store.AppendEvent("legacy-compact", "history_replaced", historyReplacementPayload{
		Engine: "local",
		Mode:   string(compactionModeManual),
		Items:  llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleAssistant, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}}),
	}); err != nil {
		t.Fatalf("append history_replaced: %v", err)
	}

	reopened, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	engineClient := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, SupportsPromptCacheKey: true}}
	reviewerClient := &fakeClient{
		caps:      llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, SupportsPromptCacheKey: true},
		responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":[]}`}}},
	}
	eng := mustNewTestEngine(t, reopened, engineClient, tools.NewRegistry(), Config{Model: "gpt-5", Reviewer: ReviewerConfig{Model: "gpt-5"}})
	if _, err := eng.runReviewerSuggestions(context.Background(), "step-1", reviewerClient); err != nil {
		t.Fatalf("run reviewer suggestions: %v", err)
	}
	if len(reviewerClient.calls) != 1 {
		t.Fatalf("reviewer client calls = %d, want 1", len(reviewerClient.calls))
	}
	if got, want := reviewerClient.calls[0].SessionID, reviewerSessionID(reopened.Meta().SessionID); got != want {
		t.Fatalf("reviewer SessionID = %q, want %q", got, want)
	}
	if got, want := reviewerClient.calls[0].PromptCacheKey, conversationPromptCacheKey(reviewerSessionID(reopened.Meta().SessionID), eng.compactionRuntimeState().Count()); got != want {
		t.Fatalf("reviewer PromptCacheKey = %q, want %q", got, want)
	}
}

func TestGenerateWithRetryClient_KeepsReviewerLineageIndependent(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{Usage: llm.Usage{InputTokens: 10, HasCachedInputTokens: true, CachedInputTokens: 8}},
		{Usage: llm.Usage{InputTokens: 10, HasCachedInputTokens: true, CachedInputTokens: 6}},
		{Usage: llm.Usage{InputTokens: 12, HasCachedInputTokens: true, CachedInputTokens: 10}},
		{Usage: llm.Usage{InputTokens: 12, HasCachedInputTokens: true, CachedInputTokens: 0}},
	}}
	store, eng := newCacheWarningTestEngine(t, client, config.CacheWarningModeVerbose)

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest("cache-key-1", "alpha"), nil, nil, nil); err != nil {
		t.Fatalf("conversation first generate: %v", err)
	}
	if _, err := eng.generateWithRetryClient(context.Background(), "step-2", client, testReviewerPromptCacheRequest("cache-key-1/supervisor", "beta"), nil, nil, nil); err != nil {
		t.Fatalf("reviewer first generate: %v", err)
	}
	if _, err := eng.generateWithRetryClient(context.Background(), "step-3", client, testPromptCacheRequest("cache-key-1", "alpha", "omega"), nil, nil, nil); err != nil {
		t.Fatalf("conversation postfix generate: %v", err)
	}
	if _, err := eng.generateWithRetryClient(context.Background(), "step-4", client, testReviewerPromptCacheRequest("cache-key-1/supervisor", "gamma"), nil, nil, nil); err != nil {
		t.Fatalf("reviewer non-postfix generate: %v", err)
	}

	warnings := persistedCacheWarnings(t, store)
	if len(warnings) != 1 {
		t.Fatalf("warning count = %d, want 1", len(warnings))
	}
	if warnings[0].Reason != transcript.CacheWarningReasonNonPostfix {
		t.Fatalf("warning reason = %q, want %q", warnings[0].Reason, transcript.CacheWarningReasonNonPostfix)
	}
	if warnings[0].Scope != transcript.CacheWarningScopeReviewer {
		t.Fatalf("warning scope = %q, want %q", warnings[0].Scope, transcript.CacheWarningScopeReviewer)
	}
}

func TestGenerateWithRetryClient_CompactionRotatesConversationCacheKeyWithoutWarning(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{Usage: llm.Usage{InputTokens: 10}}, {Usage: llm.Usage{InputTokens: 12}}}}
	store, eng := newCacheWarningTestEngine(t, client, config.CacheWarningModeDefault)

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest("cache-key-1", "alpha"), nil, nil, nil); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if err := newCompactionPersistence(eng).replaceHistory("step-compact", "local", compactionModeManual, llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleAssistant, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}})); err != nil {
		t.Fatalf("replace history: %v", err)
	}
	if len(persistedCacheWarnings(t, store)) != 0 {
		t.Fatal("expected compaction to avoid warnings before the next rotated-key request")
	}
	if _, err := eng.generateWithRetryClient(context.Background(), "step-2", client, testPromptCacheRequest("cache-key-1/compact-1", "beta"), nil, nil, nil); err != nil {
		t.Fatalf("second generate: %v", err)
	}

	warnings := persistedCacheWarnings(t, store)
	if len(warnings) != 0 {
		t.Fatalf("warning count = %d, want 0", len(warnings))
	}
}

func TestGenerateWithRetryClient_RestoreIgnoresRequestObservationWithoutResponse(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := store.AppendEvent("legacy-request", sessionEventCacheRequestObserved, persistedCacheRequestObserved{
		DigestVersion: requestCacheDigestVersion,
		CacheKey:      "cache-key-1",
		Scope:         transcript.CacheWarningScopeConversation,
		ChunkCount:    1,
		TerminalHash:  "failed-only-hash",
	}); err != nil {
		t.Fatalf("append request event: %v", err)
	}
	reopened, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	client := &fakeClient{responses: []llm.Response{{Usage: llm.Usage{InputTokens: 12}}}}
	eng := mustNewTestEngine(t, reopened, client, tools.NewRegistry(), Config{Model: "gpt-5", CacheWarningMode: config.CacheWarningModeDefault})
	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest("cache-key-1", "alpha", "omega"), nil, nil, nil); err != nil {
		t.Fatalf("generate after reopen: %v", err)
	}
	warnings := persistedCacheWarnings(t, reopened)
	if len(warnings) != 0 {
		t.Fatalf("warning count = %d, want 0", len(warnings))
	}
}

type failingCacheClient struct {
	caps llm.ProviderCapabilities
}

func (f *failingCacheClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, context.DeadlineExceeded
}

func (f *failingCacheClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return f.caps, nil
}

func TestGenerateWithRetryClient_RestorePreservesRotatedCompactionKeyWithoutWarning(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{Usage: llm.Usage{InputTokens: 10}}}}
	store, eng := newCacheWarningTestEngine(t, client, config.CacheWarningModeVerbose)

	if _, err := eng.generateWithRetryClient(context.Background(), "step-1", client, testPromptCacheRequest("cache-key-1", "alpha"), nil, nil, nil); err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if err := newCompactionPersistence(eng).replaceHistory("step-compact", "local", compactionModeManual, llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleAssistant, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"}})); err != nil {
		t.Fatalf("replace history: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}

	reopened, err := session.Open(store.Dir())
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	reopenedClient := &fakeClient{responses: []llm.Response{{Usage: llm.Usage{InputTokens: 12}}}}
	reopenedEng := mustNewTestEngine(t, reopened, reopenedClient, tools.NewRegistry(), Config{Model: "gpt-5", CacheWarningMode: config.CacheWarningModeVerbose})

	if _, err := reopenedEng.generateWithRetryClient(context.Background(), "step-2", reopenedClient, testPromptCacheRequest("cache-key-1/compact-1", "beta"), nil, nil, nil); err != nil {
		t.Fatalf("generate after reopen: %v", err)
	}

	warnings := persistedCacheWarnings(t, reopened)
	if len(warnings) != 0 {
		t.Fatalf("warning count = %d, want 0", len(warnings))
	}
}
