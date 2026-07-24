package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"core/shared/protocol"
)

type WorkflowTaskIntegrityReason string

const (
	WorkflowTaskIntegrityReasonCurrentRunMissing       WorkflowTaskIntegrityReason = "current_run_missing"
	WorkflowTaskIntegrityReasonAgentSessionMissing     WorkflowTaskIntegrityReason = "agent_session_missing"
	WorkflowTaskIntegrityReasonExactExecutionMissing   WorkflowTaskIntegrityReason = "exact_execution_missing"
	WorkflowTaskIntegrityReasonExactExecutionMismatch  WorkflowTaskIntegrityReason = "exact_execution_mismatch"
	WorkflowTaskIntegrityReasonActionProjectionInvalid WorkflowTaskIntegrityReason = "action_projection_invalid"
)

type WorkflowTaskIntegrityDurableFacts struct {
	RunPresent      bool `json:"run_present"`
	Started         bool `json:"started"`
	Completed       bool `json:"completed"`
	Interrupted     bool `json:"interrupted"`
	WaitingQuestion bool `json:"waiting_question"`
}

type WorkflowTaskIntegrityExactFacts struct {
	Present         bool    `json:"present"`
	Kind            *string `json:"kind,omitempty"`
	SessionID       *string `json:"session_id,omitempty"`
	WaitingQuestion bool    `json:"waiting_question"`
}

type WorkflowTaskIntegrityActionFacts struct {
	CanInterrupt bool `json:"can_interrupt"`
	CanResume    bool `json:"can_resume"`
}

type WorkflowTaskIntegrityError struct {
	Reason      WorkflowTaskIntegrityReason       `json:"reason"`
	TaskID      string                            `json:"task_id"`
	PlacementID string                            `json:"placement_id"`
	NodeID      string                            `json:"node_id"`
	NodeKind    string                            `json:"node_kind"`
	RunID       *string                           `json:"run_id,omitempty"`
	SessionID   *string                           `json:"session_id,omitempty"`
	Generation  *int64                            `json:"generation,omitempty"`
	StatusKind  WorkflowTaskStatusKind            `json:"status_kind"`
	Durable     WorkflowTaskIntegrityDurableFacts `json:"durable"`
	Exact       WorkflowTaskIntegrityExactFacts   `json:"exact"`
	Actions     WorkflowTaskIntegrityActionFacts  `json:"actions"`
}

func (e *WorkflowTaskIntegrityError) Error() string {
	if e == nil {
		return "workflow task integrity failure"
	}
	runID := "<absent>"
	if e.RunID != nil {
		runID = *e.RunID
	}
	return fmt.Sprintf(
		"workflow task integrity failure: reason=%s task_id=%s placement_id=%s node_id=%s node_kind=%s run_id=%s",
		e.Reason,
		e.TaskID,
		e.PlacementID,
		e.NodeID,
		e.NodeKind,
		runID,
	)
}

func (e *WorkflowTaskIntegrityError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowTaskIntegrity
}

func (e *WorkflowTaskIntegrityError) RPCErrorData() json.RawMessage {
	if e == nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type string `json:"type"`
		*WorkflowTaskIntegrityError
	}{
		Type:                       "workflow_task_integrity_error",
		WorkflowTaskIntegrityError: e,
	})
}

func DecodeWorkflowTaskIntegrityError(data json.RawMessage, message string) error {
	var envelope struct {
		Type string `json:"type"`
		WorkflowTaskIntegrityError
	}
	if err := json.Unmarshal(data, &envelope); err != nil ||
		envelope.Type != "workflow_task_integrity_error" ||
		envelope.WorkflowTaskIntegrityError.Validate() != nil {
		return errors.New(strings.TrimSpace(message))
	}
	return &envelope.WorkflowTaskIntegrityError
}

func (e *WorkflowTaskIntegrityError) Validate() error {
	if e == nil {
		return errors.New("workflow task integrity error is required")
	}
	switch e.Reason {
	case WorkflowTaskIntegrityReasonCurrentRunMissing,
		WorkflowTaskIntegrityReasonAgentSessionMissing,
		WorkflowTaskIntegrityReasonExactExecutionMissing,
		WorkflowTaskIntegrityReasonExactExecutionMismatch,
		WorkflowTaskIntegrityReasonActionProjectionInvalid:
	default:
		return errors.New("workflow task integrity reason is invalid")
	}
	for field, value := range map[string]string{
		"task_id":      e.TaskID,
		"placement_id": e.PlacementID,
		"node_id":      e.NodeID,
		"node_kind":    e.NodeKind,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is invalid", field)
		}
	}
	if e.NodeKind != "agent" && e.NodeKind != "script" {
		return errors.New("node_kind is invalid")
	}
	if e.RunID != nil {
		if strings.TrimSpace(*e.RunID) == "" || strings.TrimSpace(*e.RunID) != *e.RunID {
			return errors.New("run_id is invalid")
		}
		if e.Generation == nil || *e.Generation < 0 {
			return errors.New("generation is invalid")
		}
	} else if e.Generation != nil {
		return errors.New("generation requires run_id")
	}
	if e.SessionID != nil && (strings.TrimSpace(*e.SessionID) == "" || strings.TrimSpace(*e.SessionID) != *e.SessionID) {
		return errors.New("session_id is invalid")
	}
	if e.Exact.Present {
		if e.Exact.Kind == nil || (*e.Exact.Kind != "agent" && *e.Exact.Kind != "script") {
			return errors.New("exact kind is invalid")
		}
	} else if e.Exact.Kind != nil || e.Exact.SessionID != nil || e.Exact.WaitingQuestion {
		return errors.New("absent exact execution has facts")
	}
	if e.Exact.SessionID != nil &&
		(strings.TrimSpace(*e.Exact.SessionID) == "" || strings.TrimSpace(*e.Exact.SessionID) != *e.Exact.SessionID) {
		return errors.New("exact session_id is invalid")
	}
	if _, ok := e.StatusKind.NativeState(); !ok {
		return errors.New("status_kind is invalid")
	}
	return nil
}

var _ protocol.StructuredRPCError = (*WorkflowTaskIntegrityError)(nil)
