package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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

type providerTurnStateCompactionClient struct {
	client *llm.OpenAIClient
}

func (c providerTurnStateCompactionClient) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	return c.client.Generate(ctx, request)
}

func (c providerTurnStateCompactionClient) Compact(ctx context.Context, request llm.CompactionRequest) (llm.CompactionResponse, error) {
	return c.client.Compact(ctx, request)
}

func (providerTurnStateCompactionClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:               "chatgpt-codex",
		SupportsResponsesAPI:     true,
		SupportsResponsesCompact: true,
		IsOpenAIFirstParty:       true,
	}, nil
}

func TestGenerateWithRetryReplaysExactProviderTurnStateOverHTTP1AndHTTP2(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{0})
	const turnState = "opaque,state=value"
	var (
		mu             sync.Mutex
		receivedStates []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedStates = append(receivedStates, r.Header.Get("x-codex-turn-state"))
		attempt := len(receivedStates)
		mu.Unlock()
		if attempt == 1 {
			w.Header().Set("x-codex-turn-state", turnState)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"retry me","type":"server_error"}}`))
			return
		}
		writeRuntimeCompletedResponseJSON(w, nil)
	}))
	t.Cleanup(server.Close)

	dispatch, err := llm.NewCodexDispatchContext(llm.CodexDispatchFacts{
		SessionID:            "session-1",
		RunID:                "run-1",
		CompactionGeneration: 1,
		RequestKind:          llm.CodexRequestKindTurn.Optional(),
	})
	if err != nil {
		t.Fatalf("NewCodexDispatchContext: %v", err)
	}
	transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	transport.Client = server.Client()
	client := nonStreamingClient{client: llm.NewOpenAIClient(transport)}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})

	_, err = engine.generateWithRetryClient(
		context.Background(),
		"",
		client,
		llm.Request{
			Model:          "gpt-5",
			SessionID:      textutil.Value("session-1"),
			CodexDispatch:  dispatch,
			ToolChoiceMode: llm.ToolChoiceModeAutomatic,
		},
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("generateWithRetryClient: %v", err)
	}

	mu.Lock()
	gotStates := append([]string(nil), receivedStates...)
	mu.Unlock()
	if fmt.Sprint(gotStates) != fmt.Sprint([]string{"", turnState}) {
		t.Fatalf("received turn states = %q, want exact bounded-retry replay", gotStates)
	}
}

func TestChangedGenerationPayloadDoesNotInheritProviderTurnState(t *testing.T) {
	const turnState = "generation-repair-state"
	var (
		mu             sync.Mutex
		receivedStates []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedStates = append(receivedStates, r.Header.Get("x-codex-turn-state"))
		attempt := len(receivedStates)
		mu.Unlock()
		if attempt == 1 {
			w.Header().Set("x-codex-turn-state", turnState)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"missing tool output","type":"invalid_request_error"}}`))
			return
		}
		writeRuntimeCompletedResponseJSON(w, []byte(`[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"repaired","annotations":[]}]}]`))
	}))
	t.Cleanup(server.Close)

	transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	transport.Client = server.Client()
	client := nonStreamingClient{client: llm.NewOpenAIClient(transport)}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})
	steerDanglingToolCall(t, engine, "seed", llm.ToolCall{
		ID: "missing", Name: "exec_command", Input: []byte(`{}`),
	})

	err := withActiveTestRun(t, engine, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
		_, generateErr := engine.generateWithMissingToolOutputRepair(
			ctx,
			stepID,
			func() (llm.Request, error) {
				return engine.buildActiveTurnDispatchRequest(ctx, stepID, nil, true)
			},
			nil,
			nil,
			nil,
		)
		return generateErr
	})
	if err != nil {
		t.Fatalf("generation repair: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), receivedStates...)
	mu.Unlock()
	if fmt.Sprint(got) != fmt.Sprint([]string{"", ""}) {
		t.Fatalf("changed generation payload states = %q, want no inherited state", got)
	}
}

func writeRuntimeCompletedResponseJSON(w http.ResponseWriter, output []byte) {
	if output == nil {
		output = []byte(`[]`)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(
		w,
		"{\"id\":\"resp_1\",\"object\":\"response\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2},\"output\":%s}",
		output,
	)
}
