package workflowexecution

import (
	"errors"
	"fmt"
	"sync"

	"core/server/workflow"
)

type LifecycleFatalAvailability struct {
	mu    sync.Mutex
	cause error
}

type LifecycleUnavailableError struct {
	Cause error
}

func (e LifecycleUnavailableError) Error() string {
	return fmt.Sprintf("workflow execution lifecycle is unavailable: %v", e.Cause)
}

func (e LifecycleUnavailableError) Unwrap() error {
	return e.Cause
}

func NewLifecycleFatalAvailability() *LifecycleFatalAvailability {
	return &LifecycleFatalAvailability{}
}

func (a *LifecycleFatalAvailability) Report(cause error) {
	if a == nil {
		panic("workflow lifecycle fatal availability is required")
	}
	if cause == nil {
		panic("workflow lifecycle fatal cause is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cause == nil {
		a.cause = cause
	}
}

func (a *LifecycleFatalAvailability) Available() error {
	if a == nil {
		return errors.New("workflow lifecycle fatal availability is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cause == nil {
		return nil
	}
	return LifecycleUnavailableError{Cause: a.cause}
}

func (c *CurrentNodeController) recordLifecycleFatalLocked(run *currentNodeRun, cause error) {
	if run == nil || cause == nil {
		return
	}
	run.recordCallbackError(cause)
	c.lifecycleAvailability.Report(cause)
}

func (c *CurrentNodeController) recordInterruptionPersistenceFailureLocked(
	run *currentNodeRun,
	reason workflow.CurrentNodeInterruptionReason,
	cause error,
) {
	if reason == workflow.CurrentNodeInterruptionReasonUserInterrupt {
		return
	}
	c.recordLifecycleFatalLocked(run, cause)
}
