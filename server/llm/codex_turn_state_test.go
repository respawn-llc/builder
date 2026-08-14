package llm

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"core/shared/textutil"
)

func TestCodexTurnStateUsesInitialHTTPHeaderAndIgnoresMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(codexTurnStateHeader, "header-state")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: [DONE]\n\n",
			`{"type":"response.metadata","headers":{"x-codex-turn-state":"metadata-state"}}`,
			completedResponseSSEJSON,
		)
	}))
	t.Cleanup(server.Close)

	dispatch := newTestCodexDispatch(t)
	transport := newCanonicalOAuthTestTransport(t, server)
	if _, err := transport.GenerateStreamWithEvents(context.Background(), testCodexOpenAIRequest(dispatch), StreamCallbacks{}); err != nil {
		t.Fatalf("GenerateStreamWithEvents: %v", err)
	}
	if got, ok := dispatch.currentTurnState(); !ok || got != "header-state" {
		t.Fatalf("turn state = (%q, %v), want initial HTTP header", got, ok)
	}
	if diagnostics := dispatch.TurnStateDiagnostics(); len(diagnostics) != 0 {
		t.Fatalf("metadata affected turn-state diagnostics: %v", diagnostics)
	}
}

func TestCodexTurnStateInvalidWarningIsRedacted(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	dispatch := newTestCodexDispatch(t)
	const secret = "secret\nstate"
	dispatch.observeTurnStateCandidate(secret, codexTurnStateSourceHTTPHeader)

	if strings.Contains(logs.String(), secret) {
		t.Fatal("warning leaked provider turn state")
	}
	if got := dispatch.TurnStateDiagnostics(); len(got) != 1 || got[0] != CodexTurnStateDiagnosticInvalid {
		t.Fatalf("diagnostics = %v, want one invalid diagnostic", got)
	}
}

const completedResponseSSEJSON = `{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[]}}`

func writeCompletedResponseSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", completedResponseSSEJSON)
}

func writeCompletedResponseJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
}

func newTestCodexDispatch(t *testing.T) *CodexDispatchContext {
	t.Helper()
	dispatch, err := NewCodexDispatchContext(CodexDispatchFacts{
		SessionID:            "session-1",
		RunID:                "run-1",
		CompactionGeneration: 1,
		RequestKind:          CodexRequestKindTurn.Optional(),
	})
	if err != nil {
		t.Fatalf("NewCodexDispatchContext: %v", err)
	}
	return dispatch
}

func testCodexOpenAIRequest(dispatch *CodexDispatchContext) OpenAIRequest {
	return OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SessionID:      textutil.Value("session-1"),
		CodexDispatch:  dispatch,
	}
}
