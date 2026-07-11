// Package openairesponses provides test-only HTTP fixtures for the OpenAI
// Responses API contract. It belongs to the repository-internal test harness
// and must not be imported by production paths.
package openairesponses

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Recorder captures Responses API request payloads and returns the minimal
// successful responses required by provider-client integration tests.
type Recorder struct {
	server   *httptest.Server
	mu       sync.Mutex
	payloads map[string]map[string]any
}

// NewRecorder starts an isolated Responses API fixture and registers its
// cleanup with t.
func NewRecorder(t testing.TB) *Recorder {
	t.Helper()
	recorder := &Recorder{payloads: make(map[string]map[string]any)}
	recorder.server = httptest.NewServer(http.HandlerFunc(recorder.handle))
	t.Cleanup(recorder.server.Close)
	return recorder
}

// URL returns the fixture origin.
func (r *Recorder) URL() string {
	return r.server.URL
}

// Client returns an HTTP client configured for the fixture.
func (r *Recorder) Client() *http.Client {
	return r.server.Client()
}

// AssertTextVerbosity verifies both Responses API request paths received the
// expected text verbosity.
func (r *Recorder) AssertTextVerbosity(t testing.TB, want string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, path := range []string{"/v1/responses", "/v1/responses/input_tokens"} {
		payload, ok := r.payloads[path]
		if !ok {
			t.Fatalf("expected request payload for %s", path)
		}
		text, ok := payload["text"].(map[string]any)
		if !ok {
			t.Fatalf("expected text config in %s payload, got %#v", path, payload)
		}
		if got := text["verbosity"]; got != want {
			t.Fatalf("%s text.verbosity = %#v, want %q", path, got, want)
		}
	}
}

func (r *Recorder) handle(w http.ResponseWriter, request *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	r.payloads[request.URL.Path] = payload
	r.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch request.URL.Path {
	case "/v1/responses":
		_, _ = w.Write([]byte(`{
			"id":"resp_test_fixture",
			"object":"response",
			"output":[{"type":"message","id":"msg_test_fixture","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok"}]}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`))
	case "/v1/responses/input_tokens":
		_, _ = w.Write([]byte(`{"object":"response.input_tokens","input_tokens":1}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
