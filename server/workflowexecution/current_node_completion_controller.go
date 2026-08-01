package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"core/server/runtime"
	"core/server/runtimecommand"
	"core/server/sessionruntime"
	"core/server/workflowruntime"
	"core/server/workflowstore"
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
) *completionAttemptWorkflowController {
	if base == nil || authority == nil || commands == nil {
		return nil
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
	if state, exists := c.attempts[scopeID]; exists {
		if state.lease != nil {
			return fmt.Errorf("workflow completion attempt already reserved for scope %s: %w", scopeID, runtimecommand.ErrCompletionFenced)
		}
		return fmt.Errorf("workflow completion attempt already pending for scope %s: %w", scopeID, runtimecommand.ErrCompletionFenced)
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
	return runCompletionAttempt(c, req.ScopeID, func() (workflowruntime.CompletionResult, error) {
		return c.Controller.CompleteCurrentNode(ctx, req)
	})
}

func runCompletionAttempt[T any](
	c *completionAttemptWorkflowController,
	scopeID runtimeids.ExecutionScopeID,
	complete func() (T, error),
) (T, error) {
	var zero T
	if c == nil || complete == nil {
		return zero, errors.New("workflow completion attempt is unavailable")
	}
	c.mu.Lock()
	state, hasAttempt := c.attempts[scopeID]
	if hasAttempt {
		lease, err := state.attempt.Acquire()
		if err != nil {
			delete(c.attempts, scopeID)
			c.mu.Unlock()
			return zero, err
		}
		if err := lease.Reserve(); err != nil {
			delete(c.attempts, scopeID)
			c.mu.Unlock()
			return zero, err
		}
		state.lease = &lease
		c.attempts[scopeID] = state
	}
	result, completionErr := complete()
	if hasAttempt {
		delete(c.attempts, scopeID)
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

type CompletionFencedCurrentNodeExecution struct {
	*CurrentNodeController
	completion *completionAttemptWorkflowController
}

func NewCompletionFencedCurrentNodeExecution(
	controller *CurrentNodeController,
	authority *sessionruntime.Authority,
	commands *runtimecommand.Authority,
) (*CompletionFencedCurrentNodeExecution, error) {
	if controller == nil {
		return nil, errors.New("current node workflow controller is required")
	}
	if authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if commands == nil {
		return nil, errors.New("runtime command authority is required")
	}
	completion := controller.completion
	if completion == nil {
		completion = newCompletionAttemptWorkflowController(controller, authority, commands)
	}
	if completion == nil {
		return nil, errors.New("completion attempt workflow controller is unavailable")
	}
	return &CompletionFencedCurrentNodeExecution{
		CurrentNodeController: controller,
		completion:            completion,
	}, nil
}

func (c *CompletionFencedCurrentNodeExecution) CompleteCurrentNode(
	ctx context.Context,
	req workflowruntime.CompletionRequest,
) (workflowruntime.CompletionResult, error) {
	if c == nil || c.completion == nil {
		return workflowruntime.CompletionResult{}, errors.New("workflow completion attempt controller is unavailable")
	}
	return c.completion.CompleteCurrentNode(ctx, req)
}

func (c *CompletionFencedCurrentNodeExecution) CompleteSessionCurrentNode(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	transitionID string,
	outputValues map[string]string,
	commentary string,
) (workflowstore.CurrentNodeCompletionResult, error) {
	if c == nil || c.CurrentNodeController == nil || c.completion == nil {
		return workflowstore.CurrentNodeCompletionResult{}, errors.New("workflow completion attempt controller is unavailable")
	}
	req, err := c.CurrentNodeController.sessionCurrentNodeCompletionRequest(sessionID, transitionID, outputValues, commentary)
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	if err := c.completion.BeginCompletionAttempt(ctx, req.ScopeID); err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	scope, ok := c.completion.authority.CurrentExecutionScope(req.ScopeID)
	if !ok {
		return workflowstore.CurrentNodeCompletionResult{}, sessionruntime.ErrExecutionNoLongerLive
	}
	returned := workflowstore.CurrentNodeCompletionResult{}
	err = c.completion.commands.DispatchAgent(ctx, scope, func(turn runtime.OrderedMutationTurn) error {
		return c.completion.authority.WithExactExecutionRuntime(context.Background(), scope.ID(), func(callbackCtx context.Context, _ *runtime.Engine) error {
			return turn.Apply(func() error {
				var completionErr error
				returned, completionErr = runCompletionAttempt(c.completion, req.ScopeID, func() (workflowstore.CurrentNodeCompletionResult, error) {
					return c.CurrentNodeController.completeLiveCurrentNode(callbackCtx, req)
				})
				return completionErr
			})
		})
	})
	return returned, err
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
