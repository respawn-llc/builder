package protocol

import "testing"

func TestCurrentContractChangesProtocolVersion(t *testing.T) {
	if Version != "106" {
		t.Fatalf("current protocol version = %q, want 106", Version)
	}
}
