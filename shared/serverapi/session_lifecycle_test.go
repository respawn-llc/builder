package serverapi

import (
	"errors"
	"testing"
)

func TestSessionPersistInputDraftAcceptsComposerDraft(t *testing.T) {
	req := SessionPersistInputDraftRequest{
		ClientRequestID: "draft-1",
		SessionID:       "session-1",
		Input:           "visible draft",
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestSessionPersistInputDraftStillRequiresClientRequestID(t *testing.T) {
	err := (SessionPersistInputDraftRequest{SessionID: "session-1"}).Validate()
	if !errors.Is(err, ErrClientRequestIDRequired) {
		t.Fatalf("Validate error = %v, want ErrClientRequestIDRequired", err)
	}
}
