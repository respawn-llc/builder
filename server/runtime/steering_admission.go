package runtime

import (
	"errors"
	"sync"
)

var ErrSteeringUnavailable = errors.New("Runtime Steering is unavailable")

type steeringAdmissionFamily uint8

const (
	steeringAdmissionSend steeringAdmissionFamily = iota + 1
	steeringAdmissionPostTurnQueue
	steeringAdmissionGoal
	steeringAdmissionUserShell
	steeringAdmissionWorkflowAssignment
	steeringAdmissionManualCompaction
)

type workflowControlState struct {
	mu          sync.Mutex
	controlOnly bool
}

func newWorkflowControlState() *workflowControlState {
	return &workflowControlState{}
}

func (s *workflowControlState) validateSteering(family steeringAdmissionFamily) error {
	if s == nil {
		return errors.New("Runtime Steering admission is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.controlOnly {
		switch family {
		case steeringAdmissionGoal, steeringAdmissionUserShell, steeringAdmissionWorkflowAssignment:
			return nil
		default:
			return ErrSteeringUnavailable
		}
	}
	return nil
}

func (e *Engine) EnterRetainedWorkflowControl() {
	if e == nil || e.workflowControl == nil {
		return
	}
	e.workflowControl.mu.Lock()
	e.workflowControl.controlOnly = true
	e.workflowControl.mu.Unlock()
}

func (e *Engine) ExitRetainedWorkflowControl() {
	if e == nil || e.workflowControl == nil {
		return
	}
	e.workflowControl.mu.Lock()
	e.workflowControl.controlOnly = false
	e.workflowControl.mu.Unlock()
}

func (e *Engine) RetainedWorkflowControlOnly() bool {
	if e == nil || e.workflowControl == nil {
		return false
	}
	e.workflowControl.mu.Lock()
	defer e.workflowControl.mu.Unlock()
	return e.workflowControl.controlOnly
}

func (e *Engine) WorkflowControlled() bool {
	if e == nil || e.workflowControl == nil {
		return false
	}
	e.workflowControl.mu.Lock()
	defer e.workflowControl.mu.Unlock()
	return e.workflowControl.controlOnly
}
