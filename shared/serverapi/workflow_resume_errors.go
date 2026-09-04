package serverapi

import (
	"encoding/json"
	"errors"
	"strings"

	"core/shared/protocol"
)

type WorkflowTaskResumeConflictState string

const (
	WorkflowTaskResumeConflictPendingApproval           WorkflowTaskResumeConflictState = "pending_approval"
	WorkflowTaskResumeConflictFinished                  WorkflowTaskResumeConflictState = "finished"
	WorkflowTaskResumeConflictMovedCurrentNode          WorkflowTaskResumeConflictState = "moved_current_node"
	WorkflowTaskResumeConflictCurrentNodeNotInterrupted WorkflowTaskResumeConflictState = "current_node_not_interrupted"
	WorkflowTaskResumeConflictNoResumableCurrentNode    WorkflowTaskResumeConflictState = "no_resumable_current_node"
)

func (s WorkflowTaskResumeConflictState) IsValid() bool {
	switch s {
	case WorkflowTaskResumeConflictPendingApproval,
		WorkflowTaskResumeConflictFinished,
		WorkflowTaskResumeConflictMovedCurrentNode,
		WorkflowTaskResumeConflictCurrentNodeNotInterrupted,
		WorkflowTaskResumeConflictNoResumableCurrentNode:
		return true
	default:
		return false
	}
}

type WorkflowTaskResumeConflictError struct {
	TaskID string
	State  WorkflowTaskResumeConflictState
}

func (*WorkflowTaskResumeConflictError) Error() string {
	return "workflow task resume conflict"
}

func (e *WorkflowTaskResumeConflictError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowTaskResumeConflict
}

func (e *WorkflowTaskResumeConflictError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type   string                          `json:"type"`
		TaskID string                          `json:"task_id"`
		State  WorkflowTaskResumeConflictState `json:"state"`
	}{
		Type:   "workflow_task_resume_conflict",
		TaskID: e.TaskID,
		State:  e.State,
	})
}

func (e *WorkflowTaskResumeConflictError) Validate() error {
	if e == nil {
		return errors.New("workflow task resume conflict is required")
	}
	if strings.TrimSpace(e.TaskID) == "" {
		return errors.New("workflow task resume conflict task_id is required")
	}
	if !e.State.IsValid() {
		return errors.New("workflow task resume conflict state is invalid")
	}
	return nil
}

func DecodeWorkflowTaskResumeConflictError(data json.RawMessage, message string) error {
	fallback := errors.New(strings.TrimSpace(message))
	if strings.TrimSpace(message) == "" {
		fallback = errors.New("workflow task resume conflict")
	}
	var payload struct {
		Type   string                          `json:"type"`
		TaskID string                          `json:"task_id"`
		State  WorkflowTaskResumeConflictState `json:"state"`
	}
	if err := protocol.DecodeStrictJSON(data, &payload); err != nil ||
		payload.Type != "workflow_task_resume_conflict" {
		return fallback
	}
	result := &WorkflowTaskResumeConflictError{
		TaskID: payload.TaskID,
		State:  payload.State,
	}
	if err := result.Validate(); err != nil {
		return fallback
	}
	return result
}
