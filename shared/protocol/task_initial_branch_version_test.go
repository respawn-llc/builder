package protocol

import "testing"

func TestWorkflowTaskInitialBranchChangesProtocolVersion(t *testing.T) {
	if Version != "102" {
		t.Fatalf("workflow task initial branch protocol version = %q, want 102 after the version 101 contract", Version)
	}
}
