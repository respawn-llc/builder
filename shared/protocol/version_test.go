package protocol

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestProtocolVersionLoadsFromEmbeddedDefinition(t *testing.T) {
	var definition struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(versionDefinition, &definition); err != nil {
		t.Fatalf("unmarshal embedded protocol version definition: %v", err)
	}
	if Version != strings.TrimSpace(definition.Version) {
		t.Fatalf("protocol version = %q, want embedded definition %q", Version, definition.Version)
	}
	parsed, err := strconv.ParseUint(Version, 10, 64)
	if err != nil {
		t.Fatalf("protocol version = %q, want positive integer string: %v", Version, err)
	}
	if parsed == 0 {
		t.Fatalf("protocol version = %q, want positive integer string", Version)
	}
}
