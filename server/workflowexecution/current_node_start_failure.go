package workflowexecution

import (
	"errors"

	"core/server/workflow"
)

// CurrentNodeStartFailure carries the interruption projection selected by the
// slow runner when an admitted Current Node cannot begin execution.
type CurrentNodeStartFailure struct {
	Cause  error
	Reason workflow.CurrentNodeInterruptionReason
	Detail workflow.CurrentNodeInterruptionDetail
}

type ExecutionTargetPreparationFailure struct {
	Cause error
}

func (e *ExecutionTargetPreparationFailure) Error() string {
	if e == nil || e.Cause == nil {
		return "execution target preparation failed"
	}
	return e.Cause.Error()
}

func (e *ExecutionTargetPreparationFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *CurrentNodeStartFailure) Error() string {
	if e == nil || e.Cause == nil {
		return "current node start failed"
	}
	return e.Cause.Error()
}

func (e *CurrentNodeStartFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *CurrentNodeStartFailure) Validate() error {
	if e == nil {
		return errors.New("current node start failure is required")
	}
	if e.Cause == nil {
		return errors.New("current node start failure cause is required")
	}
	if e.Reason == "" {
		return errors.New("current node start failure reason is required")
	}
	if e.Detail.Code == "" {
		return errors.New("current node start failure detail code is required")
	}
	return nil
}
