package protocol

import "testing"

func TestWorkflowTaskInitialBranchChangesProtocolVersion(t *testing.T) {
	if Version != "106" {
		t.Fatalf("workflow task initial branch protocol version = %q, want 106 after the KENT-345 wire-contract cut", Version)
	}
}
