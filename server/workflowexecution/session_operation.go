package workflowexecution

import (
	"context"
	"errors"
	"fmt"

	"core/server/runtime"
	"core/server/runtimecommand"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func (c *CurrentNodeController) GuardOrdinaryAgentStart(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	binding, found, err := c.store.ResolveDirectSessionCurrentNodeBinding(ctx, sessionID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	return errors.Join(
		sessionruntime.ErrWorkflowSessionRoutingRequired,
		fmt.Errorf("session %s remains directly retained by workflow Current Node %v", sessionID, binding.CurrentNode),
	)
}

func (c *CurrentNodeController) RouteSessionAgentOperation(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	driver runtimecommand.SessionAgentOperationDriver,
) (bool, runtimecommand.SessionAgentOperationOutcome, error) {
	if c == nil {
		return false, nil, errors.New("current node workflow controller is required")
	}
	if driver == nil {
		return false, nil, errors.New("session Agent operation driver is required")
	}
	taskID, err := c.store.TaskIDForSession(ctx, sessionID)
	if err != nil {
		return false, nil, err
	}
	if taskID == nil {
		return false, nil, nil
	}

	for {
		var selected *currentNodeRun
		creator := false
		handled := false
		err := c.taskMutations.Run(ctx, *taskID, func(ctx context.Context) error {
			binding, run, exists, found, err := c.resolveRetainedSessionOperationRun(ctx, sessionID, *taskID)
			if err != nil {
				return err
			}
			if !found {
				return nil
			}
			if !exists {
				_, run, err = c.resumeTaskWithOperation(ctx, binding.TaskID, binding.CurrentNode, driver)
				if err != nil {
					return err
				}
				creator = true
			}
			if run == nil || !run.reference.Equal(binding.CurrentNode) {
				return errors.New("attached-operation routing selected a mismatched workflow Run")
			}
			selected = run
			handled = true
			return nil
		})
		if err != nil {
			return handled, nil, err
		}
		if !handled {
			return false, nil, nil
		}
		outcome, retry, err := c.executeSessionAgentOperation(ctx, sessionID, selected, driver, creator)
		if retry {
			continue
		}
		return true, outcome, err
	}
}

func (c *CurrentNodeController) RouteAdmittedSessionAgentOperation(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	driver runtimecommand.SessionAgentOperationDriver,
	admit runtimecommand.SessionAgentOperationAdmitter,
) (bool, error) {
	if c == nil {
		return false, errors.New("current node workflow controller is required")
	}
	if driver == nil || admit == nil {
		return false, errors.New("admitted Session Agent operation dependencies are required")
	}
	taskID, err := c.store.TaskIDForSession(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if taskID == nil {
		return false, nil
	}
	type admittedRoute struct {
		ready    chan struct{}
		selected *currentNodeRun
		creator  bool
		err      error
	}
	route := &admittedRoute{ready: make(chan struct{})}
	handled := false
	err = c.taskMutations.Run(ctx, *taskID, func(ctx context.Context) error {
		binding, run, exists, found, err := c.resolveRetainedSessionOperationRun(ctx, sessionID, *taskID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		start := func() (runtimecommand.SessionAgentOperationOutcome, error) {
			<-route.ready
			if route.err != nil {
				return nil, route.err
			}
			outcome, retry, err := c.executeSessionAgentOperation(c.workerContext, sessionID, route.selected, driver, route.creator)
			for retry {
				var handled bool
				handled, outcome, err = c.RouteSessionAgentOperation(c.workerContext, sessionID, driver)
				if !handled && err == nil {
					err = errors.Join(
						sessionruntime.ErrExecutionNoLongerLive,
						errors.New("admitted Goal lost its directly retained Workflow binding before acceptance"),
					)
				}
				retry = false
			}
			return outcome, err
		}
		if err := admit(start); err != nil {
			return err
		}
		handled = true
		if !exists {
			_, run, route.err = c.resumeTaskWithOperation(ctx, binding.TaskID, binding.CurrentNode, driver)
			route.creator = true
		}
		route.selected = run
		close(route.ready)
		return nil
	})
	if err != nil {
		return handled, err
	}
	return handled, nil
}

func (c *CurrentNodeController) resolveRetainedSessionOperationRun(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	taskID workflow.TaskID,
) (workflowstore.DirectSessionCurrentNodeBinding, *currentNodeRun, bool, bool, error) {
	binding, found, err := c.store.ResolveDirectSessionCurrentNodeBinding(ctx, sessionID)
	if err != nil || !found {
		return workflowstore.DirectSessionCurrentNodeBinding{}, nil, false, found, err
	}
	if binding.TaskID != taskID {
		return workflowstore.DirectSessionCurrentNodeBinding{}, nil, false, true, fmt.Errorf(
			"retained Session %q Task ownership changed from %q to %q during attached-operation routing",
			sessionID,
			taskID,
			binding.TaskID,
		)
	}
	association, err := c.store.EnsureCurrentNodeSessionAssociation(ctx, sessionID)
	if err != nil {
		return workflowstore.DirectSessionCurrentNodeBinding{}, nil, false, true, err
	}
	if !association.CurrentNode.Equal(binding.CurrentNode) {
		return workflowstore.DirectSessionCurrentNodeBinding{}, nil, false, true, fmt.Errorf(
			"retained Session %q binding changed from %v to %v during attached-operation routing",
			sessionID,
			binding.CurrentNode,
			association.CurrentNode,
		)
	}
	key, err := binding.CurrentNode.Key()
	if err != nil {
		return workflowstore.DirectSessionCurrentNodeBinding{}, nil, false, true, err
	}
	c.mu.Lock()
	run, exists := c.currentRunLocked(key)
	c.mu.Unlock()
	return binding, run, exists, true, nil
}

func (c *CurrentNodeController) executeSessionAgentOperation(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	expected *currentNodeRun,
	driver runtimecommand.SessionAgentOperationDriver,
	creator bool,
) (runtimecommand.SessionAgentOperationOutcome, bool, error) {
	if expected == nil {
		return nil, false, errors.New("attached-operation workflow Run is required")
	}
	for {
		c.mu.Lock()
		run := c.runs[expected.id]
		if run == nil || run != expected || c.currentRuns[expected.key] != expected.id {
			c.mu.Unlock()
			return nil, true, nil
		}
		if creator {
			result := run.operationResult
			changed := run.phaseChanged
			c.mu.Unlock()
			select {
			case completed := <-result:
				return completed.outcome, false, completed.err
			case <-changed:
				continue
			case <-ctx.Done():
				return nil, false, context.Cause(ctx)
			}
		}
		switch run.phase {
		case currentNodeRunQueued, currentNodeRunLaunching, currentNodeRunHeld, currentNodeRunStaged:
			changed := run.phaseChanged
			c.mu.Unlock()
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return nil, false, context.Cause(ctx)
			}
		case currentNodeRunRetiring:
			changed := run.phaseChanged
			c.mu.Unlock()
			select {
			case <-changed:
				return nil, true, nil
			case <-ctx.Done():
				return nil, false, context.Cause(ctx)
			}
		case currentNodeRunExact:
			scopeID := *run.exactScopeID
			ordered := run.ownerOrdered.Done()
			changed := run.phaseChanged
			c.mu.Unlock()
			select {
			case <-ordered:
				outcome, err := c.joinSessionAgentOperation(ctx, sessionID, expected.reference, scopeID, driver)
				if errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) {
					return nil, true, nil
				}
				return outcome, false, err
			case <-changed:
				continue
			case <-ctx.Done():
				return nil, false, context.Cause(ctx)
			}
		default:
			c.mu.Unlock()
			panic(fmt.Sprintf("attached-operation routing observed invalid Run phase %d", run.phase))
		}
	}
}

func (c *CurrentNodeController) joinSessionAgentOperation(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	reference workflow.CurrentNodeReference,
	scopeID runtimeids.ExecutionScopeID,
	driver runtimecommand.SessionAgentOperationDriver,
) (runtimecommand.SessionAgentOperationOutcome, error) {
	handle, live := c.authority.ExecutionByScope(scopeID)
	if !live {
		return nil, sessionruntime.ErrExecutionNoLongerLive
	}
	scope := handle.Scope()
	workflowRef, workflowOwned := scope.Workflow()
	resource, agent := scope.Resource()
	if !workflowOwned || !agent || !workflowRef.CurrentNode.Equal(reference) || resource.SessionID() != sessionID {
		return nil, sessionruntime.ErrExecutionNoLongerLive
	}
	var outcome runtimecommand.SessionAgentOperationOutcome
	err := c.authority.WithExactExecutionRuntime(ctx, handle, func(runCtx context.Context, engine *runtime.Engine) error {
		var runErr error
		outcome, runErr = driver.JoinLive(runCtx, engine)
		return runErr
	})
	return outcome, err
}

var _ runtimecommand.WorkflowSessionExecutionRouter = (*CurrentNodeController)(nil)
var _ runtimecommand.WorkflowSessionAdmittedExecutionRouter = (*CurrentNodeController)(nil)
var _ sessionruntime.WorkflowSessionOrdinaryStartGuard = (*CurrentNodeController)(nil)
