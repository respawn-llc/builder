package transport

import (
	"testing"

	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/protocol"
	"core/shared/serverapi"
)

func TestWorkflowTaskResumeRPCForwardsExplicitBranchWhenEligibilityReturnsConflict(t *testing.T) {
	taskID := workflow.TaskID("task-resume-rpc-conflict")
	branchName := "feature/rpc-conflict"
	source := &workflowexecution.TaskResumeConflictError{TaskID: taskID}
	response := decodeAndHandle[serverapi.WorkflowTaskResumeRequest, serverapi.WorkflowTaskResumeResponse](
		protocol.Request{ID: "resume-conflict", Params: mustJSON(t, serverapi.WorkflowTaskResumeRequest{
			TaskID:           string(taskID),
			SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
			BranchName:       &branchName,
		})},
		func(request serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
			if request.BranchName == nil || *request.BranchName != branchName {
				t.Fatalf("Resume request branch = %v, want %q", request.BranchName, branchName)
			}
			return serverapi.WorkflowTaskResumeResponse{}, source
		},
	)

	if response.Error == nil ||
		response.Error.Code != protocol.ErrCodeInternalError ||
		len(response.Result) != 0 {
		t.Fatalf("Resume RPC response = %+v, want eligibility conflict", response)
	}
}

func TestWorkflowTaskResumeRPCForwardsExplicitBranchWhenEligibilityReturnsValidationError(t *testing.T) {
	taskID := workflow.TaskID("task-resume-rpc-invalid")
	branchName := "feature/rpc-invalid"
	reference, err := workflow.NewCurrentNodeReference(taskID, workflow.NodeID("node-review"), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	source := &workflowstore.CurrentNodeResumeValidationError{
		Diagnostics: []workflowstore.CurrentNodeResumeValidationDiagnostic{{
			Code:           workflowstore.CurrentNodeResumeParameterNotMaterializedCode,
			CurrentNode:    reference,
			EnteringEdgeID: workflow.EdgeID("edge-review"),
			ParameterKey:   "reviewer",
		}},
	}
	response := decodeAndHandle[serverapi.WorkflowTaskResumeRequest, serverapi.WorkflowTaskResumeResponse](
		protocol.Request{ID: "resume-invalid", Params: mustJSON(t, serverapi.WorkflowTaskResumeRequest{
			TaskID:           string(taskID),
			SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
			BranchName:       &branchName,
		})},
		func(request serverapi.WorkflowTaskResumeRequest) (serverapi.WorkflowTaskResumeResponse, error) {
			if request.BranchName == nil || *request.BranchName != branchName {
				t.Fatalf("Resume request branch = %v, want %q", request.BranchName, branchName)
			}
			return serverapi.WorkflowTaskResumeResponse{}, source
		},
	)

	if response.Error == nil ||
		response.Error.Code != protocol.ErrCodeInternalError ||
		len(response.Result) != 0 {
		t.Fatalf("Resume RPC response = %+v, want eligibility validation error", response)
	}
}
