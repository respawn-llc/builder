package protocol

import "testing"

func TestCurrentContractChangesProtocolVersion(t *testing.T) {
	if Version != "120" {
		t.Fatalf("current protocol version = %q, want 120", Version)
	}
}
