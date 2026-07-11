package serverapi

import (
	"encoding/json"
	"errors"
	"strings"

	"core/shared/protocol"
)

var (
	ErrWorkflowTaskNotFound                     = errors.New("workflow task not found")
	ErrWorkflowTaskLegacyExecutionTargetMissing = errors.New("legacy workflow task execution target is unavailable")
)

// WorkflowTaskLegacyExecutionTargetMissingError reports a historical task that
// predates execution targets and no longer has a usable managed-worktree
// attachment. Its original execution source cannot be reconstructed safely.
type WorkflowTaskLegacyExecutionTargetMissingError struct {
	TaskID string
}

func (e *WorkflowTaskLegacyExecutionTargetMissingError) Error() string {
	if e == nil || strings.TrimSpace(e.TaskID) == "" {
		return ErrWorkflowTaskLegacyExecutionTargetMissing.Error()
	}
	return "legacy workflow task " + e.TaskID + " execution target is unavailable"
}

func (e *WorkflowTaskLegacyExecutionTargetMissingError) Is(target error) bool {
	return target == ErrWorkflowTaskLegacyExecutionTargetMissing
}

func (e *WorkflowTaskLegacyExecutionTargetMissingError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowTaskLegacyExecutionTargetMissing
}

func (e *WorkflowTaskLegacyExecutionTargetMissingError) RPCErrorData() json.RawMessage {
	data, err := json.Marshal(workflowTaskLegacyExecutionTargetMissingEnvelope{
		Type:   "workflow_task_legacy_execution_target_missing",
		TaskID: e.TaskID,
	})
	if err != nil {
		return nil
	}
	return data
}

type workflowTaskLegacyExecutionTargetMissingEnvelope struct {
	Type   string `json:"type"`
	TaskID string `json:"task_id"`
}

func DecodeWorkflowTaskLegacyExecutionTargetMissingError(data json.RawMessage, message string) error {
	var envelope workflowTaskLegacyExecutionTargetMissingEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return errors.Join(ErrWorkflowTaskLegacyExecutionTargetMissing, err)
	}
	if envelope.Type != "workflow_task_legacy_execution_target_missing" {
		return errors.Join(ErrWorkflowTaskLegacyExecutionTargetMissing, errors.New("invalid legacy workflow task execution target error envelope"))
	}
	return &WorkflowTaskLegacyExecutionTargetMissingError{TaskID: strings.TrimSpace(envelope.TaskID)}
}
