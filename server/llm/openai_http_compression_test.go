package llm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestGenerateChatGPTCodexCompressesLargeResponsesBodyWithZstd(t *testing.T) {
	var requestEncoding string
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestEncoding = r.Header.Get("Content-Encoding")
		requestBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		_, _ = writer.Write([]byte(`{"id":"response-1","object":"response","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		_ = writer.Close()
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()

	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true

	response, err := transport.Generate(context.Background(), OpenAIRequest{
		Model:          "gpt-5.6-sol",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SystemPrompt:   strings.Repeat("large request content ", 100),
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if requestEncoding != "zstd" {
		t.Fatalf("Content-Encoding = %q, want zstd", requestEncoding)
	}

	decoder, err := zstd.NewReader(bytes.NewReader(requestBody))
	if err != nil {
		t.Fatalf("create zstd decoder: %v", err)
	}
	decoded, err := io.ReadAll(decoder)
	if err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	decoder.Close()

	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		t.Fatalf("decode Responses payload: %v", err)
	}
	if payload["model"] != "gpt-5.6-sol" {
		t.Fatalf("model = %#v, want gpt-5.6-sol", payload["model"])
	}
	if response.Usage.InputTokens != 1 || response.Usage.OutputTokens != 1 {
		t.Fatalf("response usage = %+v, want input/output tokens 1/1", response.Usage)
	}
}

func TestGenerateOpenAIAPIKeyLeavesLargeResponsesBodyUncompressed(t *testing.T) {
	var requestEncoding string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestEncoding = r.Header.Get("Content-Encoding")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"response-1","object":"response","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	transport.Client = server.Client()

	if _, err := transport.Generate(context.Background(), OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SystemPrompt:   strings.Repeat("large request content ", 100),
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if requestEncoding != "" {
		t.Fatalf("Content-Encoding = %q, want absent", requestEncoding)
	}
}
