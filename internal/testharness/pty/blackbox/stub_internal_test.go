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

func TestParseSessionCacheKeyUsesExactSegmentDiscriminators(t *testing.T) {
	const sessionID = "018fdd67-89ab-4cde-8123-456789abcdef"

	key, err := parseSessionCacheKey(sessionID + "/prompt-contract-3/contract-1/compact-3")
	if err != nil {
		t.Fatalf("parse versioned session cache key: %v", err)
	}
	if key.Compaction == nil || *key.Compaction != 3 {
		t.Fatalf("parsed cache key = %+v, want compact-3", key)
	}

	if _, err := parseSessionCacheKey(sessionID + "/prompt-contract-3-extra"); err == nil {
		t.Fatal("parse accepted a cache-key segment with an unmatched discriminator")
	}
}
