package protocol

import "testing"

func TestCurrentContractChangesProtocolVersion(t *testing.T) {
	if Version != "121" {
		t.Fatalf("current protocol version = %q, want 121", Version)
	}
}
