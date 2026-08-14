package sessionruntime

import (
	"errors"

	"core/shared/runtimeids"
)

func (a *Authority) RecordWorkflowProtocolViolation(
	scopeID runtimeids.ExecutionScopeID,
	maxCount int,
) (int64, WorkflowOperationRef, bool, error) {
	if a == nil {
		return 0, WorkflowOperationRef{}, false, errors.New("session runtime authority is required")
	}
	if scopeID.IsZero() {
		return 0, WorkflowOperationRef{}, false, errors.New("workflow exact execution scope id is required")
	}
	if maxCount <= 0 {
		return 0, WorkflowOperationRef{}, false, errors.New("workflow protocol violation cap must be positive")
	}
	handle, live := a.ExecutionByScope(scopeID)
	if !live {
		return 0, WorkflowOperationRef{}, false, ErrExecutionNoLongerLive
	}
	exact := handle.(executionHandle).execution
	exact.exactMu.Lock()
	defer exact.exactMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byScope[scopeID] != exact {
		return 0, WorkflowOperationRef{}, false, ErrExecutionNoLongerLive
	}
	workflowRef, workflowScoped := exact.scope.Workflow()
	if !workflowScoped {
		return 0, WorkflowOperationRef{}, false, ErrExecutionNoLongerLive
	}
	exact.protocolViolations++
	return exact.protocolViolations, workflowRef.Operation(), exact.protocolViolations >= int64(maxCount), nil
}

func (a *Authority) ResetWorkflowProtocolViolationBudget(scopeID runtimeids.ExecutionScopeID) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if scopeID.IsZero() {
		return errors.New("workflow exact execution scope id is required")
	}
	handle, live := a.ExecutionByScope(scopeID)
	if !live {
		return ErrExecutionNoLongerLive
	}
	exact := handle.(executionHandle).execution
	exact.exactMu.Lock()
	defer exact.exactMu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byScope[scopeID] != exact {
		return ErrExecutionNoLongerLive
	}
	if _, workflowScoped := exact.scope.Workflow(); !workflowScoped {
		return ErrExecutionNoLongerLive
	}
	exact.protocolViolations = 0
	return nil
}
