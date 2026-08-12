package protocol

import "testing"

func TestSessionRuntimeSettingsChangesProtocolVersion(t *testing.T) {
	if Version == "119" {
		t.Fatalf("Session runtime settings retained the pre-contract protocol version")
	}
}
