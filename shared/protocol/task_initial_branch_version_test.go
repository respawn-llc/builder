package protocol

import "testing"

func TestCurrentContractChangesProtocolVersion(t *testing.T) {
	if Version != "130" {
		t.Fatalf("current protocol version = %q, want 130", Version)
	}
}
