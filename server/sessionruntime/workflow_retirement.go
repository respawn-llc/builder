package sessionruntime

import (
	"context"
	"errors"

	"core/shared/runtimeids"
)

// WaitForWorkflowExecutionRetirement waits until the selected Session no
// longer has a live Workflow execution. Durable completion may precede scope
// retirement while post-completion runtime work is still unwinding.
func WaitForWorkflowExecutionRetirement(
	ctx context.Context,
	authority *Authority,
	sessionID runtimeids.SessionID,
) error {
	if authority == nil {
		return errors.New("session runtime authority is required")
	}
	if sessionID.IsZero() {
		return errors.New("session id is required")
	}
	for {
		handle, live := authority.SessionExecution(sessionID)
		if !live {
			return nil
		}
		if _, workflowScoped := handle.Scope().Workflow(); !workflowScoped {
			return nil
		}
		if _, err := handle.Wait(ctx); err != nil {
			if context.Cause(ctx) != nil {
				return err
			}
		}
	}
}
