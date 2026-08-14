package protocol

import "testing"

func TestCurrentContractChangesProtocolVersion(t *testing.T) {
	if Version != "118" {
		t.Fatalf("current protocol version = %q, want 118", Version)
	}
}
