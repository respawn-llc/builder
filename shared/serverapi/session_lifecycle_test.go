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

func TestSessionRetargetWorkspaceRequestRequiresTypedCompletionMode(t *testing.T) {
	request := SessionRetargetWorkspaceRequest{
		WorktreeTransitionHeader: WorktreeTransitionHeader{OperationID: NewWorktreeOperationID(), SessionID: "session-1"},
		WorkspaceRoot:            "/workspace",
		CompletionMode:           SessionRetargetCompletionScheduled,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	request.CompletionMode = ""
	if err := request.Validate(); err == nil {
		t.Fatal("missing completion mode validated")
	}
}

func TestSessionRetargetOutcomeDiscriminatesSuccessAndFailure(t *testing.T) {
	operationID := NewWorktreeOperationID()
	success := SessionRetargetOutcome{
		OperationID: operationID,
		Kind:        SessionRetargetOutcomeSucceeded,
		Success: &SessionRetargetSuccess{
			Binding: ProjectBinding{
				ProjectID:     "project-1",
				ProjectName:   "Project",
				WorkspaceID:   "workspace-1",
				CanonicalRoot: "/workspace",
			},
			WorkspaceBindingCreated: true,
		},
	}
	failure := SessionRetargetOutcome{
		OperationID: operationID,
		Kind:        SessionRetargetOutcomeFailed,
		Failure: &SessionRetargetFailure{
			Diagnostic:                "move failed",
			UnchangedProject:          ProjectReference{ID: "project-1", Name: "Project"},
			UnchangedWorkingDirectory: "/workspace",
		},
	}
	for _, valid := range []SessionRetargetOutcome{success, failure} {
		if err := valid.Validate(); err != nil {
			t.Fatalf("valid outcome rejected: %+v: %v", valid, err)
		}
	}
}
