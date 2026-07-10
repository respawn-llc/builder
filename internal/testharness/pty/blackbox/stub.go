package blackbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	maxHTTPBodyBytes    = 64 * 1024
	maxHTTPHeadersBytes = 16 * 1024
)

type StubSnapshot struct {
	RequiredIndex  int
	RequiredTotal  int
	ActiveRequests int
	Failure        error
	Observed       []ObservedCall
}

func (snapshot StubSnapshot) RequiredConsumed() bool {
	return snapshot.Failure == nil && snapshot.RequiredIndex == snapshot.RequiredTotal && snapshot.ActiveRequests == 0
}

type ObservedCall struct {
	Route   Route
	Headers map[string][]string
	Body    json.RawMessage
}

type ResponsesStub struct {
	server     *http.Server
	listener   net.Listener
	required   []RequiredOperation
	mu         sync.Mutex
	index      int
	active     int
	failure    error
	observed   []ObservedCall
	handlers   map[uint64]context.CancelFunc
	nextHandle uint64
	done       chan struct{}
}

func StartResponsesStub(required []RequiredOperation) (*ResponsesStub, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen response stub: %w", err)
	}
	stub := &ResponsesStub{
		listener: listener,
		required: append([]RequiredOperation(nil), required...),
		handlers: map[uint64]context.CancelFunc{},
		done:     make(chan struct{}),
	}
	stub.server = &http.Server{
		Handler:           http.HandlerFunc(stub.serveHTTP),
		MaxHeaderBytes:    maxHTTPHeadersBytes,
		ReadHeaderTimeout: 500 * time.Millisecond,
	}
	go func() {
		_ = stub.server.Serve(listener)
		close(stub.done)
	}()
	return stub, nil
}

func (s *ResponsesStub) URL() string {
	if s == nil || s.listener == nil {
		return ""
	}
	return "http://" + s.listener.Addr().String() + "/v1"
}

func (s *ResponsesStub) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

func (s *ResponsesStub) Snapshot() StubSnapshot {
	if s == nil {
		return StubSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return StubSnapshot{
		RequiredIndex:  s.index,
		RequiredTotal:  len(s.required),
		ActiveRequests: s.active,
		Failure:        s.failure,
		Observed:       append([]ObservedCall(nil), s.observed...),
	}
}

func (s *ResponsesStub) Verify() error {
	snapshot := s.Snapshot()
	if snapshot.Failure != nil {
		return snapshot.Failure
	}
	if !snapshot.RequiredConsumed() {
		return fmt.Errorf("required model operations remain: consumed=%d total=%d active=%d", snapshot.RequiredIndex, snapshot.RequiredTotal, snapshot.ActiveRequests)
	}
	return nil
}

// Close stops admission, cancels all handlers, and closes listener connections
// without an unbounded graceful shutdown.
func (s *ResponsesStub) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	for _, cancel := range s.handlers {
		cancel()
	}
	s.handlers = map[uint64]context.CancelFunc{}
	s.mu.Unlock()
	_ = s.server.Close()
}

func (s *ResponsesStub) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	handle, ctx, done := s.beginHandler(request.Context())
	defer done()
	body, err := boundedBody(request)
	if err != nil {
		s.recordFailure(err)
		http.Error(writer, "invalid request", http.StatusRequestEntityTooLarge)
		return
	}
	route, err := routeForRequest(request)
	if err != nil {
		s.recordFailure(err)
		http.NotFound(writer, request)
		return
	}
	call := ObservedCall{Route: route, Headers: request.Header.Clone(), Body: append(json.RawMessage(nil), body...)}
	if err := s.consume(route, body, request.Header); err != nil {
		s.recordFailure(err)
		http.Error(writer, "unexpected model operation", http.StatusBadRequest)
		return
	}
	s.recordObserved(call)
	switch route {
	case RouteResponses:
		s.writeResponse(ctx, writer)
	case RouteInputTokens:
		writeJSON(writer, http.StatusOK, map[string]int{"input_tokens": 0})
	case RouteModel:
		writeJSON(writer, http.StatusOK, map[string]any{"id": "gpt-5", "object": "model"})
	case RouteCompact:
		writeJSON(writer, http.StatusOK, map[string]any{"output": []any{}})
	}
	_ = handle
}

func (s *ResponsesStub) beginHandler(parent context.Context) (uint64, context.Context, func()) {
	s.mu.Lock()
	handle := s.nextHandle
	s.nextHandle++
	ctx, cancel := context.WithCancel(parent)
	s.handlers[handle] = cancel
	s.active++
	s.mu.Unlock()
	return handle, ctx, func() {
		s.mu.Lock()
		delete(s.handlers, handle)
		s.active--
		s.mu.Unlock()
		cancel()
	}
}

func (s *ResponsesStub) consume(route Route, body []byte, headers http.Header) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return s.failure
	}
	if s.index >= len(s.required) {
		if route == RouteInputTokens || route == RouteModel {
			return nil
		}
		return fmt.Errorf("unexpected model operation route=%s after required queue", route)
	}
	required := s.required[s.index]
	if route != required.Route {
		if route == RouteInputTokens || route == RouteModel {
			return nil
		}
		return fmt.Errorf("required model operation route=%s got=%s", required.Route, route)
	}
	if required.Probe != "" && !requestContainsProbe(body, required.Probe) {
		return errors.New("required response probe was not present in typed input")
	}
	if required.SessionCacheKey && !hasMatchingSessionCacheKey(body, headers) {
		return errors.New("required response session_id and prompt_cache_key relation was not present")
	}
	s.index++
	return nil
}

func (s *ResponsesStub) writeResponse(ctx context.Context, writer http.ResponseWriter) {
	s.mu.Lock()
	index := s.index - 1
	operation := s.required[index]
	s.mu.Unlock()
	if operation.Stream {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[]}}\n\n")
		_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", operation.Output)
		_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n", operation.Output)
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
		return
	}
	select {
	case <-ctx.Done():
		return
	default:
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"output": []any{map[string]any{
			"type": "message", "role": "assistant", "phase": "final_answer",
			"content": []any{map[string]any{"type": "output_text", "text": operation.Output}},
		}},
	})
}

func (s *ResponsesStub) recordFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		s.failure = err
	}
}

func (s *ResponsesStub) recordObserved(call ObservedCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.observed) < maxModelOperations {
		s.observed = append(s.observed, call)
	}
}

func boundedBody(request *http.Request) ([]byte, error) {
	if request.ContentLength > maxHTTPBodyBytes {
		return nil, errors.New("request body exceeds limit")
	}
	defer request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(request.Body, maxHTTPBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read model request: %w", err)
	}
	if len(body) > maxHTTPBodyBytes {
		return nil, errors.New("request body exceeds limit")
	}
	return body, nil
}

func routeForRequest(request *http.Request) (Route, error) {
	if request.Method != http.MethodPost && request.Method != http.MethodGet {
		return "", fmt.Errorf("unsupported model method %s", request.Method)
	}
	switch request.URL.Path {
	case "/v1/responses":
		if request.Method == http.MethodPost {
			return RouteResponses, nil
		}
	case "/v1/responses/compact":
		if request.Method == http.MethodPost {
			return RouteCompact, nil
		}
	case "/v1/responses/input_tokens":
		if request.Method == http.MethodPost {
			return RouteInputTokens, nil
		}
	default:
		segments := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
		if request.Method == http.MethodGet && len(segments) == 3 && segments[0] == "v1" && segments[1] == "models" && segments[2] != "" {
			return RouteModel, nil
		}
	}
	return "", fmt.Errorf("unsupported model route %s %s", request.Method, request.URL.Path)
}

type responseRequest struct {
	Input          []responseInputItem `json:"input"`
	PromptCacheKey string              `json:"prompt_cache_key"`
}

func hasMatchingSessionCacheKey(body []byte, headers http.Header) bool {
	var request responseRequest
	if json.Unmarshal(body, &request) != nil || request.PromptCacheKey == "" {
		return false
	}
	return headers.Get("session_id") == request.PromptCacheKey
}

type responseInputItem struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type responseContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func requestContainsProbe(body []byte, probe string) bool {
	var request responseRequest
	if json.Unmarshal(body, &request) != nil {
		return false
	}
	for _, item := range request.Input {
		if item.Role != "user" {
			continue
		}
		var content []responseContentItem
		if json.Unmarshal(item.Content, &content) != nil {
			continue
		}
		for _, part := range content {
			if (part.Type == "input_text" || part.Type == "text") && part.Text == probe {
				return true
			}
		}
	}
	return false
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
