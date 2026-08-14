package protocol

import "testing"

func TestCurrentContractChangesProtocolVersion(t *testing.T) {
	if Version != "117" {
		t.Fatalf("current protocol version = %q, want 117", Version)
	}
}
