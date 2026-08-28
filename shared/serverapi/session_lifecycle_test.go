package serverapi

import (
	"testing"

	"core/shared/worktreecontract"
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
func TestSessionRetargetWorkspaceResponseAcceptsScheduledAcknowledgement(t *testing.T) {
	response := SessionRetargetWorkspaceResponse{
		Scheduled: &SessionWorkspaceRetargetScheduledAcknowledgement{OperationID: worktreecontract.NewOperationID()},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
