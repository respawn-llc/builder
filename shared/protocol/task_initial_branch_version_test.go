package protocol

import "testing"

func TestCurrentContractChangesProtocolVersion(t *testing.T) {
	if Version != "116" {
		t.Fatalf("current protocol version = %q, want 116", Version)
	}
}
