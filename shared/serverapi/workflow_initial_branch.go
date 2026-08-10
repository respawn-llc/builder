package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/protocol"
)

type WorkflowTaskInitialBranchErrorReason string

const (
	WorkflowTaskInitialBranchErrorReasonInvalidName                   WorkflowTaskInitialBranchErrorReason = "invalid_name"
	WorkflowTaskInitialBranchErrorReasonLocalCollision                WorkflowTaskInitialBranchErrorReason = "local_collision"
	WorkflowTaskInitialBranchErrorReasonRemoteTrackingCollision       WorkflowTaskInitialBranchErrorReason = "remote_tracking_collision"
	WorkflowTaskInitialBranchErrorReasonNoManagedTarget               WorkflowTaskInitialBranchErrorReason = "no_managed_target"
	WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree WorkflowTaskInitialBranchErrorReason = "operation_cannot_create_worktree"
	WorkflowTaskInitialBranchErrorReasonPostCreationMismatch          WorkflowTaskInitialBranchErrorReason = "post_creation_mismatch"
)

type WorkflowTaskInitialBranchError struct {
	Reason             WorkflowTaskInitialBranchErrorReason
	BranchName         string
	Ref                *string
	Remote             *string
	ExistingBranchName *string
}

func (e *WorkflowTaskInitialBranchError) Error() string {
	if e == nil {
		return "workflow task initial branch failed"
	}
	return fmt.Sprintf("workflow task initial branch %q failed: %s", e.BranchName, e.Reason)
}

func (e *WorkflowTaskInitialBranchError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowTaskInitialBranch
}

func (e *WorkflowTaskInitialBranchError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type               string                               `json:"type"`
		Reason             WorkflowTaskInitialBranchErrorReason `json:"reason"`
		BranchName         string                               `json:"branch_name"`
		Ref                *string                              `json:"ref,omitempty"`
		Remote             *string                              `json:"remote,omitempty"`
		ExistingBranchName *string                              `json:"existing_branch_name,omitempty"`
	}{
		Type:               "workflow_task_initial_branch_error",
		Reason:             e.Reason,
		BranchName:         e.BranchName,
		Ref:                e.Ref,
		Remote:             e.Remote,
		ExistingBranchName: e.ExistingBranchName,
	})
}

func (e *WorkflowTaskInitialBranchError) Validate() error {
	if e == nil {
		return errors.New("workflow task initial branch error is required")
	}
	if strings.TrimSpace(e.BranchName) == "" {
		return errors.New("workflow task initial branch error branch name is required")
	}
	switch e.Reason {
	case WorkflowTaskInitialBranchErrorReasonInvalidName,
		WorkflowTaskInitialBranchErrorReasonNoManagedTarget,
		WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree:
		if e.Ref != nil || e.Remote != nil || e.ExistingBranchName != nil {
			return errors.New("workflow task initial branch error has inapplicable facts")
		}
	case WorkflowTaskInitialBranchErrorReasonLocalCollision:
		if !validInitialBranchErrorString(e.Ref) || e.Remote != nil || e.ExistingBranchName != nil {
			return errors.New("workflow task local branch collision facts are invalid")
		}
	case WorkflowTaskInitialBranchErrorReasonRemoteTrackingCollision:
		if !validInitialBranchErrorString(e.Ref) || !validInitialBranchErrorString(e.Remote) || e.ExistingBranchName != nil {
			return errors.New("workflow task remote-tracking branch collision facts are invalid")
		}
	case WorkflowTaskInitialBranchErrorReasonPostCreationMismatch:
		if !validInitialBranchErrorString(e.Ref) || e.Remote != nil || !validInitialBranchErrorString(e.ExistingBranchName) {
			return errors.New("workflow task post-creation branch mismatch facts are invalid")
		}
	default:
		return errors.New("workflow task initial branch error reason is invalid")
	}
	return nil
}

func DecodeWorkflowTaskInitialBranchError(data json.RawMessage, message string) error {
	var envelope struct {
		Type               string                               `json:"type"`
		Reason             WorkflowTaskInitialBranchErrorReason `json:"reason"`
		BranchName         string                               `json:"branch_name"`
		Ref                *string                              `json:"ref,omitempty"`
		Remote             *string                              `json:"remote,omitempty"`
		ExistingBranchName *string                              `json:"existing_branch_name,omitempty"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Type != "workflow_task_initial_branch_error" {
		return errors.New(strings.TrimSpace(message))
	}
	result := &WorkflowTaskInitialBranchError{
		Reason:             envelope.Reason,
		BranchName:         envelope.BranchName,
		Ref:                envelope.Ref,
		Remote:             envelope.Remote,
		ExistingBranchName: envelope.ExistingBranchName,
	}
	if err := result.Validate(); err != nil {
		return errors.New(strings.TrimSpace(message))
	}
	return result
}

func validInitialBranchErrorString(value *string) bool {
	return value != nil && strings.TrimSpace(*value) != ""
}
