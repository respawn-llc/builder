package protocol

import "testing"

func TestCurrentContractChangesProtocolVersion(t *testing.T) {
	if Version != "122" {
		t.Fatalf("current protocol version = %q, want 122", Version)
	}
}
