package protocol

import "testing"

func TestWorkspaceChatMaterializationChangesProtocolVersion(t *testing.T) {
	if MethodSessionWorkspaceChatMaterialize == "" {
		t.Fatal("workspace Chat materialization method is required")
	}
	if Version != "117" {
		t.Fatalf("workspace Chat materialization protocol version = %q, want 117", Version)
	}
}
