package protocol

import "testing"

func TestWorkflowTaskInitialBranchChangesProtocolVersion(t *testing.T) {
	if Version != "98" {
		t.Fatalf("workflow task initial branch protocol version = %q, want 98 after the version 97 contract", Version)
	}
}
