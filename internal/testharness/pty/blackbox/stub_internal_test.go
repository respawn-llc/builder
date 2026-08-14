package blackbox

import (
	"context"
	"testing"
	"time"
)

func TestResponsesStubReportsUnexpectedServeFailure(t *testing.T) {
	stub, err := StartResponsesStub(nil)
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	t.Cleanup(func() {
		if err := stub.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	if err := stub.listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	select {
	case <-stub.Done():
	case <-time.After(time.Second):
		t.Fatal("stub did not stop after listener failure")
	}
	if err := stub.Verify(); err == nil {
		t.Fatal("Verify accepted unexpected server failure")
	}
}

func TestResponsesStubDoneWaitsForActiveHandlerCompletion(t *testing.T) {
	stub, err := StartResponsesStub(nil)
	if err != nil {
		t.Fatalf("StartResponsesStub: %v", err)
	}
	_, done, admitted := stub.beginHandler(context.Background())
	if !admitted {
		t.Fatal("beginHandler was not admitted")
	}
	if err := stub.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-stub.Done():
		t.Fatal("Done closed while active handler remained")
	case <-time.After(20 * time.Millisecond):
	}
	done()
	select {
	case <-stub.Done():
	case <-time.After(time.Second):
		t.Fatal("Done did not close after handler completion")
	}
}
