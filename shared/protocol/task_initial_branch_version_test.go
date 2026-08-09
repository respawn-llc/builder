package protocol

import "testing"

func TestWorkflowTaskInitialBranchChangesProtocolVersion(t *testing.T) {
	if ErrCodeWorkflowTaskInitialBranch == 0 {
		t.Fatal("workflow task initial branch RPC error code is required")
	}
	if Version == "101" {
		t.Fatal("workflow task initial branch retained the pre-contract protocol version")
	}
}
