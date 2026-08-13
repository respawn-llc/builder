package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestSequentialSuccessfulAgentTurnsIsolateProviderDispatches(t *testing.T) {
	server, observations := newSuccessfulDispatchIsolationServer(t)
	client := newDispatchIsolationClient(server)
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})

	for _, input := range []string{"first turn", "second turn"} {
		if _, err := engine.SubmitUserMessage(context.Background(), input); err != nil {
			t.Fatalf("SubmitUserMessage(%q): %v", input, err)
		}
	}

	calls := client.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want two Agent Turns", len(calls))
	}
	assertDistinctDispatchHandles(t, calls[0], calls[1])
	got := observations()
	assertSequentialTurnIsolation(t, got)
}

func TestAgentTurnAfterFailureOrCancellationDoesNotReplayProviderState(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
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
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-codex-turn-state", "state-for-request")
			if call == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"invalid request","type":"invalid_request_error"}}`))
				return
			}
			writeDispatchIsolationSuccess(w)
		}))
		t.Cleanup(server.Close)

		client := newDispatchIsolationClient(server)
		engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})
		if _, err := engine.SubmitUserMessage(context.Background(), "fail"); err == nil {
			t.Fatal("failed Agent Turn unexpectedly succeeded")
		}
		if _, err := engine.SubmitUserMessage(context.Background(), "recover"); err != nil {
			t.Fatalf("following Agent Turn: %v", err)
		}

		calls := client.snapshotCalls()
		if len(calls) != 2 {
			t.Fatalf("provider calls = %d, want failed and following turns", len(calls))
		}
		assertDistinctDispatchHandles(t, calls[0], calls[1])
		mu.Lock()
		got := append([]dispatchIsolationObservation(nil), observations...)
		mu.Unlock()
		assertSequentialTurnIsolation(t, got)
	})

	t.Run("cancellation", func(t *testing.T) {
		firstStarted := make(chan struct{})
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
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("x-codex-turn-state", "state-for-request")
			if call == 1 {
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				close(firstStarted)
				<-request.Context().Done()
				return
			}
			writeDispatchIsolationSuccess(w)
		}))
		t.Cleanup(server.Close)

		client := newDispatchIsolationClient(server)
		engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := engine.SubmitUserMessage(ctx, "cancel")
			done <- err
		}()
		<-firstStarted
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Agent Turn error = %v, want context.Canceled", err)
		}
		if _, err := engine.SubmitUserMessage(context.Background(), "recover"); err != nil {
			t.Fatalf("following Agent Turn: %v", err)
		}

		calls := client.snapshotCalls()
		if len(calls) != 2 {
			t.Fatalf("provider calls = %d, want cancelled and following turns", len(calls))
		}
		assertDistinctDispatchHandles(t, calls[0], calls[1])
		mu.Lock()
		got := append([]dispatchIsolationObservation(nil), observations...)
		mu.Unlock()
		assertSequentialTurnIsolation(t, got)
	})
}

func newDispatchIsolationClient(server *httptest.Server) *dispatchIsolationClient {
	transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	transport.Client = server.Client()
	return &dispatchIsolationClient{inner: llm.NewOpenAIClient(transport)}
}

func newSuccessfulDispatchIsolationServer(
	t *testing.T,
) (*httptest.Server, func() []dispatchIsolationObservation) {
	t.Helper()
	var (
		mu           sync.Mutex
		observations []dispatchIsolationObservation
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		observation := observeDispatchIsolationRequest(t, request)
		mu.Lock()
		observations = append(observations, observation)
		mu.Unlock()
		w.Header().Set("x-codex-turn-state", "state-for-request")
		writeDispatchIsolationSuccess(w)
	}))
	t.Cleanup(server.Close)
	return server, func() []dispatchIsolationObservation {
		mu.Lock()
		defer mu.Unlock()
		return append([]dispatchIsolationObservation(nil), observations...)
	}
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
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_success\",\"object\":\"response\",\"output\":%s,\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\ndata: [DONE]\n\n", output)
}

func assertDistinctDispatchHandles(t *testing.T, first llm.Request, second llm.Request) {
	t.Helper()
	if first.CodexDispatch == nil || second.CodexDispatch == nil {
		t.Fatal("provider request omitted Codex dispatch context")
	}
	if first.CodexDispatch.SameState(second.CodexDispatch) {
		t.Fatal("newly built provider requests shared a retry-local state handle")
	}
}

func assertSequentialTurnIsolation(t *testing.T, observations []dispatchIsolationObservation) {
	t.Helper()
	if len(observations) != 2 {
		t.Fatalf("wire observations = %d, want two", len(observations))
	}
	if observations[0].TurnID == "" || observations[1].TurnID == "" {
		t.Fatalf("turn IDs = %q, want nonblank Run IDs", []string{observations[0].TurnID, observations[1].TurnID})
	}
	if observations[0].TurnID == observations[1].TurnID {
		t.Fatalf("sequential Agent Turns reused Run ID %q", observations[0].TurnID)
	}
	if observations[0].TurnState != "" || observations[1].TurnState != "" {
		t.Fatalf("sequential Agent Turn states = %q, want no cross-request replay", []string{observations[0].TurnState, observations[1].TurnState})
	}
}
