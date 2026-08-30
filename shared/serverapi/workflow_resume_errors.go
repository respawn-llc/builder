package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
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

func (e *WorkflowTaskResumeConflictError) Error() string {
	if e == nil {
		return "workflow task resume conflict"
	}
	const unsupported = "Direct interactive continuation of this retained Workflow Session is not currently supported."
	switch e.State {
	case WorkflowTaskResumeConflictPendingApproval:
		return fmt.Sprintf(
			"Workflow Task %q is waiting for an Approval; resolve that Approval before continuing the Task. %s",
			e.TaskID,
			unsupported,
		)
	case WorkflowTaskResumeConflictFinished:
		return fmt.Sprintf(
			"Workflow Task %q has finished; start a new ordinary Session. %s",
			e.TaskID,
			unsupported,
		)
	case WorkflowTaskResumeConflictMovedCurrentNode:
		return fmt.Sprintf(
			"Workflow Task %q has moved to a different Current Node; continue through the Task's current Node. %s",
			e.TaskID,
			unsupported,
		)
	case WorkflowTaskResumeConflictCurrentNodeNotInterrupted:
		return fmt.Sprintf(
			"Workflow Task %q's Current Node is no longer interrupted; use the Task's current Node controls or wait for its lifecycle state to change. %s",
			e.TaskID,
			unsupported,
		)
	case WorkflowTaskResumeConflictNoResumableCurrentNode:
		return fmt.Sprintf(
			"Workflow Task %q has no interrupted executable Current Node; use the Task's current Node controls or start a new ordinary Session. %s",
			e.TaskID,
			unsupported,
		)
	default:
		return fmt.Sprintf("Workflow Task %q cannot be resumed. %s", e.TaskID, unsupported)
	}
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
