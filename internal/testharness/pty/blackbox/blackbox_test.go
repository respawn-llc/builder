package blackbox_test

import (
	"bytes"
	"context"
	"net/http"
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

	unconsumed, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{ID: uuid.New(), Route: blackbox.RouteResponses}})
	if err != nil {
		t.Fatalf("Start unconsumed stub: %v", err)
	}
	t.Cleanup(unconsumed.Close)
	if err := unconsumed.Verify(); err == nil {
		t.Fatal("Verify accepted unconsumed operation")
	}
}

func TestResponsesStubRejectsProbeMismatch(t *testing.T) {
	t.Parallel()

	probe := uuid.New().String()
	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Probe: &probe,
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

func TestResponsesStubStreamsRequiredOperationToHTTPTransport(t *testing.T) {
	t.Parallel()

	probe := uuid.New().String()
	output := "ok"
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
		Model: "gpt-5",
		Items: llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleUser, Content: probe}}),
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
