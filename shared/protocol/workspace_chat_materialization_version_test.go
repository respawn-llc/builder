package protocol

import "testing"

func TestWorkspaceChatMaterializationChangesProtocolVersion(t *testing.T) {
	if MethodSessionWorkspaceChatMaterialize == "" {
		t.Fatal("workspace Chat materialization method is required")
	}
	if Version == "119" {
		t.Fatalf("workspace Chat materialization retained the pre-contract protocol version")
	}
}
