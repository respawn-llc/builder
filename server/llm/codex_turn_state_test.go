package llm

import (
	"context"
	"core/shared/textutil"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCodexTurnStateObserverPreservesFirstValidCandidateAndTypedDiagnostics(t *testing.T) {
	dispatch := newTestCodexDispatch(t)

	for _, candidate := range []string{
		"",
		" leading",
		"trailing\t",
		strings.Repeat("s", maxCodexHeaderValueBytes+1),
		"line\nbreak",
		"opaque-state",
		"opaque-state",
		"different-state",
		"",
		"different-state",
	} {
		dispatch.observeTurnStateCandidate(candidate, codexTurnStateSourceHTTPHeader)
	}

	if got, ok := dispatch.currentTurnState(); !ok || got != "opaque-state" {
		t.Fatalf("turn state = (%q, %v), want exact first valid candidate", got, ok)
	}
	if got, want := dispatch.TurnStateDiagnostics(), []CodexTurnStateDiagnosticCategory{
		CodexTurnStateDiagnosticInvalid,
		CodexTurnStateDiagnosticConflict,
	}; !equalCodexDiagnostics(got, want) {
		t.Fatalf("diagnostics = %v, want %v", got, want)
	}
	if got := dispatch.TurnStateDiagnostics(); len(got) != 2 {
		t.Fatalf("repeated snapshot = %v, want idempotent two-category snapshot", got)
	}
}

func TestCodexTurnStateObserverCandidateBoundary(t *testing.T) {
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "one byte", value: "s", valid: true},
		{name: "8192 bytes", value: strings.Repeat("s", maxCodexHeaderValueBytes), valid: true},
		{name: "8193 bytes", value: strings.Repeat("s", maxCodexHeaderValueBytes+1)},
		{name: "leading SP", value: " state"},
		{name: "trailing SP", value: "state "},
		{name: "leading HTAB", value: "\tstate"},
		{name: "trailing HTAB", value: "state\t"},
		{name: "DEL", value: "state\x7f"},
		{name: "NUL", value: "state\x00"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatch := newTestCodexDispatch(t)
			dispatch.observeTurnStateCandidate(test.value, codexTurnStateSourceHTTPHeader)
			got, ok := dispatch.currentTurnState()
			if test.valid {
				if !ok || got != test.value {
					t.Fatalf("turn state = (%q, %v), want exact accepted value", got, ok)
				}
				if got := dispatch.TurnStateDiagnostics(); len(got) != 0 {
					t.Fatalf("diagnostics = %v, want none", got)
				}
				return
			}
			if ok {
				t.Fatalf("turn state = %q, want absent", got)
			}
			if got := dispatch.TurnStateDiagnostics(); !equalCodexDiagnostics(got, []CodexTurnStateDiagnosticCategory{CodexTurnStateDiagnosticInvalid}) {
				t.Fatalf("diagnostics = %v, want invalid", got)
			}
		})
	}
}

func TestDecodeCodexTurnStateMetadataOrderedContainerContract(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		candidates []string
		invalid    bool
	}{
		{name: "missing headers", raw: `{"type":"response.metadata"}`},
		{name: "missing field", raw: `{"type":"response.metadata","headers":{"other":"x"}}`},
		{name: "string", raw: `{"type":"response.metadata","headers":{"x-codex-turn-state":"one"}}`, candidates: []string{"one"}},
		{name: "case insensitive", raw: `{"type":"response.metadata","headers":{"X-CoDeX-TuRn-StAtE":"one"}}`, candidates: []string{"one"}},
		{name: "ordered array", raw: `{"type":"response.metadata","headers":{"x-codex-turn-state":["one","two"]}}`, candidates: []string{"one", "two"}},
		{name: "empty array", raw: `{"type":"response.metadata","headers":{"x-codex-turn-state":[]}}`, invalid: true},
		{name: "null", raw: `{"type":"response.metadata","headers":{"x-codex-turn-state":null}}`, invalid: true},
		{name: "number", raw: `{"type":"response.metadata","headers":{"x-codex-turn-state":1}}`, invalid: true},
		{name: "non-string array element", raw: `{"type":"response.metadata","headers":{"x-codex-turn-state":["one",null,"two"]}}`, invalid: true},
		{name: "headers null", raw: `{"type":"response.metadata","headers":null}`, invalid: true},
		{name: "headers array", raw: `{"type":"response.metadata","headers":[]}`, invalid: true},
		{name: "malformed root", raw: `[]`, invalid: true},
		{name: "exact duplicate valid valid", raw: `{"type":"response.metadata","headers":{"x-codex-turn-state":"one","x-codex-turn-state":"one"}}`, invalid: true},
		{name: "case duplicate valid valid", raw: `{"type":"response.metadata","headers":{"x-codex-turn-state":"one","X-Codex-Turn-State":"two"}}`, invalid: true},
		{name: "duplicate valid invalid", raw: `{"type":"response.metadata","headers":{"x-codex-turn-state":"one","X-Codex-Turn-State":null}}`, invalid: true},
		{name: "duplicate invalid valid", raw: `{"type":"response.metadata","headers":{"x-codex-turn-state":null,"X-Codex-Turn-State":"one"}}`, invalid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidates, invalid := decodeCodexTurnStateMetadata(test.raw)
			if invalid != test.invalid {
				t.Fatalf("invalid = %v, want %v", invalid, test.invalid)
			}
			if fmt.Sprint(candidates) != fmt.Sprint(test.candidates) {
				t.Fatalf("candidates = %v, want %v", candidates, test.candidates)
			}
		})
	}
}

func TestGenerateObservesAllHTTPHeaderValuesBeforeReturning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add(codexTurnStateHeader, "")
		w.Header().Add(codexTurnStateHeader, "accepted")
		w.Header().Add(codexTurnStateHeader, "accepted")
		w.Header().Add(codexTurnStateHeader, "conflict")
		writeCompletedResponseJSON(w)
	}))
	t.Cleanup(server.Close)
	dispatch := newTestCodexDispatch(t)
	transport := newOAuthTestTransport(server, server.Client())

	if _, err := transport.Generate(context.Background(), testCodexOpenAIRequest(dispatch)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got, ok := dispatch.currentTurnState(); !ok || got != "accepted" {
		t.Fatalf("turn state = (%q, %v), want accepted", got, ok)
	}
	if got := dispatch.TurnStateDiagnostics(); !equalCodexDiagnostics(got, []CodexTurnStateDiagnosticCategory{
		CodexTurnStateDiagnosticInvalid,
		CodexTurnStateDiagnosticConflict,
	}) {
		t.Fatalf("diagnostics = %v", got)
	}
}

func TestNonOAuthGenerateDoesNotObserveProviderTurnState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(codexTurnStateHeader, "must-not-be-observed")
		writeCompletedResponseJSON(w)
	}))
	t.Cleanup(server.Close)
	dispatch := newTestCodexDispatch(t)
	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = server.URL
	transport.Client = server.Client()

	if _, err := transport.Generate(context.Background(), testCodexOpenAIRequest(dispatch)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, ok := dispatch.currentTurnState(); ok {
		t.Fatal("API-key response mutated OAuth-only provider turn state")
	}
	if diagnostics := dispatch.TurnStateDiagnostics(); len(diagnostics) != 0 {
		t.Fatalf("API-key response diagnostics = %v, want none", diagnostics)
	}
}

func TestStreamingMetadataObservesStringAndOrderedArray(t *testing.T) {
	dispatch := newTestCodexDispatch(t)
	transport := newCodexSSETransport(t,
		`{"type":"response.metadata","headers":{"x-codex-turn-state":"first"}}`,
		`{"type":"response.metadata","headers":{"x-codex-turn-state":["first","second"]}}`,
		completedResponseSSEJSON,
		`[DONE]`,
	)
	if _, err := transport.GenerateStreamWithEvents(context.Background(), testCodexOpenAIRequest(dispatch), StreamCallbacks{}); err != nil {
		t.Fatalf("GenerateStreamWithEvents: %v", err)
	}
	if got, ok := dispatch.currentTurnState(); !ok || got != "first" {
		t.Fatalf("turn state = (%q, %v), want first", got, ok)
	}
	if got := dispatch.TurnStateDiagnostics(); !equalCodexDiagnostics(got, []CodexTurnStateDiagnosticCategory{CodexTurnStateDiagnosticConflict}) {
		t.Fatalf("diagnostics = %v, want conflict", got)
	}
}

func TestStreamingMetadataStateSurvivesReadError(t *testing.T) {
	dispatch := newTestCodexDispatch(t)
	transport := newCodexSSETransport(t,
		`{"type":"response.metadata","headers":{"x-codex-turn-state":null}}`,
		`{"type":"response.metadata","headers":{"x-codex-turn-state":"accepted"}}`,
		`{"type":`,
	)
	_, err := transport.GenerateStreamWithEvents(context.Background(), testCodexOpenAIRequest(dispatch), StreamCallbacks{})
	if err == nil {
		t.Fatal("expected stream decode error")
	}
	if got, ok := dispatch.currentTurnState(); !ok || got != "accepted" {
		t.Fatalf("turn state = (%q, %v), want retained accepted state", got, ok)
	}
	if got := dispatch.TurnStateDiagnostics(); !equalCodexDiagnostics(got, []CodexTurnStateDiagnosticCategory{CodexTurnStateDiagnosticInvalid}) {
		t.Fatalf("diagnostics = %v, want retained invalid category", got)
	}
}

func TestStreamingMetadataStateAndDiagnosticSurviveCancellation(t *testing.T) {
	dispatch := newTestCodexDispatch(t)
	ready := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.metadata\",\"headers\":{\"x-codex-turn-state\":null}}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.metadata\",\"headers\":{\"x-codex-turn-state\":\"accepted\"}}\n\n")
		flusher.Flush()
		close(ready)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	transport := newOAuthTestTransport(server, server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-ready
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := transport.GenerateStreamWithEvents(ctx, testCodexOpenAIRequest(dispatch), StreamCallbacks{})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want caller cancellation", err)
	}
	if got, ok := dispatch.currentTurnState(); !ok || got != "accepted" {
		t.Fatalf("turn state = (%q, %v), want accepted", got, ok)
	}
	if got := dispatch.TurnStateDiagnostics(); !equalCodexDiagnostics(got, []CodexTurnStateDiagnosticCategory{CodexTurnStateDiagnosticInvalid}) {
		t.Fatalf("diagnostics = %v, want invalid", got)
	}
}

func TestStreamingInitialHeaderIsObservedBeforeFirstEvent(t *testing.T) {
	dispatch := newTestCodexDispatch(t)
	requestStarted := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(codexTurnStateHeader, "header-before-event")
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		close(requestStarted)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	transport := newOAuthTestTransport(server, server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-requestStarted
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := transport.GenerateStreamWithEvents(ctx, testCodexOpenAIRequest(dispatch), StreamCallbacks{})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation before first event", err)
	}
	if got, ok := dispatch.currentTurnState(); !ok || got != "header-before-event" {
		t.Fatalf("turn state = (%q, %v), want initial header retained before first event", got, ok)
	}
}

func TestTurnStateObserverIsSynchronized(t *testing.T) {
	dispatch := newTestCodexDispatch(t)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for _, candidate := range []string{"accepted", "accepted", "conflict-a", "conflict-b", "", "\tinvalid"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			dispatch.observeTurnStateCandidate(candidate, codexTurnStateSourceMetadata)
		}()
	}
	close(start)
	wait.Wait()

	if _, ok := dispatch.currentTurnState(); !ok {
		t.Fatal("concurrent observer accepted no valid state")
	}
	if got := dispatch.TurnStateDiagnostics(); !equalCodexDiagnostics(got, []CodexTurnStateDiagnosticCategory{
		CodexTurnStateDiagnosticInvalid,
		CodexTurnStateDiagnosticConflict,
	}) {
		t.Fatalf("diagnostics = %v, want synchronized deduplicated categories", got)
	}
}

func TestResponsesV2CompactionObservesHeaderAndMetadataTurnState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add(codexTurnStateHeader, "header-state")
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"response.metadata","headers":{"x-codex-turn-state":"header-state"}}`,
			`{"type":"response.metadata","headers":{"x-codex-turn-state":"metadata-conflict"}}`,
			`{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[{"type":"compaction","id":"cmp_1","encrypted_content":"ciphertext"}]}}`,
			`[DONE]`,
		}
		for _, event := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	t.Cleanup(server.Close)
	dispatch := newTestCodexDispatch(t)
	transport := newOAuthTestTransport(server, server.Client())

	_, err := transport.Compact(context.Background(), OpenAICompactionRequest{
		Model:         "gpt-5",
		SessionID:     textutil.Value("session-1"),
		CodexDispatch: dispatch,
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if got, ok := dispatch.currentTurnState(); !ok || got != "header-state" {
		t.Fatalf("turn state = (%q, %v), want header-state", got, ok)
	}
	if got := dispatch.TurnStateDiagnostics(); !equalCodexDiagnostics(got, []CodexTurnStateDiagnosticCategory{CodexTurnStateDiagnosticConflict}) {
		t.Fatalf("diagnostics = %v, want conflict", got)
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

func newOAuthTestTransport(server *httptest.Server, client *http.Client) *HTTPTransport {
	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	transport.Client = client
	return transport
}

func newCodexSSETransport(t *testing.T, events ...string) *HTTPTransport {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	t.Cleanup(server.Close)
	return newOAuthTestTransport(server, server.Client())
}

func equalCodexDiagnostics(got, want []CodexTurnStateDiagnosticCategory) bool {
	return fmt.Sprint(got) == fmt.Sprint(want)
}
