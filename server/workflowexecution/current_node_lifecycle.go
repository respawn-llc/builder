package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const ReasonCurrentNodeStartupRecovery workflow.CurrentNodeInterruptionReason = "workflow_startup_recovery"

type ManualMoveDisposition string

const (
	ManualMoveDispositionQuiescent         ManualMoveDisposition = "quiescent"
	ManualMoveDispositionAutoInterruptible ManualMoveDisposition = "auto_interruptible"
	ManualMoveDispositionWaitingQuestion   ManualMoveDisposition = "waiting_question"
	ManualMoveDispositionLifecycleConflict ManualMoveDisposition = "lifecycle_conflict"
)

var ErrManualMoveLifecycleConflict = errors.New("workflow task has a non-interruptible lifecycle conflict")

type TaskStartPreparation func(context.Context) error

func (c *CurrentNodeController) ManualMoveDisposition(taskID workflow.TaskID) (ManualMoveDisposition, error) {
	if c == nil {
		return "", errors.New("current node workflow controller is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return "", errors.New("workflow task id is required")
	}
	state, err := c.authority.CurrentWorkflowTaskExecutionState(taskID)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureTaskAvailableLocked(taskID); err != nil && !errors.Is(err, ErrTaskExecutionNotQuiescent) {
		return "", err
	}
	quiescent, err := c.taskQuiescentLocked(taskID)
	if err != nil {
		return "", err
	}
	if state.WaitingQuestions > 0 {
		return ManualMoveDispositionWaitingQuestion, nil
	}
	if state.WaitingApprovals > 0 || state.Queued > 0 || state.Finalizing > 0 {
		return ManualMoveDispositionLifecycleConflict, nil
	}
	if state.Running > 0 {
		return ManualMoveDispositionAutoInterruptible, nil
	}
	if quiescent {
		return ManualMoveDispositionQuiescent, nil
	}
	return ManualMoveDispositionLifecycleConflict, nil
}

// Recover marks any executable Current Nodes found at process startup as
// interrupted before a new execution lifecycle is admitted.
func (c *CurrentNodeController) Recover(ctx context.Context) (int64, error) {
	if c == nil {
		return 0, errors.New("current node workflow controller is required")
	}
	recovered, err := RunMutation(ctx, c.permit, func(ctx context.Context) ([]workflow.CurrentNodeReference, error) {
		return c.store.RecoverExecutableCurrentNodes(
			ctx,
			ReasonCurrentNodeStartupRecovery,
			workflow.NewCurrentNodeInterruptionDetail(string(ReasonCurrentNodeStartupRecovery), nil),
		)
	})
	if err != nil {
		return 0, err
	}
	for _, reference := range recovered {
		c.publishPendingInterruptedCurrentNode(ctx, reference, ReasonCurrentNodeStartupRecovery)
	}
	return int64(len(recovered)), nil
}

func (c *CurrentNodeController) StartTask(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation TaskStartPreparation,
) (workflowstore.StartTaskResult, error) {
	if c == nil {
		return workflowstore.StartTaskResult{}, errors.New("current node workflow controller is required")
	}
	if preparation == nil {
		return workflowstore.StartTaskResult{}, errors.New("task start preparation is required")
	}
	return runTaskLifecycle(ctx, c.lifecycle, taskID, func(ctx context.Context) (workflowstore.StartTaskResult, error) {
		c.mu.Lock()
		if err := c.ensureTaskQuiescentLocked(taskID); err != nil {
			c.mu.Unlock()
			return workflowstore.StartTaskResult{}, err
		}
		c.mu.Unlock()
		started, err := c.publication.PublishTaskStart(ctx, taskID, func(started workflowstore.StartTaskResult) (
			workflowstore.TaskLifecycleDelta,
			func(error),
			error,
		) {
			if len(started.Mutation.Created) != 1 || started.Mutation.Created[0].Scheduling == nil {
				return workflowstore.TaskLifecycleDelta{}, nil, errors.New("task start did not create exactly one executable current node")
			}
			currentNode := started.Mutation.Created[0]
			nodeKind := started.CreatedNodeKind
			if nodeKind != workflow.NodeKindAgent && nodeKind != workflow.NodeKindScript {
				var err error
				nodeKind, err = c.store.CurrentNodeKind(ctx, currentNode.Reference)
				if err != nil {
					return workflowstore.TaskLifecycleDelta{}, nil, fmt.Errorf("resolve started current node kind: %w", err)
				}
			}
			c.mu.Lock()
			if err := c.ensureTaskAvailableLocked(taskID); err != nil {
				c.mu.Unlock()
				return workflowstore.TaskLifecycleDelta{}, nil, err
			}
			run := newCurrentNodeRun(currentNode.Reference, nodeKind, currentNodeAdmissionExplicitOverride)
			run.preparation = preparation
			run.taskPromptDelivery = workflowruntime.TaskPromptDeliveryAssignment
			registered, _, registerErr := c.runs.register(run)
			c.mu.Unlock()
			if registerErr != nil {
				return workflowstore.TaskLifecycleDelta{}, nil, registerErr
			}
			delta, deltaErr := workflowstore.NewTaskStartLifecycleDelta(started)
			if deltaErr != nil {
				c.rollbackPreparedStartRun(currentNode.Reference, registered, deltaErr)
				return workflowstore.TaskLifecycleDelta{}, nil, deltaErr
			}
			rollback := func(cause error) {
				c.rollbackPreparedStartRun(currentNode.Reference, registered, cause)
			}
			return delta, rollback, nil
		})
		if err != nil {
			return workflowstore.StartTaskResult{}, err
		}
		c.makePublishedStartSchedulable(started.Mutation.Created[0].Reference)
		return started, nil
	})
}

func (c *CurrentNodeController) rollbackPreparedStartRun(
	reference workflow.CurrentNodeReference,
	expected *currentNodeRun,
	cause error,
) {
	key, err := reference.Key()
	if err != nil {
		panic(fmt.Sprintf("rollback prepared Start Run reference: %v", err))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	run, exists := c.runs.get(key)
	if !exists || run != expected {
		return
	}
	run.stopOnce(currentNodeRunStopWorkerFailed, cause)
	c.runs.delete(key)
}

func (c *CurrentNodeController) makePublishedStartSchedulable(
	reference workflow.CurrentNodeReference,
) {
	key, err := reference.Key()
	if err != nil {
		panic(fmt.Sprintf("publish Task Start Current Node reference: %v", err))
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	run, exists := c.runs.get(key)
	if !exists || run.disposition != currentNodeRunDispositionQueued {
		panic(fmt.Sprintf(
			"published Task Start lost staged Run ownership: current_node=%v",
			reference,
		))
	}
	c.explicitQueue = append(c.explicitQueue, key)
	c.explicitQueued[key] = struct{}{}
	c.wakeAdmissionWorker()
}

func (c *CurrentNodeController) ResumeTask(ctx context.Context, taskID workflow.TaskID) ([]workflow.CurrentNode, error) {
	return c.resumeTask(ctx, taskID, nil)
}

func (c *CurrentNodeController) ResumeTaskWithPreparation(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation TaskStartPreparation,
) ([]workflow.CurrentNode, error) {
	if preparation == nil {
		return nil, errors.New("task resume preparation is required")
	}
	return c.resumeTask(ctx, taskID, preparation)
}

type TaskResumeConflictError struct {
	TaskID workflow.TaskID
}

func (e *TaskResumeConflictError) Error() string {
	return fmt.Sprintf("task %q has no interrupted executable Current Nodes to resume", e.TaskID)
}

type taskExecutionRetirementPendingError struct {
	taskID workflow.TaskID
}

func (e *taskExecutionRetirementPendingError) Error() string {
	return fmt.Sprintf("task %q is waiting for its stopped exact execution to retire", e.taskID)
}

func (c *CurrentNodeController) resumeTask(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation TaskStartPreparation,
) ([]workflow.CurrentNode, error) {
	if c == nil {
		return nil, errors.New("current node workflow controller is required")
	}
	if preparation != nil {
		var once sync.Once
		var preparationErr error
		run := preparation
		preparation = func(ctx context.Context) error {
			once.Do(func() {
				preparationErr = run(ctx)
			})
			return preparationErr
		}
	}
	for {
		if err := c.waitStoppedTaskExecutionRetirement(ctx, taskID); err != nil {
			return nil, err
		}
		var resolution workflowstore.TaskAttentionResolution
		resumed, err := runTaskLifecycle(ctx, c.lifecycle, taskID, func(ctx context.Context) ([]workflow.CurrentNode, error) {
			c.mu.Lock()
			if err := c.ensureTaskAvailableLocked(taskID); err != nil {
				c.mu.Unlock()
				return nil, err
			}
			if c.stoppedTaskExecutionPendingLocked(taskID) {
				c.mu.Unlock()
				return nil, &taskExecutionRetirementPendingError{taskID: taskID}
			}
			c.mu.Unlock()
			classifications, err := c.store.PreflightTaskResume(ctx, taskID)
			if err != nil {
				return nil, err
			}
			if len(classifications) == 0 {
				return nil, &TaskResumeConflictError{TaskID: taskID}
			}
			resumable := make([]workflow.CurrentNode, 0, len(classifications))
			var resumeErrs []error
			for _, classification := range classifications {
				currentNode := classification.CurrentNode
				if validationErr := classification.ValidationError(); validationErr != nil {
					resumeErrs = append(resumeErrs, validationErr)
					continue
				}
				nodeKind, kindErr := c.store.CurrentNodeKind(ctx, currentNode.Reference)
				if kindErr != nil {
					resumeErrs = append(resumeErrs, fmt.Errorf("resolve resumed current node kind %v: %w", currentNode.Reference, kindErr))
					continue
				}
				c.mu.Lock()
				if c.closed {
					c.mu.Unlock()
					resumeErrs = append(resumeErrs, errCurrentNodeControllerClosed)
					continue
				}
				run := newCurrentNodeRun(currentNode.Reference, nodeKind, currentNodeAdmissionExplicitOverride)
				run.preparation = preparation
				run.taskPromptDelivery = workflowruntime.TaskPromptDeliveryResume
				_, _, registerErr := c.runs.register(run)
				c.mu.Unlock()
				if registerErr != nil {
					resumeErrs = append(resumeErrs, fmt.Errorf("stage resumed current node %v: %w", currentNode.Reference, registerErr))
					continue
				}
				resumable = append(resumable, currentNode)
			}
			if len(resumable) == 0 {
				return nil, errors.Join(resumeErrs...)
			}
			references := make([]workflow.CurrentNodeReference, 0, len(resumable))
			for _, currentNode := range resumable {
				references = append(references, currentNode.Reference)
			}
			delta, deltaErr := workflowstore.NewQueuedTaskLifecycleDelta(taskID, references)
			if deltaErr != nil {
				c.rollbackPreparedResumeRuns(references, deltaErr)
				return nil, errors.Join(errors.Join(resumeErrs...), deltaErr)
			}
			publicationCtx, cancelPublication := context.WithCancelCause(ctx)
			stopCloseCancellation := context.AfterFunc(c.workerContext, func() {
				cancelPublication(errCurrentNodeControllerClosed)
			})
			attention, publishErr := c.publication.PublishResume(publicationCtx, delta)
			stopCloseCancellation()
			cancelPublication(context.Canceled)
			if publishErr != nil {
				c.rollbackPreparedResumeRuns(references, publishErr)
				return nil, errors.Join(errors.Join(resumeErrs...), publishErr)
			}
			resolution.InterruptedCurrentNodes = append(
				resolution.InterruptedCurrentNodes,
				attention...,
			)
			c.makePreparedResumeRunsSchedulable(references)
			return resumable, errors.Join(resumeErrs...)
		})
		var retirementPending *taskExecutionRetirementPendingError
		if errors.As(err, &retirementPending) {
			continue
		}
		c.finalizeTaskAttentionResolution(resolution)
		return resumed, err
	}
}

func (c *CurrentNodeController) waitStoppedTaskExecutionRetirement(
	ctx context.Context,
	taskID workflow.TaskID,
) error {
	for {
		released := c.lifecycle.releasedSignal()
		c.mu.Lock()
		scopeIDs := make([]runtimeids.ExecutionScopeID, 0)
		for _, run := range c.runs.byCurrentNode {
			if run.reference.TaskID != taskID ||
				run.disposition != currentNodeRunDispositionStopped ||
				run.executionLease == nil {
				continue
			}
			scopeIDs = append(scopeIDs, run.executionLease.ScopeID())
		}
		c.mu.Unlock()
		if len(scopeIDs) == 0 {
			return nil
		}
		waited := false
		for _, scopeID := range scopeIDs {
			handle, live := c.authority.ExecutionByScope(scopeID)
			if !live {
				continue
			}
			waited = true
			_, waitErr := handle.Wait(ctx)
			if cause := context.Cause(ctx); cause != nil {
				return errors.Join(waitErr, cause)
			}
			// The completed execution's operational error is already persisted
			// on the interrupted Current Node; Resume only waits for retirement.
		}
		if waited {
			continue
		}
		select {
		case <-released:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
}

func (c *CurrentNodeController) stoppedTaskExecutionPendingLocked(taskID workflow.TaskID) bool {
	for _, run := range c.runs.byCurrentNode {
		if run.reference.TaskID == taskID &&
			run.disposition == currentNodeRunDispositionStopped &&
			run.executionLease != nil {
			return true
		}
	}
	return false
}

func (c *CurrentNodeController) rollbackPreparedResumeRuns(
	references []workflow.CurrentNodeReference,
	cause error,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, reference := range references {
		key, err := reference.Key()
		if err != nil {
			panic(fmt.Sprintf("rollback prepared Resume Run reference: %v", err))
		}
		run, exists := c.runs.get(key)
		if !exists {
			continue
		}
		if run.agentActivation != nil {
			run.agentActivation.resolve(currentNodeAgentActivationResult{}, cause)
		}
		c.runs.delete(key)
	}
}

func (c *CurrentNodeController) makePreparedResumeRunsSchedulable(
	references []workflow.CurrentNodeReference,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, reference := range references {
		key, err := reference.Key()
		if err != nil {
			panic(fmt.Sprintf("publish prepared Resume Run reference: %v", err))
		}
		run, exists := c.runs.get(key)
		if !exists {
			panic(fmt.Sprintf("Resume publication lost staged Run for Current Node %v", reference))
		}
		if run.disposition != currentNodeRunDispositionQueued ||
			run.phase != currentNodeRunQueued {
			panic(fmt.Sprintf(
				"Resume publication found invalid staged Run for Current Node %v: disposition=%d phase=%d",
				reference,
				run.disposition,
				run.phase,
			))
		}
		c.explicitQueue = append(c.explicitQueue, key)
		c.explicitQueued[key] = struct{}{}
	}
	c.wakeAdmissionWorker()
}

func (c *CurrentNodeController) ApplyPendingApproval(
	ctx context.Context,
	approvalID workflow.ApprovalID,
) (workflowstore.PendingApprovalApplyResult, error) {
	if c == nil {
		return workflowstore.PendingApprovalApplyResult{}, errors.New("current node workflow controller is required")
	}
	approval, err := c.store.PendingApproval(ctx, approvalID)
	if err != nil {
		return workflowstore.PendingApprovalApplyResult{}, err
	}
	return runTaskLifecycle(ctx, c.lifecycle, approval.Source.TaskID, func(ctx context.Context) (workflowstore.PendingApprovalApplyResult, error) {
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
		sourceRun, sourceExists := c.runs.get(sourceKey)
		if sourceExists && sourceRun.phase == currentNodeRunRunning && sourceRun.executionLease != nil {
			sourceScopeID := sourceRun.executionLease.ScopeID()
			if _, completed := c.completed[sourceScopeID]; !completed {
				c.mu.Unlock()
				return workflowstore.PendingApprovalApplyResult{}, errors.New("pending approval source scope has not completed")
			}
			if _, finalizing := c.postTurnFinalization[sourceScopeID]; finalizing {
				c.mu.Unlock()
				return workflowstore.PendingApprovalApplyResult{}, ErrTaskExecutionNotQuiescent
			}
			if _, stopping := c.stopping[sourceScopeID]; stopping {
				c.mu.Unlock()
				return workflowstore.PendingApprovalApplyResult{}, sessionruntime.ErrExecutionNoLongerLive
			}
		}
		c.mu.Unlock()

		var starts []currentNodeQueuedStart
		var pending []*pendingCurrentNodeAssignmentEnsure
		var staged []stagedCurrentNodeRun
		applied, err := c.publication.PublishPendingApproval(
			ctx,
			approvalID,
			func(result workflowstore.PendingApprovalApplyResult) (
				workflowstore.TaskLifecycleDelta,
				func(error),
				error,
			) {
				starts, err = c.currentNodeExplicitStarts(ctx, result.Mutation.Created)
				if err != nil {
					return workflowstore.TaskLifecycleDelta{}, nil, err
				}
				starts, pending = pendingCurrentNodeAssignmentStarts(starts)
				staged, err = c.stageCurrentNodeRunCreations(starts)
				if err != nil {
					return workflowstore.TaskLifecycleDelta{}, nil, err
				}
				delta, err := currentNodeCompletionLifecycleDelta(
					approval.Source,
					workflowstore.LifecycleFieldAbsent,
					starts,
				)
				if err != nil {
					c.rollbackStagedRunCreations(staged, err)
					return workflowstore.TaskLifecycleDelta{}, nil, err
				}
				return delta, func(cause error) {
					c.rollbackStagedRunCreations(staged, cause)
				}, nil
			},
		)
		if err != nil {
			return workflowstore.PendingApprovalApplyResult{}, err
		}
		resolveErr := c.resolvePendingCurrentNodeAssignmentEnsures(ctx, starts, pending)
		outcome := waitCurrentNodeAssignmentEnsures(ctx, starts)
		if len(outcome.pending) > 0 {
			c.continuePublishedCurrentNodeAssignmentStarts(
				stagedCurrentNodeRunsForStarts(staged, outcome.pending),
				outcome.pending,
			)
		}
		if err := errors.Join(
			resolveErr,
			c.completePublishedCurrentNodeAssignmentStarts(ctx, staged, outcome),
		); err != nil {
			return workflowstore.PendingApprovalApplyResult{}, err
		}
		return applied, nil
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
	taskID := prepared.TaskID()
	return runTaskLifecycle(ctx, c.lifecycle, taskID, func(ctx context.Context) (workflowstore.ManualMoveResult, error) {
		c.mu.Lock()
		if err := c.ensureTaskQuiescentLocked(taskID); err != nil {
			c.mu.Unlock()
			return workflowstore.ManualMoveResult{}, err
		}
		c.mu.Unlock()
		var starts []currentNodeQueuedStart
		var pending []*pendingCurrentNodeAssignmentEnsure
		var staged []stagedCurrentNodeRun
		moved, err := c.publication.PublishManualMove(
			ctx,
			prepared,
			candidate,
			func(result workflowstore.ManualMoveResult) (
				workflowstore.TaskLifecycleDelta,
				func(error),
				error,
			) {
				var stageErr error
				starts, stageErr = c.currentNodeExplicitStarts(ctx, result.Mutation.Created)
				if stageErr != nil {
					return workflowstore.TaskLifecycleDelta{}, nil, stageErr
				}
				starts, pending = pendingCurrentNodeAssignmentStarts(starts)
				staged, stageErr = c.stageCurrentNodeRunCreations(starts)
				if stageErr != nil {
					return workflowstore.TaskLifecycleDelta{}, nil, stageErr
				}
				delta, stageErr := quiescentCurrentNodeReplacementLifecycleDelta(result.Mutation, starts)
				if stageErr != nil {
					c.rollbackStagedRunCreations(staged, stageErr)
					return workflowstore.TaskLifecycleDelta{}, nil, stageErr
				}
				return delta, func(cause error) {
					c.rollbackStagedRunCreations(staged, cause)
				}, nil
			},
		)
		if err != nil {
			return workflowstore.ManualMoveResult{}, err
		}
		if moved.Outcome == workflowstore.ManualMoveResultOutcomeNoOp {
			return moved, nil
		}
		resolveErr := c.resolvePendingCurrentNodeAssignmentEnsures(ctx, starts, pending)
		outcome := waitCurrentNodeAssignmentEnsures(ctx, starts)
		if len(outcome.pending) > 0 {
			c.continuePublishedCurrentNodeAssignmentStarts(
				stagedCurrentNodeRunsForStarts(staged, outcome.pending),
				outcome.pending,
			)
		}
		if err := errors.Join(
			resolveErr,
			c.completePublishedCurrentNodeAssignmentStarts(ctx, staged, outcome),
		); err != nil {
			return moved, err
		}
		return moved, nil
	})
}

// EnsureTaskQuiescent rejects Task-wide state replacement while the
// controller owns live, admitted, or automatic work for the Task. Destructive
// callers retain their outer mutation permit while invoking it.
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

func (c *CurrentNodeController) RunTaskDeletion(
	ctx context.Context,
	taskIDs []workflow.TaskID,
	operation func(context.Context) error,
) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if operation == nil {
		return errors.New("Task deletion operation is required")
	}
	return c.lifecycle.RunTasks(ctx, taskIDs, func(ctx context.Context) error {
		c.mu.Lock()
		for _, taskID := range taskIDs {
			if err := c.ensureTaskQuiescentLocked(taskID); err != nil {
				c.mu.Unlock()
				return err
			}
		}
		c.mu.Unlock()
		return operation(ctx)
	})
}

func (c *CurrentNodeController) DeleteTask(
	ctx context.Context,
	taskID workflow.TaskID,
) (workflowstore.DeleteTaskResult, error) {
	if c == nil {
		return workflowstore.DeleteTaskResult{}, errors.New("current node workflow controller is required")
	}
	return runTaskLifecycle(ctx, c.lifecycle, taskID, func(ctx context.Context) (workflowstore.DeleteTaskResult, error) {
		c.mu.Lock()
		err := c.ensureTaskQuiescentLocked(taskID)
		c.mu.Unlock()
		if err != nil {
			return workflowstore.DeleteTaskResult{}, err
		}
		return c.publication.PublishTaskDeletion(ctx, taskID)
	})
}

func (c *CurrentNodeController) DeleteWorkflow(
	ctx context.Context,
	req workflowstore.WorkflowDeleteRequest,
) (workflowstore.WorkflowDeleteResult, error) {
	if c == nil {
		return workflowstore.WorkflowDeleteResult{}, errors.New("current node workflow controller is required")
	}
	return c.publication.PublishWorkflowDeletion(ctx, req)
}

func (c *CurrentNodeController) DeleteProject(
	ctx context.Context,
	req workflowstore.ProjectDeleteRequest,
) ([]serverapi.ProjectDeleteBlocker, error) {
	if c == nil {
		return nil, errors.New("current node workflow controller is required")
	}
	return c.publication.PublishProjectDeletion(ctx, req)
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
	if err := c.ensureTaskAvailableLocked(taskID); err != nil {
		return err
	}
	if !c.taskExecutionQuiescentLocked(taskID) {
		return ErrTaskExecutionNotQuiescent
	}
	return nil
}

func (c *CurrentNodeController) taskQuiescentLocked(taskID workflow.TaskID) (bool, error) {
	if c.closed {
		return false, errors.New("current node workflow controller is closed")
	}
	return c.taskExecutionQuiescentLocked(taskID), nil
}

func (c *CurrentNodeController) taskExecutionQuiescentLocked(taskID workflow.TaskID) bool {
	if c.interrupts.taskActive(taskID) {
		return false
	}
	for _, run := range c.runs.byCurrentNode {
		if run.reference.TaskID == taskID {
			return false
		}
	}
	return true
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
