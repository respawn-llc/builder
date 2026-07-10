package blackbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
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
	server           *http.Server
	listener         net.Listener
	required         []RequiredOperation
	mu               sync.Mutex
	index            int
	requiredInFlight bool
	active           int
	failure          error
	observed         []ObservedCall
	handlers         map[uint64]context.CancelFunc
	nextHandle       uint64
	stopping         bool
	serveStopped     bool
	done             chan struct{}
	doneOnce         sync.Once
	events           chan struct{}
}

func StartResponsesStub(required []RequiredOperation) (*ResponsesStub, error) {
	seen := make(map[uuid.UUID]struct{}, len(required))
	for index, operation := range required {
		if err := operation.Validate(); err != nil {
			return nil, fmt.Errorf("required operation %d: %w", index, err)
		}
		if _, exists := seen[operation.ID]; exists {
			return nil, fmt.Errorf("duplicate required operation identity %s", operation.ID)
		}
		seen[operation.ID] = struct{}{}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen response stub: %w", err)
	}
	stub := &ResponsesStub{
		listener: listener,
		required: append([]RequiredOperation(nil), required...),
		handlers: map[uint64]context.CancelFunc{},
		done:     make(chan struct{}),
		events:   make(chan struct{}, 1),
	}
	router := http.NewServeMux()
	router.HandleFunc("POST /v1/responses", stub.serveRoute(RouteResponses))
	router.HandleFunc("POST /v1/responses/compact", stub.serveRoute(RouteCompact))
	router.HandleFunc("POST /v1/responses/input_tokens", stub.serveRoute(RouteInputTokens))
	router.HandleFunc("GET /v1/models/{model}", stub.serveRoute(RouteModel))
	router.HandleFunc("/", stub.serveUnsupportedRoute)
	stub.server = &http.Server{
		Handler:           router,
		MaxHeaderBytes:    maxHTTPHeadersBytes,
		ReadHeaderTimeout: 500 * time.Millisecond,
	}
	go func() {
		if err := stub.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			stub.recordFailure(fmt.Errorf("serve Responses stub: %w", err))
		}
		stub.mu.Lock()
		stub.serveStopped = true
		stub.closeDoneLocked()
		stub.mu.Unlock()
	}()
	return stub, nil
}

func (s *ResponsesStub) serveUnsupportedRoute(writer http.ResponseWriter, request *http.Request) {
	s.recordFailure(errors.New("unsupported model route or method"))
	http.Error(writer, "unsupported model route", http.StatusNotFound)
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

// Events notifies consumers that a predicate-relevant stub state transition
// occurred. Consumers must always obtain the authoritative immutable state
// through Snapshot after receiving it.
func (s *ResponsesStub) Events() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.events
}

func (s *ResponsesStub) Snapshot() StubSnapshot {
	if s == nil {
		return StubSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	observed := make([]ObservedCall, len(s.observed))
	for index, call := range s.observed {
		headers := make(map[string][]string, len(call.Headers))
		for name, values := range call.Headers {
			headers[name] = append([]string(nil), values...)
		}
		observed[index] = ObservedCall{
			Route:   call.Route,
			Headers: headers,
			Body:    append(json.RawMessage(nil), call.Body...),
		}
	}
	return StubSnapshot{
		RequiredIndex:  s.index,
		RequiredTotal:  len(s.required),
		ActiveRequests: s.active,
		Failure:        s.failure,
		Observed:       observed,
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

// Stop stops admission, cancels all handlers, and closes listener connections
// without an unbounded graceful shutdown.
func (s *ResponsesStub) Stop() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.stopping = true
	for _, cancel := range s.handlers {
		cancel()
	}
	s.handlers = map[uint64]context.CancelFunc{}
	s.mu.Unlock()
	s.notify()
	if err := s.server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("close Responses stub server: %w", err)
	}
	return nil
}

// Close preserves the convenience lifecycle boundary used by existing direct
// tests. The runner uses Stop so its sole cleanup supervisor retains failures.
func (s *ResponsesStub) Close() {
	_ = s.Stop()
}

func (s *ResponsesStub) serveRoute(route Route) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		ctx, done, admitted := s.beginHandler(request.Context())
		if !admitted {
			http.Error(writer, "model stub is stopping", http.StatusServiceUnavailable)
			return
		}
		defer done()
		body, err := boundedBody(request)
		if err != nil {
			s.recordFailure(err)
			http.Error(writer, "invalid request", http.StatusRequestEntityTooLarge)
			return
		}
		if err := validateRouteBody(route, body); err != nil {
			s.recordFailure(err)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		call := ObservedCall{Route: route, Headers: request.Header.Clone(), Body: append(json.RawMessage(nil), body...)}
		operation, err := s.consume(route, body, request.Header)
		if err != nil {
			s.recordFailure(err)
			http.Error(writer, "unexpected model operation", http.StatusBadRequest)
			return
		}
		if operation != nil {
			defer s.completeRequired()
		}
		s.recordObserved(call)
		s.writeOperationResponse(ctx, writer, route, operation)
	}
}

func (s *ResponsesStub) beginHandler(parent context.Context) (context.Context, func(), bool) {
	s.mu.Lock()
	if s.stopping {
		s.mu.Unlock()
		return parent, nil, false
	}
	handle := s.nextHandle
	s.nextHandle++
	ctx, cancel := context.WithCancel(parent)
	s.handlers[handle] = cancel
	s.active++
	s.mu.Unlock()
	s.notify()
	return ctx, func() {
		s.mu.Lock()
		delete(s.handlers, handle)
		s.active--
		s.closeDoneLocked()
		s.mu.Unlock()
		cancel()
		s.notify()
	}, true
}

func (s *ResponsesStub) closeDoneLocked() {
	if s.serveStopped && s.active == 0 {
		s.doneOnce.Do(func() { close(s.done) })
	}
}

func (s *ResponsesStub) consume(route Route, body []byte, headers http.Header) (*RequiredOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return nil, s.failure
	}
	if s.index >= len(s.required) {
		if route == RouteInputTokens || route == RouteModel {
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected model operation route=%s after required queue", route)
	}
	required := s.required[s.index]
	if route != required.Route {
		if route == RouteInputTokens || route == RouteModel {
			return nil, nil
		}
		return nil, fmt.Errorf("required model operation route=%s got=%s", required.Route, route)
	}
	if s.requiredInFlight {
		return nil, errors.New("concurrent declared model operation")
	}
	if required.Probe != nil && !requestContainsProbe(body, *required.Probe) {
		return nil, errors.New("required response probe was not present in typed input")
	}
	if required.SessionCacheKey && !hasMatchingSessionCacheKey(body, headers) {
		return nil, errors.New("required response session_id and prompt_cache_key relation was not present")
	}
	s.index++
	s.requiredInFlight = true
	s.notify()
	return &required, nil
}

func (s *ResponsesStub) completeRequired() {
	s.mu.Lock()
	s.requiredInFlight = false
	s.mu.Unlock()
	s.notify()
}

func (s *ResponsesStub) writeOperationResponse(ctx context.Context, writer http.ResponseWriter, route Route, operation *RequiredOperation) {
	if operation != nil && operation.Outcome == OutcomeProviderFailure {
		http.Error(writer, "declared provider failure", http.StatusBadGateway)
		return
	}
	switch route {
	case RouteResponses:
		s.writeResponse(ctx, writer, operation)
	case RouteInputTokens:
		writeJSON(writer, http.StatusOK, map[string]int{"input_tokens": 0})
	case RouteModel:
		writeJSON(writer, http.StatusOK, map[string]any{"id": "gpt-5", "object": "model"})
	case RouteCompact:
		writeJSON(writer, http.StatusOK, map[string]any{"output": []any{}})
	default:
		http.Error(writer, "unsupported model route", http.StatusNotFound)
	}
}

func (s *ResponsesStub) writeResponse(ctx context.Context, writer http.ResponseWriter, operation *RequiredOperation) {
	if operation == nil {
		http.Error(writer, "response operation is not declared", http.StatusBadRequest)
		return
	}
	switch operation.Outcome {
	case OutcomeHoldSSE:
		writer.Header().Set("Content-Type", "text/event-stream")
		<-ctx.Done()
		return
	case OutcomeProviderFailure:
	case OutcomeStream:
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[]}}\n\n")
		_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", optionalText(operation.Output))
		_, _ = fmt.Fprintf(writer, "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n", optionalText(operation.Output))
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
		return
	case OutcomeJSON:
	default:
		http.Error(writer, "unsupported declared outcome", http.StatusInternalServerError)
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
			"content": []any{map[string]any{"type": "output_text", "text": optionalText(operation.Output)}},
		}},
	})
}

func optionalText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (s *ResponsesStub) recordFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		s.failure = err
	}
	s.notify()
}

func (s *ResponsesStub) recordObserved(call ObservedCall) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.observed) < maxModelOperations {
		s.observed = append(s.observed, call)
	}
	s.notify()
}

func (s *ResponsesStub) notify() {
	if s == nil {
		return
	}
	select {
	case s.events <- struct{}{}:
	default:
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

func validateRouteBody(route Route, body []byte) error {
	if route == RouteModel {
		if len(body) != 0 {
			return errors.New("model metadata request must not include a body")
		}
		return nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return fmt.Errorf("decode %s request DTO: %w", route, err)
	}
	if object == nil {
		return fmt.Errorf("decode %s request DTO: object is required", route)
	}
	return nil
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
