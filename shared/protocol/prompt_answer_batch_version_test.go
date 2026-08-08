package protocol

import "testing"

func TestPromptAnswerBatchChangesProtocolVersion(t *testing.T) {
	if MethodPromptAnswerBatch == "" {
		t.Fatal("prompt answer batch method is required")
	}
	if Version == "95" {
		t.Fatal("prompt answer batch retained the pre-contract protocol version")
	}
}
