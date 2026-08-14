package llm

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"core/internal/testharness/httpclient"
)

const completedResponseSSEJSON = `{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2},"output":[]}}`

func writeCompletedResponseSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", completedResponseSSEJSON)
}

func newCanonicalOAuthTestTransport(t *testing.T, server *httptest.Server) *HTTPTransport {
	t.Helper()
	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.BaseURL = "https://chatgpt.com/backend-api/codex"
	transport.BaseURLExplicit = true
	transport.Client = newRewritingHTTPClient(t, server)
	return transport
}

func newRewritingHTTPClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	return &http.Client{
		Transport: httpclient.NewURLRewriteTransport(target, server.Client().Transport, ""),
	}
}
