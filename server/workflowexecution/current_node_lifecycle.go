package workflowexecution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"core/server/runtime"
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

type WorkflowSessionContinuationInput struct {
	Text  string
	Steer *runtime.AgentSteer
}

type WorkflowSessionContinuation struct {
	input WorkflowSessionContinuationInput

	turn        continuationSignal[runtime.UserTurnResult]
	exact       continuationSignal[sessionruntime.ExecutionResult]
	nameMu      sync.RWMutex
	sessionName string
	progressMu  sync.RWMutex
	progress    func(runtime.Event)
	stepIDs     map[string]struct{}
	closed      bool
}

func NewWorkflowSessionContinuation(text string, steer *runtime.AgentSteer) (*WorkflowSessionContinuation, error) {
	if steer != nil && strings.TrimSpace(text) != "" {
		return nil, errors.New("workflow Session continuation cannot contain both text and Agent steer input")
	}
	if steer == nil && strings.TrimSpace(text) == "" {
		return nil, errors.New("workflow Session continuation input is required")
	}
	return &WorkflowSessionContinuation{
		input:   WorkflowSessionContinuationInput{Text: text, Steer: steer},
		turn:    newContinuationSignal[runtime.UserTurnResult](),
		exact:   newContinuationSignal[sessionruntime.ExecutionResult](),
		stepIDs: make(map[string]struct{}),
	}, nil
}

func (c *WorkflowSessionContinuation) Input() WorkflowSessionContinuationInput {
	if c == nil {
		return WorkflowSessionContinuationInput{}
	}
	return c.input
}

func (c *WorkflowSessionContinuation) RecordTurn(result runtime.UserTurnResult, err error) {
	if c == nil {
		return
	}
	c.turn.set(result, err)
}

func (c *WorkflowSessionContinuation) WaitTurn(ctx context.Context) (runtime.UserTurnResult, error) {
	if c == nil {
		return runtime.UserTurnResult{}, errors.New("workflow Session continuation is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.turn.wait(ctx)
}

func (c *WorkflowSessionContinuation) RecordExact(result sessionruntime.ExecutionResult, err error) {
	if c == nil {
		return
	}
	c.exact.set(result, err)
}

func (c *WorkflowSessionContinuation) WaitExact(ctx context.Context) (sessionruntime.ExecutionResult, error) {
	if c == nil {
		return sessionruntime.ExecutionResult{}, errors.New("workflow Session continuation is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return c.exact.wait(ctx)
}

type continuationSignal[T any] struct {
	once  sync.Once
	done  chan struct{}
	value T
	err   error
}

func newContinuationSignal[T any]() continuationSignal[T] {
	return continuationSignal[T]{done: make(chan struct{})}
}

func (s *continuationSignal[T]) set(value T, err error) {
	s.once.Do(func() {
		s.value = value
		s.err = err
		close(s.done)
	})
}

func (s *continuationSignal[T]) wait(ctx context.Context) (T, error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.done:
		return s.value, s.err
	default:
	}
	select {
	case <-s.done:
		return s.value, s.err
	case <-ctx.Done():
		select {
		case <-s.done:
			return s.value, s.err
		default:
			return zero, context.Cause(ctx)
		}
	}
}

func (c *WorkflowSessionContinuation) RecordSessionName(name string) {
	if c == nil {
		return
	}
	c.nameMu.Lock()
	c.sessionName = name
	c.nameMu.Unlock()
}

func (c *WorkflowSessionContinuation) SessionName() string {
	if c == nil {
		return ""
	}
	c.nameMu.RLock()
	defer c.nameMu.RUnlock()
	return c.sessionName
}

func (c *WorkflowSessionContinuation) SetProgressSink(progress func(runtime.Event)) {
	if c == nil {
		return
	}
	c.progressMu.Lock()
	c.progress = progress
	c.progressMu.Unlock()
}

func (c *WorkflowSessionContinuation) RegisterStep(stepID string) {
	if c == nil || strings.TrimSpace(stepID) == "" {
		return
	}
	c.progressMu.Lock()
	if !c.closed {
		c.stepIDs[stepID] = struct{}{}
	}
	c.progressMu.Unlock()
}

func (c *WorkflowSessionContinuation) PublishEvent(event runtime.Event) {
	if c == nil || event.StepID == nil {
		return
	}
	c.progressMu.RLock()
	_, selected := c.stepIDs[*event.StepID]
	progress := c.progress
	c.progressMu.RUnlock()
	if selected && progress != nil {
		progress(event)
	}
}

func (c *WorkflowSessionContinuation) CloseProgress() {
	if c == nil {
		return
	}
	c.progressMu.Lock()
	c.closed = true
	c.stepIDs = nil
	c.progressMu.Unlock()
}

type WorkflowSessionResumeDiagnostic struct {
	Reference workflow.CurrentNodeReference
	Cause     error
}

func (d WorkflowSessionResumeDiagnostic) Error() string {
	return fmt.Sprintf("resume current node %v: %v", d.Reference, d.Cause)
}

func (d WorkflowSessionResumeDiagnostic) Unwrap() error {
	return d.Cause
}

type WorkflowSessionContinuationResult struct {
	Handle             sessionruntime.ExecutionHandle
	SiblingDiagnostics []WorkflowSessionResumeDiagnostic
	waitAdmission      func(context.Context) (sessionruntime.ExecutionHandle, error)
	accepted           bool
}

type WorkflowSessionReactivatorWithAcceptance interface {
	ReactivateWorkflowSessionWithAcceptance(
		context.Context,
		runtimeids.SessionID,
		runtime.CommandAcceptance,
		context.Context,
		*WorkflowSessionContinuation,
	) (WorkflowSessionContinuationResult, error)
}

func (r WorkflowSessionContinuationResult) WaitAdmission(ctx context.Context) (sessionruntime.ExecutionHandle, error) {
	if r.waitAdmission != nil {
		return r.waitAdmission(ctx)
	}
	if r.Handle == nil {
		return nil, errors.New("workflow Session continuation admission is unavailable")
	}
	return r.Handle, nil
}

func (r WorkflowSessionContinuationResult) Accepted() bool {
	return r.accepted
}

func (r WorkflowSessionContinuationResult) DiagnosticsError() error {
	return workflowResumeDiagnosticsError(r.SiblingDiagnostics)
}

type workflowSessionResumeAcceptance struct {
	Selected     workflow.CurrentNodeReference
	Accept       runtime.CommandAcceptance
	AcceptedCtx  context.Context
	Continuation *WorkflowSessionContinuation
}

type workflowSessionResumeState struct {
	acceptance *workflowSessionResumeAcceptance
	accepted   bool
}

type workflowSessionResumeResult struct {
	result        TaskResumeResult
	completion    *currentNodeAdmissionCompletion
	selectedError error
	diagnostics   []WorkflowSessionResumeDiagnostic
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
	resumed, err := c.resumeTask(ctx, taskID, nil, nil, nil, nil)
	return resumed.result, errors.Join(err, resumed.selectedError, workflowResumeDiagnosticsError(resumed.diagnostics))
}

func (c *CurrentNodeController) ReactivateWorkflowSession(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) (sessionruntime.ExecutionHandle, error) {
	result, err := c.reactivateWorkflowSession(ctx, sessionID, nil)
	return result.Handle, errors.Join(err, result.DiagnosticsError())
}

func (c *CurrentNodeController) ReactivateWorkflowSessionWithAcceptance(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	accept runtime.CommandAcceptance,
	acceptedCtx context.Context,
	continuation *WorkflowSessionContinuation,
) (WorkflowSessionContinuationResult, error) {
	if accept == nil {
		return WorkflowSessionContinuationResult{}, errors.New("workflow Session continuation acceptance is required")
	}
	if continuation == nil {
		return WorkflowSessionContinuationResult{}, errors.New("workflow Session continuation is required")
	}
	return c.reactivateWorkflowSession(ctx, sessionID, &workflowSessionResumeState{
		acceptance: &workflowSessionResumeAcceptance{
			Selected:     workflow.CurrentNodeReference{},
			Accept:       accept,
			AcceptedCtx:  acceptedCtx,
			Continuation: continuation,
		},
	})
}

func (c *CurrentNodeController) reactivateWorkflowSession(
	ctx context.Context,
	sessionID runtimeids.SessionID,
	resumeState *workflowSessionResumeState,
) (WorkflowSessionContinuationResult, error) {
	if c == nil {
		return WorkflowSessionContinuationResult{}, errors.New("current node workflow controller is required")
	}
	if sessionID.IsZero() {
		return WorkflowSessionContinuationResult{}, errors.New("session id is required")
	}
	input, err := c.store.ResolveCurrentSessionStartContext(ctx, sessionID)
	if err != nil {
		return WorkflowSessionContinuationResult{}, err
	}
	if handle, live := c.authority.SessionExecution(sessionID); live {
		if resumeState != nil && resumeState.acceptance != nil {
			return WorkflowSessionContinuationResult{}, &TaskResumeConflictError{TaskID: input.Task.ID}
		}
		handle, err := c.validateReactivatedWorkflowExecution(handle, sessionID, input.CurrentNode.Reference)
		return WorkflowSessionContinuationResult{Handle: handle}, err
	}
	if resumeState != nil && resumeState.acceptance != nil {
		resumeState.acceptance.Selected = input.CurrentNode.Reference
	}
	watch := &input.CurrentNode.Reference
	resumed, err := c.resumeTask(
		ctx,
		input.Task.ID,
		nil,
		nil,
		watch,
		resumeState,
	)
	if err != nil {
		var conflict *TaskResumeConflictError
		if errors.As(err, &conflict) {
			return WorkflowSessionContinuationResult{}, workflowSessionConflictError(input.Task.ID, err)
		}
		return WorkflowSessionContinuationResult{}, err
	}
	if resumeState != nil && resumeState.acceptance != nil {
		if resumed.result.Outcome != TaskResumeApplied {
			conflict, conflictErr := c.workflowSessionConflict(ctx, input.Task.ID, &input.CurrentNode.Reference, nil)
			return WorkflowSessionContinuationResult{}, errors.Join(conflictErr, conflict)
		}
		if resumed.selectedError != nil {
			return WorkflowSessionContinuationResult{
				SiblingDiagnostics: resumed.diagnostics,
				accepted:           resumeState.accepted,
			}, errors.Join(resumed.selectedError, workflowResumeDiagnosticsError(resumed.diagnostics))
		}
		if !resumeState.accepted {
			conflict, conflictErr := c.workflowSessionConflict(ctx, input.Task.ID, &input.CurrentNode.Reference, nil)
			return WorkflowSessionContinuationResult{}, errors.Join(conflictErr, conflict)
		}
	}
	if resumed.result.Outcome != TaskResumeApplied && resumed.result.Outcome != TaskResumeNoOp {
		return WorkflowSessionContinuationResult{}, fmt.Errorf("workflow Session reactivation returned invalid Resume outcome %q", resumed.result.Outcome)
	}
	if resumed.completion != nil {
		waitAdmission := func(waitCtx context.Context) (sessionruntime.ExecutionHandle, error) {
			handle, waitErr := resumed.completion.wait(waitCtx)
			if waitErr != nil {
				return nil, fmt.Errorf("reactivate workflow Session %s: %w", sessionID, waitErr)
			}
			return c.validateReactivatedWorkflowExecution(handle, sessionID, input.CurrentNode.Reference)
		}
		if resumeState != nil && resumeState.acceptance != nil {
			return WorkflowSessionContinuationResult{
				SiblingDiagnostics: resumed.diagnostics,
				waitAdmission:      waitAdmission,
				accepted:           true,
			}, nil
		}
		handle, err := waitAdmission(ctx)
		return WorkflowSessionContinuationResult{Handle: handle, SiblingDiagnostics: resumed.diagnostics}, err
	}
	handle, live := c.authority.SessionExecution(sessionID)
	if !live {
		conflict, conflictErr := c.workflowSessionConflict(ctx, input.Task.ID, &input.CurrentNode.Reference, nil)
		return WorkflowSessionContinuationResult{}, errors.Join(conflictErr, conflict)
	}
	handle, err = c.validateReactivatedWorkflowExecution(handle, sessionID, input.CurrentNode.Reference)
	return WorkflowSessionContinuationResult{Handle: handle, SiblingDiagnostics: resumed.diagnostics}, err
}

func (c *CurrentNodeController) validateReactivatedWorkflowExecution(
	handle sessionruntime.ExecutionHandle,
	sessionID runtimeids.SessionID,
	currentNode workflow.CurrentNodeReference,
) (sessionruntime.ExecutionHandle, error) {
	return c.authority.ValidateLiveWorkflowAgentExecution(handle, sessionID, currentNode)
}

func (c *CurrentNodeController) currentNodePreparingLocked(
	key workflow.CurrentNodeReferenceKey,
) bool {
	for _, batch := range c.preparationQueue {
		for _, start := range batch.starts {
			startKey, err := start.reference.Key()
			if err != nil {
				panic(fmt.Sprintf("inspect queued Task preparation admission: %v", err))
			}
			if startKey == key {
				return true
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
				return true
			}
		}
	}
	for _, start := range c.explicitQueue {
		startKey, err := start.reference.Key()
		if err != nil {
			panic(fmt.Sprintf("inspect explicit admission queue: %v", err))
		}
		if startKey == key {
			return true
		}
	}
	for entry := c.automaticQueue.first; entry != nil; entry = entry.globalNext {
		startKey, err := entry.start.reference.Key()
		if err != nil {
			panic(fmt.Sprintf("inspect automatic admission queue: %v", err))
		}
		if startKey == key {
			return true
		}
	}
	if _, exists := c.explicitReservations[key]; exists {
		return true
	}
	if _, exists := c.automaticReservations[key]; exists {
		return true
	}
	if _, exists := c.admissionWorkers[key]; exists {
		return true
	}
	return false
}

func (c *CurrentNodeController) WorkflowSessionPreparing(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) (bool, error) {
	if c == nil {
		return false, errors.New("current node workflow controller is required")
	}
	if sessionID.IsZero() {
		return false, errors.New("session id is required")
	}
	input, err := c.store.ResolveCurrentSessionStartContext(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if handle, live := c.authority.SessionExecution(sessionID); live {
		if _, err := c.validateReactivatedWorkflowExecution(handle, sessionID, input.CurrentNode.Reference); err != nil {
			return false, err
		}
		return false, nil
	}
	key, err := input.CurrentNode.Reference.Key()
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentNodePreparingLocked(key), nil
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
	resumed, err := c.resumeTask(ctx, taskID, &preparation, finalizer, nil, nil)
	return resumed.result, errors.Join(err, resumed.selectedError, workflowResumeDiagnosticsError(resumed.diagnostics))
}

func workflowResumeDiagnosticsError(diagnostics []WorkflowSessionResumeDiagnostic) error {
	if len(diagnostics) == 0 {
		return nil
	}
	causes := make([]error, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		causes = append(causes, diagnostic)
	}
	return errors.Join(causes...)
}

type TaskResumeConflictState uint8

const (
	taskResumeConflictUnspecified TaskResumeConflictState = iota
	TaskResumeConflictPendingApproval
	TaskResumeConflictFinished
	TaskResumeConflictMovedCurrentNode
	TaskResumeConflictCurrentNodeNotInterrupted
	TaskResumeConflictNoResumableCurrentNode
)

type TaskResumeConflictError struct {
	TaskID workflow.TaskID
	State  TaskResumeConflictState
}

func workflowSessionConflictError(taskID workflow.TaskID, cause error) error {
	var conflict *TaskResumeConflictError
	if errors.As(cause, &conflict) && conflict.State != taskResumeConflictUnspecified {
		return &TaskResumeConflictError{TaskID: taskID, State: conflict.State}
	}
	state := taskResumeConflictUnspecified
	switch {
	case errors.Is(cause, workflowstore.ErrCurrentNodePendingApproval):
		state = TaskResumeConflictPendingApproval
	case errors.Is(cause, workflowstore.ErrSessionNotCurrentWorkflowNode):
		state = TaskResumeConflictMovedCurrentNode
	}
	return &TaskResumeConflictError{TaskID: taskID, State: state}
}

func (e *TaskResumeConflictError) Error() string {
	state := "no interrupted executable Current Node is resumable"
	action := "continue through the Task's current node when it has moved on"
	switch e.State {
	case TaskResumeConflictPendingApproval:
		state = "the Workflow Task is waiting for an Approval"
		action = "resolve that Approval before continuing the Task"
	case TaskResumeConflictFinished:
		state = "the Workflow Task has finished"
		action = "start a new ordinary Session"
	case TaskResumeConflictMovedCurrentNode:
		state = "the retained Session is no longer the Task's current Workflow Node"
	case TaskResumeConflictCurrentNodeNotInterrupted:
		state = "the retained Session's Current Node is no longer interrupted"
	case TaskResumeConflictNoResumableCurrentNode:
		state = "the Workflow Task has no interrupted executable Current Node"
	}
	return fmt.Sprintf(
		"Workflow Task %q is blocked because %s; %s. Direct interactive continuation of this retained Workflow Session is not currently supported",
		e.TaskID,
		state,
		action,
	)
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
	taskID                workflow.TaskID
	resumable             []workflow.CurrentNode
	alreadyResumed        []workflow.CurrentNode
	validationErr         error
	validationDiagnostics []WorkflowSessionResumeDiagnostic
	conflict              *TaskResumeConflictError
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
	if selected != nil {
		currentNodes, err := c.store.ListCurrentNodes(ctx, taskID)
		if err != nil {
			return taskResumeClassification{}, err
		}
		for _, currentNode := range currentNodes {
			if !currentNode.Reference.Equal(*selected) || currentNode.Scheduling == nil {
				continue
			}
			switch currentNode.Scheduling.State {
			case workflow.CurrentNodeSchedulingReady, workflow.CurrentNodeSchedulingAdmitted:
				return taskResumeClassification{taskID: taskID, alreadyResumed: []workflow.CurrentNode{currentNode}}, nil
			}
		}
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
				return taskResumeClassification{taskID: taskID, alreadyResumed: []workflow.CurrentNode{currentNode}}, nil
			}
		}
		conflict, conflictErr := c.workflowSessionConflict(ctx, taskID, selected, nil)
		if conflictErr != nil {
			return taskResumeClassification{}, conflictErr
		}
		return taskResumeClassification{
			taskID:   taskID,
			conflict: conflict,
		}, conflict
	}
	result := taskResumeClassification{
		taskID:    taskID,
		resumable: make([]workflow.CurrentNode, 0, len(classifications)),
	}
	var validationErrs []error
	for _, classification := range classifications {
		if selected != nil && !classification.CurrentNode.Reference.Equal(*selected) {
			continue
		}
		if validationErr := classification.ValidationError(); validationErr != nil {
			validationErrs = append(validationErrs, validationErr)
			result.validationDiagnostics = append(result.validationDiagnostics, WorkflowSessionResumeDiagnostic{
				Reference: classification.CurrentNode.Reference,
				Cause:     validationErr,
			})
			continue
		}
		result.resumable = append(result.resumable, classification.CurrentNode)
	}
	result.validationErr = errors.Join(validationErrs...)
	if len(result.resumable) == 0 && result.validationErr == nil {
		result.conflict, err = c.workflowSessionConflict(ctx, taskID, selected, nil)
		if err != nil {
			return taskResumeClassification{}, err
		}
	}
	return result, nil
}

func (c taskResumeClassification) eligibilityError() error {
	if len(c.resumable) != 0 {
		return nil
	}
	if c.conflict != nil {
		return c.conflict
	}
	if c.validationErr == nil {
		return &TaskResumeConflictError{TaskID: c.taskID}
	}
	return c.validationErr
}

func (c *CurrentNodeController) workflowSessionConflict(
	ctx context.Context,
	taskID workflow.TaskID,
	selected *workflow.CurrentNodeReference,
	cause error,
) (*TaskResumeConflictError, error) {
	var existing *TaskResumeConflictError
	if errors.As(cause, &existing) && existing.State != taskResumeConflictUnspecified {
		return &TaskResumeConflictError{TaskID: taskID, State: existing.State}, nil
	}
	if errors.Is(cause, workflowstore.ErrCurrentNodePendingApproval) {
		return &TaskResumeConflictError{
			TaskID: taskID,
			State:  TaskResumeConflictPendingApproval,
		}, nil
	}
	if approvals, ok := c.store.(interface {
		ListPendingApprovals(context.Context, workflow.TaskID) ([]workflow.PendingApproval, error)
	}); ok {
		pending, err := approvals.ListPendingApprovals(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if len(pending) != 0 {
			return &TaskResumeConflictError{
				TaskID: taskID,
				State:  TaskResumeConflictPendingApproval,
			}, nil
		}
	}
	currentNodes, err := c.store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if len(currentNodes) == 0 {
		return &TaskResumeConflictError{
			TaskID: taskID,
			State:  TaskResumeConflictFinished,
		}, nil
	}
	if selected != nil {
		for _, currentNode := range currentNodes {
			if currentNode.Reference.Equal(*selected) {
				return &TaskResumeConflictError{
					TaskID: taskID,
					State:  TaskResumeConflictCurrentNodeNotInterrupted,
				}, nil
			}
		}
		return &TaskResumeConflictError{
			TaskID: taskID,
			State:  TaskResumeConflictMovedCurrentNode,
		}, nil
	}
	return &TaskResumeConflictError{TaskID: taskID, State: TaskResumeConflictNoResumableCurrentNode}, nil
}

func (c *CurrentNodeController) resumeTask(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation *TaskStartPreparation,
	finalizer TaskPreparationFinalizer,
	watch *workflow.CurrentNodeReference,
	resumeState *workflowSessionResumeState,
) (workflowSessionResumeResult, error) {
	if c == nil {
		return workflowSessionResumeResult{}, errors.New("current node workflow controller is required")
	}
	if preparation != nil && finalizer == nil {
		return workflowSessionResumeResult{}, errors.New("task resume preparation finalizer is required")
	}
	var watchedCompletion *currentNodeAdmissionCompletion
	resumed := workflowSessionResumeResult{}
	_, err := runCurrentNodeTaskMutation(ctx, c, taskID, func(ctx context.Context) (TaskResumeResult, error) {
		var resolution workflowstore.TaskAttentionResolution
		if resumeState != nil && resumeState.acceptance != nil {
			selectedClassification, selectedErr := c.classifyTaskResume(ctx, taskID, &resumeState.acceptance.Selected)
			if selectedErr == nil && len(selectedClassification.alreadyResumed) != 0 {
				resumed.result = TaskResumeResult{
					Outcome:      TaskResumeNoOp,
					CurrentNodes: selectedClassification.alreadyResumed,
				}
				return resumed.result, nil
			}
			if selectedErr != nil {
				resumed.selectedError = WorkflowSessionResumeDiagnostic{
					Reference: resumeState.acceptance.Selected,
					Cause:     selectedErr,
				}
			} else if eligibilityErr := selectedClassification.eligibilityError(); eligibilityErr != nil {
				resumed.selectedError = WorkflowSessionResumeDiagnostic{
					Reference: resumeState.acceptance.Selected,
					Cause:     eligibilityErr,
				}
			}
		}
		classificationSelection := watch
		if resumeState != nil && resumeState.acceptance != nil {
			classificationSelection = nil
		}
		classification, err := c.classifyTaskResume(ctx, taskID, classificationSelection)
		if err != nil {
			return TaskResumeResult{}, err
		}
		if len(classification.alreadyResumed) != 0 {
			if watch != nil {
				for _, currentNode := range classification.alreadyResumed {
					if currentNode.Reference.Equal(*watch) {
						resumed.result = TaskResumeResult{
							Outcome:      TaskResumeNoOp,
							CurrentNodes: []workflow.CurrentNode{currentNode},
						}
						return resumed.result, nil
					}
				}
			}
			resumed.result = TaskResumeResult{
				Outcome:      TaskResumeNoOp,
				CurrentNodes: classification.alreadyResumed,
			}
			return resumed.result, nil
		}
		eligible := make([]workflow.CurrentNode, 0, len(classification.resumable))
		eligibleStarts := make([]currentNodeQueuedStart, 0, len(classification.resumable))
		seen := make(map[workflow.CurrentNodeReferenceKey]struct{}, len(classification.resumable))
		recordDiagnostic := func(diagnostic WorkflowSessionResumeDiagnostic) {
			resumed.diagnostics = append(resumed.diagnostics, diagnostic)
			if resumeState != nil && resumeState.acceptance != nil &&
				diagnostic.Reference.Equal(resumeState.acceptance.Selected) {
				resumed.selectedError = diagnostic
			}
		}
		for _, diagnostic := range classification.validationDiagnostics {
			recordDiagnostic(diagnostic)
		}
		for _, currentNode := range classification.resumable {
			key, keyErr := currentNode.Reference.Key()
			if keyErr != nil {
				recordDiagnostic(WorkflowSessionResumeDiagnostic{Reference: currentNode.Reference, Cause: keyErr})
				continue
			}
			if currentNode.SessionID != nil {
				if active, exists := c.authority.SessionExecution(*currentNode.SessionID); exists {
					recordDiagnostic(WorkflowSessionResumeDiagnostic{
						Reference: currentNode.Reference,
						Cause: fmt.Errorf(
							"retained Session %s already has active execution scope %s: %w",
							*currentNode.SessionID,
							active.Scope().ID(),
							ErrTaskExecutionNotQuiescent,
						),
					})
					continue
				}
			}
			if _, duplicate := seen[key]; duplicate {
				recordDiagnostic(WorkflowSessionResumeDiagnostic{
					Reference: currentNode.Reference,
					Cause:     errors.New("resumable current node is duplicated"),
				})
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
		if resumeState != nil && resumeState.acceptance != nil {
			for index := range eligible {
				if eligible[index].Reference.Equal(resumeState.acceptance.Selected) {
					eligible[0], eligible[index] = eligible[index], eligible[0]
					eligibleStarts[0], eligibleStarts[index] = eligibleStarts[index], eligibleStarts[0]
					break
				}
			}
		}
		c.mu.Lock()
		if err := c.ensureTaskAvailableLocked(taskID); err != nil {
			c.mu.Unlock()
			return TaskResumeResult{}, err
		}
		for _, start := range eligibleStarts {
			key, _ := start.reference.Key()
			if c.currentNodeOwnedLocked(key) {
				c.mu.Unlock()
				return TaskResumeResult{}, fmt.Errorf("current node %v cannot resume while controller ownership remains: %w", start.reference, ErrTaskExecutionNotQuiescent)
			}
		}
		c.mu.Unlock()

		resumedNodes := make([]workflow.CurrentNode, 0, len(eligible))
		starts := make([]currentNodeQueuedStart, 0, len(eligible))
		resumeCtx := ctx
		for index, currentNode := range eligible {
			commit := func() (bool, error) {
				projection, found, err := c.store.ResumeCurrentNode(resumeCtx, currentNode.Reference)
				if err != nil {
					return false, err
				}
				if found {
					resolution.InterruptedCurrentNodes = append(resolution.InterruptedCurrentNodes, projection)
				}
				if resumeState != nil && resumeState.acceptance != nil &&
					currentNode.Reference.Equal(resumeState.acceptance.Selected) {
					eligibleStarts[index].continuation = resumeState.acceptance.Continuation
				}
				starts = append(starts, eligibleStarts[index])
				resumedNodes = append(resumedNodes, currentNode)
				return true, nil
			}
			if resumeState != nil &&
				resumeState.acceptance != nil &&
				currentNode.Reference.Equal(resumeState.acceptance.Selected) {
				committed, err := resumeState.acceptance.Accept(commit)
				if err != nil {
					diagnostic := WorkflowSessionResumeDiagnostic{Reference: currentNode.Reference, Cause: err}
					recordDiagnostic(diagnostic)
					continue
				}
				if !committed {
					diagnostic := WorkflowSessionResumeDiagnostic{Reference: currentNode.Reference, Cause: errors.New("resume was not accepted")}
					recordDiagnostic(diagnostic)
					continue
				}
				resumeState.accepted = true
				if resumeState.acceptance.AcceptedCtx != nil {
					resumeCtx = resumeState.acceptance.AcceptedCtx
				}
				continue
			}
			if _, err := commit(); err != nil {
				diagnostic := WorkflowSessionResumeDiagnostic{Reference: currentNode.Reference, Cause: err}
				recordDiagnostic(diagnostic)
			}
		}
		c.finalizeTaskAttentionResolution(resolution)
		c.mu.Lock()
		if preparation == nil {
			for _, start := range starts {
				if watch != nil && start.reference.Equal(*watch) {
					key, keyErr := start.reference.Key()
					if keyErr != nil {
						recordDiagnostic(WorkflowSessionResumeDiagnostic{Reference: start.reference, Cause: keyErr})
						continue
					}
					if c.currentNodeOwnedLocked(key) {
						recordDiagnostic(WorkflowSessionResumeDiagnostic{
							Reference: start.reference,
							Cause:     fmt.Errorf("queue resumed current node while controller ownership remains: %w", ErrTaskExecutionNotQuiescent),
						})
						continue
					}
				}
				if queueErr := c.queueExplicitStartLocked(start); queueErr != nil {
					recordDiagnostic(WorkflowSessionResumeDiagnostic{Reference: start.reference, Cause: queueErr})
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
				reference := workflow.CurrentNodeReference{}
				if len(starts) > 0 {
					reference = starts[0].reference
				}
				recordDiagnostic(WorkflowSessionResumeDiagnostic{Reference: reference, Cause: batchErr})
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
		resumed.result = TaskResumeResult{
			Outcome:      TaskResumeApplied,
			CurrentNodes: resumedNodes,
		}
		return TaskResumeResult{
			Outcome:      TaskResumeApplied,
			CurrentNodes: resumedNodes,
		}, nil
	})
	resumed.completion = watchedCompletion
	return resumed, err
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
