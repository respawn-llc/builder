package blackbox_test

import (
	"bytes"
	"net/http"
	"testing"

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
}

func TestResponsesStubConsumesTypedProbeAndRejectsUnconsumedQueue(t *testing.T) {
	t.Parallel()

	probe := uuid.New().String()
	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID:     uuid.New(),
		Route:  blackbox.RouteResponses,
		Probe:  probe,
		Stream: true,
		Output: "ok",
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

	stub, err := blackbox.StartResponsesStub([]blackbox.RequiredOperation{{
		ID: uuid.New(), Route: blackbox.RouteResponses, Probe: uuid.New().String(),
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
