package protocol

import "testing"

func TestWorkflowTaskInitialBranchChangesProtocolVersion(t *testing.T) {
	if Version != "111" {
		t.Fatalf("workflow task initial branch protocol version = %q, want 111 after merging the setup-recovery and initial-branch contract histories", Version)
	}
}
