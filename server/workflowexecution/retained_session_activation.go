package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
)

func (c *CurrentNodeController) ActivateOrAttachRetainedSession(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	ownerID string,
) (sessionruntime.RuntimeAttachment, bool, error) {
	return c.activateOrAttachRetainedSession(ctx, sessionID, ownerID, true)
}

func (c *CurrentNodeController) activateOrAttachRetainedSession(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	ownerID string,
	reclassifyRetired bool,
) (sessionruntime.RuntimeAttachment, bool, error) {
	if c == nil {
		return sessionruntime.RuntimeAttachment{}, false, errors.New("current node workflow controller is required")
	}
	if sessionID.IsZero() {
		return sessionruntime.RuntimeAttachment{}, false, errors.New("retained Session id is required")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return sessionruntime.RuntimeAttachment{}, false, errors.New("runtime owner id is required")
	}
	taskID, err := c.store.TaskIDForSession(ctx, sessionID)
	if err != nil {
		return sessionruntime.RuntimeAttachment{}, true, err
	}
	if taskID == nil {
		return sessionruntime.RuntimeAttachment{}, false, nil
	}

	handled := true
	var (
		outcome    *currentNodeAgentActivationOutcome
		resolution workflowstore.TaskAttentionResolution
	)
	err = c.lifecycle.Run(ctx, *taskID, func(ctx context.Context) error {
		input, resolveErr := c.store.EnsureCurrentSessionStartContext(ctx, sessionID)
		if errors.Is(resolveErr, workflowstore.ErrSessionNotCurrentWorkflowNode) {
			handled = false
			return nil
		}
		if resolveErr != nil {
			return resolveErr
		}
		if input.Node.Kind != workflow.NodeKindAgent {
			return fmt.Errorf(
				"retained Session %s is bound to non-Agent Current Node %v",
				sessionID,
				input.CurrentNode.Reference,
			)
		}
		if input.CurrentNode.SessionID == nil || *input.CurrentNode.SessionID != sessionID {
			return fmt.Errorf(
				"retained Session %s does not own Current Node %v",
				sessionID,
				input.CurrentNode.Reference,
			)
		}
		if input.CurrentNode.Reference.TaskID != *taskID {
			return fmt.Errorf(
				"retained Session %s Task ownership %q contradicts Current Node %v",
				sessionID,
				*taskID,
				input.CurrentNode.Reference,
			)
		}
		key, keyErr := input.CurrentNode.Reference.Key()
		if keyErr != nil {
			return keyErr
		}

		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(*taskID); err != nil {
			c.mu.Unlock()
			return err
		}
		if run, exists := c.runs.get(key); exists {
			outcome, err = run.joinAgentActivation(sessionID)
			c.mu.Unlock()
			return err
		}
		c.mu.Unlock()

		if input.CurrentNode.Scheduling == nil ||
			input.CurrentNode.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
			return fmt.Errorf(
				"retained Session %s Current Node %v has no Run and is not interrupted",
				sessionID,
				input.CurrentNode.Reference,
			)
		}
		classifications, preflightErr := c.store.PreflightTaskResume(ctx, *taskID)
		if preflightErr != nil {
			return preflightErr
		}
		var classification *workflowstore.CurrentNodeResumeClassification
		for index := range classifications {
			if classifications[index].CurrentNode.Reference.Equal(input.CurrentNode.Reference) {
				classification = &classifications[index]
				break
			}
		}
		if classification == nil {
			return fmt.Errorf(
				"retained Session %s Current Node %v is not resumable",
				sessionID,
				input.CurrentNode.Reference,
			)
		}
		if validationErr := classification.ValidationError(); validationErr != nil {
			return validationErr
		}
		run := newCurrentNodeRun(
			input.CurrentNode.Reference,
			workflow.NodeKindAgent,
			currentNodeAdmissionExplicitOverride,
		)
		run.taskPromptDelivery = workflowruntime.TaskPromptDeliveryResume
		outcome, err = run.joinAgentActivation(sessionID)
		if err != nil {
			return err
		}
		c.mu.Lock()
		registered, _, registerErr := c.runs.register(run)
		c.mu.Unlock()
		if registerErr != nil {
			return registerErr
		}
		delta, deltaErr := workflowstore.NewQueuedTaskLifecycleDelta(
			*taskID,
			[]workflow.CurrentNodeReference{input.CurrentNode.Reference},
		)
		if deltaErr != nil {
			c.rollbackPreparedResumeRuns([]workflow.CurrentNodeReference{input.CurrentNode.Reference}, deltaErr)
			return deltaErr
		}
		attention, publishErr := c.publication.PublishResume(ctx, delta)
		if publishErr != nil {
			c.rollbackPreparedResumeRuns([]workflow.CurrentNodeReference{input.CurrentNode.Reference}, publishErr)
			return publishErr
		}
		if registered != run {
			panic("retained Session activation registered a different Current Node Run")
		}
		resolution.InterruptedCurrentNodes = append(resolution.InterruptedCurrentNodes, attention...)
		c.makePreparedResumeRunsSchedulable([]workflow.CurrentNodeReference{input.CurrentNode.Reference})
		return nil
	})
	c.finalizeTaskAttentionResolution(resolution)
	if err != nil || !handled {
		return sessionruntime.RuntimeAttachment{}, handled, err
	}
	if outcome == nil {
		return sessionruntime.RuntimeAttachment{}, true, errors.New("retained Workflow Session activation has no Agent outcome")
	}
	return c.attachRetainedSessionOutcome(ctx, sessionID, ownerID, outcome, reclassifyRetired)
}

func (c *CurrentNodeController) attachRetainedSessionOutcome(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	ownerID string,
	outcome *currentNodeAgentActivationOutcome,
	reclassifyRetired bool,
) (sessionruntime.RuntimeAttachment, bool, error) {
	activated, err := outcome.await(ctx)
	if err != nil {
		return sessionruntime.RuntimeAttachment{}, true, err
	}
	attachment, err := c.authority.AttachWorkflowRuntime(ctx, activated.resource, activated.scopeID, ownerID)
	if errors.Is(err, sessionruntime.ErrExecutionNoLongerLive) && reclassifyRetired {
		return c.activateOrAttachRetainedSession(ctx, sessionID, ownerID, false)
	}
	if err != nil {
		return sessionruntime.RuntimeAttachment{}, true, err
	}
	return attachment, true, nil
}

var _ sessionruntime.RetainedWorkflowSessionActivator = (*CurrentNodeController)(nil)
