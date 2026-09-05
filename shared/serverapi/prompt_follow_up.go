package serverapi

import (
	"context"
	"errors"

	"core/shared/clientui"
	"core/shared/runtimeids"
)

type PromptFollowUpWatchRequest struct {
	SessionID  runtimeids.SessionID `json:"session_id"`
	StepID     runtimeids.StepID    `json:"step_id"`
	ToolCallID clientui.ToolCallID  `json:"tool_call_id"`
}

func (r PromptFollowUpWatchRequest) Validate() error {
	if r.SessionID.IsZero() {
		return errors.New("session_id is required")
	}
	if r.StepID.IsZero() {
		return errors.New("step_id is required")
	}
	return r.ToolCallID.Validate()
}

type PromptFollowUpEventKind string

const (
	PromptFollowUpSuccessorReady      PromptFollowUpEventKind = "successor_ready"
	PromptFollowUpNoPreparedSuccessor PromptFollowUpEventKind = "no_prepared_successor"
	PromptFollowUpExecutionClosed     PromptFollowUpEventKind = "execution_closed"
)

type PromptFollowUpEvent struct {
	Kind PromptFollowUpEventKind `json:"kind"`
}

func (e PromptFollowUpEvent) Validate() error {
	switch e.Kind {
	case PromptFollowUpSuccessorReady,
		PromptFollowUpNoPreparedSuccessor,
		PromptFollowUpExecutionClosed:
		return nil
	default:
		return errors.New("prompt follow-up event kind is invalid")
	}
}

type PromptFollowUpSubscription interface {
	Next(context.Context) (PromptFollowUpEvent, error)
	Close() error
}
