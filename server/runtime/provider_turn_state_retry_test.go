package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	for _, protocol := range []string{"http1", "http2"} {
		t.Run(protocol, func(t *testing.T) {
			withGenerateRetryDelays(t, []time.Duration{0})
			const turnState = "opaque,state=value"
			var mu sync.Mutex
			var states []string
			var protocols []int
			handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				mu.Lock()
				states = append(states, request.Header.Get("x-codex-turn-state"))
				protocols = append(protocols, request.ProtoMajor)
				attempt := len(states)
				mu.Unlock()
				if attempt == 1 {
					w.Header().Set("x-codex-turn-state", turnState)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":{"message":"retry me","type":"server_error"}}`))
					return
				}
				writeRuntimeCompletedResponseJSON(w, nil)
			})
			var server *httptest.Server
			if protocol == "http2" {
				server = httptest.NewUnstartedServer(handler)
				server.EnableHTTP2 = true
				server.StartTLS()
			} else {
				server = httptest.NewServer(handler)
			}
			t.Cleanup(server.Close)

			dispatch, err := llm.NewCodexDispatchContext(llm.CodexDispatchFacts{
				SessionID: "session-1", RunID: "run-1",
				CompactionGeneration: 1, RequestKind: llm.CodexRequestKindTurn.Optional(),
			})
			if err != nil {
				t.Fatalf("NewCodexDispatchContext: %v", err)
			}
			transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
			transport.BaseURL, transport.BaseURLExplicit, transport.Client = server.URL, true, server.Client()
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
			gotStates, gotProtocols := append([]string(nil), states...), append([]int(nil), protocols...)
			mu.Unlock()
			if fmt.Sprint(gotStates) != fmt.Sprint([]string{"", turnState}) {
				t.Fatalf("received turn states = %q, want exact bounded-retry replay", gotStates)
			}
			wantProtocol := 1
			if protocol == "http2" {
				wantProtocol = 2
			}
			for _, got := range gotProtocols {
				if got != wantProtocol {
					t.Fatalf("HTTP protocol = %d, want %d", got, wantProtocol)
				}
			}
		})
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
		writeRuntimeCompletedResponseJSON(w, []byte(`[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"repaired","annotations":[]}]}]`))
	}))
	t.Cleanup(server.Close)
	transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
	transport.BaseURL, transport.BaseURLExplicit, transport.Client = server.URL, true, server.Client()
	client := nonStreamingClient{client: llm.NewOpenAIClient(transport)}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})
	steerDanglingToolCall(t, engine, "seed", llm.ToolCall{ID: "missing", Name: "exec_command", Input: []byte(`{}`)})
	err := withActiveTestRun(t, engine, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
		_, err := engine.generateWithMissingToolOutputRepair(ctx, stepID, func() (llm.Request, error) {
			return engine.buildActiveTurnDispatchRequest(ctx, stepID, nil, true)
		}, nil, nil, nil)
		return err
	})
	if err != nil {
		t.Fatalf("generation repair: %v", err)
	}
	if fmt.Sprint(states) != fmt.Sprint([]string{"", ""}) {
		t.Fatalf("changed generation payload states = %q, want no inherited state", states)
	}
}

func TestChangedCompactionPayloadDoesNotInheritProviderTurnState(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *Engine) error
	}{
		{
			name: "remote overflow repair",
			run: func(t *testing.T, engine *Engine) error {
				for _, steering := range []struct {
					id      string
					message llm.Message
				}{
					{id: "input", message: llm.Message{Role: llm.RoleUser, Content: textutil.Value("input")}},
					{id: "call", message: llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
						ID: "call-1", Name: "exec_command", Input: []byte(`{"cmd":"pwd"}`),
					}}}},
					{id: "output", message: llm.Message{
						Role: llm.RoleTool, ToolCallID: textutil.Value("call-1"), Name: textutil.Value("exec_command"),
						Content: textutil.Value(`{"output":"` + strings.Repeat("x", 4_000) + `"}`),
					}},
				} {
					if err := engine.steer(steering.id, steerMessagesWithPersistenceIntent(
						steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{steering.message},
					)); err != nil {
						t.Fatalf("persist %s: %v", steering.id, err)
					}
				}
				return engine.CompactContext(context.Background(), "")
			},
		},
		{
			name: "remote missing-tool-output repair",
			run: func(t *testing.T, engine *Engine) error {
				steerDanglingToolCall(t, engine, "step", llm.ToolCall{
					ID: "missing", Name: "exec_command", Input: []byte(`{}`),
				})
				request := llm.CompactionRequest{
					Model: "gpt-5", InputItems: engine.transcriptRuntimeState().SnapshotItems(),
				}
				factory := mustTestDispatchRequestFactory(
					t, engine.SessionID(), "run-compaction", llm.CodexRequestKindCompaction,
				)
				_, _, _, err := engine.compactWithContextRepairRetry(
					context.Background(), "step", engine.llm.(llm.CompactionClient), request, factory,
				)
				return err
			},
		},
		{
			name: "local tool-call repair",
			run: func(_ *testing.T, engine *Engine) error {
				return withActiveTestRun(t, engine, ActiveKindUserTurn, func(ctx context.Context, stepID string) error {
					_, _, err := engine.compactNow(ctx, stepID, compactionModeHandoff, compactionInstructionsInput{}, false)
					return err
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				mu             sync.Mutex
				receivedStates []string
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				mu.Lock()
				receivedStates = append(receivedStates, request.Header.Get("x-codex-turn-state"))
				attempt := len(receivedStates)
				mu.Unlock()
				if attempt == 1 {
					w.Header().Set("x-codex-turn-state", "compaction-repair-state")
					if test.name == "remote overflow repair" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusRequestEntityTooLarge)
						_, _ = w.Write([]byte(`{"error":{"message":"context length exceeded","type":"invalid_request_error","code":"context_length_exceeded"}}`))
						return
					}
					if test.name == "remote missing-tool-output repair" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":{"message":"missing tool output","type":"invalid_request_error"}}`))
						return
					}
					writeRuntimeCompletedResponseJSON(w, []byte(`[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec_command","arguments":"{\"cmd\":\"pwd\"}","status":"completed"}]`))
					return
				}
				if strings.HasPrefix(test.name, "remote ") {
					writeRuntimeCompletedResponseSSE(w, []byte(`[{"type":"compaction","id":"cmp_1","encrypted_content":"enc_1"}]`))
					return
				}
				writeRuntimeCompletedResponseJSON(w, []byte(`[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"summary","annotations":[]}]}]`))
			}))
			t.Cleanup(server.Close)

			transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
			transport.BaseURL, transport.BaseURLExplicit, transport.Client = server.URL, true, server.Client()
			openAIClient := llm.NewOpenAIClient(transport)
			var client llm.Client = nonStreamingClient{client: openAIClient}
			config := Config{Model: "gpt-5", CompactionMode: "local"}
			if strings.HasPrefix(test.name, "remote ") {
				client = providerTurnStateCompactionClient{client: openAIClient}
				config.CompactionMode, config.ContextWindowTokens = "native", 2_500
			}
			engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), config)
			if test.name == "local tool-call repair" {
				if err := engine.steer("input", steerMessagesWithPersistenceIntent(
					steeringPriorityNormal, steeringMessageEventNone, true,
					[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
				)); err != nil {
					t.Fatalf("persist input: %v", err)
				}
			}
			if err := test.run(t, engine); err != nil {
				t.Fatalf("compaction repair: %v", err)
			}
			mu.Lock()
			got := append([]string(nil), receivedStates...)
			mu.Unlock()
			if fmt.Sprint(got) != fmt.Sprint([]string{"", ""}) {
				t.Fatalf("changed compaction payload states = %q, want no inherited state", got)
			}
		})
	}
}

func writeRuntimeCompletedResponseSSE(w http.ResponseWriter, output []byte) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(
		w,
		"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2},\"output\":%s}}\n\ndata: [DONE]\n\n",
		output,
	)
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
