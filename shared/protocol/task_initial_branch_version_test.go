package protocol

import "testing"

func TestCurrentContractChangesProtocolVersion(t *testing.T) {
	if Version != "115" {
		t.Fatalf("current protocol version = %q, want 115", Version)
	}
}
