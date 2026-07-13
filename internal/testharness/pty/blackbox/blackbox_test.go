package blackbox_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/pty/analyzer"
	"core/internal/testharness/pty/blackbox"
	"core/server/llm"

	"github.com/google/uuid"
)

type staticTransportAuth struct{}

func (staticTransportAuth) AuthorizationHeader(context.Context) (string, error) {
	return "Bearer test", nil
}

func TestDecodeScenarioRejectsUnknownFieldsAndUnionEscapeHatches(t *testing.T) {
	t.Parallel()

	id := uuid.New().String()
	_, err := blackbox.DecodeScenario([]byte(`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[],"delay_ms":1}`))
	if err == nil {
		t.Fatal("DecodeScenario accepted unknown delay field")
	}
	_, err = blackbox.DecodeScenario([]byte(`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[{"id":"` + uuid.New().String() + `","kind":"terminate_process","input":"x"}]}`))
	if err == nil {
		t.Fatal("DecodeScenario accepted mixed action union")
	}
	_, err = blackbox.DecodeScenario([]byte(`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[{"id":"` + uuid.New().String() + `","kind":"terminate_process","input":""}]}`))
	if err == nil {
		t.Fatal("DecodeScenario accepted explicitly empty mixed action union field")
	}
	_, err = blackbox.DecodeScenario([]byte(`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[{"id":"` + uuid.New().String() + `","kind":"wait","predicate":{"kind":"private_mode","mode":25,"enabled":true,"rows":0}}]}`))
	if err == nil {
		t.Fatal("DecodeScenario accepted an irrelevant predicate field")
	}
	_, err = blackbox.DecodeScenario([]byte(`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[{"id":"` + uuid.New().String() + `","route":"compact","stream":false}],"actions":[]}`))
	if err == nil {
		t.Fatal("DecodeScenario accepted explicitly irrelevant model operation field")
	}
}

func TestDecodeScenarioRejectsInvalidIdentitiesPayloadsAndRouteCombinations(t *testing.T) {
	t.Parallel()

	id := uuid.New().String()
	action := uuid.New().String()
	operation := uuid.New().String()
	cases := []string{
		`{"version":2,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[]}`,
		`{"version":1,"id":"00000000-0000-0000-0000-000000000000","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[]}`,
		`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[{"id":"` + action + `","kind":"enter_input","input":""}]}`,
		`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[{"id":"` + operation + `","route":"compact","outcome":"hold_sse"}],"actions":[]}`,
		`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[{"id":"` + operation + `","route":"model_metadata","outcome":"json","output":"x"}],"actions":[]}`,
		`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[{"id":"` + action + `","kind":"wait","predicate":{"kind":"all","children":[]}}]}`,
	}
	for _, document := range cases {
		if _, err := blackbox.DecodeScenario([]byte(document)); err == nil {
			t.Fatalf("DecodeScenario accepted invalid document: %s", document)
		}
	}
	oversized := []byte(`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[],"padding":"` + strings.Repeat("x", 256*1024) + `"}`)
	if _, err := blackbox.DecodeScenario(oversized); err == nil {
		t.Fatal("DecodeScenario accepted oversized document")
	}
}

func TestPredicateVocabularyAndCompositesUseStructuredObservation(t *testing.T) {
	t.Parallel()

	dimensions := analyzer.MustDimensions(2, 8)
	screen := analyzer.NewScreenSnapshot(dimensions)
	analysis := analyzer.Analysis{
		Dimensions:         dimensions,
		Screen:             screen,
		PrivateModeChanges: []analyzer.PrivateModeChange{{Mode: 25, Enabled: true}},
	}
	observation := blackbox.RunObservation{
		Analysis:     &analysis,
		ClientExited: true,
		ServerReady:  true,
		Model: blackbox.StubSnapshot{
			RequiredIndex: 1, RequiredTotal: 1,
		},
	}
	enabled := true
	rows, cols, mode := 2, 8, 25
	for _, predicate := range []blackbox.Predicate{
		{Kind: blackbox.PredicateParseable},
		{Kind: blackbox.PredicateBlank},
		{Kind: blackbox.PredicateDimensions, Rows: &rows, Cols: &cols},
		{Kind: blackbox.PredicatePrivateMode, Mode: &mode, Enabled: &enabled},
		{Kind: blackbox.PredicatePromptReady},
		{Kind: blackbox.PredicateProcessExited},
		{Kind: blackbox.PredicateServerReady},
		{Kind: blackbox.PredicateModelConsumed},
		{Kind: blackbox.PredicateNoActiveModels},
		{Kind: blackbox.PredicateAll, Children: []blackbox.Predicate{{Kind: blackbox.PredicateParseable}, {Kind: blackbox.PredicateServerReady}}},
		{Kind: blackbox.PredicateAny, Children: []blackbox.Predicate{{Kind: blackbox.PredicateNonBlank}, {Kind: blackbox.PredicateServerReady}}},
	} {
		if !predicate.Matches(observation) {
			t.Fatalf("structured predicate %s did not match %#v", predicate.Kind, observation)
		}
	}
	analysis.Screen.Cells[0][0] = analyzer.Cell{Content: "x"}
	if !(&blackbox.Predicate{Kind: blackbox.PredicateNonBlank}).Matches(observation) {
		t.Fatal("nonblank predicate did not inspect screen structure")
	}
}

func TestDecodeScenarioDistinguishesAbsentAndInvalidZeroPredicateFields(t *testing.T) {
	t.Parallel()

	id := uuid.New().String()
	action := uuid.New().String()
	for _, document := range []string{
		`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[{"id":"` + action + `","kind":"wait","predicate":{"kind":"dimensions","rows":0,"cols":8}}]}`,
		`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[{"id":"` + action + `","kind":"wait","predicate":{"kind":"dimensions","cols":8}}]}`,
		`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[{"id":"` + action + `","kind":"wait","predicate":{"kind":"private_mode","mode":0,"enabled":true}}]}`,
		`{"version":1,"id":"` + id + `","dimensions":{"rows":2,"cols":8},"model_operations":[],"actions":[{"id":"` + action + `","kind":"wait","predicate":{"kind":"private_mode","enabled":true}}]}`,
	} {
		if _, err := blackbox.DecodeScenario([]byte(document)); err == nil {
			t.Fatalf("DecodeScenario accepted invalid zero or absent required predicate field: %s", document)
		}
	}
}

func TestPromptReadyRequiresCursorVisibilityAfterMostRecentAlternateExit(t *testing.T) {
	t.Parallel()

	predicate := blackbox.Predicate{Kind: blackbox.PredicatePromptReady}
	observation := func(changes ...analyzer.PrivateModeChange) blackbox.RunObservation {
		return blackbox.RunObservation{Analysis: &analyzer.Analysis{PrivateModeChanges: changes}}
	}
	if !predicate.Matches(observation(analyzer.PrivateModeChange{Mode: 25, Enabled: true})) {
		t.Fatal("startup cursor-visible transition did not satisfy prompt readiness")
	}
	if predicate.Matches(observation(
		analyzer.PrivateModeChange{Mode: 1049, Enabled: true},
		analyzer.PrivateModeChange{Mode: 25, Enabled: true},
	)) {
		t.Fatal("cursor-visible transition inside alternate screen satisfied prompt readiness")
	}
	if predicate.Matches(observation(
		analyzer.PrivateModeChange{Mode: 25, Enabled: true},
		analyzer.PrivateModeChange{Mode: 1049, Enabled: true},
		analyzer.PrivateModeChange{Mode: 1049, Enabled: false},
	)) {
		t.Fatal("cursor transition before alternate exit satisfied prompt readiness")
	}
	if !predicate.Matches(observation(
		analyzer.PrivateModeChange{Mode: 1049, Enabled: true},
		analyzer.PrivateModeChange{Mode: 1049, Enabled: false},
		analyzer.PrivateModeChange{Mode: 25, Enabled: true},
	)) {
		t.Fatal("cursor-visible transition after alternate exit did not satisfy prompt readiness")
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
	response, err = http.Post(compact.URL()+"/responses/compact", "application/json", bytes.NewBufferString(`{"input":[]}`))
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
		ID:      uuid.New(),
		Route:   blackbox.RouteResponses,
		Probe:   &probe,
		Outcome: blackbox.OutcomeStream,
		Output:  &output,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)

	response, err := http.Post(stub.URL()+"/responses", "application/json", bytes.NewBufferString(`{"input":[{"role":"user","content":[{"type":"input_text","text":"`+probe+`"}]}]}`))
	if err != nil {
		t.Fatalf("POST responses: %v", err)
	}
	_ = response.Body.Close()
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
		{method: http.MethodPost, path: "/responses/input_tokens", body: `{"input":[]}`},
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
	if snapshot := stub.Snapshot(); snapshot.RequiredIndex != 0 || len(snapshot.Observed) != 2 {
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
	request.Header.Set("session_id", cacheKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST response: %v", err)
	}
	_ = response.Body.Close()
	if err := stub.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestResponsesStubAcceptsCompactedSessionCacheKey(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New().String()
	cacheKey := sessionID + "/compact-1"
	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, SessionCacheKey: true, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)

	request, err := http.NewRequest(http.MethodPost, stub.URL()+"/responses", bytes.NewBufferString(`{"input":[],"prompt_cache_key":"`+cacheKey+`"}`))
	if err != nil {
		t.Fatalf("NewRequest response: %v", err)
	}
	request.Header.Set("session_id", sessionID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST response: %v", err)
	}
	_ = response.Body.Close()
	if err := stub.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestResponsesStubAcceptsSupervisorCompactedSessionCacheKey(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New().String()
	sessionKey := sessionID + "/supervisor"
	cacheKey := sessionKey + "/compact-1"
	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, SessionCacheKey: true, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)

	request, err := http.NewRequest(http.MethodPost, stub.URL()+"/responses", bytes.NewBufferString(`{"input":[],"prompt_cache_key":"`+cacheKey+`"}`))
	if err != nil {
		t.Fatalf("NewRequest response: %v", err)
	}
	request.Header.Set("session_id", sessionKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST response: %v", err)
	}
	_ = response.Body.Close()
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
	_ = response.Body.Close()
	if err := stub.Verify(); err == nil {
		t.Fatal("Verify accepted probe mismatch")
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

	inputMissing, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteInputTokens, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub input DTO: %v", err)
	}
	t.Cleanup(inputMissing.Close)
	response, err = http.Post(inputMissing.URL()+"/responses/input_tokens", "application/json", bytes.NewBufferString(`{"model":"gpt-5"}`))
	if err != nil {
		t.Fatalf("POST missing input DTO: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing input DTO status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	if err := inputMissing.Verify(); err == nil {
		t.Fatal("Verify accepted missing input DTO")
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

	if _, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		Route: blackbox.RouteResponses,
	}}); err == nil {
		t.Fatal("StartResponsesStub accepted an invalid declared operation")
	}
	output := "invalid"
	if _, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteCompact, Outcome: blackbox.OutcomeStream,
	}}); err == nil {
		t.Fatal("StartResponsesStub accepted compact stream outcome")
	}
	if _, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteInputTokens, Outcome: blackbox.OutcomeJSON, Output: &output,
	}}); err == nil {
		t.Fatal("StartResponsesStub accepted route-irrelevant output")
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
	waitForActiveRequest(t, stub)
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

func waitForActiveRequest(t *testing.T, stub *blackbox.ResponsesStub) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for stub.Snapshot().ActiveRequests == 0 {
		select {
		case <-stub.Events():
		case <-deadline.C:
			t.Fatal("model request did not become active")
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

	diagnostics, err := blackbox.StartResponsesStub(nil)
	if err != nil {
		t.Fatalf("StartResponsesStub diagnostics: %v", err)
	}
	t.Cleanup(diagnostics.Close)
	body := []byte(`{"input":"` + strings.Repeat("d", 64*1024-32) + `"}`)
	for requestNumber := 0; ; requestNumber++ {
		response, err = http.Post(diagnostics.URL()+"/responses/input_tokens", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("POST diagnostics request %d: %v", requestNumber, err)
		}
		status := response.StatusCode
		_ = response.Body.Close()
		if status == http.StatusRequestEntityTooLarge {
			break
		}
		if status != http.StatusOK {
			t.Fatalf("diagnostic request %d status = %d", requestNumber, status)
		}
		if requestNumber > 32 {
			t.Fatal("model diagnostics did not enforce their aggregate bound")
		}
	}
	var limit *analyzer.EvidenceLimitExceeded
	if !errors.As(diagnostics.Snapshot().Failure, &limit) {
		t.Fatalf("diagnostic overflow failure = %T %v, want EvidenceLimitExceeded", diagnostics.Snapshot().Failure, diagnostics.Snapshot().Failure)
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
		{route: blackbox.RouteInputTokens, method: http.MethodPost, path: "/responses/input_tokens", body: `{"input":[]}`},
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
		ID: uuid.New(), Route: blackbox.RouteResponses, Probe: &probe, Outcome: blackbox.OutcomeStream, Output: &output,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(stub.Close)

	transport := llm.NewHTTPTransport(staticTransportAuth{})
	transport.BaseURL = stub.URL()
	transport.Client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	var deltas []string
	response, err := transport.GenerateStream(context.Background(), llm.OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: llm.ToolChoiceModeAutomatic,
		Items:          llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: probe}}),
	}, func(delta string) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	if response.AssistantText != output {
		t.Fatalf("assistant text = %q, want %q", response.AssistantText, output)
	}
	if len(deltas) != 1 || deltas[0] != output {
		t.Fatalf("assistant deltas = %#v, want %q", deltas, output)
	}
	if err := stub.Verify(); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestResponsesStubServesCompactInputTokenAndModelMetadataTransportRoutes(t *testing.T) {
	t.Parallel()

	compact, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteCompact, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub compact: %v", err)
	}
	t.Cleanup(compact.Close)
	compactTransport := newStubTransport(compact)
	if _, err := compactTransport.Compact(context.Background(), llm.OpenAICompactionRequest{
		Model:      "gpt-5",
		InputItems: []llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "input"}},
	}); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if err := compact.Verify(); err != nil {
		t.Fatalf("Verify compact: %v", err)
	}

	inputTokens, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteInputTokens, Outcome: blackbox.OutcomeJSON,
	}})
	if err != nil {
		t.Fatalf("StartResponsesStub input_tokens: %v", err)
	}
	t.Cleanup(inputTokens.Close)
	count, err := newStubTransport(inputTokens).CountRequestInputTokens(context.Background(), llm.OpenAIRequest{
		Model:          "gpt-5",
		ToolChoiceMode: llm.ToolChoiceModeAutomatic,
		Items:          []llm.ResponseItem{{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "input"}},
	})
	if err != nil {
		t.Fatalf("CountRequestInputTokens: %v", err)
	}
	if count != 0 {
		t.Fatalf("input token count = %d, want 0", count)
	}
	if err := inputTokens.Verify(); err != nil {
		t.Fatalf("Verify input_tokens: %v", err)
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

func newStubTransport(stub *blackbox.ResponsesStub) *llm.HTTPTransport {
	transport := llm.NewHTTPTransport(staticTransportAuth{})
	transport.BaseURL = stub.URL()
	transport.Client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	return transport
}

type unknownLengthReader struct {
	io.Reader
}
