package protocol

import (
	"strconv"
	"testing"
)

func TestSessionRuntimeSettingsChangesProtocolVersion(t *testing.T) {
	version, err := strconv.Atoi(Version)
	if err != nil || version < 119 {
		t.Fatalf("Session runtime settings protocol version = %q, want at least 119", Version)
	}
}
