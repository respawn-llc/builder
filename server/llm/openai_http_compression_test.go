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
	"sync"
	"testing"

	"core/server/httpcompression"
	"core/shared/textutil"
	"github.com/klauspost/compress/zstd"
)

func compressionDispatch(t *testing.T, kind CodexRequestKind) (*string, *CodexDispatchContext) {
	t.Helper()
	dispatch, err := NewCodexDispatchContext(CodexDispatchFacts{
		SessionID:   "session-1",
		RunID:       "run-1",
		RequestKind: kind.Optional(),
	})
	if err != nil {
		t.Fatalf("dispatch context: %v", err)
	}
	return textutil.Value("session-1"), dispatch
}

func TestGenerateChatGPTCodexCompressesLargeResponsesBodyWithZstd(t *testing.T) {
	var requestEncoding string
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestEncoding = r.Header.Get("Content-Encoding")
		requestBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		_, _ = writer.Write([]byte("data: " + completedResponseSSEJSON + "\n\ndata: [DONE]\n\n"))
		_ = writer.Close()
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()

	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.Client = httpcompression.NewClient(newRewritingHTTPClient(t, server))
	sessionID, dispatch := compressionDispatch(t, CodexRequestKindTurn)

	response, err := transport.Generate(context.Background(), OpenAIRequest{
		Model:          "gpt-5.6-sol",
		SessionID:      sessionID,
		CodexDispatch:  dispatch,
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SystemPrompt:   strings.Repeat("large request content ", 100),
	}, StreamCallbacks{})
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
		writeCompletedResponseSSE(w)
	}))
	defer server.Close()

	transport := NewHTTPTransport(staticAuth{})
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = true
	transport.Client = server.Client()
	sessionID, dispatch := compressionDispatch(t, CodexRequestKindTurn)

	if _, err := transport.Generate(context.Background(), OpenAIRequest{
		Model:          "gpt-5",
		SessionID:      sessionID,
		CodexDispatch:  dispatch,
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SystemPrompt:   strings.Repeat("large request content ", 100),
	}, StreamCallbacks{}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if requestEncoding != "" {
		t.Fatalf("Content-Encoding = %q, want absent", requestEncoding)
	}
}

func TestGenerateExplicitLocalOAuthCompatibleEndpointLeavesResponsesBodyUncompressed(t *testing.T) {
	var requestEncoding string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestEncoding = r.Header.Get("Content-Encoding")
		writeCompletedResponseSSE(w)
	}))
	defer server.Close()

	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.Client = newRewritingHTTPClient(t, server)
	transport.BaseURL = "http://127.0.0.1:11434/v1"
	transport.BaseURLExplicit = true
	sessionID, dispatch := compressionDispatch(t, CodexRequestKindTurn)
	if _, err := transport.Generate(context.Background(), OpenAIRequest{
		Model:          "gpt-5.6-sol",
		SessionID:      sessionID,
		CodexDispatch:  dispatch,
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SystemPrompt:   strings.Repeat("large request content ", 100),
	}, StreamCallbacks{}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if requestEncoding != "" {
		t.Fatalf("Content-Encoding = %q, want absent for explicit local OpenAI-compatible endpoint", requestEncoding)
	}
}

func TestGenerateChatGPTCodexCompressesResponsesBody(t *testing.T) {
	var requestEncoding string
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestEncoding = r.Header.Get("Content-Encoding")
		requestBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.Client = newRewritingHTTPClient(t, server)
	transport.BaseURL = server.URL
	transport.BaseURLExplicit = false
	sessionID, dispatch := compressionDispatch(t, CodexRequestKindTurn)
	_, err := transport.Generate(context.Background(), OpenAIRequest{
		Model:          "gpt-5.6-sol",
		SessionID:      sessionID,
		CodexDispatch:  dispatch,
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SystemPrompt:   strings.Repeat("large request content ", 100),
	}, StreamCallbacks{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if requestEncoding != "zstd" {
		t.Fatalf("Content-Encoding = %q, want zstd", requestEncoding)
	}
	if len(requestBody) == 0 {
		t.Fatal("stream request body was empty")
	}
}

func TestCompactChatGPTCodexCompressesResponsesBody(t *testing.T) {
	var requestEncoding string
	var requestBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestEncoding = r.Header.Get("Content-Encoding")
		requestBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"compaction\",\"id\":\"cmp_1\",\"encrypted_content\":\"enc_1\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.Client = newRewritingHTTPClient(t, server)
	sessionID, dispatch := compressionDispatch(t, CodexRequestKindCompaction)
	response, err := transport.Compact(context.Background(), OpenAIRequest{
		Model:          "gpt-5.6-sol",
		SessionID:      sessionID,
		CodexDispatch:  dispatch,
		ToolChoiceMode: ToolChoiceModeAutomatic,
		Items:          PrepareOpenAIInputItems([]ResponseItem{{Type: ResponseItemTypeMessage, Role: textutil.Value(RoleUser), Content: textutil.Value(strings.Repeat("history ", 200))}}),
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if requestEncoding != "zstd" {
		t.Fatalf("Content-Encoding = %q, want zstd", requestEncoding)
	}
	if len(requestBody) == 0 || response.Checkpoint.Type != ResponseItemTypeCompaction {
		t.Fatalf("compact request/response = body=%d checkpoint=%+v", len(requestBody), response.Checkpoint)
	}
}

func TestGenerateLogicalRetrySendsCompressedSemanticEquivalents(t *testing.T) {
	var mu sync.Mutex
	var requestBodies [][]byte
	var requestEncodings []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requestBodies = append(requestBodies, body)
		requestEncodings = append(requestEncodings, r.Header.Get("Content-Encoding"))
		attempt := len(requestBodies)
		mu.Unlock()
		if attempt == 1 {
			http.Error(w, `{"error":{"message":"retry"}}`, http.StatusServiceUnavailable)
			return
		}
		writeCompletedResponseSSE(w)
	}))
	defer server.Close()

	transport := NewHTTPTransport(oauthStaticAuth{})
	transport.Client = newRewritingHTTPClient(t, server)
	sessionID, dispatch := compressionDispatch(t, CodexRequestKindTurn)
	request := OpenAIRequest{
		Model:          "gpt-5.6-sol",
		SessionID:      sessionID,
		CodexDispatch:  dispatch,
		ToolChoiceMode: ToolChoiceModeAutomatic,
		SystemPrompt:   strings.Repeat("large request content ", 100),
	}
	if _, err := transport.Generate(context.Background(), request, StreamCallbacks{}); err == nil {
		t.Fatal("first Generate unexpectedly succeeded")
	}
	if _, err := transport.Generate(context.Background(), request, StreamCallbacks{}); err != nil {
		t.Fatalf("retry Generate: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requestBodies) != 2 {
		t.Fatalf("attempts = %d, want 2", len(requestBodies))
	}
	if requestEncodings[0] != "zstd" || requestEncodings[1] != "zstd" {
		t.Fatalf("Codex request encodings = %q/%q, want zstd", requestEncodings[0], requestEncodings[1])
	}
	for index, body := range requestBodies {
		decoder, err := zstd.NewReader(bytes.NewReader(body))
		if err != nil {
			t.Fatalf("decode retry body %d: %v", index, err)
		}
		decoded, err := io.ReadAll(decoder)
		decoder.Close()
		if err != nil {
			t.Fatalf("read retry body %d: %v", index, err)
		}
		requestBodies[index] = decoded
	}
	if string(requestBodies[0]) != string(requestBodies[1]) {
		t.Fatal("retry request semantic bodies differ")
	}
}
