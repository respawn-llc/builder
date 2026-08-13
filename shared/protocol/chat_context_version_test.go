package protocol

import "testing"

func TestChatContextContractProtocolVersion(t *testing.T) {
	if Version != "123" {
		t.Fatalf("protocol version = %q, want 123", Version)
	}
}
