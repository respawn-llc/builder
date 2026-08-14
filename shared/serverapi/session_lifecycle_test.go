package serverapi

import (
	"testing"
)

func TestSessionPersistInputDraftAcceptsComposerDraft(t *testing.T) {
	req := SessionPersistInputDraftRequest{
		SessionID: "session-1",
		Input:     "visible draft",
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
