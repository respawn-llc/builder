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

const ReasonCurrentNodeStartupRecovery workflow.CurrentNodeInterruptionReason = "workflow_startup_recovery"

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
		if err := c.EnsureTaskQuiescent(taskID); err != nil {
			return workflowstore.StartTaskResult{}, err
		}
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
	result, _, err := c.resumeTask(ctx, taskID, nil, nil, nil)
	return result, err
}

func (c *CurrentNodeController) ReactivateWorkflowSession(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) (sessionruntime.ExecutionHandle, error) {
	if c == nil {
		return nil, errors.New("current node workflow controller is required")
	}
	if sessionID.IsZero() {
		return nil, errors.New("session id is required")
	}
	input, err := c.store.ResolveCurrentSessionStartContext(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if handle, live := c.authority.SessionExecution(sessionID); live {
		return c.validateReactivatedWorkflowExecution(handle, sessionID, input.CurrentNode.Reference)
	}
	result, completion, err := c.resumeTask(
		ctx,
		input.Task.ID,
		nil,
		nil,
		&input.CurrentNode.Reference,
	)
	if err != nil {
		return nil, err
	}
	if result.Outcome != TaskResumeApplied && result.Outcome != TaskResumeNoOp {
		return nil, fmt.Errorf("workflow Session reactivation returned invalid Resume outcome %q", result.Outcome)
	}
	if completion != nil {
		handle, waitErr := completion.wait(ctx)
		if waitErr != nil {
			return nil, fmt.Errorf("reactivate workflow Session %s: %w", sessionID, waitErr)
		}
		return c.validateReactivatedWorkflowExecution(handle, sessionID, input.CurrentNode.Reference)
	}
	handle, live := c.authority.SessionExecution(sessionID)
	if !live {
		return nil, fmt.Errorf(
			"reactivate workflow Session %s: Current Node %v has no admission owner or live execution",
			sessionID,
			input.CurrentNode.Reference,
		)
	}
	return c.validateReactivatedWorkflowExecution(handle, sessionID, input.CurrentNode.Reference)
}

func (c *CurrentNodeController) validateReactivatedWorkflowExecution(
	handle sessionruntime.ExecutionHandle,
	sessionID runtimeids.SessionID,
	currentNode workflow.CurrentNodeReference,
) (sessionruntime.ExecutionHandle, error) {
	if handle == nil {
		return nil, errors.New("reactivated workflow execution is absent")
	}
	scope := handle.Scope()
	if scope.Kind() != sessionruntime.ExecutionScopeAgent {
		return nil, fmt.Errorf(
			"reactivated workflow Session %s started non-Agent execution scope %s",
			sessionID,
			scope.ID(),
		)
	}
	resource, hasResource := scope.Resource()
	workflowRef, workflowScoped := scope.Workflow()
	if !hasResource ||
		resource.SessionID() != sessionID ||
		!workflowScoped ||
		!workflowRef.CurrentNode.Equal(currentNode) {
		return nil, fmt.Errorf(
			"reactivated workflow Session %s started mismatched execution scope %s",
			sessionID,
			scope.ID(),
		)
	}
	live, exists := c.authority.SessionExecution(sessionID)
	if !exists {
		return nil, fmt.Errorf(
			"reactivated workflow Session %s returned execution scope %s before publication",
			sessionID,
			scope.ID(),
		)
	}
	liveScope := live.Scope()
	liveWorkflowRef, liveWorkflowScoped := liveScope.Workflow()
	if liveScope.ID() != scope.ID() ||
		!liveWorkflowScoped ||
		!liveWorkflowRef.CurrentNode.Equal(currentNode) {
		return nil, fmt.Errorf(
			"reactivated workflow Session %s returned execution scope %s that does not match live scope %s",
			sessionID,
			scope.ID(),
			liveScope.ID(),
		)
	}
	return live, nil
}

func (c *CurrentNodeController) currentNodeAdmissionCompletionLocked(
	key workflow.CurrentNodeReferenceKey,
) *currentNodeAdmissionCompletion {
	for _, batch := range c.preparationQueue {
		for _, start := range batch.starts {
			startKey, err := start.reference.Key()
			if err != nil {
				panic(fmt.Sprintf("inspect queued Task preparation admission: %v", err))
			}
			if startKey == key {
				return start.completion
			}
		}
	}
	for _, batch := range c.preparationRunning {
		for _, start := range batch.starts {
			startKey, err := start.reference.Key()
			if err != nil {
				panic(fmt.Sprintf("inspect running Task preparation admission: %v", err))
			}
			if startKey == key {
				return start.completion
			}
		}
	}
	for _, start := range c.explicitQueue {
		startKey, err := start.reference.Key()
		if err != nil {
			panic(fmt.Sprintf("inspect explicit admission queue: %v", err))
		}
		if startKey == key {
			return start.completion
		}
	}
	for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
		startKey, err := entry.start.reference.Key()
		if err != nil {
			panic(fmt.Sprintf("inspect automatic admission queue: %v", err))
		}
		if startKey == key {
			return entry.start.completion
		}
	}
	if start, exists := c.explicitReservations[key]; exists {
		return start.completion
	}
	if start, exists := c.automaticReservations[key]; exists {
		return start.completion
	}
	if start, exists := c.admissionWorkers[key]; exists {
		return start.completion
	}
	return nil
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
	result, _, err := c.resumeTask(ctx, taskID, &preparation, finalizer, nil)
	return result, err
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
		classification, err := c.classifyTaskResume(ctx, taskID, nil)
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
	selected *workflow.CurrentNodeReference,
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
			if selected != nil && !currentNode.Reference.Equal(*selected) {
				continue
			}
			if currentNode.Scheduling == nil {
				continue
			}
			switch currentNode.Scheduling.State {
			case workflow.CurrentNodeSchedulingReady, workflow.CurrentNodeSchedulingAdmitted:
				return taskResumeClassification{alreadyResumed: []workflow.CurrentNode{currentNode}}, nil
			}
		}
		return taskResumeClassification{}, &TaskResumeConflictError{TaskID: taskID}
	}
	result := taskResumeClassification{
		resumable: make([]workflow.CurrentNode, 0, len(classifications)),
	}
	var validationErrs []error
	for _, classification := range classifications {
		if selected != nil && !classification.CurrentNode.Reference.Equal(*selected) {
			continue
		}
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
	watch *workflow.CurrentNodeReference,
) (TaskResumeResult, *currentNodeAdmissionCompletion, error) {
	if c == nil {
		return TaskResumeResult{}, nil, errors.New("current node workflow controller is required")
	}
	if preparation != nil && finalizer == nil {
		return TaskResumeResult{}, nil, errors.New("task resume preparation finalizer is required")
	}
	var watchedCompletion *currentNodeAdmissionCompletion
	result, err := runCurrentNodeTaskMutation(ctx, c, taskID, func(ctx context.Context) (TaskResumeResult, error) {
		var resolution workflowstore.TaskAttentionResolution
		classification, err := c.classifyTaskResume(ctx, taskID, watch)
		if err != nil {
			return TaskResumeResult{}, err
		}
		if len(classification.alreadyResumed) != 0 {
			if watch != nil {
				key, keyErr := watch.Key()
				if keyErr != nil {
					return TaskResumeResult{}, keyErr
				}
				c.mu.Lock()
				watchedCompletion = c.currentNodeAdmissionCompletionLocked(key)
				c.mu.Unlock()
			}
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
				completion:         newCurrentNodeAdmissionCompletion(),
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
				if watch != nil && start.reference.Equal(*watch) {
					key, keyErr := start.reference.Key()
					if keyErr != nil {
						resumeErrs = append(resumeErrs, keyErr)
						continue
					}
					if c.currentNodeOwnedLocked(key) {
						resumeErrs = append(
							resumeErrs,
							fmt.Errorf(
								"queue resumed current node %v while controller ownership remains: %w",
								start.reference,
								ErrTaskExecutionNotQuiescent,
							),
						)
						continue
					}
				}
				if queueErr := c.queueExplicitStartLocked(start); queueErr != nil {
					resumeErrs = append(resumeErrs, fmt.Errorf("queue resumed current node %v: %w", start.reference, queueErr))
					continue
				}
				if watch != nil && start.reference.Equal(*watch) {
					watchedCompletion = start.completion
				}
			}
		} else if len(starts) > 0 {
			batch, batchErr := newTaskPreparationBatch(c.workerContext, taskID, starts, *preparation, finalizer)
			if batchErr == nil {
				batchErr = c.queueTaskPreparationBatchLocked(batch)
			}
			if batchErr != nil {
				resumeErrs = append(resumeErrs, batchErr)
			} else if watch != nil {
				for _, start := range starts {
					if start.reference.Equal(*watch) {
						watchedCompletion = start.completion
						break
					}
				}
			}
		}
		c.mu.Unlock()
		return TaskResumeResult{
			Outcome:      TaskResumeApplied,
			CurrentNodes: resumed,
		}, errors.Join(resumeErrs...)
	})
	return result, watchedCompletion, err
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
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(approval.Source.TaskID); err != nil {
			c.mu.Unlock()
			return workflowstore.PendingApprovalApplyResult{}, err
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
		if err := c.EnsureTaskQuiescent(taskID); err != nil {
			return workflowstore.ManualMoveResult{}, err
		}
		assignmentPreparer, ok := c.steerer.(CurrentNodeManualMoveAssignmentPreparer)
		if !ok {
			return workflowstore.ManualMoveResult{}, errors.New("manual move assignment preparation is required")
		}
		manualMoveStore, ok := c.store.(interface {
			ApplyManualMoveWithTargetAssignments(
				context.Context,
				workflowstore.ManualMovePreparation,
				*workflowstore.ExecutionTargetCandidate,
				workflowstore.ManualMoveTargetAssignmentPreparer,
			) (workflowstore.ManualMoveResult, error)
		})
		if !ok {
			return workflowstore.ManualMoveResult{}, errors.New("manual move assignment store is required")
		}
		var (
			assignmentSteers     map[workflow.CurrentNodeReferenceKey]CurrentNodeAssignmentSteer
			assignmentDiagnostic error
		)
		moved, err := manualMoveStore.ApplyManualMoveWithTargetAssignments(
			ctx,
			prepared,
			candidate,
			func(
				ctx context.Context,
				contexts []workflowstore.CurrentNodeStartContext,
			) (workflowstore.ManualMoveTargetAssignmentPreparation, error) {
				preparation, steers, err := assignmentPreparer.PrepareManualMoveAssignments(ctx, contexts)
				assignmentSteers = steers
				assignmentDiagnostic = preparation.Diagnostic
				return preparation, err
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
			key, err := starts[index].reference.Key()
			if err != nil {
				return moved, err
			}
			if steer := assignmentSteers[key]; steer != nil {
				starts[index].assignmentSteer = steer
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, start := range starts {
			if err := c.queueExplicitStartLocked(start); err != nil {
				return moved, err
			}
		}
		return moved, assignmentDiagnostic
	})
}

// EnsureTaskQuiescent rejects Task-wide state replacement while the
// controller owns live, admitted, or automatic work for the Task. Callers
// hold the Task mutation lane while invoking it and applying the durable
// replacement.
func (c *CurrentNodeController) EnsureTaskQuiescent(taskID workflow.TaskID) error {
	if c == nil {
		return errors.New("current node workflow controller is required")
	}
	if taskID == "" {
		return errors.New("workflow task id is required")
	}
	live, err := c.authority.HasLiveWorkflowTaskExecution(taskID)
	if err != nil {
		return err
	}
	if live {
		return ErrTaskExecutionNotQuiescent
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureTaskQuiescentLocked(taskID)
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

func (c *CurrentNodeController) taskExecutionQuiescentLocked(taskID workflow.TaskID) bool {
	if c.interrupts.taskActive(taskID) {
		return false
	}
	if c.queuedTaskPreparationLocked(taskID) != nil || c.runningTaskPreparationLocked(taskID) != nil {
		return false
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
