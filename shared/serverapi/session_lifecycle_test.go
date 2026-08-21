package serverapi

import (
	"encoding/json"
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

func TestSessionRetargetWorkspaceRequestUsesScheduledExecutionIdentity(t *testing.T) {
	operationID := NewWorktreeOperationID()
	origin := &RuntimeStepOrigin{
		RunID:  "018fdd67-89ab-4cde-8123-456789abc001",
		StepID: "018fdd67-89ab-4cde-8123-456789abc002",
	}
	request := SessionRetargetWorkspaceRequest{
		WorktreeTransitionHeader: WorktreeTransitionHeader{
			OperationID: operationID,
			SessionID:   "session-1",
			Origin:      origin,
		},
		WorkspaceRoot:  "/workspace",
		CompletionMode: SessionRetargetCompletionScheduled,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"operation_id", "session_id", "origin", "workspace_root", "completion_mode"} {
		if decoded[field] == nil {
			t.Fatalf("request omitted %s: %s", field, data)
		}
	}
	if decoded["client_request_id"] != nil {
		t.Fatalf("request retained client_request_id: %s", data)
	}
}

func TestSessionRetargetWorkspaceRequestRequiresTypedCompletionMode(t *testing.T) {
	operationID := NewWorktreeOperationID()
	validHeader := WorktreeTransitionHeader{OperationID: operationID, SessionID: "session-1"}
	for _, mode := range []SessionRetargetCompletionMode{
		SessionRetargetCompletionScheduled,
		SessionRetargetCompletionWait,
	} {
		request := SessionRetargetWorkspaceRequest{
			WorktreeTransitionHeader: validHeader,
			WorkspaceRoot:            "/workspace",
			CompletionMode:           mode,
		}
		if err := request.Validate(); err != nil {
			t.Fatalf("completion mode %q rejected: %v", mode, err)
		}
	}
	for _, mode := range []SessionRetargetCompletionMode{"", "later"} {
		request := SessionRetargetWorkspaceRequest{
			WorktreeTransitionHeader: validHeader,
			WorkspaceRoot:            "/workspace",
			CompletionMode:           mode,
		}
		if err := request.Validate(); err == nil {
			t.Fatalf("completion mode %q validated", mode)
		}
	}
}

func TestSessionRetargetWorkspaceResponseCarriesAcknowledgementAndOptionalOutcome(t *testing.T) {
	operationID := NewWorktreeOperationID()
	ack := WorktreeScheduledAcknowledgement{OperationID: operationID}
	scheduled := SessionRetargetWorkspaceResponse{Acknowledgement: ack}
	if err := scheduled.Validate(); err != nil {
		t.Fatalf("scheduled response rejected: %v", err)
	}
	if err := scheduled.ValidateForCompletionMode(SessionRetargetCompletionScheduled); err != nil {
		t.Fatalf("scheduled response rejected for scheduled mode: %v", err)
	}
	completed := SessionRetargetWorkspaceResponse{
		Acknowledgement: ack,
		Outcome: &SessionRetargetOutcome{
			OperationID: operationID,
			Kind:        SessionRetargetOutcomeSucceeded,
			Success: &SessionRetargetSuccess{
				Binding: ProjectBinding{
					ProjectID:     "project-1",
					ProjectName:   "Project",
					WorkspaceID:   "workspace-1",
					CanonicalRoot: "/workspace",
				},
			},
		},
	}
	if err := completed.Validate(); err != nil {
		t.Fatalf("completed response rejected: %v", err)
	}
	if err := completed.ValidateForCompletionMode(SessionRetargetCompletionWait); err != nil {
		t.Fatalf("completed response rejected for wait mode: %v", err)
	}
	if err := scheduled.ValidateForCompletionMode(SessionRetargetCompletionWait); err == nil {
		t.Fatal("wait mode accepted a response without an outcome")
	}
	if err := completed.ValidateForCompletionMode(SessionRetargetCompletionScheduled); err == nil {
		t.Fatal("scheduled mode accepted an inline outcome")
	}
	completed.Outcome.OperationID = NewWorktreeOperationID()
	if err := completed.Validate(); err == nil {
		t.Fatal("response accepted an outcome for another operation")
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
	if err := success.Validate(); err != nil {
		t.Fatalf("success outcome rejected: %v", err)
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
	if err := failure.Validate(); err != nil {
		t.Fatalf("failure outcome rejected: %v", err)
	}
	for _, invalid := range []SessionRetargetOutcome{
		{OperationID: operationID, Kind: SessionRetargetOutcomeSucceeded},
		{OperationID: operationID, Kind: SessionRetargetOutcomeFailed},
		{OperationID: operationID, Kind: SessionRetargetOutcomeSucceeded, Success: success.Success, Failure: failure.Failure},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid outcome validated: %+v", invalid)
		}
	}
}
