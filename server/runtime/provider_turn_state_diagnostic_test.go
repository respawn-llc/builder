package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/server/llm"
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
		SessionID: "session-1", RunID: "run-1", RequestKind: llm.CodexRequestKindTurn,
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
		Model: "gpt-5", SessionID: "session-1", CodexDispatch: dispatch,
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

func TestCompactionAttemptPublishesProviderStateDiagnosticsOnceBeforeTerminalReturn(t *testing.T) {
	withCompactionRetryDelays(t, []time.Duration{0})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("x-codex-turn-state", "accepted")
		w.Header().Add("x-codex-turn-state", "different")
		w.Header().Add("x-codex-turn-state", "")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"retry","type":"server_error"}}`))
	}))
	t.Cleanup(server.Close)

	dispatch, err := llm.NewCodexDispatchContext(llm.CodexDispatchFacts{
		SessionID: "session-1", RunID: "run-1", RequestKind: llm.CodexRequestKindCompaction,
	})
	if err != nil {
		t.Fatalf("NewCodexDispatchContext: %v", err)
	}
	transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	client := providerTurnStateCompactionClient{client: llm.NewOpenAIClient(transport)}
	var events []Event
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})
	_, _ = engine.compactWithRetry(context.Background(), "step-1", client, llm.CompactionRequest{
		Model: "gpt-5", SessionID: "session-1", CodexDispatch: dispatch,
	})
	assertProviderTurnStateDiagnosticEvents(t, events)
}

func assertProviderTurnStateDiagnosticEvents(t *testing.T, events []Event) {
	t.Helper()
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
}

func TestProviderStateDiagnosticPublicationFailureIsLoggedAndRemainsPending(t *testing.T) {
	dispatch, err := llm.NewCodexDispatchContext(llm.CodexDispatchFacts{
		SessionID: "session-1", RunID: "run-1", RequestKind: llm.CodexRequestKindTurn,
	})
	if err != nil {
		t.Fatalf("NewCodexDispatchContext: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("x-codex-turn-state", "")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp","object":"response","output":[]}`))
	}))
	t.Cleanup(server.Close)
	transport := llm.NewHTTPTransport(providerTurnStateOAuthAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	client := nonStreamingClient{client: llm.NewOpenAIClient(transport)}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{Model: "gpt-5"})
	engine.closed.Store(true)

	handler := &runtimeCapturingSlogHandler{}
	previous := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(previous) })
	published := make(map[llm.CodexTurnStateDiagnosticCategory]struct{}, 1)

	_, _ = client.Generate(context.Background(), llm.Request{
		Model: "gpt-5", SessionID: "session-1", CodexDispatch: dispatch,
		ToolChoiceMode: llm.ToolChoiceModeAutomatic,
	})
	engine.publishProviderTurnStateDiagnostics("step-1", dispatch, published)

	if _, marked := published[llm.CodexTurnStateDiagnosticInvalid]; marked {
		t.Fatal("failed publication was marked published")
	}
	if len(handler.snapshot()) == 0 {
		t.Fatal("failed publication was not operator-logged")
	}
	if engine.steer("step-1", steerEventIntent(Event{})) == nil {
		t.Fatal("test engine did not reject steering after close")
	}
}

type runtimeCapturingSlogHandler struct {
	mu      sync.Mutex
	records []string
}

func (*runtimeCapturingSlogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *runtimeCapturingSlogHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, record.Message+fmt.Sprint(record.Level))
	return nil
}

func (h *runtimeCapturingSlogHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *runtimeCapturingSlogHandler) WithGroup(string) slog.Handler      { return h }

func (h *runtimeCapturingSlogHandler) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.records...)
}
