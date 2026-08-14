package llm

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"core/internal/testharness/httpclient"
)

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
