package runtime

import (
	"context"
	"core/internal/testharness/httpclient"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/shared/textutil"
)

type providerTurnStateOAuthAuth struct{}

func (providerTurnStateOAuthAuth) AuthorizationHeader(context.Context) (string, error) {
	return "Bearer token", nil
}

func (providerTurnStateOAuthAuth) OpenAIAuthMetadata(context.Context) (string, string, error) {
	return "oauth", "account-1", nil
}

type nonStreamingClient struct {
	client llm.Client
}

func (c nonStreamingClient) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	return c.client.Generate(ctx, request)
}

func (nonStreamingClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:           "chatgpt-codex",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}, nil
}

func TestGenerateWithRetryReplaysExactProviderTurnState(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{0})
	const turnState = "opaque,state=value"
	var mu sync.Mutex
	var states []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		states = append(states, request.Header.Get("x-codex-turn-state"))
		attempt := len(states)
		mu.Unlock()
		if attempt == 1 {
			w.Header().Set("x-codex-turn-state", turnState)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"retry me","type":"server_error"}}`))
			return
		}
		writeRuntimeCompletedResponseSSE(w, nil)
	}))
	t.Cleanup(server.Close)

	dispatch, err := llm.NewCodexDispatchContext(llm.CodexDispatchFacts{
		SessionID: "session-1", RunID: "run-1",
		CompactionGeneration: 1, RequestKind: llm.CodexRequestKindTurn.Optional(),
	})
	if err != nil {
		t.Fatalf("NewCodexDispatchContext: %v", err)
	}
	transport := newProviderTurnStateTransport(t, server)
	client := nonStreamingClient{client: llm.NewOpenAIClient(transport)}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})
	_, err = engine.generateWithRetryClient(context.Background(), "", client, llm.Request{
		Model: "gpt-5", SessionID: textutil.Value("session-1"), CodexDispatch: dispatch,
		ToolChoiceMode: llm.ToolChoiceModeAutomatic,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("generateWithRetryClient: %v", err)
	}
	mu.Lock()
	gotStates := append([]string(nil), states...)
	mu.Unlock()
	if fmt.Sprint(gotStates) != fmt.Sprint([]string{"", turnState}) {
		t.Fatalf("received turn states = %q, want exact bounded-retry replay", gotStates)
	}
}

func TestGenerationMissingOutputRebuildDoesNotReplayProviderTurnState(t *testing.T) {
	var states []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		states = append(states, request.Header.Get("x-codex-turn-state"))
		if len(states) == 1 {
			w.Header().Set("x-codex-turn-state", "generation-repair-state")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"missing tool output","type":"invalid_request_error"}}`))
			return
		}
		writeRuntimeCompletedResponseSSE(w, []byte(`[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"repaired","annotations":[]}]}]`))
	}))
	t.Cleanup(server.Close)
	transport := newProviderTurnStateTransport(t, server)
	client := nonStreamingClient{client: llm.NewOpenAIClient(transport)}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})
	steerDanglingToolCall(t, engine, "seed", llm.ToolCall{ID: "missing", Name: "exec_command", Input: []byte(`{}`)})
	const stepID = "generation-repair"
	restoreStep := setTestActiveStep(engine, stepID)
	defer restoreStep()
	_, err := engine.generateWithMissingToolOutputRepair(t.Context(), stepID, func() (llm.Request, error) {
		return engine.buildRequest(t.Context(), stepID, true)
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("generation repair: %v", err)
	}
	if fmt.Sprint(states) != fmt.Sprint([]string{"", ""}) {
		t.Fatalf("changed generation payload states = %q, want no inherited state", states)
	}
}

func writeRuntimeCompletedResponseSSE(w http.ResponseWriter, output []byte) {
	if output == nil {
		output = []byte(`[]`)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(
		w,
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2},\"output\":%s}}\n\ndata: [DONE]\n\n",
		output,
	)
}

func newProviderTurnStateTransport(t *testing.T, server *httptest.Server) *llm.HTTPTransport {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
	transport.BaseURL = "https://chatgpt.com/backend-api/codex"
	transport.BaseURLExplicit = true
	transport.Client = &http.Client{
		Transport: httpclient.NewURLRewriteTransport(target, server.Client().Transport, ""),
	}
	return transport
}
