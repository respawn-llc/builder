package serverapi

import (
	"errors"
	"testing"

	"core/shared/clientui"
)

func TestSessionPersistInputDraftAcceptsStructuredRecoveryBuffers(t *testing.T) {
	req := SessionPersistInputDraftRequest{
		ClientRequestID: "draft-1",
		SessionID:       "session-1",
		Input:           "visible draft",
		RecoveryBuffers: []SessionDraftRecoveryBuffer{{
			Kind: SessionDraftRecoveryBufferActiveSubmit,
			Text: "submitted before forced exit",
			OperationRef: clientui.RuntimeOperationRef{
				Kind:            clientui.RuntimeOperationKindSubmit,
				ClientRequestID: "submit-1",
			},
		}},
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestSessionPersistInputDraftRejectsInvalidRecoveryBuffer(t *testing.T) {
	for _, req := range []SessionPersistInputDraftRequest{
		{ClientRequestID: "draft-1", SessionID: "session-1", RecoveryBuffers: []SessionDraftRecoveryBuffer{{Text: "missing kind"}}},
		{ClientRequestID: "draft-1", SessionID: "session-1", RecoveryBuffers: []SessionDraftRecoveryBuffer{{Kind: SessionDraftRecoveryBufferQueuedInput}}},
	} {
		if err := req.Validate(); err == nil {
			t.Fatalf("Validate(%+v) = nil, want error", req)
		}
	}
}

func TestSessionPersistInputDraftStillRequiresClientRequestID(t *testing.T) {
	err := (SessionPersistInputDraftRequest{SessionID: "session-1"}).Validate()
	if !errors.Is(err, ErrClientRequestIDRequired) {
		t.Fatalf("Validate error = %v, want ErrClientRequestIDRequired", err)
	}
}
