package protocol

import "testing"

func TestSessionRuntimeSettingsChangesProtocolVersion(t *testing.T) {
	if Version != "118" {
		t.Fatalf("Session runtime settings protocol version = %q, want 118", Version)
	}
}
