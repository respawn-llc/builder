package serverapi

import "testing"

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
		Scheduled: &WorktreeScheduledAcknowledgement{OperationID: NewWorktreeOperationID()},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}
