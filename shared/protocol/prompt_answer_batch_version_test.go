package protocol

import "testing"

func TestPromptAnswerBatchChangesProtocolVersion(t *testing.T) {
	if MethodPromptAnswerBatch == "" {
		t.Fatal("prompt answer batch method is required")
	}
	if Version != "99" {
		t.Fatalf("prompt answer hard cutover protocol version = %q, want 99", Version)
	}
}
