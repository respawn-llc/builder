package protocol

import "testing"

func TestManualMoveOptionalDescriptionChangesProtocolVersion(t *testing.T) {
	if Version != "102" {
		t.Fatalf("manual move optional description requires protocol version 102, got %s", Version)
	}
}
