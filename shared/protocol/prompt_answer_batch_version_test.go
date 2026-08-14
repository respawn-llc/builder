package protocol

import "testing"

func TestPromptAnswerBatchChangesProtocolVersion(t *testing.T) {
	if MethodPromptAnswerBatch == "" {
		t.Fatal("prompt answer batch method is required")
	}
	if Version != "118" {
		t.Fatalf("active contract protocol version = %q, want 118", Version)
	}
}
