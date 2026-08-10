package serverapi

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"core/shared/protocol"
)

func TestWorkflowTaskInitialBranchRequestContract(t *testing.T) {
	exact := " feature/MBL-742 "
	blank := " \t "
	requests := []struct {
		name      string
		request   interface{ Validate() error }
		roundTrip func([]byte) *string
	}{
		{
			name: "start",
			request: WorkflowTaskStartRequest{
				TaskID: "task", SetupOperationID: NewWorktreeSetupOperationID(), BranchName: &exact,
			},
			roundTrip: func(data []byte) *string {
				var decoded WorkflowTaskStartRequest
				if err := json.Unmarshal(data, &decoded); err != nil {
					t.Fatalf("unmarshal start request: %v", err)
				}
				return decoded.BranchName
			},
		},
		{
			name: "move",
			request: WorkflowTaskMoveRequest{
				TaskID: "task", TargetNodeID: "node", BranchName: &exact,
			},
			roundTrip: func(data []byte) *string {
				var decoded WorkflowTaskMoveRequest
				if err := json.Unmarshal(data, &decoded); err != nil {
					t.Fatalf("unmarshal move request: %v", err)
				}
				return decoded.BranchName
			},
		},
		{
			name: "resume",
			request: WorkflowTaskResumeRequest{
				TaskID: "task", SetupOperationID: NewWorktreeSetupOperationID(), BranchName: &exact,
			},
			roundTrip: func(data []byte) *string {
				var decoded WorkflowTaskResumeRequest
				if err := json.Unmarshal(data, &decoded); err != nil {
					t.Fatalf("unmarshal resume request: %v", err)
				}
				return decoded.BranchName
			},
		},
	}

	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.request.Validate(); err != nil {
				t.Fatalf("exact branch request rejected: %v", err)
			}
			data, err := json.Marshal(test.request)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			decoded := test.roundTrip(data)
			if decoded == nil || *decoded != exact {
				t.Fatalf("round-tripped branch name = %+v, want exact %q", decoded, exact)
			}
		})
	}

	blankRequests := []interface{ Validate() error }{
		WorkflowTaskStartRequest{TaskID: "task", SetupOperationID: NewWorktreeSetupOperationID(), BranchName: &blank},
		WorkflowTaskMoveRequest{TaskID: "task", TargetNodeID: "node", BranchName: &blank},
		WorkflowTaskResumeRequest{TaskID: "task", SetupOperationID: NewWorktreeSetupOperationID(), BranchName: &blank},
	}
	for _, request := range blankRequests {
		if err := request.Validate(); err == nil {
			t.Fatalf("%T accepted explicitly blank branch_name", request)
		}
	}

	omitted, err := json.Marshal(WorkflowTaskStartRequest{
		TaskID: "task", SetupOperationID: NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("marshal omitted branch request: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(omitted, &fields); err != nil {
		t.Fatalf("unmarshal omitted branch request: %v", err)
	}
	if _, present := fields["branch_name"]; present {
		t.Fatalf("omitted branch_name encoded: %s", omitted)
	}

	if _, exists := reflect.TypeOf(WorkflowTaskApproveRequest{}).FieldByName("BranchName"); exists {
		t.Fatal("WorkflowTaskApproveRequest exposes branch_name")
	}
}

func TestWorkflowTaskInitialBranchErrorRoundTripsStructuredFacts(t *testing.T) {
	localRef := "refs/heads/feature/MBL-742"
	remoteRef := "refs/remotes/upstream/feature/MBL-742"
	remote := "upstream"
	existingBranch := "feature/existing"
	existingRef := "refs/heads/feature/existing"
	tests := []WorkflowTaskInitialBranchError{
		{Reason: WorkflowTaskInitialBranchErrorReasonInvalidName, BranchName: "feature invalid"},
		{Reason: WorkflowTaskInitialBranchErrorReasonLocalCollision, BranchName: "feature/MBL-742", Ref: &localRef},
		{Reason: WorkflowTaskInitialBranchErrorReasonRemoteTrackingCollision, BranchName: "feature/MBL-742", Ref: &remoteRef, Remote: &remote},
		{Reason: WorkflowTaskInitialBranchErrorReasonNoManagedTarget, BranchName: "feature/MBL-742"},
		{Reason: WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree, BranchName: "feature/MBL-742"},
		{Reason: WorkflowTaskInitialBranchErrorReasonPostCreationMismatch, BranchName: "feature/MBL-742", ExistingBranchName: &existingBranch, Ref: &existingRef},
	}

	for _, source := range tests {
		t.Run(string(source.Reason), func(t *testing.T) {
			if err := source.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if source.RPCErrorCode() != protocol.ErrCodeWorkflowTaskInitialBranch {
				t.Fatalf("RPC error code = %d, want %d", source.RPCErrorCode(), protocol.ErrCodeWorkflowTaskInitialBranch)
			}
			decoded := DecodeWorkflowTaskInitialBranchError(source.RPCErrorData(), source.Error())
			var typed *WorkflowTaskInitialBranchError
			if !errors.As(decoded, &typed) {
				t.Fatalf("decoded error = %T %v, want WorkflowTaskInitialBranchError", decoded, decoded)
			}
			if !reflect.DeepEqual(*typed, source) {
				t.Fatalf("decoded error = %+v, want %+v", *typed, source)
			}
		})
	}
}
