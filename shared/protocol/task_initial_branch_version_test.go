package protocol

import "testing"

func TestWorkflowTaskInitialBranchChangesProtocolVersion(t *testing.T) {
	if Version != "105" {
		t.Fatalf("workflow task initial branch protocol version = %q, want 105 after merging the version 102 and version 104 contract histories", Version)
	}
}
