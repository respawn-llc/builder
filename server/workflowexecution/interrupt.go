package workflowexecution

import (
	"context"
	"errors"
	"strings"

	"core/server/workflow"
	"core/shared/runtimeids"
)

type InterruptSelector struct {
	TaskID    workflow.TaskID
	SessionID *runtimeids.SessionID
}

func (s InterruptSelector) Validate() error {
	if strings.TrimSpace(string(s.TaskID)) == "" {
		return errors.New("workflow interrupt task id is required")
	}
	if s.SessionID != nil && s.SessionID.IsZero() {
		return errors.New("workflow interrupt session id is invalid")
	}
	return nil
}

type ExactExecutionScope struct {
	ScopeID    runtimeids.ExecutionScopeID
	TaskID     workflow.TaskID
	RunID      workflow.RunID
	Generation int64
}

func (s ExactExecutionScope) Validate() error {
	if s.ScopeID.IsZero() {
		return errors.New("workflow exact execution scope id is required")
	}
	if strings.TrimSpace(string(s.TaskID)) == "" {
		return errors.New("workflow exact execution task id is required")
	}
	if strings.TrimSpace(string(s.RunID)) == "" {
		return errors.New("workflow exact execution run id is required")
	}
	if s.Generation < 0 {
		return errors.New("workflow exact execution generation is invalid")
	}
	return nil
}

type PreparedInterrupt interface {
	Commit(func([]ExactExecutionScope) error) error
	Wait(context.Context) error
}

type InterruptAuthority interface {
	PrepareWorkflowInterrupt(InterruptSelector) (PreparedInterrupt, error)
}

type PreparedMoveStop interface {
	RequestStop()
	Wait(context.Context) error
}

type MoveAuthority interface {
	PrepareWorkflowMove(workflow.TaskID) (PreparedMoveStop, error)
}
