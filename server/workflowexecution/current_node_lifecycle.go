package workflowexecution

import (
	"context"
	"errors"
	"fmt"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
)

const ReasonCurrentNodeStartupRecovery workflow.CurrentNodeInterruptionReason = "workflow_startup_recovery"

// Recover marks any executable Current Nodes found at process startup as
// interrupted before a new execution lifecycle is admitted.
func (c *CurrentNodeController) Recover(ctx context.Context) (int64, error) {
	if c == nil {
		return 0, errors.New("current node workflow controller is required")
	}
	recovered, err := RunMutation(ctx, c.permit, func(ctx context.Context) ([]workflow.CurrentNodeReference, error) {
		return c.store.RecoverExecutableCurrentNodes(ctx, ReasonCurrentNodeStartupRecovery, workflow.CurrentNodeInterruptionDetail{
			Code:   string(ReasonCurrentNodeStartupRecovery),
			Fields: map[string]string{},
		})
	})
	if err != nil {
		return 0, err
	}
	for _, reference := range recovered {
		c.publishPendingInterruptedCurrentNode(ctx, reference, ReasonCurrentNodeStartupRecovery)
	}
	return int64(len(recovered)), nil
}

func (c *CurrentNodeController) StartTaskWithExecutionTarget(
	ctx context.Context,
	taskID workflow.TaskID,
	candidate *workflowstore.ExecutionTargetCandidate,
) (workflowstore.StartTaskResult, error) {
	if c == nil {
		return workflowstore.StartTaskResult{}, errors.New("current node workflow controller is required")
	}
	return RunMutation(ctx, c.permit, func(ctx context.Context) (workflowstore.StartTaskResult, error) {
		c.mu.Lock()
		if err := c.ensureTaskQuiescentLocked(taskID); err != nil {
			c.mu.Unlock()
			return workflowstore.StartTaskResult{}, err
		}
		c.mu.Unlock()
		started, err := c.store.StartTaskWithExecutionTarget(ctx, taskID, candidate)
		if err != nil {
			return workflowstore.StartTaskResult{}, err
		}
		if len(started.Mutation.Created) != 1 || started.Mutation.Created[0].Scheduling == nil {
			return workflowstore.StartTaskResult{}, errors.New("task start did not create exactly one executable current node")
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if err := c.ensureTaskAvailableLocked(taskID); err != nil {
			return workflowstore.StartTaskResult{}, err
		}
		if err := c.queueExplicitStartLocked(currentNodeQueuedStart{
			reference: started.Mutation.Created[0].Reference,
		}); err != nil {
			return workflowstore.StartTaskResult{}, err
		}
		return started, nil
	})
}

func (c *CurrentNodeController) ResumeTask(ctx context.Context, taskID workflow.TaskID) ([]workflow.CurrentNode, error) {
	if c == nil {
		return nil, errors.New("current node workflow controller is required")
	}
	var resolution workflowstore.TaskAttentionResolution
	resumed, err := RunMutation(ctx, c.permit, func(ctx context.Context) ([]workflow.CurrentNode, error) {
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(taskID); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		c.mu.Unlock()
		selected, err := c.store.InterruptedExecutableCurrentNodes(ctx, taskID)
		if err != nil {
			return nil, err
		}
		resumed := make([]workflow.CurrentNode, 0, len(selected))
		var resumeErrs []error
		for _, currentNode := range selected {
			projection, found, err := c.store.ResumeCurrentNode(ctx, currentNode.Reference)
			if err != nil {
				resumeErrs = append(resumeErrs, fmt.Errorf("resume current node %v: %w", currentNode.Reference, err))
				continue
			}
			if found {
				resolution.InterruptedCurrentNodes = append(resolution.InterruptedCurrentNodes, projection)
			}
			c.mu.Lock()
			queueErr := c.queueExplicitStartLocked(currentNodeQueuedStart{
				reference:          currentNode.Reference,
				taskPromptDelivery: workflowruntime.TaskPromptDeliveryResume,
			})
			c.mu.Unlock()
			if queueErr != nil {
				resumeErrs = append(resumeErrs, fmt.Errorf("queue resumed current node %v: %w", currentNode.Reference, queueErr))
				continue
			}
			resumed = append(resumed, currentNode)
		}
		return resumed, errors.Join(resumeErrs...)
	})
	c.finalizeTaskAttentionResolution(resolution)
	return resumed, err
}

func (c *CurrentNodeController) ApplyPendingApproval(
	ctx context.Context,
	approvalID workflow.ApprovalID,
) (workflowstore.PendingApprovalApplyResult, error) {
	if c == nil {
		return workflowstore.PendingApprovalApplyResult{}, errors.New("current node workflow controller is required")
	}
	return RunMutation(ctx, c.permit, func(ctx context.Context) (workflowstore.PendingApprovalApplyResult, error) {
		approval, err := c.store.PendingApproval(ctx, approvalID)
		if err != nil {
			return workflowstore.PendingApprovalApplyResult{}, err
		}
		sourceKey, err := approval.Source.Key()
		if err != nil {
			return workflowstore.PendingApprovalApplyResult{}, err
		}
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(approval.Source.TaskID); err != nil {
			c.mu.Unlock()
			return workflowstore.PendingApprovalApplyResult{}, err
		}
		sourceScopeID, sourceLive := c.liveByNode[sourceKey]
		if sourceLive {
			if _, completed := c.completed[sourceScopeID]; !completed {
				c.mu.Unlock()
				return workflowstore.PendingApprovalApplyResult{}, errors.New("pending approval source scope has not completed")
			}
			if _, stopping := c.stopping[sourceScopeID]; stopping {
				c.mu.Unlock()
				return workflowstore.PendingApprovalApplyResult{}, sessionruntime.ErrExecutionNoLongerLive
			}
		}
		c.mu.Unlock()

		apply := func() (workflowstore.PendingApprovalApplyResult, error) {
			applied, err := c.store.ApplyPendingApproval(ctx, approvalID)
			if err != nil {
				return workflowstore.PendingApprovalApplyResult{}, err
			}
			starts, err := currentNodeExplicitStarts(applied.Mutation.Created)
			if err != nil {
				return workflowstore.PendingApprovalApplyResult{}, err
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if sourceLive {
				c.heldStarts[sourceScopeID] = append(c.heldStarts[sourceScopeID], starts...)
				return applied, nil
			}
			for _, start := range starts {
				if err := c.queueExplicitStartLocked(start); err != nil {
					return workflowstore.PendingApprovalApplyResult{}, err
				}
			}
			return applied, nil
		}
		if !sourceLive {
			return apply()
		}
		handle, live := c.authority.ExecutionByScope(sourceScopeID)
		if !live {
			return workflowstore.PendingApprovalApplyResult{}, sessionruntime.ErrExecutionNoLongerLive
		}
		var applied workflowstore.PendingApprovalApplyResult
		err = c.authority.WithExactExecutions([]sessionruntime.ExecutionHandle{handle}, func() error {
			var applyErr error
			applied, applyErr = apply()
			return applyErr
		})
		return applied, err
	})
}

func (c *CurrentNodeController) ApplyManualMove(
	ctx context.Context,
	prepared workflowstore.ManualMovePreparation,
	candidate *workflowstore.ExecutionTargetCandidate,
) (workflowstore.ManualMoveResult, error) {
	if c == nil {
		return workflowstore.ManualMoveResult{}, errors.New("current node workflow controller is required")
	}
	return RunMutation(ctx, c.permit, func(ctx context.Context) (workflowstore.ManualMoveResult, error) {
		taskID := prepared.TaskID()
		c.mu.Lock()
		if err := c.ensureTaskQuiescentLocked(taskID); err != nil {
			c.mu.Unlock()
			return workflowstore.ManualMoveResult{}, err
		}
		c.mu.Unlock()
		moved, err := c.store.ApplyManualMove(ctx, prepared, candidate)
		if err != nil {
			return workflowstore.ManualMoveResult{}, err
		}
		if moved.PendingApproval != nil {
			return moved, nil
		}
		starts, err := currentNodeExplicitStarts(moved.Created)
		if err != nil {
			return workflowstore.ManualMoveResult{}, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, start := range starts {
			if err := c.queueExplicitStartLocked(start); err != nil {
				return workflowstore.ManualMoveResult{}, err
			}
		}
		return moved, nil
	})
}

// EnsureTaskQuiescent rejects Task-wide state replacement while the
// controller owns live, admitted, or automatic work for the Task. Callers
// hold the shared mutation permit while invoking it and applying the durable
// replacement.
func (c *CurrentNodeController) EnsureTaskQuiescent(taskID workflow.TaskID) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if taskID == "" {
		return errors.New("workflow task id is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureTaskQuiescentLocked(taskID)
}

// CurrentTaskQuiescence returns an immutable view of the same controller-owned
// Quiescence state enforced by Task Delete.
func (c *CurrentNodeController) CurrentTaskQuiescence(taskIDs []workflow.TaskID) (map[workflow.TaskID]bool, error) {
	if c == nil {
		return nil, errors.New("current node workflow controller is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	quiescence := make(map[workflow.TaskID]bool, len(taskIDs))
	for _, taskID := range taskIDs {
		if taskID == "" {
			return nil, errors.New("workflow task id is required")
		}
		quiescent, err := c.taskQuiescentLocked(taskID)
		if err != nil {
			return nil, err
		}
		quiescence[taskID] = quiescent
	}
	return quiescence, nil
}

func (c *CurrentNodeController) ensureTaskQuiescentLocked(taskID workflow.TaskID) error {
	quiescent, err := c.taskQuiescentLocked(taskID)
	if err != nil {
		return err
	}
	if !quiescent {
		return ErrTaskExecutionNotQuiescent
	}
	return nil
}

func (c *CurrentNodeController) taskQuiescentLocked(taskID workflow.TaskID) (bool, error) {
	if err := c.ensureTaskAvailableLocked(taskID); err != nil {
		if errors.Is(err, ErrTaskExecutionNotQuiescent) {
			return false, nil
		}
		return false, err
	}
	for _, gate := range c.gates {
		if gate.reference.TaskID == taskID {
			return false, nil
		}
	}
	for _, live := range c.live {
		if live.reference.TaskID == taskID {
			return false, nil
		}
	}
	for _, intent := range c.automaticQueue {
		if intent.CurrentNode.TaskID == taskID {
			return false, nil
		}
	}
	for _, intent := range c.automaticReservations {
		if intent.reference.TaskID == taskID {
			return false, nil
		}
	}
	for _, start := range c.explicitQueue {
		if start.reference.TaskID == taskID {
			return false, nil
		}
	}
	for _, start := range c.explicitReservations {
		if start.reference.TaskID == taskID {
			return false, nil
		}
	}
	for _, start := range c.admissionWorkers {
		if start.reference.TaskID == taskID {
			return false, nil
		}
	}
	for _, starts := range c.heldStarts {
		for _, start := range starts {
			if start.reference.TaskID == taskID {
				return false, nil
			}
		}
	}
	return true, nil
}

func (c *CurrentNodeController) ensureTaskAvailableLocked(taskID workflow.TaskID) error {
	if c.closed {
		return errors.New("current node workflow controller is closed")
	}
	if c.workerErr != nil {
		return fmt.Errorf("workflow execution lifecycle failed: %w", c.workerErr)
	}
	if c.interrupts.taskActive(taskID) {
		return ErrTaskExecutionNotQuiescent
	}
	return nil
}
