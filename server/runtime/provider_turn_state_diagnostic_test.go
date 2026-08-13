package runtime

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
	"core/shared/textutil"
)

func TestGenerateAttemptPublishesProviderStateDiagnosticsOnceBeforeTerminalReturn(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{0})
	var (
		attempts             atomic.Int32
		publishedDiagnostics atomic.Int32
		publishedBeforeRetry atomic.Bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 2 && publishedDiagnostics.Load() == 2 {
			publishedBeforeRetry.Store(true)
		}
		w.Header().Add("x-codex-turn-state", "accepted")
		w.Header().Add("x-codex-turn-state", "different")
		w.Header().Add("x-codex-turn-state", "")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"retry","type":"server_error"}}`))
	}))
	t.Cleanup(server.Close)

	dispatch, err := llm.NewCodexDispatchContext(llm.CodexDispatchFacts{
		SessionID: "session-1", RunID: "run-1", RequestKind: llm.CodexRequestKindTurn.Optional(),
	})
	if err != nil {
		t.Fatalf("NewCodexDispatchContext: %v", err)
	}

	var events []Event
	transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	client := nonStreamingClient{client: llm.NewOpenAIClient(transport)}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			events = append(events, event)
			if event.Kind == EventProviderTurnStateInvalid || event.Kind == EventProviderTurnStateConflict {
				publishedDiagnostics.Add(1)
			}
		},
	})
	_, _ = engine.generateWithRetryClient(context.Background(), "step-1", client, llm.Request{
		Model: "gpt-5", SessionID: textutil.Value("session-1"), CodexDispatch: dispatch,
		ToolChoiceMode: llm.ToolChoiceModeAutomatic,
	}, nil, nil, nil)

	counts := map[EventKind]int{}
	for _, event := range events {
		if event.Kind == EventProviderTurnStateInvalid || event.Kind == EventProviderTurnStateConflict {
			if event.StepID != "step-1" || event.Error != "" {
				t.Fatalf("provider diagnostic event = %+v", event)
			}
			counts[event.Kind]++
		}
	}
	if counts[EventProviderTurnStateInvalid] != 1 || counts[EventProviderTurnStateConflict] != 1 {
		t.Fatalf("provider diagnostic counts = %+v, want each once", counts)
	}
	if !publishedBeforeRetry.Load() {
		t.Fatal("provider diagnostics were not published before the bounded retry")
	}
}
func TestCompactionAttemptPublishesProviderStateDiagnosticsOnceBeforeRetry(t *testing.T) {
	withCompactionRetryDelays(t, []time.Duration{0})
	var (
		attempts             atomic.Int32
		publishedDiagnostics atomic.Int32
		publishedBeforeRetry atomic.Bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 2 && publishedDiagnostics.Load() == 2 {
			publishedBeforeRetry.Store(true)
		}
		w.Header().Add("x-codex-turn-state", "accepted")
		w.Header().Add("x-codex-turn-state", "different")
		w.Header().Add("x-codex-turn-state", "")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"retry","type":"server_error"}}`))
	}))
	t.Cleanup(server.Close)

	dispatch, err := llm.NewCodexDispatchContext(llm.CodexDispatchFacts{
		SessionID: "session-1", RunID: "run-1", RequestKind: llm.CodexRequestKindCompaction.Optional(),
	})
	if err != nil {
		t.Fatalf("NewCodexDispatchContext: %v", err)
	}
	transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
	transport.BaseURL, transport.BaseURLExplicit, transport.Client = server.URL, true, server.Client()
	client := providerTurnStateCompactionClient{client: llm.NewOpenAIClient(transport)}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			if event.Kind == EventProviderTurnStateInvalid || event.Kind == EventProviderTurnStateConflict {
				publishedDiagnostics.Add(1)
			}
		},
	})
	_, _ = engine.compactWithRetry(context.Background(), "step-1", client, llm.CompactionRequest{
		Model: "gpt-5", SessionID: textutil.Value("session-1"), CodexDispatch: dispatch,
	})
	if publishedDiagnostics.Load() != 2 {
		t.Fatalf("published diagnostics = %d, want each category once", publishedDiagnostics.Load())
	}
	if !publishedBeforeRetry.Load() {
		t.Fatal("compaction diagnostics were not published before the bounded retry")
	}
}

func TestCompactionDiagnosticPublicationFailureRemainsPending(t *testing.T) {
	withCompactionRetryDelays(t, []time.Duration{0})
	dispatch, err := llm.NewCodexDispatchContext(llm.CodexDispatchFacts{
		SessionID: "session-1", RunID: "run-1", RequestKind: llm.CodexRequestKindCompaction.Optional(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("x-codex-turn-state", "")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"retry","type":"server_error"}}`))
			return
		}
		writeRuntimeCompletedResponseSSE(w, []byte(`[{"type":"compaction","id":"cmp_1","encrypted_content":"enc_1"}]`))
	}))
	t.Cleanup(server.Close)
	transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
	transport.BaseURL, transport.BaseURLExplicit, transport.Client = server.URL, true, server.Client()
	client := providerTurnStateCompactionClient{client: llm.NewOpenAIClient(transport)}
	var events []Event
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})
	engine.closed.Store(true)
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	response, compactErr := engine.compactWithRetry(context.Background(), "step-1", client, llm.CompactionRequest{
		Model: "gpt-5", SessionID: textutil.Value("session-1"), CodexDispatch: dispatch,
	})
	if compactErr != nil || len(response.OutputItems) != 1 || attempts.Load() != 2 {
		t.Fatalf("compaction changed by diagnostic failure: response=%+v error=%v", response, compactErr)
	}
	if !bytes.Contains(logs.Bytes(), []byte("publish provider turn-state diagnostic")) {
		t.Fatal("failed diagnostic publication was not operator-logged")
	}
	engine.closed.Store(false)
	published := make(map[llm.CodexTurnStateDiagnosticCategory]struct{}, 1)
	engine.publishProviderTurnStateDiagnostics("step-1", dispatch, published)
	if _, marked := published[llm.CodexTurnStateDiagnosticInvalid]; !marked || len(events) != 1 {
		t.Fatal("pending diagnostic was not eligible for later publication")
	}
}
