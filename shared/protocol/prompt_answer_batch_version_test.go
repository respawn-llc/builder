package protocol

import "testing"

func TestPromptAnswerBatchChangesProtocolVersion(t *testing.T) {
	if MethodPromptAnswerBatch == "" {
		t.Fatal("prompt answer batch method is required")
	}
	if Version != "116" {
		t.Fatalf("prompt answer hard cutover protocol version = %q, want 116", Version)
	}
}

func TestGoalMutationResponseChangesProtocolVersion(t *testing.T) {
	if Version != "116" {
		t.Fatalf("Goal hard cutover protocol version = %q, want 116", Version)
	}
}
