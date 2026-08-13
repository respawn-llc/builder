package runtime

import (
	"context"
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
