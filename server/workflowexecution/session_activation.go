package workflowexecution

import (
	"context"
	"errors"
	"fmt"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func (c *CurrentNodeController) ActivateWorkflowSession(
	ctx context.Context,
	request sessionruntime.WorkflowSessionActivationRequest,
) (sessionruntime.WorkflowSessionActivationResult, error) {
	if c == nil {
		return sessionruntime.WorkflowSessionActivationResult{}, errors.New("current node workflow controller is required")
	}
	switch request.Operation {
	case serverapi.SessionRuntimeActivationUserActivation:
		taskID, err := c.store.TaskIDForSession(ctx, request.SessionID)
		if err != nil {
			return sessionruntime.WorkflowSessionActivationResult{}, err
		}
		if taskID == nil {
			return sessionruntime.WorkflowSessionActivationResult{}, nil
		}
		return c.activateRetainedWorkflowSession(ctx, request, *taskID)
	case serverapi.SessionRuntimeActivationTechnicalReattachment:
		binding, found, err := c.store.ResolveDirectSessionCurrentNodeBinding(ctx, request.SessionID)
		if err != nil {
			return sessionruntime.WorkflowSessionActivationResult{}, err
		}
		if !found {
			return sessionruntime.WorkflowSessionActivationResult{}, nil
		}
		return c.reattachRetainedWorkflowSession(ctx, request, binding)
	default:
		return sessionruntime.WorkflowSessionActivationResult{}, errors.New("invalid workflow Session activation operation")
	}
}

func (c *CurrentNodeController) activateRetainedWorkflowSession(
	ctx context.Context,
	request sessionruntime.WorkflowSessionActivationRequest,
	taskID workflow.TaskID,
) (sessionruntime.WorkflowSessionActivationResult, error) {
	var run *currentNodeRun
	handled := false
	err := c.taskMutations.Run(ctx, taskID, func(ctx context.Context) error {
		binding, found, err := c.store.ResolveDirectSessionCurrentNodeBinding(ctx, request.SessionID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if binding.TaskID != taskID {
			return fmt.Errorf(
				"retained Session %q Task ownership changed from %q to %q during activation",
				request.SessionID,
				taskID,
				binding.TaskID,
			)
		}
		association, err := c.store.EnsureCurrentNodeSessionAssociation(ctx, request.SessionID)
		if err != nil {
			return err
		}
		if !association.CurrentNode.Equal(binding.CurrentNode) {
			return fmt.Errorf(
				"retained Session %q binding changed from %v to %v during activation",
				request.SessionID,
				binding.CurrentNode,
				association.CurrentNode,
			)
		}
		key, err := binding.CurrentNode.Key()
		if err != nil {
			return err
		}
		c.mu.Lock()
		current, exists := c.currentRunLocked(key)
		c.mu.Unlock()
		if !exists {
			if _, err := c.resumeTask(ctx, binding.TaskID, nil); err != nil {
				return err
			}
			c.mu.Lock()
			current, exists = c.currentRunLocked(key)
			c.mu.Unlock()
			if !exists {
				return errors.New("workflow Session activation Resume created no Run")
			}
		}
		if !current.reference.Equal(binding.CurrentNode) {
			return errors.New("workflow Session activation selected a mismatched Run")
		}
		run = current
		handled = true
		return nil
	})
	if err != nil {
		return sessionruntime.WorkflowSessionActivationResult{}, err
	}
	if !handled {
		return sessionruntime.WorkflowSessionActivationResult{}, nil
	}
	expected, err := c.waitForWorkflowSessionExact(ctx, run, request.SessionID)
	if err != nil {
		return sessionruntime.WorkflowSessionActivationResult{}, err
	}
	attachment, err := c.authority.AttachWorkflowRuntime(ctx, expected, request.OwnerID)
	if err != nil {
		return sessionruntime.WorkflowSessionActivationResult{}, err
	}
	return sessionruntime.WorkflowSessionActivationResult{Handled: true, Attachment: attachment}, nil
}

func (c *CurrentNodeController) reattachRetainedWorkflowSession(
	ctx context.Context,
	request sessionruntime.WorkflowSessionActivationRequest,
	binding workflowstore.DirectSessionCurrentNodeBinding,
) (sessionruntime.WorkflowSessionActivationResult, error) {
	if err := c.store.ValidateCurrentNodeSessionBinding(ctx, request.SessionID, binding.CurrentNode); err != nil {
		return sessionruntime.WorkflowSessionActivationResult{}, err
	}
	expected, err := c.currentWorkflowSessionExact(binding, request.SessionID)
	if err != nil {
		return sessionruntime.WorkflowSessionActivationResult{}, err
	}
	attachment, err := c.authority.AttachWorkflowRuntime(ctx, expected, request.OwnerID)
	if err != nil {
		return sessionruntime.WorkflowSessionActivationResult{}, err
	}
	return sessionruntime.WorkflowSessionActivationResult{Handled: true, Attachment: attachment}, nil
}

func (c *CurrentNodeController) waitForWorkflowSessionExact(
	ctx context.Context,
	expectedRun *currentNodeRun,
	sessionID runtimeids.SessionID,
) (sessionruntime.WorkflowRuntimeAttachmentExpectation, error) {
	if expectedRun == nil {
		return sessionruntime.WorkflowRuntimeAttachmentExpectation{}, errors.New("workflow Session activation Run is required")
	}
	for {
		c.mu.Lock()
		run := c.runs[expectedRun.id]
		if run == nil || run != expectedRun || c.currentRuns[expectedRun.key] != expectedRun.id {
			c.mu.Unlock()
			return sessionruntime.WorkflowRuntimeAttachmentExpectation{}, workflowSessionUnavailable(expectedRun.reference)
		}
		if run.exact() {
			scopeID := *run.exactScopeID
			reference := run.reference
			c.mu.Unlock()
			return c.workflowSessionExactExpectation(scopeID, reference, sessionID)
		}
		changed := run.phaseChanged
		c.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return sessionruntime.WorkflowRuntimeAttachmentExpectation{}, context.Cause(ctx)
		}
	}
}

func (c *CurrentNodeController) currentWorkflowSessionExact(
	binding workflowstore.DirectSessionCurrentNodeBinding,
	sessionID runtimeids.SessionID,
) (sessionruntime.WorkflowRuntimeAttachmentExpectation, error) {
	key, err := binding.CurrentNode.Key()
	if err != nil {
		return sessionruntime.WorkflowRuntimeAttachmentExpectation{}, err
	}
	c.mu.Lock()
	run, exists := c.currentRunLocked(key)
	if !exists || !run.exact() || !run.reference.Equal(binding.CurrentNode) {
		c.mu.Unlock()
		return sessionruntime.WorkflowRuntimeAttachmentExpectation{}, workflowSessionUnavailable(binding.CurrentNode)
	}
	scopeID := *run.exactScopeID
	c.mu.Unlock()
	return c.workflowSessionExactExpectation(scopeID, binding.CurrentNode, sessionID)
}

func (c *CurrentNodeController) workflowSessionExactExpectation(
	scopeID runtimeids.ExecutionScopeID,
	reference workflow.CurrentNodeReference,
	sessionID runtimeids.SessionID,
) (sessionruntime.WorkflowRuntimeAttachmentExpectation, error) {
	handle, live := c.authority.ExecutionByScope(scopeID)
	if !live {
		return sessionruntime.WorkflowRuntimeAttachmentExpectation{}, workflowSessionUnavailable(reference)
	}
	scope := handle.Scope()
	resource, agent := scope.Resource()
	workflowRef, workflowOwned := scope.Workflow()
	if !agent ||
		resource.SessionID() != sessionID ||
		!workflowOwned ||
		!workflowRef.CurrentNode.Equal(reference) {
		return sessionruntime.WorkflowRuntimeAttachmentExpectation{}, workflowSessionUnavailable(reference)
	}
	return sessionruntime.WorkflowRuntimeAttachmentExpectation{
		SessionID:          sessionID,
		ScopeID:            scope.ID(),
		CurrentNode:        reference,
		ResourceGeneration: resource.Generation(),
	}, nil
}

func workflowSessionUnavailable(reference workflow.CurrentNodeReference) error {
	return errors.Join(
		serverapi.ErrRuntimeUnavailable,
		fmt.Errorf("workflow Current Node %v has no matching exact Session Runtime", reference),
	)
}

var _ sessionruntime.WorkflowSessionActivator = (*CurrentNodeController)(nil)
