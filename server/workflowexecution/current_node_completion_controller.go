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
// completion; this wrapper holds the immutable completion baseline and reserves
// the exact lease only when a completion result re-enters the controller.
type completionAttemptWorkflowController struct {
	workflowruntime.Controller
	authority *sessionruntime.Authority
	commands  *runtimecommand.Authority

	mu       sync.Mutex
	attempts map[runtimeids.ExecutionScopeID]completionAttemptState
}

type completionAttemptState struct {
	attempt runtimecommand.CompletionAttempt
	lease   *runtimecommand.CompletionLease
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
		attempts:   make(map[runtimeids.ExecutionScopeID]completionAttemptState),
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

	c.mu.Lock()
	defer c.mu.Unlock()
	if state, exists := c.attempts[scopeID]; exists && state.lease != nil {
		return fmt.Errorf("workflow completion attempt already reserved for scope %s: %w", scopeID, runtimecommand.ErrCompletionFenced)
	}
	c.attempts[scopeID] = completionAttemptState{attempt: attempt}
	return nil
}

func (c *completionAttemptWorkflowController) AbortCompletionAttempt(
	scopeID runtimeids.ExecutionScopeID,
) error {
	state, ok := c.takeAttempt(scopeID)
	if !ok || state.lease == nil {
		return nil
	}
	return state.lease.Abort()
}

func (c *completionAttemptWorkflowController) CompleteCurrentNode(
	ctx context.Context,
	req workflowruntime.CompletionRequest,
) (workflowruntime.CompletionResult, error) {
	c.mu.Lock()
	state, hasAttempt := c.attempts[req.ScopeID]
	if hasAttempt {
		lease, err := state.attempt.Acquire()
		if err != nil {
			delete(c.attempts, req.ScopeID)
			c.mu.Unlock()
			return workflowruntime.CompletionResult{}, err
		}
		if err := lease.Reserve(); err != nil {
			delete(c.attempts, req.ScopeID)
			c.mu.Unlock()
			return workflowruntime.CompletionResult{}, err
		}
		state.lease = &lease
		c.attempts[req.ScopeID] = state
	}
	result, completionErr := c.Controller.CompleteCurrentNode(ctx, req)
	if hasAttempt {
		delete(c.attempts, req.ScopeID)
	}
	c.mu.Unlock()

	if !hasAttempt {
		return result, completionErr
	}
	lease := state.lease
	if completionErr != nil {
		return result, errors.Join(completionErr, lease.Abort())
	}
	return result, errors.Join(completionErr, lease.Commit())
}

func (c *completionAttemptWorkflowController) takeAttempt(
	scopeID runtimeids.ExecutionScopeID,
) (completionAttemptState, bool) {
	if c == nil {
		return completionAttemptState{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state, ok := c.attempts[scopeID]
	delete(c.attempts, scopeID)
	return state, ok
}

var _ workflowruntime.Controller = (*completionAttemptWorkflowController)(nil)
