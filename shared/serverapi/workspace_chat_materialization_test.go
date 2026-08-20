package serverapi

import (
	"testing"

	"core/shared/runtimeids"
)

func TestWorkspaceChatMaterializationResponseRequiresCanonicalUUIDv4(t *testing.T) {
	canonical := runtimeids.NewSessionID()
	response := WorkspaceChatMaterializeResponse{SessionID: canonical}
	if err := response.Validate(); err != nil {
		t.Fatalf("validate canonical response: %v", err)
	}

	legacy, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("parse legacy Session identity: %v", err)
	}
	if err := (WorkspaceChatMaterializeResponse{SessionID: legacy}).Validate(); err == nil {
		t.Fatal("materialization response accepted a non-UUIDv4 Session identity")
	}
}

func TestWorkspaceChatDraftConsumeOperationIsRejected(t *testing.T) {
	if err := (WorkspaceChatDraftOperation{Kind: "consume"}).Validate(); err == nil {
		t.Fatal("workspace Chat draft accepted the removed consume operation")
	}
}
