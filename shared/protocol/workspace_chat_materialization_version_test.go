package protocol

import "testing"

func TestWorkspaceChatMaterializationChangesProtocolVersion(t *testing.T) {
	if MethodSessionWorkspaceChatMaterialize == "" {
		t.Fatal("workspace Chat materialization method is required")
	}
	if Version != "116" {
		t.Fatalf("workspace Chat materialization protocol version = %q, want 116", Version)
	}
}
