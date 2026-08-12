package protocol

import "testing"

func TestSessionRuntimeSettingsChangesProtocolVersion(t *testing.T) {
	if Version != "120" {
		t.Fatalf("Session runtime settings protocol version = %q, want 120", Version)
	}
}
