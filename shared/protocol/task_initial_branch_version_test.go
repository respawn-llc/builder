package protocol

import "testing"

func TestWorkflowTaskInitialBranchChangesProtocolVersion(t *testing.T) {
	if Version == "101" {
		t.Fatal("workflow task initial branch retained the pre-contract protocol version")
	}
}
