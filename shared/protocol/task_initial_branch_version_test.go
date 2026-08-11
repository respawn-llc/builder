package protocol

import "testing"

func TestCurrentContractChangesProtocolVersion(t *testing.T) {
	if Version != "114" {
		t.Fatalf("current protocol version = %q, want 114", Version)
	}
}
