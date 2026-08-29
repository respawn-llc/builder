package blackbox_test

import (
	"bytes"
	"context"
	"core/internal/testharness/httpclient"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/pty/blackbox"
	"core/server/llm"
	"core/shared/textutil"

	"github.com/google/uuid"
)

type staticTransportAuth struct{}

func (staticTransportAuth) AuthorizationHeader(context.Context) (string, error) {
	return "Bearer test", nil
}

type oauthStaticTransportAuth struct{ staticTransportAuth }

func (oauthStaticTransportAuth) OpenAIAuthMetadata(context.Context) (string, string, error) {
	return "oauth", "test-account", nil
}

func TestResponsesStubRejectsUnexpectedDeveloperMessageCount(t *testing.T) {
	want := 0
	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID:                    uuid.New(),
		Route:                 blackbox.RouteResponses,
		DeveloperMessageCount: &want,
		Outcome:               blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(func() { stub.Close() })

	response, err := http.Post(stub.URL()+"/responses", "application/json", strings.NewReader(`{"input":[{"role":"developer","content":[{"type":"input_text","text":"context"}]}]}`))
	if err != nil {
		t.Fatalf("POST responses: %v", err)
	}
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatalf("close response: %v", closeErr)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	if err := stub.Verify(); err == nil {
		t.Fatal("Verify accepted an unexpected developer message count")
	}
}

func TestResponsesStubAcceptsExpectedDeveloperMessageCount(t *testing.T) {
	want := 1
	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID:                    uuid.New(),
		Route:                 blackbox.RouteResponses,
		DeveloperMessageCount: &want,
		Outcome:               blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(func() { stub.Close() })

	response, err := http.Post(stub.URL()+"/responses", "application/json", strings.NewReader(`{"input":[{"role":"developer","content":[{"type":"input_text","text":"context"}]}]}`))
	if err != nil {
		t.Fatalf("POST responses: %v", err)
	}
	if closeErr := response.Body.Close(); closeErr != nil {
		t.Fatalf("close response: %v", closeErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if err := stub.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestResponsesStubCancelsHeldSSEAndReturnsDeclaredProviderFailure(t *testing.T) {
	hold, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeHoldSSE,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub hold: %v", err)
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, hold.URL()+"/responses", bytes.NewBufferString(`{"input":[]}`))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	responseDone := make(chan error, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		responseDone <- requestErr
	}()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for hold.Snapshot().ActiveRequests == 0 {
		select {
		case <-hold.Events():
		case <-deadline.C:
			t.Fatal("held SSE did not become active")
		}
	}
	hold.Close()
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		t.Fatal("held SSE request did not unblock on stub close")
	}
	select {
	case <-hold.Done():
	case <-time.After(time.Second):
		t.Fatal("held SSE stub did not close")
	}

	provider, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeProviderFailure,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub provider failure: %v", err)
	}
	t.Cleanup(provider.Close)
	response, err := http.Post(provider.URL()+"/responses", "application/json", bytes.NewBufferString(`{"input":[]}`))
	if err != nil {
		t.Fatalf("POST provider failure: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("provider failure status = %d, want %d", response.StatusCode, http.StatusBadGateway)
	}

	compact, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteCompact, Outcome: blackbox.OutcomeProviderFailure,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub compact failure: %v", err)
	}
	t.Cleanup(compact.Close)
	response, err = http.Post(compact.URL()+"/responses", "application/json", bytes.NewBufferString(`{"input":[{"type":"compaction_trigger"}]}`))
	if err != nil {
		t.Fatalf("POST compact provider failure: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("compact provider failure status = %d, want %d", response.StatusCode, http.StatusBadGateway)
	}
}

func TestResponsesStubConsumesTypedProbeAndRejectsUnconsumedQueue(t *testing.T) {
	t.Parallel()

	probe := uuid.New().String()
	output := "ok"
	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID:            uuid.New(),
		Route:         blackbox.RouteResponses,
		Probe:         &probe,
		Outcome:       blackbox.OutcomeStream,
		Output:        &output,
		ResponsePhase: blackbox.NewResponsePhase(blackbox.ResponsePhaseFinal),
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)

	response, err := http.Post(stub.URL()+"/responses", "application/json", bytes.NewBufferString(`{"input":[{"role":"user","content":[{"type":"input_text","text":"`+probe+`"}]}]}`))
	if err != nil {
		t.Fatalf("POST responses: %v", err)
	}
	drainResponseBody(t, response)
	if err := stub.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	unconsumed, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("Start unconsumed stub: %v", err)
	}
	t.Cleanup(unconsumed.Close)
	if err := unconsumed.Verify(); err == nil {
		t.Fatal("Verify accepted unconsumed operation")
	}
}

func TestResponsesStubAcceptsLosslessResponseDTOAndStaticAdaptiveDefaults(t *testing.T) {
	t.Parallel()

	probe := uuid.New().String()
	cacheKey := uuid.New().String()
	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Probe: &probe, SessionCacheKey: true, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)

	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/models/gpt-5", body: ""},
	} {
		httpRequest, err := http.NewRequest(request.method, stub.URL()+request.path, bytes.NewBufferString(request.body))
		if err != nil {
			t.Fatalf("NewRequest %s: %v", request.path, err)
		}
		response, err := http.DefaultClient.Do(httpRequest)
		if err != nil {
			t.Fatalf("request %s: %v", request.path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("adaptive request %s status = %d, want %d", request.path, response.StatusCode, http.StatusOK)
		}
	}
	if snapshot := stub.Snapshot(); snapshot.RequiredIndex != 0 || len(snapshot.Observed) != 1 {
		t.Fatalf("adaptive defaults affected required proof: %#v", snapshot)
	}

	request, err := http.NewRequest(http.MethodPost, stub.URL()+"/responses", bytes.NewBufferString(`{
		"input":[
			{"role":"system","content":[{"type":"input_text","text":"system"}]},
			{"role":"developer","content":[{"type":"input_text","text":"developer"}]},
			{"role":"user","content":[{"type":"input_text","text":"`+probe+`"}]}
		],
		"prompt_cache_key":"`+cacheKey+`",
		"tools":[{"type":"function","name":"shell","parameters":{"type":"object","properties":{"command":{"type":"string"}}}}],
		"metadata":{"generated_skill":"present"}
	}`))
	if err != nil {
		t.Fatalf("NewRequest response: %v", err)
	}
	request.Header.Set("session-id", cacheKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST response: %v", err)
	}
	drainResponseBody(t, response)
	if err := stub.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestResponsesStubRejectsProbeMismatch(t *testing.T) {
	t.Parallel()

	probe := uuid.New().String()
	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Probe: &probe, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)
	response, err := http.Post(stub.URL()+"/responses", "application/json", bytes.NewBufferString(`{"input":[]}`))
	if err != nil {
		t.Fatalf("POST responses: %v", err)
	}
	drainResponseBody(t, response)
	if err := stub.Verify(); err == nil {
		t.Fatal("Verify accepted probe mismatch")
	}
}

func drainResponseBody(t *testing.T, response *http.Response) {
	t.Helper()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
}

func TestResponsesStubRejectsMalformedRouteDTO(t *testing.T) {
	t.Parallel()

	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)

	response, err := http.Post(stub.URL()+"/responses", "application/json", bytes.NewBufferString(`{"input":`))
	if err != nil {
		t.Fatalf("POST malformed DTO: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed DTO status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	if err := stub.Verify(); err == nil {
		t.Fatal("Verify accepted malformed DTO")
	}

}

func TestResponsesStubRecordsUnsupportedRouteAsProtocolFailure(t *testing.T) {
	t.Parallel()

	stub, err := blackbox.StartResponsesStub(nil)
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)

	response, err := http.Get(stub.URL() + "/unsupported")
	if err != nil {
		t.Fatalf("GET unsupported route: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unsupported route status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
	if err := stub.Verify(); err == nil {
		t.Fatal("Verify accepted unsupported route")
	}

	methodStub, err := blackbox.StartResponsesStub(nil)
	if err != nil {
		t.Fatalf("StartResponsesStub method: %v", err)
	}
	t.Cleanup(methodStub.Close)
	response, err = http.Get(methodStub.URL() + "/responses")
	if err != nil {
		t.Fatalf("GET unsupported method: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unsupported method status = %d, want %d", response.StatusCode, http.StatusNotFound)
	}
	if err := methodStub.Verify(); err == nil {
		t.Fatal("Verify accepted unsupported method")
	}
}

func TestResponsesStubRejectsInvalidDeclaredOperationBeforeListening(t *testing.T) {
	t.Parallel()

	negative := -1
	invalidProbe := "not-a-uuid"
	output := "invalid"
	oversized := strings.Repeat("x", 64*1024+1)
	invalidPhase := blackbox.ResponsePhase("invalid")
	for name, operation := range map[string]blackbox.RequiredOperation{
		"missing identity": {
			Route: blackbox.RouteResponses,
		},
		"unsupported route": {
			ID: uuid.New(), Route: blackbox.Route("unsupported"), Outcome: blackbox.OutcomeJSON,
		},
		"unsupported outcome": {
			ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.Outcome("unsupported"),
		},
		"invalid probe": {
			ID: uuid.New(), Route: blackbox.RouteResponses, Probe: &invalidProbe, Outcome: blackbox.OutcomeJSON,
		},
		"negative developer message count": {
			ID: uuid.New(), Route: blackbox.RouteResponses, DeveloperMessageCount: &negative, Outcome: blackbox.OutcomeJSON,
		},
		"oversized probe": {
			ID: uuid.New(), Route: blackbox.RouteResponses, Probe: &oversized, Outcome: blackbox.OutcomeJSON,
		},
		"oversized output": {
			ID: uuid.New(), Route: blackbox.RouteResponses, Output: &oversized, Outcome: blackbox.OutcomeJSON,
		},
		"non-responses response phase": {
			ID: uuid.New(), Route: blackbox.RouteCompact, Outcome: blackbox.OutcomeJSON,
			ResponsePhase: blackbox.NewResponsePhase(blackbox.ResponsePhaseFinal),
		},
		"emitted message missing phase": {
			ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeJSON, Output: &output,
		},
		"phase without emitted message": {
			ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeJSON,
			ResponsePhase: blackbox.NewResponsePhase(blackbox.ResponsePhaseFinal),
		},
		"invalid response phase": {
			ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeJSON,
			Output: &output, ResponsePhase: &invalidPhase,
		},
	} {
		if _, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{operation}); err == nil {
			t.Fatalf("StartResponsesStub accepted %s", name)
		}
	}
}

func TestResponsesStubRejectsConcurrentDeclaredOperation(t *testing.T) {
	t.Parallel()

	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{
		{ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeHoldSSE},
		{ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeJSON},
	})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)

	firstDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Post(stub.URL()+"/responses", "application/json", bytes.NewBufferString(`{"input":[]}`))
		if response != nil {
			_ = response.Body.Close()
		}
		firstDone <- requestErr
	}()
	waitForRequiredOperationAdmission(t, stub, 1)
	response, err := http.Post(stub.URL()+"/responses", "application/json", bytes.NewBufferString(`{"input":[]}`))
	if err != nil {
		t.Fatalf("POST concurrent request: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("concurrent request status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	if err := stub.Verify(); err == nil {
		t.Fatal("Verify accepted concurrent declared operation")
	}
	stub.Close()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("held first request did not unblock")
	}
}

func waitForRequiredOperationAdmission(t *testing.T, stub *blackbox.ResponsesStub, admitted int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		snapshot := stub.Snapshot()
		if snapshot.Failure != nil {
			t.Fatalf("model request failed before operation admission: %v", snapshot.Failure)
		}
		if snapshot.RequiredIndex >= admitted {
			return
		}
		select {
		case <-stub.Events():
		case <-deadline.C:
			t.Fatalf("model operation admission = %d, want at least %d", snapshot.RequiredIndex, admitted)
		}
	}
}

func TestResponsesStubRejectsOversizedBodyBeforeQueueConsumption(t *testing.T) {
	t.Parallel()

	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)

	response, err := http.Post(stub.URL()+"/responses", "application/json", bytes.NewReader(bytes.Repeat([]byte("x"), 64*1024+1)))
	if err != nil {
		t.Fatalf("POST oversized DTO: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized DTO status = %d, want %d", response.StatusCode, http.StatusRequestEntityTooLarge)
	}
	snapshot := stub.Snapshot()
	if snapshot.RequiredIndex != 0 {
		t.Fatalf("oversized DTO consumed required operation: %#v", snapshot)
	}
	if err := stub.Verify(); err == nil {
		t.Fatal("Verify accepted oversized DTO")
	}
}

func TestResponsesStubRejectsOversizedHeadersAndChunkedBodiesBeforeQueueConsumption(t *testing.T) {
	t.Parallel()

	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)

	headerRequest, err := http.NewRequest(http.MethodPost, stub.URL()+"/responses", bytes.NewBufferString(`{"input":[]}`))
	if err != nil {
		t.Fatalf("NewRequest headers: %v", err)
	}
	headerRequest.Header.Set("X-Harness-Overflow", strings.Repeat("h", 16*1024))
	response, err := http.DefaultClient.Do(headerRequest)
	if err != nil {
		t.Fatalf("POST oversized headers: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("oversized header status = %d, want %d", response.StatusCode, http.StatusRequestHeaderFieldsTooLarge)
	}
	if stub.Snapshot().RequiredIndex != 0 {
		t.Fatal("oversized headers consumed required operation")
	}

	chunkedStub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub chunked: %v", err)
	}
	t.Cleanup(chunkedStub.Close)
	chunkedRequest, err := http.NewRequest(http.MethodPost, chunkedStub.URL()+"/responses", unknownLengthReader{Reader: bytes.NewReader(bytes.Repeat([]byte("x"), 64*1024+1))})
	if err != nil {
		t.Fatalf("NewRequest chunked: %v", err)
	}
	if chunkedRequest.ContentLength != 0 {
		t.Fatalf("chunked request content length = %d, want unknown", chunkedRequest.ContentLength)
	}
	response, err = http.DefaultClient.Do(chunkedRequest)
	if err != nil {
		t.Fatalf("POST chunked oversized body: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked oversized body status = %d, want %d", response.StatusCode, http.StatusRequestEntityTooLarge)
	}
	if chunkedStub.Snapshot().RequiredIndex != 0 {
		t.Fatal("chunked oversized body consumed required operation")
	}
}

func TestResponsesStubBoundsObservedDiagnosticsAndEnforcesRequiredOrder(t *testing.T) {
	t.Parallel()

	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{
		{ID: uuid.New(), Route: blackbox.RouteCompact, Outcome: blackbox.OutcomeJSON},
		{ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeJSON},
	})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)
	response, err := http.Post(stub.URL()+"/responses", "application/json", bytes.NewBufferString(`{"input":[]}`))
	if err != nil {
		t.Fatalf("POST route-order mismatch: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("route-order mismatch status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	if err := stub.Verify(); err == nil {
		t.Fatal("Verify accepted required operation order mismatch")
	}

}

func TestResponsesStubRejectsRequiredQueueExhaustionAndProvidesProviderFailuresForAllRoutes(t *testing.T) {
	t.Parallel()

	exhausted, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub exhausted: %v", err)
	}
	t.Cleanup(exhausted.Close)
	for requestNumber := 0; requestNumber < 2; requestNumber++ {
		response, err := http.Post(exhausted.URL()+"/responses", "application/json", bytes.NewBufferString(`{"input":[]}`))
		if err != nil {
			t.Fatalf("POST exhausted request %d: %v", requestNumber, err)
		}
		status := response.StatusCode
		_ = response.Body.Close()
		if requestNumber == 0 && status != http.StatusOK {
			t.Fatalf("first exhausted request status = %d, want %d", status, http.StatusOK)
		}
		if requestNumber == 1 && status != http.StatusBadRequest {
			t.Fatalf("second exhausted request status = %d, want %d", status, http.StatusBadRequest)
		}
	}
	if err := exhausted.Verify(); err == nil {
		t.Fatal("Verify accepted required queue exhaustion")
	}

	for _, failure := range []struct {
		route  blackbox.Route
		method string
		path   string
		body   string
	}{
		{route: blackbox.RouteModel, method: http.MethodGet, path: "/models/gpt-5"},
	} {
		stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
			ID: uuid.New(), Route: failure.route, Outcome: blackbox.OutcomeProviderFailure,
		}})
		if err != nil {
			t.Fatalf("StartResponsesStub %s: %v", failure.route, err)
		}
		request, err := http.NewRequest(failure.method, stub.URL()+failure.path, bytes.NewBufferString(failure.body))
		if err != nil {
			t.Fatalf("NewRequest %s: %v", failure.route, err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("provider failure request %s: %v", failure.route, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadGateway {
			t.Fatalf("provider failure %s status = %d, want %d", failure.route, response.StatusCode, http.StatusBadGateway)
		}
		if err := stub.Stop(); err != nil {
			t.Fatalf("Stop provider failure stub: %v", err)
		}
	}
}

func TestResponsesStubStreamsRequiredOperationToHTTPTransport(t *testing.T) {
	t.Parallel()

	probe := uuid.New().String()
	output := "\x1b\x00\n"
	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Probe: &probe, Outcome: blackbox.OutcomeStream, Output: &output, ResponsePhase: blackbox.NewResponsePhase(blackbox.ResponsePhaseFinal),
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)

	transport := llm.NewHTTPTransport(staticTransportAuth{})
	transport.BaseURL = stub.URL()
	transport.Client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	var deltas []string
	response, err := transport.Generate(context.Background(), llm.OpenAIRequest{
		Model:          "gpt-5",
		SessionID:      textutil.Value("session-1"),
		ToolChoiceMode: llm.ToolChoiceModeAutomatic,
		Items:          llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: textutil.Value(probe)}}),
	}, llm.StreamCallbacks{OnAssistantDelta: func(delta llm.AssistantDelta) {
		deltas = append(deltas, delta.Text)
	}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if response.AssistantText == nil || *response.AssistantText != output {
		t.Fatalf("assistant text = %#v, want %q", response.AssistantText, output)
	}
	if len(deltas) != 1 || deltas[0] != output {
		t.Fatalf("assistant deltas = %#v, want %q", deltas, output)
	}
	if err := stub.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestResponsesStubServesCompactAndModelMetadataTransportRoutes(t *testing.T) {
	t.Parallel()

	compact, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteCompact, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub compact: %v", err)
	}
	t.Cleanup(compact.Close)
	compactTransport := llm.NewHTTPTransport(oauthStaticTransportAuth{})
	compactTransport.BaseURL = "https://chatgpt.com/backend-api/codex"
	compactTransport.BaseURLExplicit = true
	compactTransport.Client = newCanonicalOAuthStubClient(t, compact)
	if _, err := compactTransport.Compact(context.Background(), llm.OpenAIRequest{
		Model:          "gpt-5",
		SessionID:      textutil.Value("session-1"),
		CodexDispatch:  testCodexDispatch(t, "session-1", llm.CodexRequestKindCompaction),
		ToolChoiceMode: llm.ToolChoiceModeAutomatic,
		Items:          llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}}),
	}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := compact.Verify(); err != nil {
		t.Fatalf("Verify compact: %v", err)
	}

	model, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteModel, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub model metadata: %v", err)
	}
	t.Cleanup(model.Close)
	modelTransport := newStubTransport(model)
	modelTransport.ContextWindowTokens = 0
	window, err := modelTransport.ResolveModelContextWindow(context.Background(), "gpt-5")
	if err != nil {
		t.Fatalf("ResolveModelContextWindow: %v", err)
	}
	if window != 200000 {
		t.Fatalf("model context window = %d, want 200000", window)
	}
	if err := model.Verify(); err != nil {
		t.Fatalf("Verify model metadata: %v", err)
	}
}

func testCodexDispatch(t *testing.T, sessionID string, requestKind llm.CodexRequestKind) *llm.CodexDispatchContext {
	t.Helper()
	dispatch, err := llm.NewCodexDispatchContext(llm.CodexDispatchFacts{
		SessionID:   sessionID,
		RunID:       "run-1",
		RequestKind: requestKind.Optional(),
	})
	if err != nil {
		t.Fatalf("NewCodexDispatchContext: %v", err)
	}
	return dispatch
}

func newStubTransport(stub *blackbox.ResponsesStub) *llm.HTTPTransport {
	transport := llm.NewHTTPTransport(staticTransportAuth{})
	transport.BaseURL = stub.URL()
	transport.Client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	return transport
}

func newCanonicalOAuthStubClient(t *testing.T, stub *blackbox.ResponsesStub) *http.Client {
	t.Helper()
	target, err := url.Parse(stub.URL())
	if err != nil {
		t.Fatalf("parse Responses stub URL: %v", err)
	}
	roundTripper := &http.Transport{Proxy: nil}
	t.Cleanup(roundTripper.CloseIdleConnections)
	return &http.Client{
		Transport: httpclient.NewURLRewriteTransport(target, roundTripper, "/backend-api/codex"),
	}
}

type unknownLengthReader struct {
	io.Reader
}
