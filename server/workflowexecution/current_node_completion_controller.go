package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"core/server/runtimecommand"
	"core/server/sessionruntime"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
)

// completionAttemptWorkflowController adds the process-local command
// completion fence to the production Current Node controller. The base
// controller remains responsible for workflow lifecycle and durable
// completion; this wrapper owns the exact lease that protects the model turn
// immediately preceding that completion.
type completionAttemptWorkflowController struct {
	workflowruntime.Controller
	authority *sessionruntime.Authority
	commands  *runtimecommand.Authority

	mu     sync.Mutex
	leases map[runtimeids.ExecutionScopeID]*runtimecommand.CompletionLease
}

func newCompletionAttemptWorkflowController(
	base workflowruntime.Controller,
	authority *sessionruntime.Authority,
	commands *runtimecommand.Authority,
) workflowruntime.Controller {
	if base == nil || authority == nil || commands == nil {
		return base
	}
	return &completionAttemptWorkflowController{
		Controller: base,
		authority:  authority,
		commands:   commands,
		leases:     make(map[runtimeids.ExecutionScopeID]*runtimecommand.CompletionLease),
	}
}

func (c *completionAttemptWorkflowController) BeginCompletionAttempt(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
) error {
	if c == nil || c.authority == nil || c.commands == nil {
		return errors.New("workflow completion attempt controller is unavailable")
	}
	scope, ok := c.authority.CurrentExecutionScope(scopeID)
	if !ok {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	resource, ok := scope.Resource()
	if !ok {
		return sessionruntime.ErrExecutionNoLongerLive
	}
	attempt, err := c.commands.BeginCompletionAttempt(ctx, resource)
	if err != nil {
		return err
	}
	lease, err := attempt.Acquire()
	if err != nil {
		return err
	}
	if err := lease.Reserve(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.leases[scopeID]; exists {
		abortErr := lease.Abort()
		return errors.Join(
			fmt.Errorf("workflow completion attempt already exists for scope %s", scopeID),
			abortErr,
		)
	}
	c.leases[scopeID] = &lease
	return nil
}

func (c *completionAttemptWorkflowController) AbortCompletionAttempt(
	scopeID runtimeids.ExecutionScopeID,
) error {
	lease := c.takeLease(scopeID)
	if lease == nil {
		return nil
	}
	return lease.Abort()
}

func (c *completionAttemptWorkflowController) CompleteCurrentNode(
	ctx context.Context,
	req workflowruntime.CompletionRequest,
) (workflowruntime.CompletionResult, error) {
	result, completionErr := c.Controller.CompleteCurrentNode(ctx, req)
	lease := c.takeLease(req.ScopeID)
	if lease == nil {
		return result, completionErr
	}
	if completionErr != nil {
		return result, errors.Join(completionErr, lease.Abort())
	}
	return result, errors.Join(completionErr, lease.Commit())
}

func (c *completionAttemptWorkflowController) takeLease(
	scopeID runtimeids.ExecutionScopeID,
) *runtimecommand.CompletionLease {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	lease := c.leases[scopeID]
	delete(c.leases, scopeID)
	return lease
}

var _ workflowruntime.Controller = (*completionAttemptWorkflowController)(nil)
