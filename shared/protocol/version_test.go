package protocol

import "testing"

func TestProtocolVersionIncludesWorkflowPromptCommentaryParameter(t *testing.T) {
	if Version != "29" {
		t.Fatalf("protocol version = %q, want 29 for workflow prompt commentary parameter", Version)
	}
}
