package protocol

import "testing"

func TestProtocolVersionIncludesTargetedRuntimeInterrupts(t *testing.T) {
	if Version != "28" {
		t.Fatalf("protocol version = %q, want 28 for targeted runtime interrupt operation refs", Version)
	}
}
