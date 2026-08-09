package transport

import (
	"errors"
	"testing"

	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestWorkflowTaskMoveRPCPreservesNoOpBranchRejection(t *testing.T) {
	branchName := "feature/rpc-no-op"
	response := decodeAndHandle[serverapi.WorkflowTaskMoveRequest, serverapi.WorkflowTaskMoveResponse](
		protocol.Request{ID: "move-no-op-branch", Params: mustJSON(t, serverapi.WorkflowTaskMoveRequest{
			TaskID:           "task-rpc-no-op",
			TargetNodeID:     "node-current",
			SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
			BranchName:       &branchName,
		})},
		func(request serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error) {
			if request.BranchName == nil || *request.BranchName != branchName {
				t.Fatalf("Move request branch = %v, want %q", request.BranchName, branchName)
			}
			return serverapi.WorkflowTaskMoveResponse{}, &serverapi.WorkflowTaskInitialBranchError{
				Reason:     serverapi.WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree,
				BranchName: branchName,
			}
		},
	)

	if response.Error == nil || response.Error.Code != protocol.ErrCodeWorkflowTaskInitialBranch {
		t.Fatalf("Move RPC response = %+v, want initial-branch error", response)
	}
	decoded := serverapi.DecodeWorkflowTaskInitialBranchError(response.Error.Data, response.Error.Message)
	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(decoded, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree ||
		branchErr.BranchName != branchName {
		t.Fatalf("decoded error = %T %v, want no-op branch rejection", decoded, decoded)
	}
}

func TestWorkflowTaskMoveRPCPreservesNonExecutableBranchRejection(t *testing.T) {
	branchName := "feature/rpc-non-executable"
	response := decodeAndHandle[serverapi.WorkflowTaskMoveRequest, serverapi.WorkflowTaskMoveResponse](
		protocol.Request{ID: "move-non-executable-branch", Params: mustJSON(t, serverapi.WorkflowTaskMoveRequest{
			TaskID:           "task-rpc-non-executable",
			TargetNodeID:     "node-terminal",
			SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
			BranchName:       &branchName,
		})},
		func(request serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error) {
			if request.BranchName == nil || *request.BranchName != branchName {
				t.Fatalf("Move request branch = %v, want %q", request.BranchName, branchName)
			}
			return serverapi.WorkflowTaskMoveResponse{}, &serverapi.WorkflowTaskInitialBranchError{
				Reason:     serverapi.WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree,
				BranchName: branchName,
			}
		},
	)

	if response.Error == nil || response.Error.Code != protocol.ErrCodeWorkflowTaskInitialBranch {
		t.Fatalf("Move RPC response = %+v, want initial-branch error", response)
	}
	decoded := serverapi.DecodeWorkflowTaskInitialBranchError(response.Error.Data, response.Error.Message)
	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(decoded, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree ||
		branchErr.BranchName != branchName {
		t.Fatalf("decoded error = %T %v, want non-executable branch rejection", decoded, decoded)
	}
}

func TestWorkflowTaskMoveRPCPreservesFinalLocalBranchCollisionFacts(t *testing.T) {
	branchName := "feature/rpc-local-race"
	ref := "refs/heads/" + branchName
	response := decodeAndHandle[serverapi.WorkflowTaskMoveRequest, serverapi.WorkflowTaskMoveResponse](
		protocol.Request{ID: "move-local-branch-race", Params: mustJSON(t, serverapi.WorkflowTaskMoveRequest{
			TaskID:           "task-rpc-local-race",
			TargetNodeID:     "node-agent",
			SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
			BranchName:       &branchName,
		})},
		func(request serverapi.WorkflowTaskMoveRequest) (serverapi.WorkflowTaskMoveResponse, error) {
			if request.BranchName == nil || *request.BranchName != branchName {
				t.Fatalf("Move request branch = %v, want %q", request.BranchName, branchName)
			}
			return serverapi.WorkflowTaskMoveResponse{}, &serverapi.WorkflowTaskInitialBranchError{
				Reason:     serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision,
				BranchName: branchName,
				Ref:        &ref,
			}
		},
	)

	if response.Error == nil || response.Error.Code != protocol.ErrCodeWorkflowTaskInitialBranch {
		t.Fatalf("Move RPC response = %+v, want initial-branch error", response)
	}
	decoded := serverapi.DecodeWorkflowTaskInitialBranchError(response.Error.Data, response.Error.Message)
	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(decoded, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision ||
		branchErr.BranchName != branchName ||
		branchErr.Ref == nil ||
		*branchErr.Ref != ref {
		t.Fatalf("decoded error = %T %+v, want local collision for %q", decoded, decoded, ref)
	}
}
