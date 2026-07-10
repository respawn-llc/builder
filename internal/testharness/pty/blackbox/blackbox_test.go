package blackbox_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"core/internal/testharness/pty/blackbox"

	"github.com/google/uuid"
)

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
	deadline := time.After(time.Second)
	for hold.Snapshot().ActiveRequests == 0 {
		select {
		case <-deadline:
			t.Fatal("held SSE did not become active")
		case <-time.After(time.Millisecond):
		}
	}
	hold.Close()
	select {
	case <-responseDone:
	case <-time.After(time.Second):
		t.Fatal("held SSE request did not unblock on stub close")
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
