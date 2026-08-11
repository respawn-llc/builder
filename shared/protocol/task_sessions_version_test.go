package protocol

import "testing"

func TestWorkflowTaskSessionsChangesProtocolVersion(t *testing.T) {
	if MethodWorkflowTaskSessionList == "" {
		t.Fatal("Workflow Task Session list method is required")
	}
	if Version != "116" {
		t.Fatalf("Workflow Task Session hard cutover protocol version = %q, want 116", Version)
	}
}
