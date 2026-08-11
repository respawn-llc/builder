package protocol

import "testing"

func TestCurrentContractChangesProtocolVersion(t *testing.T) {
	if Version != "113" {
		t.Fatalf("current protocol version = %q, want 113", Version)
	}
}
