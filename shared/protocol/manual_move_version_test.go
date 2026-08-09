package protocol

import "testing"

func TestManualMoveOptionalDescriptionChangesProtocolVersion(t *testing.T) {
	if Version == "101" {
		t.Fatalf("manual move optional description retained the pre-contract protocol version")
	}
}
