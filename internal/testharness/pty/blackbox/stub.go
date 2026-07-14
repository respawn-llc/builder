package blackbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"core/internal/testharness/pty/analyzer"
	"core/shared/runtimeids"

	"github.com/google/uuid"
)

const (
	maxHTTPBodyBytes    = 64 * 1024
	maxHTTPHeadersBytes = 16 * 1024
	maxModelDiagnostics = 1 * 1024 * 1024
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
	observedBytes    int
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
		Handler: router,
		// Keep net/http's ingress guard while retaining enough headroom for
		// the handler to record a typed protocol failure at the harness's
		// stricter diagnostic-header boundary.
		MaxHeaderBytes:    maxHTTPHeadersBytes + 4096,
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
		if headerBytes := requestHeaderBytes(request.Header); headerBytes > maxHTTPHeadersBytes {
			err := &analyzer.EvidenceLimitExceeded{
				Source:   analyzer.EvidenceSourceModelDiagnostics,
				Limit:    maxHTTPHeadersBytes,
				Observed: headerBytes,
				Detail:   "request headers",
			}
			s.recordFailure(err)
			http.Error(writer, "request headers exceed limit", http.StatusRequestHeaderFieldsTooLarge)
			return
		}
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
		if err := s.recordObserved(call); err != nil {
			s.recordFailure(err)
			http.Error(writer, "model diagnostics exceed limit", http.StatusRequestEntityTooLarge)
			return
		}
		operation, err := s.consume(route, body, request.Header)
		if err != nil {
			s.recordFailure(err)
			http.Error(writer, "unexpected model operation", http.StatusBadRequest)
			return
		}
		if operation != nil {
			defer s.completeRequired()
		}
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
		if err := writeJSON(writer, http.StatusOK, map[string]int{"input_tokens": 0}); err != nil {
			s.recordFailure(fmt.Errorf("write input-token response: %w", err))
		}
	case RouteModel:
		if err := writeJSON(writer, http.StatusOK, map[string]any{
			"id":             "gpt-5",
			"object":         "model",
			"created":        0,
			"owned_by":       "kent",
			"context_window": 200000,
		}); err != nil {
			s.recordFailure(fmt.Errorf("write model metadata response: %w", err))
		}
	case RouteCompact:
		if err := writeJSON(writer, http.StatusOK, map[string]any{"output": []any{}}); err != nil {
			s.recordFailure(fmt.Errorf("write compact response: %w", err))
		}
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
		addedItem, err := json.Marshal(assistantMessageOutputItem(nil, *operation.ResponsePhase))
		if err != nil {
			s.recordFailure(fmt.Errorf("encode response output item: %w", err))
			return
		}
		if !s.writeSSE(writer, fmt.Sprintf("data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":%s}\n\n", addedItem)) {
			return
		}
		if operation.Output != nil {
			delta, err := json.Marshal(*operation.Output)
			if err != nil {
				s.recordFailure(fmt.Errorf("encode response delta: %w", err))
				return
			}
			if !s.writeSSE(writer, fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%s}\n\n", delta)) {
				return
			}
		}
		if !s.writeSSE(writer, fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"output\":%s}}\n\n", responseOutputJSON(operation.Output, operation.ResponsePhase))) {
			return
		}
		s.writeSSE(writer, "data: [DONE]\n\n")
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
	if err := writeJSON(writer, http.StatusOK, map[string]any{"output": responseOutput(operation.Output, operation.ResponsePhase)}); err != nil {
		s.recordFailure(fmt.Errorf("write JSON response: %w", err))
	}
}

func responseOutput(value *string, phase *ResponsePhase) []any {
	if value == nil {
		return []any{}
	}
	return []any{assistantMessageOutputItem(value, *phase)}
}

func assistantMessageOutputItem(value *string, phase ResponsePhase) map[string]any {
	content := []any{}
	if value != nil {
		content = append(content, map[string]any{"type": "output_text", "text": *value})
	}
	item := map[string]any{
		"type": "message", "role": "assistant", "content": content,
	}
	if phase != ResponsePhaseAbsent {
		item["phase"] = string(phase)
	}
	return item
}

func responseOutputJSON(value *string, phase *ResponsePhase) string {
	encoded, err := json.Marshal(responseOutput(value, phase))
	if err != nil {
		panic(fmt.Sprintf("marshal fixed response output: %v", err))
	}
	return string(encoded)
}

func HandleInputTokenCount(writer http.ResponseWriter, request *http.Request, inputTokens int) bool {
	if request.URL.Path != "/responses/input_tokens" {
		return false
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}
	writer.Header().Set("Content-Type", "application/json")
	mustWriteFixtureResponse(writer, fmt.Sprintf(`{"object":"response.input_tokens","input_tokens":%d}`, inputTokens))
	return true
}

func WriteCompletedResponseStream(writer http.ResponseWriter, assistantText string, inputTokens, outputTokens int) {
	totalTokens := inputTokens + outputTokens
	phase := ResponsePhaseFinal
	output := responseOutput(&assistantText, &phase)
	payload, err := json.Marshal(map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"usage": map[string]any{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens,
				"total_tokens":  totalTokens,
			},
			"output": output,
		},
	})
	if err != nil {
		panic(fmt.Sprintf("marshal completed Responses fixture: %v", err))
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	mustWriteFixtureResponse(writer, "data: "+string(payload)+"\n\ndata: [DONE]\n\n")
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func mustWriteFixtureResponse(writer io.Writer, payload string) {
	if _, err := io.WriteString(writer, payload); err != nil {
		panic(fmt.Sprintf("write Responses fixture: %v", err))
	}
}

func (s *ResponsesStub) writeSSE(writer http.ResponseWriter, payload string) bool {
	if _, err := io.WriteString(writer, payload); err != nil {
		s.recordFailure(fmt.Errorf("write SSE response: %w", err))
		return false
	}
	return true
}

func (s *ResponsesStub) recordFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		s.failure = err
	}
	s.notify()
}

func (s *ResponsesStub) recordObserved(call ObservedCall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	observedBytes := observedCallBytes(call)
	if len(s.observed) >= maxModelOperations || observedBytes > maxModelDiagnostics-s.observedBytes {
		return &analyzer.EvidenceLimitExceeded{
			Source:   analyzer.EvidenceSourceModelDiagnostics,
			Limit:    maxModelDiagnostics,
			Observed: s.observedBytes + observedBytes,
			Detail:   "observed model diagnostics",
		}
	}
	s.observed = append(s.observed, call)
	s.observedBytes += observedBytes
	s.notify()
	return nil
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

func boundedBody(request *http.Request) (body []byte, returnErr error) {
	if request.ContentLength > maxHTTPBodyBytes {
		return nil, errors.New("request body exceeds limit")
	}
	defer func() {
		if err := request.Body.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("close model request: %w", err)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(request.Body, maxHTTPBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read model request: %w", err)
	}
	if len(body) > maxHTTPBodyBytes {
		return nil, errors.New("request body exceeds limit")
	}
	return body, nil
}

func requestHeaderBytes(headers http.Header) int {
	total := 0
	for name, values := range headers {
		for _, value := range values {
			total += len(name) + len(value) + len(": \r\n")
		}
	}
	return total
}

func observedCallBytes(call ObservedCall) int {
	return len(call.Route) + len(call.Body) + requestHeaderBytes(call.Headers)
}

func validateRouteBody(route Route, body []byte) error {
	if route == RouteModel {
		if len(body) != 0 {
			return errors.New("model metadata request must not include a body")
		}
		return nil
	}
	var request modelRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return fmt.Errorf("decode %s request DTO: %w", route, err)
	}
	if request.Input == nil {
		return fmt.Errorf("decode %s request DTO: input is required", route)
	}
	return nil
}

type modelRequest struct {
	Input *json.RawMessage `json:"input"`
}

type responseRequest struct {
	Input          []responseInputItem `json:"input"`
	PromptCacheKey *string             `json:"prompt_cache_key"`
}

func hasMatchingSessionCacheKey(body []byte, headers http.Header) bool {
	var request responseRequest
	if json.Unmarshal(body, &request) != nil || request.PromptCacheKey == nil {
		return false
	}
	session, err := parseSessionCacheKey(headers.Get("session_id"))
	if err != nil {
		return false
	}
	cacheKey, err := parseSessionCacheKey(*request.PromptCacheKey)
	return err == nil && session.Compaction == nil && cacheKey.SessionID.String() == session.SessionID.String() && cacheKey.Supervisor == session.Supervisor
}

type sessionCacheKey struct {
	SessionID  runtimeids.SessionID
	Supervisor bool
	Compaction *int
}

func parseSessionCacheKey(raw string) (sessionCacheKey, error) {
	sessionPart, suffix, hasSuffix := strings.Cut(raw, "/")
	sessionID, err := runtimeids.ParseSessionID(sessionPart)
	if err != nil {
		return sessionCacheKey{}, err
	}
	key := sessionCacheKey{SessionID: sessionID}
	if !hasSuffix {
		return key, nil
	}
	segment, tail, hasTail := strings.Cut(suffix, "/")
	if segment == "supervisor" {
		key.Supervisor = true
		if !hasTail {
			return key, nil
		}
		segment = tail
	} else if hasTail {
		return sessionCacheKey{}, errors.New("invalid session cache key suffix")
	}
	count, err := parseCompactionSegment(segment)
	if err != nil {
		return sessionCacheKey{}, err
	}
	key.Compaction = &count
	return key, nil
}

func parseCompactionSegment(segment string) (int, error) {
	prefix, sequence, valid := strings.Cut(segment, "-")
	if !valid || prefix != "compact" {
		return 0, errors.New("invalid compacted session cache key")
	}
	count, err := strconv.Atoi(sequence)
	if err != nil || count <= 0 {
		return 0, errors.New("invalid compacted session cache key sequence")
	}
	return count, nil
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

func writeJSON(writer http.ResponseWriter, status int, value any) error {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	return json.NewEncoder(writer).Encode(value)
}
