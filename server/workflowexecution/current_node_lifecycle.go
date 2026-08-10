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
	recovered, err := c.store.RecoverExecutableCurrentNodes(
		ctx,
		ReasonCurrentNodeStartupRecovery,
		workflow.NewCurrentNodeInterruptionDetail(string(ReasonCurrentNodeStartupRecovery), nil),
	)
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
	return RunTaskMutation(ctx, c.taskMutations, taskID, func(ctx context.Context) (workflowstore.StartTaskResult, error) {
		c.mu.Lock()
		if err := c.ensureTaskQuiescentLocked(taskID); err != nil {
			c.mu.Unlock()
			return workflowstore.StartTaskResult{}, err
		}
		c.mu.Unlock()
		prepared, err := c.store.PrepareTaskStart(ctx, taskID)
		if err != nil {
			return workflowstore.StartTaskResult{}, err
		}
		result := prepared.Result()
		if len(result.Mutation.Created) != 1 || len(result.CreatedExecutableCurrentNodes) != 1 {
			_ = prepared.Rollback()
			return workflowstore.StartTaskResult{}, errors.New("task start did not create exactly one executable current node")
		}
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(taskID); err != nil {
			c.mu.Unlock()
			_ = prepared.Rollback()
			return workflowstore.StartTaskResult{}, err
		}
		runIDs, err := c.stageExplicitRunsLocked(
			result.CreatedExecutableCurrentNodes,
			preparation,
			workflowruntime.TaskPromptDeliveryAssignment,
		)
		if err != nil {
			c.mu.Unlock()
			return workflowstore.StartTaskResult{}, errors.Join(err, prepared.Rollback())
		}
		if err := c.validateStagedRunsLocked(runIDs); err != nil {
			c.discardStagedRunsLocked(runIDs)
			c.mu.Unlock()
			return workflowstore.StartTaskResult{}, errors.Join(err, prepared.Rollback())
		}
		if err := prepared.Commit(); err != nil {
			c.discardStagedRunsLocked(runIDs)
			c.mu.Unlock()
			return workflowstore.StartTaskResult{}, err
		}
		c.installStagedRunsLocked(runIDs)
		c.mu.Unlock()
		c.wakeAdmissionWorker()
		return workflowstore.StartTaskResult{Mutation: result.Mutation}, nil
	})
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
	var resolution workflowstore.TaskAttentionResolution
	resumed, err := RunTaskMutation(ctx, c.taskMutations, taskID, func(ctx context.Context) ([]workflow.CurrentNode, error) {
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(taskID); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		c.mu.Unlock()
		prepared, err := c.store.PrepareTaskResume(ctx, taskID)
		if err != nil {
			return nil, err
		}
		result := prepared.Result()
		if len(result.CreatedExecutableCurrentNodes) == 0 {
			_ = prepared.Rollback()
			c.mu.Lock()
			existing := c.currentTaskRunNodesLocked(taskID)
			c.mu.Unlock()
			if len(existing) != 0 {
				return existing, nil
			}
			return nil, &TaskResumeConflictError{TaskID: taskID}
		}
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(taskID); err != nil {
			c.mu.Unlock()
			return nil, errors.Join(err, prepared.Rollback())
		}
		runIDs, err := c.stageExplicitRunsLocked(
			result.CreatedExecutableCurrentNodes,
			preparation,
			workflowruntime.TaskPromptDeliveryResume,
		)
		if err != nil {
			c.mu.Unlock()
			return nil, errors.Join(err, prepared.Rollback())
		}
		if err := c.validateStagedRunsLocked(runIDs); err != nil {
			c.discardStagedRunsLocked(runIDs)
			c.mu.Unlock()
			return nil, errors.Join(err, prepared.Rollback())
		}
		if err := prepared.Commit(); err != nil {
			c.discardStagedRunsLocked(runIDs)
			c.mu.Unlock()
			return nil, err
		}
		c.installStagedRunsLocked(runIDs)
		c.mu.Unlock()
		c.wakeAdmissionWorker()
		resolution = result.TaskAttentionResolution
		return result.CreatedExecutableCurrentNodes, nil
	})
	c.finalizeTaskAttentionResolution(resolution)
	return resumed, err
}

func (c *CurrentNodeController) currentTaskRunNodesLocked(taskID workflow.TaskID) []workflow.CurrentNode {
	var nodes []workflow.CurrentNode
	for _, run := range c.runs {
		if run.reference.TaskID != taskID ||
			(run.phase != currentNodeRunStaged &&
				run.phase != currentNodeRunQueued &&
				run.phase != currentNodeRunLaunching) {
			continue
		}
		node, err := workflow.NewCurrentNode(
			run.reference,
			nil,
			&workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
		)
		if err != nil {
			panic(err)
		}
		nodes = append(nodes, node)
	}
	return nodes
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
	return RunTaskMutation(ctx, c.taskMutations, approval.Source.TaskID, func(ctx context.Context) (workflowstore.PendingApprovalApplyResult, error) {
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
		sourceRun, sourceLive := c.currentRunLocked(sourceKey)
		sourceLive = sourceLive && sourceRun.exact()
		var sourceScopeID runtimeids.ExecutionScopeID
		if sourceLive {
			sourceScopeID = *sourceRun.exactScopeID
			if !sourceRun.completion.committed() {
				c.mu.Unlock()
				return workflowstore.PendingApprovalApplyResult{}, errors.New("pending approval source scope has not completed")
			}
			if sourceRun.postTurn != nil {
				c.mu.Unlock()
				return workflowstore.PendingApprovalApplyResult{}, ErrTaskExecutionNotQuiescent
			}
			if sourceRun.stopping() {
				c.mu.Unlock()
				return workflowstore.PendingApprovalApplyResult{}, sessionruntime.ErrExecutionNoLongerLive
			}
		}
		c.mu.Unlock()

		apply := func() (workflowstore.PendingApprovalApplyResult, []currentNodeQueuedStart, error) {
			applied, err := c.store.ApplyPendingApproval(ctx, approvalID)
			if err != nil {
				return workflowstore.PendingApprovalApplyResult{}, nil, err
			}
			starts, err := currentNodeExplicitStarts(applied.Mutation.Created)
			if err != nil {
				return workflowstore.PendingApprovalApplyResult{}, nil, err
			}
			return applied, starts, nil
		}
		if !sourceLive {
			applied, starts, err := apply()
			if err != nil {
				return workflowstore.PendingApprovalApplyResult{}, err
			}
			starts, err = c.steerAndWaitStarts(ctx, starts, recoverCommittedCurrentNodeStarts)
			if err != nil {
				return workflowstore.PendingApprovalApplyResult{}, err
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			for _, start := range starts {
				if err := c.queueExplicitStartLocked(start); err != nil {
					return workflowstore.PendingApprovalApplyResult{}, err
				}
			}
			return applied, nil
		}
		handle, live := c.authority.ExecutionByScope(sourceScopeID)
		if !live {
			return workflowstore.PendingApprovalApplyResult{}, sessionruntime.ErrExecutionNoLongerLive
		}
		var applied workflowstore.PendingApprovalApplyResult
		var starts []currentNodeQueuedStart
		var pending []*pendingCurrentNodeAssignmentSteer
		var successorRunIDs []currentNodeRunID
		err = c.authority.WithExactExecutions([]sessionruntime.ExecutionHandle{handle}, func() error {
			var applyErr error
			applied, starts, applyErr = apply()
			if applyErr != nil {
				return applyErr
			}
			starts, pending = pendingCurrentNodeAssignmentStarts(starts)
			c.mu.Lock()
			runIDs, stageErr := c.stageSuccessorRunsLocked(starts, sourceRun.id, currentNodeRunHeld)
			if stageErr == nil {
				successorRunIDs = append([]currentNodeRunID(nil), runIDs...)
				sourceRun.successors = append(sourceRun.successors, runIDs...)
			}
			c.mu.Unlock()
			return stageErr
		})
		if err != nil {
			return workflowstore.PendingApprovalApplyResult{}, err
		}
		if err := c.resolvePendingCurrentNodeAssignmentSteers(ctx, starts, pending); err != nil {
			return workflowstore.PendingApprovalApplyResult{}, err
		}
		if sourceRun.completion == currentNodeRunCompletionScriptSucceeded {
			c.mu.Lock()
			for index, id := range successorRunIDs {
				c.queueRunLocked(id, starts[index].assignmentSteer)
			}
			c.mu.Unlock()
			c.wakeAdmissionWorker()
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
	return RunTaskMutation(ctx, c.taskMutations, taskID, func(ctx context.Context) (workflowstore.ManualMoveResult, error) {
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
		if moved.Outcome == workflowstore.ManualMoveResultOutcomeNoOp {
			return moved, nil
		}
		starts, err := currentNodeExplicitStarts(moved.Mutation.Created)
		if err != nil {
			return moved, err
		}
		starts, err = c.steerAndWaitStarts(ctx, starts, recoverAllCurrentNodeStarts)
		if err != nil {
			return moved, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, start := range starts {
			if err := c.queueExplicitStartLocked(start); err != nil {
				return moved, err
			}
		}
		return moved, nil
	})
}

// EnsureTaskQuiescent rejects Task-wide state replacement while the
// controller owns live, admitted, or automatic work for the Task. Mutation
// callers hold the Task writer while invoking it and applying durable changes.
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
	if c.taskInterruptActiveLocked(taskID) {
		return false
	}
	for _, run := range c.runs {
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
	for _, run := range c.runs {
		if run.reference.TaskID == taskID && run.callbackErr != nil {
			return fmt.Errorf("workflow execution lifecycle failed for Run %d: %w", run.id.sequence, run.callbackErr)
		}
	}
	if c.taskInterruptActiveLocked(taskID) {
		return ErrTaskExecutionNotQuiescent
	}
	return nil
}
