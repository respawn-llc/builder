package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/toolspec"
)

type dispatchIsolationClient struct {
	inner llm.Client

	mu    sync.Mutex
	calls []llm.Request
}

func (c *dispatchIsolationClient) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.calls = append(c.calls, request)
	c.mu.Unlock()
	return c.inner.Generate(ctx, request)
}

func (*dispatchIsolationClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:           "chatgpt-codex",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}, nil
}

func (c *dispatchIsolationClient) snapshotCalls() []llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.Request(nil), c.calls...)
}

type dispatchIsolationObservation struct {
	TurnID    string
	TurnState string
}

func TestConsecutiveRequestsWithinAgentTurnUseFreshProviderDispatches(t *testing.T) {
	var (
		mu           sync.Mutex
		observations []dispatchIsolationObservation
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		observation := observeDispatchIsolationRequest(t, request)
		mu.Lock()
		observations = append(observations, observation)
		call := len(observations)
		mu.Unlock()

		w.Header().Set("x-codex-turn-state", "state-for-request")
		if call == 1 {
			writeDispatchIsolationResponse(w, `[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"exec_command","arguments":"{}","status":"completed"}]`)
			return
		}
		writeDispatchIsolationSuccess(w)
	}))
	t.Cleanup(server.Close)

	client := newDispatchIsolationClient(server)
	engine := mustNewTestEngine(
		t,
		mustCreateTestSession(t),
		client,
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: fakeTool{name: toolspec.ToolExecCommand},
		}),
		Config{Model: "gpt-5"},
	)

	message, err := engine.SubmitUserMessage(context.Background(), "use the tool")
	if err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
	if got := messageContent(message); got != "done" {
		t.Fatalf("assistant message = %q, want done", got)
	}

	calls := client.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want two Agent Steps", len(calls))
	}
	assertDistinctDispatchHandles(t, calls[0], calls[1])

	mu.Lock()
	got := append([]dispatchIsolationObservation(nil), observations...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("wire observations = %d, want two", len(got))
	}
	if got[0].TurnID == "" || got[1].TurnID != got[0].TurnID {
		t.Fatalf("same Agent Turn turn IDs = %q, want one nonblank Run ID", []string{got[0].TurnID, got[1].TurnID})
	}
	for index, observation := range got {
		if observation.TurnState != "" {
			t.Fatalf("request %d replayed another request's provider turn state", index+1)
		}
	}
}

func TestSequentialAgentTurnsIsolateProviderDispatches(t *testing.T) {
	tests := []struct {
		name       string
		firstReply func(http.ResponseWriter, *http.Request, chan<- struct{})
		wantError  func(error) bool
	}{
		{
			name: "success",
			firstReply: func(w http.ResponseWriter, _ *http.Request, _ chan<- struct{}) {
				writeDispatchIsolationSuccess(w)
			},
			wantError: func(err error) bool { return err == nil },
		},
		{
			name: "failure",
			firstReply: func(w http.ResponseWriter, _ *http.Request, _ chan<- struct{}) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid request","type":"invalid_request_error"}}`))
			},
			wantError: func(err error) bool { return err != nil },
		},
		{
			name: "cancellation",
			firstReply: func(w http.ResponseWriter, request *http.Request, started chan<- struct{}) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				close(started)
				<-request.Context().Done()
			},
			wantError: func(err error) bool { return errors.Is(err, context.Canceled) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var (
				mu           sync.Mutex
				observations []dispatchIsolationObservation
			)
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				observation := observeDispatchIsolationRequest(t, request)
				mu.Lock()
				observations = append(observations, observation)
				call := len(observations)
				mu.Unlock()
				w.Header().Set("x-codex-turn-state", "state-for-request")
				if call == 1 {
					test.firstReply(w, request, started)
					return
				}
				writeDispatchIsolationSuccess(w)
			}))
			t.Cleanup(server.Close)

			client := newDispatchIsolationClient(server)
			engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			first := make(chan error, 1)
			go func() {
				_, err := engine.SubmitUserMessage(ctx, "first")
				first <- err
			}()
			if test.name == "cancellation" {
				<-started
				cancel()
			}
			if err := <-first; !test.wantError(err) {
				t.Fatalf("first Agent Turn error = %v", err)
			}
			if _, err := engine.SubmitUserMessage(context.Background(), "next"); err != nil {
				t.Fatalf("following Agent Turn: %v", err)
			}

			calls := client.snapshotCalls()
			if len(calls) != 2 {
				t.Fatalf("provider calls = %d, want two Agent Turns", len(calls))
			}
			assertDistinctDispatchHandles(t, calls[0], calls[1])
			mu.Lock()
			got := append([]dispatchIsolationObservation(nil), observations...)
			mu.Unlock()
			if len(got) != 2 || got[0].TurnID == "" || got[1].TurnID == "" || got[0].TurnID == got[1].TurnID {
				t.Fatalf("sequential Agent Turn IDs = %+v, want distinct nonblank Run IDs", got)
			}
			if got[0].TurnState != "" || got[1].TurnState != "" {
				t.Fatalf("sequential Agent Turn states = %+v, want no replay", got)
			}
		})
	}
}

func newDispatchIsolationClient(server *httptest.Server) *dispatchIsolationClient {
	transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	transport.Client = server.Client()
	return &dispatchIsolationClient{inner: llm.NewOpenAIClient(transport)}
}

func observeDispatchIsolationRequest(t *testing.T, request *http.Request) dispatchIsolationObservation {
	t.Helper()
	var payload struct {
		ClientMetadata map[string]string `json:"client_metadata"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		t.Errorf("decode provider request: %v", err)
		return dispatchIsolationObservation{}
	}
	rawMetadata := payload.ClientMetadata["x-codex-turn-metadata"]
	var metadata struct {
		TurnID string `json:"turn_id"`
	}
	if err := json.Unmarshal([]byte(rawMetadata), &metadata); err != nil {
		t.Errorf("decode Codex turn metadata: %v", err)
	}
	return dispatchIsolationObservation{
		TurnID:    metadata.TurnID,
		TurnState: request.Header.Get("x-codex-turn-state"),
	}
}

func writeDispatchIsolationSuccess(w http.ResponseWriter) {
	writeDispatchIsolationResponse(w, `[{"type":"message","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"done","annotations":[]}]}]`)
}

func writeDispatchIsolationResponse(w http.ResponseWriter, output string) {
	writeRuntimeCompletedResponseSSE(w, []byte(output))
}

func assertDistinctDispatchHandles(t *testing.T, first llm.Request, second llm.Request) {
	t.Helper()
	if first.CodexDispatch == nil || second.CodexDispatch == nil {
		t.Fatal("provider request omitted Codex dispatch context")
	}
	if first.CodexDispatch == second.CodexDispatch {
		t.Fatal("newly built provider requests shared a retry-local state handle")
	}
}
