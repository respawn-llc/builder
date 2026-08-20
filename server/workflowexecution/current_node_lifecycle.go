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
)

const ReasonCurrentNodeStartupRecovery workflow.CurrentNodeInterruptionReason = "workflow_startup_recovery"

type ManualMoveDisposition string

const (
	ManualMoveDispositionQuiescent         ManualMoveDisposition = "quiescent"
	ManualMoveDispositionAutoInterruptible ManualMoveDisposition = "auto_interruptible"
	ManualMoveDispositionLifecycleConflict ManualMoveDisposition = "lifecycle_conflict"
)

var ErrManualMoveLifecycleConflict = errors.New("workflow task has a non-interruptible lifecycle conflict")

type TaskStartPreparation struct {
	Prepare func(context.Context) error
	Commit  func(context.Context) error
}

func (p TaskStartPreparation) validate() error {
	if p.Prepare == nil {
		return errors.New("task preparation runner is required")
	}
	if p.Commit == nil {
		return errors.New("task preparation commit is required")
	}
	return nil
}

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
	if state.WaitingApprovals > 0 || state.Queued > 0 || state.Finalizing > 0 {
		return ManualMoveDispositionLifecycleConflict, nil
	}
	if state.Running > 0 || state.WaitingQuestions > 0 {
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
	committed, diagnostic := classifyCurrentNodeInterruption(err)
	if err != nil && !committed {
		return 0, err
	}
	for _, reference := range recovered {
		c.publishPendingInterruptedCurrentNode(ctx, reference, ReasonCurrentNodeStartupRecovery)
	}
	if diagnostic != nil {
		c.mu.Lock()
		c.workerDiagnostics = errors.Join(c.workerDiagnostics, diagnostic)
		c.mu.Unlock()
	}
	return int64(len(recovered)), nil
}

func (c *CurrentNodeController) StartTask(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation TaskStartPreparation,
	finalizer TaskPreparationFinalizer,
) (workflowstore.StartTaskResult, error) {
	if c == nil {
		return workflowstore.StartTaskResult{}, errors.New("current node workflow controller is required")
	}
	if err := preparation.validate(); err != nil {
		return workflowstore.StartTaskResult{}, err
	}
	if finalizer == nil {
		return workflowstore.StartTaskResult{}, errors.New("task start preparation finalizer is required")
	}
	return runCurrentNodeTaskMutation(ctx, c, taskID, func(ctx context.Context) (workflowstore.StartTaskResult, error) {
		c.mu.Lock()
		if err := c.ensureTaskQuiescentLocked(taskID); err != nil {
			c.mu.Unlock()
			return workflowstore.StartTaskResult{}, err
		}
		c.mu.Unlock()
		started, err := c.store.StartTask(ctx, taskID)
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
		batch, err := newTaskPreparationBatch(c.workerContext, taskID, []currentNodeQueuedStart{{
			reference:          started.Mutation.Created[0].Reference,
			taskPromptDelivery: workflowruntime.TaskPromptDeliveryAssignment,
		}}, preparation, finalizer)
		if err != nil {
			return workflowstore.StartTaskResult{}, err
		}
		if err := c.queueTaskPreparationBatchLocked(batch); err != nil {
			return workflowstore.StartTaskResult{}, err
		}
		return started, nil
	})
}

type TaskResumeOutcome string

const (
	TaskResumeApplied TaskResumeOutcome = "applied"
	TaskResumeNoOp    TaskResumeOutcome = "no_op"
)

type TaskResumeResult struct {
	Outcome      TaskResumeOutcome
	CurrentNodes []workflow.CurrentNode
}

type TaskResumePreflightOutcome string

const (
	TaskResumePreflightResumable TaskResumePreflightOutcome = "resumable"
	TaskResumePreflightNoOp      TaskResumePreflightOutcome = "no_op"
)

type TaskResumePreflight struct {
	Outcome      TaskResumePreflightOutcome
	CurrentNodes []workflow.CurrentNode
}

func (c *CurrentNodeController) ResumeTask(ctx context.Context, taskID workflow.TaskID) (TaskResumeResult, error) {
	return c.resumeTask(ctx, taskID, nil, nil)
}

// PromoteConcurrencyQueuedTask moves automatic Current Nodes waiting for agent
// capacity into the explicit admission lane. Explicit admission intentionally
// bypasses the automatic concurrency limit.
func (c *CurrentNodeController) PromoteConcurrencyQueuedTask(
	ctx context.Context,
	taskID workflow.TaskID,
) ([]workflow.CurrentNode, bool, error) {
	if c == nil {
		return nil, false, errors.New("current node workflow controller is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, false, errors.New("workflow task id is required")
	}
	var promoted []workflow.CurrentNode
	err := c.runTaskMutation(ctx, taskID, func(context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.closed {
			return errors.New("current node workflow controller is closed")
		}
		starts := c.automaticQueue.removeTask(taskID)
		for _, start := range starts {
			key, err := start.reference.Key()
			if err != nil {
				return err
			}
			delete(c.queued, key)
			start.policy = currentNodeAdmissionExplicitOverride
			c.explicitQueue = append(c.explicitQueue, start)
			c.explicitQueued[key] = struct{}{}
			promoted = append(promoted, workflow.CurrentNode{Reference: start.reference})
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if len(promoted) == 0 {
		return nil, false, nil
	}
	c.wakeAdmissionWorker()
	return promoted, true, nil
}

func (c *CurrentNodeController) ResumeTaskWithPreparation(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation TaskStartPreparation,
	finalizer TaskPreparationFinalizer,
) (TaskResumeResult, error) {
	if err := preparation.validate(); err != nil {
		return TaskResumeResult{}, err
	}
	if finalizer == nil {
		return TaskResumeResult{}, errors.New("task resume preparation finalizer is required")
	}
	return c.resumeTask(ctx, taskID, &preparation, finalizer)
}

type TaskResumeConflictError struct {
	TaskID workflow.TaskID
}

func (e *TaskResumeConflictError) Error() string {
	return fmt.Sprintf("task %q has no interrupted executable Current Nodes to resume", e.TaskID)
}

func (c *CurrentNodeController) PreflightTaskResume(
	ctx context.Context,
	taskID workflow.TaskID,
) (TaskResumePreflight, error) {
	if c == nil {
		return TaskResumePreflight{}, errors.New("current node workflow controller is required")
	}
	return runCurrentNodeTaskMutation(ctx, c, taskID, func(ctx context.Context) (TaskResumePreflight, error) {
		classification, err := c.classifyTaskResume(ctx, taskID)
		if err != nil {
			return TaskResumePreflight{}, err
		}
		if len(classification.alreadyResumed) != 0 {
			return TaskResumePreflight{
				Outcome:      TaskResumePreflightNoOp,
				CurrentNodes: classification.alreadyResumed,
			}, nil
		}
		if err := classification.eligibilityError(); err != nil {
			return TaskResumePreflight{}, err
		}
		return TaskResumePreflight{
			Outcome:      TaskResumePreflightResumable,
			CurrentNodes: classification.resumable,
		}, nil
	})
}

type taskResumeClassification struct {
	resumable      []workflow.CurrentNode
	alreadyResumed []workflow.CurrentNode
	validationErr  error
}

func (c *CurrentNodeController) classifyTaskResume(
	ctx context.Context,
	taskID workflow.TaskID,
) (taskResumeClassification, error) {
	c.mu.Lock()
	if err := c.ensureTaskAvailableLocked(taskID); err != nil {
		c.mu.Unlock()
		return taskResumeClassification{}, err
	}
	c.mu.Unlock()
	classifications, err := c.store.PreflightTaskResume(ctx, taskID)
	if err != nil {
		return taskResumeClassification{}, err
	}
	if len(classifications) == 0 {
		currentNodes, err := c.store.ListCurrentNodes(ctx, taskID)
		if err != nil {
			return taskResumeClassification{}, err
		}
		for _, currentNode := range currentNodes {
			if currentNode.Scheduling == nil {
				continue
			}
			switch currentNode.Scheduling.State {
			case workflow.CurrentNodeSchedulingReady, workflow.CurrentNodeSchedulingAdmitted:
				return taskResumeClassification{alreadyResumed: currentNodes}, nil
			}
		}
		return taskResumeClassification{}, &TaskResumeConflictError{TaskID: taskID}
	}
	result := taskResumeClassification{
		resumable: make([]workflow.CurrentNode, 0, len(classifications)),
	}
	var validationErrs []error
	for _, classification := range classifications {
		if validationErr := classification.ValidationError(); validationErr != nil {
			validationErrs = append(validationErrs, validationErr)
			continue
		}
		result.resumable = append(result.resumable, classification.CurrentNode)
	}
	result.validationErr = errors.Join(validationErrs...)
	return result, nil
}

func (c taskResumeClassification) eligibilityError() error {
	if len(c.resumable) != 0 {
		return nil
	}
	return c.validationErr
}

func (c *CurrentNodeController) resumeTask(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation *TaskStartPreparation,
	finalizer TaskPreparationFinalizer,
) (TaskResumeResult, error) {
	if c == nil {
		return TaskResumeResult{}, errors.New("current node workflow controller is required")
	}
	if preparation != nil && finalizer == nil {
		return TaskResumeResult{}, errors.New("task resume preparation finalizer is required")
	}
	result, err := runCurrentNodeTaskMutation(ctx, c, taskID, func(ctx context.Context) (TaskResumeResult, error) {
		var resolution workflowstore.TaskAttentionResolution
		classification, err := c.classifyTaskResume(ctx, taskID)
		if err != nil {
			return TaskResumeResult{}, err
		}
		if len(classification.alreadyResumed) != 0 {
			return TaskResumeResult{
				Outcome:      TaskResumeNoOp,
				CurrentNodes: classification.alreadyResumed,
			}, nil
		}
		var resumeErrs []error
		if classification.validationErr != nil {
			resumeErrs = append(resumeErrs, classification.validationErr)
		}
		eligible := make([]workflow.CurrentNode, 0, len(classification.resumable))
		eligibleStarts := make([]currentNodeQueuedStart, 0, len(classification.resumable))
		seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(classification.resumable))
		for _, currentNode := range classification.resumable {
			key, keyErr := currentNode.Reference.Key()
			if keyErr != nil {
				resumeErrs = append(resumeErrs, keyErr)
				continue
			}
			if currentNode.SessionID != nil {
				if active, exists := c.authority.SessionExecution(*currentNode.SessionID); exists {
					resumeErrs = append(
						resumeErrs,
						fmt.Errorf(
							"resume current node %v: retained Session %s already has active execution scope %s: %w",
							currentNode.Reference,
							*currentNode.SessionID,
							active.Scope().ID(),
							ErrTaskExecutionNotQuiescent,
						),
					)
					continue
				}
			}
			if _, duplicate := seen[key]; duplicate {
				resumeErrs = append(resumeErrs, fmt.Errorf("resumable current node %v is duplicated", currentNode.Reference))
				continue
			}
			seen[key] = struct{}{}
			promptDelivery := workflowruntime.TaskPromptDeliveryResume
			if currentNode.AgentExecutionSelection != nil && currentNode.SessionID == nil {
				promptDelivery = workflowruntime.TaskPromptDeliveryAssignment
			}
			eligible = append(eligible, currentNode)
			eligibleStarts = append(eligibleStarts, currentNodeQueuedStart{
				reference:          currentNode.Reference,
				taskPromptDelivery: promptDelivery,
			})
		}
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(taskID); err != nil {
			c.mu.Unlock()
			return TaskResumeResult{}, errors.Join(errors.Join(resumeErrs...), err)
		}
		for _, start := range eligibleStarts {
			key, _ := start.reference.Key()
			if c.currentNodeOwnedLocked(key) {
				c.mu.Unlock()
				return TaskResumeResult{}, errors.Join(
					errors.Join(resumeErrs...),
					fmt.Errorf("current node %v cannot resume while controller ownership remains: %w", start.reference, ErrTaskExecutionNotQuiescent),
				)
			}
		}
		c.mu.Unlock()

		resumed := make([]workflow.CurrentNode, 0, len(eligible))
		starts := make([]currentNodeQueuedStart, 0, len(eligible))
		for index, currentNode := range eligible {
			if err := c.store.RepairCurrentNodeSessionProvenanceForResume(ctx, currentNode); err != nil {
				resumeErrs = append(resumeErrs, fmt.Errorf(
					"repair retained current node provenance %v: %w",
					currentNode.Reference,
					err,
				))
				continue
			}
			projection, found, err := c.store.ResumeCurrentNode(ctx, currentNode.Reference)
			if err != nil {
				resumeErrs = append(resumeErrs, fmt.Errorf("resume current node %v: %w", currentNode.Reference, err))
				continue
			}
			if found {
				resolution.InterruptedCurrentNodes = append(resolution.InterruptedCurrentNodes, projection)
			}
			starts = append(starts, eligibleStarts[index])
			resumed = append(resumed, currentNode)
		}
		c.finalizeTaskAttentionResolution(resolution)
		c.mu.Lock()
		if preparation == nil {
			for _, start := range starts {
				if queueErr := c.queueExplicitStartLocked(start); queueErr != nil {
					resumeErrs = append(resumeErrs, fmt.Errorf("queue resumed current node %v: %w", start.reference, queueErr))
				}
			}
		} else if len(starts) > 0 {
			batch, batchErr := newTaskPreparationBatch(c.workerContext, taskID, starts, *preparation, finalizer)
			if batchErr == nil {
				batchErr = c.queueTaskPreparationBatchLocked(batch)
			}
			if batchErr != nil {
				resumeErrs = append(resumeErrs, batchErr)
			}
		}
		c.mu.Unlock()
		return TaskResumeResult{
			Outcome:      TaskResumeApplied,
			CurrentNodes: resumed,
		}, errors.Join(resumeErrs...)
	})
	return result, err
}

func (c *CurrentNodeController) ApplyPendingApproval(
	ctx context.Context,
	approvalID workflow.ApprovalID,
) (workflowstore.PendingApprovalApplyResult, error) {
	if c == nil {
		return workflowstore.PendingApprovalApplyResult{}, errors.New("current node workflow controller is required")
	}
	initial, err := c.store.PendingApproval(ctx, approvalID)
	if err != nil {
		return workflowstore.PendingApprovalApplyResult{}, err
	}
	return runCurrentNodeTaskMutation(ctx, c, initial.Source.TaskID, func(ctx context.Context) (workflowstore.PendingApprovalApplyResult, error) {
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
			_, assignmentErr := c.classifyAutomaticStarts(ctx, starts, nil)
			return applied, assignmentErr
		}
		handle, live := c.authority.ExecutionByScope(sourceScopeID)
		if !live {
			return workflowstore.PendingApprovalApplyResult{}, sessionruntime.ErrExecutionNoLongerLive
		}
		var applied workflowstore.PendingApprovalApplyResult
		var starts []currentNodeQueuedStart
		var prepared *currentNodeAssignmentBatch
		err = c.authority.WithExactExecutions([]sessionruntime.ExecutionHandle{handle}, func() error {
			var applyErr error
			applied, starts, applyErr = apply()
			if applyErr != nil {
				return applyErr
			}
			prepared = c.prepareCurrentNodeAssignments(ctx, starts, true, &sourceScopeID)
			return nil
		})
		if err != nil {
			return applied, err
		}
		_, assignmentErr := c.classifyPreparedAutomaticStarts(prepared)
		return applied, assignmentErr
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
	return runCurrentNodeTaskMutation(ctx, c, taskID, func(ctx context.Context) (workflowstore.ManualMoveResult, error) {
		c.mu.Lock()
		if err := c.ensureTaskQuiescentLocked(taskID); err != nil {
			c.mu.Unlock()
			return workflowstore.ManualMoveResult{}, err
		}
		c.mu.Unlock()
		var (
			preparedAssignments  map[workflow.CurrentNodeReferenceKey]CurrentNodeAssignmentSteer
			assignmentDiagnostic error
		)
		moved, err := c.store.ApplyManualMoveWithTargetAssignments(
			ctx,
			prepared,
			candidate,
			func(
				ctx context.Context,
				contexts []workflowstore.CurrentNodeStartContext,
			) (workflowstore.ManualMoveTargetAssignmentPreparation, error) {
				preparation, steers, err := c.steerer.PrepareManualMoveAssignments(ctx, contexts)
				if err != nil {
					return preparation, err
				}
				for _, input := range contexts {
					if input.CurrentNode.AgentExecutionSelection == nil {
						continue
					}
					key, keyErr := input.CurrentNode.Reference.Key()
					if keyErr != nil {
						return preparation, keyErr
					}
					if steers[key] == nil {
						return preparation, fmt.Errorf("manual move target %v has no prepared assignment steer", input.CurrentNode.Reference)
					}
				}
				preparedAssignments = steers
				assignmentDiagnostic = preparation.Diagnostic
				return preparation, nil
			},
		)
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
		for index := range starts {
			if !starts[index].requiresAssignment {
				continue
			}
			key, err := starts[index].reference.Key()
			if err != nil {
				return moved, err
			}
			steer := preparedAssignments[key]
			if steer == nil {
				return moved, fmt.Errorf("Manual Move target %v has no prepared assignment", starts[index].reference)
			}
			starts[index].assignment = newCurrentNodeClassifiedAssignment(starts[index].reference, steer)
		}
		c.deliverClassifiedStarts(starts, nil)
		return moved, assignmentDiagnostic
	})
}

// EnsureTaskQuiescent rejects Task-wide state replacement while the
// controller owns live, admitted, or automatic work for the Task. Callers
// hold the Task's mutation ownership while invoking it and applying the
// durable replacement.
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
	if c.interrupts.taskActive(taskID) {
		return false
	}
	if c.queuedTaskPreparationLocked(taskID) != nil || c.runningTaskPreparationLocked(taskID) != nil {
		return false
	}
	for _, gate := range c.gates {
		if gate.reference.TaskID == taskID {
			return false
		}
	}
	for _, live := range c.live {
		if live.reference.TaskID == taskID {
			return false
		}
	}
	for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
		start := entry.start
		if start.reference.TaskID == taskID {
			return false
		}
	}
	for _, intent := range c.automaticReservations {
		if intent.reference.TaskID == taskID {
			return false
		}
	}
	for _, start := range c.explicitQueue {
		if start.reference.TaskID == taskID {
			return false
		}
	}
	for _, start := range c.explicitReservations {
		if start.reference.TaskID == taskID {
			return false
		}
	}
	for _, start := range c.admissionWorkers {
		if start.reference.TaskID == taskID {
			return false
		}
	}
	for _, starts := range c.heldStarts {
		for _, start := range starts {
			if start.reference.TaskID == taskID {
				return false
			}
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
