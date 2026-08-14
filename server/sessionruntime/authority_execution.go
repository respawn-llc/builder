package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"core/server/runtime"
	"core/server/tools"
	"core/shared/runtimeids"
)

type ExecutionHandle interface {
	Scope() ExecutionScope
	RequestStop() bool
	Stop(context.Context) error
	Wait(context.Context) (ExecutionResult, error)
	Close(context.Context) error
}

type ExecutionResult struct {
	Script               *ScriptResult
	DroppedRuntimeEvents uint64
}

type executionPhase uint8

const (
	executionPhaseQueued executionPhase = iota + 1
	executionPhaseRunning
	executionPhaseFinalizing
)

type workflowExecutionActivity uint8

const (
	workflowExecutionNotRunning workflowExecutionActivity = iota
	workflowExecutionQueued
	workflowExecutionRunning
)

type ExecutionPromptSnapshot struct {
	Scope     ExecutionScope
	Request   tools.AskQuestionRequest
	CreatedAt time.Time
}

type ExecutionPromptFeed interface {
	PromptPendingScope(ExecutionScope, tools.AskQuestionRequest, time.Time) error
	PromptResolvedScope(ExecutionScope, string) error
}

type execution struct {
	authority *Authority
	exactMu   sync.Mutex
	resource  *agentResource
	scope     ExecutionScope
	script    *TaskScriptExecutionTarget
	workflow  *runtime.CurrentNodeExecutionBinding
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}

	resultMu sync.RWMutex
	result   ExecutionResult
	runErr   error
	stopErr  error
	prompts  executionPromptStore

	phase executionPhase

	protocolViolations int64
	completed          bool

	closeResource bool
}

func (e *execution) workflowActivity() (workflowExecutionActivity, error) {
	switch e.phase {
	case executionPhaseQueued:
		return workflowExecutionQueued, nil
	case executionPhaseRunning:
		if e.completed {
			return workflowExecutionNotRunning, nil
		}
		return workflowExecutionRunning, nil
	default:
		if e.phase < executionPhaseQueued || e.phase > executionPhaseFinalizing {
			return workflowExecutionNotRunning, fmt.Errorf(
				"workflow execution scope %s has invalid phase %d",
				e.scope.ID(),
				e.phase,
			)
		}
		return workflowExecutionNotRunning, nil
	}
}

type executionHandle struct {
	execution *execution
}

func (h executionHandle) Scope() ExecutionScope {
	if h.execution == nil {
		panic("execution handle is uninitialized")
	}
	return h.execution.scope
}

func (h executionHandle) Stop(ctx context.Context) error {
	if h.execution == nil {
		panic("execution handle is uninitialized")
	}
	h.RequestStop()
	if err := h.execution.awaitDone(ctx); err != nil {
		return err
	}
	return h.execution.stopError()
}

func (h executionHandle) RequestStop() bool {
	if h.execution == nil {
		panic("execution handle is uninitialized")
	}
	stopped, _ := h.execution.requestStop()
	return stopped
}

func (e *execution) requestStop() (bool, error) {
	e.exactMu.Lock()
	defer e.exactMu.Unlock()
	e.authority.mu.Lock()
	defer e.authority.mu.Unlock()
	if e.authority.byScope[e.scope.ID()] != e || e.phase != executionPhaseRunning || e.completed {
		return false, nil
	}
	if e.scope.Kind() == ExecutionScopeAgent {
		if e.resource == nil {
			return false, e.authority.invariant(
				"stop running Agent execution",
				fmt.Errorf("scope=%s has no Runtime resource", e.scope.ID()),
			)
		}
		e.phase = executionPhaseFinalizing
		if _, workflowAgent := e.scope.Workflow(); workflowAgent {
			if err := e.retireWorkflowLocked(); err != nil {
				return false, err
			}
			e.resource.engine.RemoveStoppedHumanSteering(e.scope.ID())
		}
	}
	e.cancel()
	return true, nil
}

func (h executionHandle) Wait(ctx context.Context) (ExecutionResult, error) {
	if h.execution == nil {
		panic("execution handle is uninitialized")
	}
	if err := h.execution.awaitDone(ctx); err != nil {
		return ExecutionResult{}, err
	}
	return h.execution.outcome()
}

func (h executionHandle) Close(ctx context.Context) error {
	if h.execution == nil {
		panic("execution handle is uninitialized")
	}
	return h.execution.awaitDone(ctx)
}

func (e *execution) awaitDone(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-e.done:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (e *execution) outcome() (ExecutionResult, error) {
	e.resultMu.RLock()
	defer e.resultMu.RUnlock()
	result := e.result
	if result.Script != nil {
		script := result.Script.clone()
		result.Script = &script
	}
	return result, e.runErr
}

func (e *execution) stopError() error {
	e.resultMu.RLock()
	defer e.resultMu.RUnlock()
	return e.stopErr
}

func (e *execution) finish(result ExecutionResult, runErr error, stopErr error) {
	var invariantErr error
	workflowAgent := false
	stoppedWorkflowAgent := false
	if e.scope.Kind() == ExecutionScopeAgent {
		_, workflowAgent = e.scope.Workflow()
	}
	if workflowAgent {
		e.exactMu.Lock()
		e.authority.mu.Lock()
		if e.authority.byScope[e.scope.ID()] == e {
			stoppedWorkflowAgent = e.phase == executionPhaseFinalizing
			switch e.phase {
			case executionPhaseRunning:
				e.phase = executionPhaseFinalizing
			case executionPhaseFinalizing:
			default:
				invariantErr = errors.Join(invariantErr, e.authority.invariant(
					"begin Agent execution retirement",
					fmt.Errorf("scope=%s phase=%d", e.scope.ID(), e.phase),
				))
				e.phase = executionPhaseFinalizing
			}
			invariantErr = errors.Join(invariantErr, e.retireWorkflowLocked())
		}
		e.authority.mu.Unlock()
		e.exactMu.Unlock()
	}
	if stoppedWorkflowAgent && e.resource != nil {
		e.resource.engine.RemoveStoppedHumanSteering(e.scope.ID())
	}
	cleanupErr := errors.Join(invariantErr, e.cleanup())
	cleanupErr = errors.Join(cleanupErr, e.retire())
	authority := e.authority
	executionErr := errors.Join(runErr, invariantErr)
	abort, abortErr := runtimeAbortFromError(runErr)
	if abortErr != nil {
		executionErr = errors.Join(executionErr, abortErr)
	}
	var closeErr error
	if e.resource != nil {
		if e.resource.eventBridge != nil {
			result.DroppedRuntimeEvents = e.resource.eventBridge.Dropped.Load()
		}
		if e.resource.logger != nil {
			if executionErr != nil {
				e.resource.logger.Logf("runtime.execution.exit scope_id=%s error=%q", e.scope.ID(), executionErr.Error())
			} else {
				e.resource.logger.Logf("runtime.execution.exit scope_id=%s ok", e.scope.ID())
			}
			if result.DroppedRuntimeEvents != 0 {
				e.resource.logger.Logf("runtime.event.drop.total=%d", result.DroppedRuntimeEvents)
			}
		}
		if e.closeResource {
			e.resource.requestRetirementIfOwnerless()
		}
		closeErr = e.resource.releasePin()
		if abort {
			closeErr = errors.Join(
				closeErr,
				authority.retireRuntimeAbortResource(context.Background(), e.resource),
			)
		}
	}
	finalErr := executionErr
	if cleanupErr != nil || closeErr != nil {
		finalErr = errors.Join(finalErr, cleanupErr, closeErr)
	}
	e.resultMu.Lock()
	e.result = result
	e.runErr = finalErr
	e.stopErr = errors.Join(stopErr, cleanupErr, closeErr, abortErr)
	e.resultMu.Unlock()
	if workflowRef, hasWorkflow := e.scope.Workflow(); hasWorkflow && authority.workflowExecutionRetired != nil {
		disposition := WorkflowRetirementOutcomeLess
		if e.completed {
			disposition = WorkflowRetirementCompleted
		}
		authority.workflowExecutionRetired.WorkflowExecutionRetired(WorkflowRetirementOutcome{
			Operation: workflowRef.Operation(), Kind: e.scope.Kind(), Disposition: disposition,
		})
	}
	close(e.done)
}

func runtimeAbortFromError(err error) (bool, error) {
	type runtimeAbort interface {
		RuntimeAbortDisposition() (committed bool, cause error)
	}
	var abort runtimeAbort
	if !errors.As(err, &abort) {
		return false, nil
	}
	_, cause := abort.RuntimeAbortDisposition()
	if cause == nil || !errors.Is(err, cause) {
		return true, fmt.Errorf(
			"runtime abort disposition %T must expose its exact cause through the error chain",
			abort,
		)
	}
	return true, nil
}

func (e *execution) retire() error {
	authority := e.authority
	e.exactMu.Lock()
	defer e.exactMu.Unlock()
	authority.mu.Lock()
	removedScope := false
	if authority.byScope[e.scope.ID()] == e {
		delete(authority.byScope, e.scope.ID())
		removedScope = true
	}
	var err error
	if removedScope {
		err = e.retireWorkflowLocked()
	}
	authority.mu.Unlock()
	return err
}

// beginWorkflowFinalization removes a terminal Script from workflow liveness
// indexes while retaining its Exact Execution Scope for its completion
// finalizer. A Script becomes terminal when its process exits or Start fails.
// Its finalizer can still prove Current Node ownership, but no longer
// authorizes Interrupt or appears in queued/running read models.
func (e *execution) beginWorkflowFinalization() error {
	e.exactMu.Lock()
	defer e.exactMu.Unlock()
	e.authority.mu.Lock()
	if e.authority.byScope[e.scope.ID()] != e {
		e.authority.mu.Unlock()
		return nil
	}
	if e.phase != executionPhaseRunning {
		e.authority.mu.Unlock()
		return e.authority.invariant(
			"begin Script execution finalization",
			fmt.Errorf("scope=%s phase=%d", e.scope.ID(), e.phase),
		)
	}
	if e.scope.Kind() != ExecutionScopeScript {
		e.authority.mu.Unlock()
		return e.authority.invariant(
			"begin Script execution finalization",
			fmt.Errorf("scope=%s kind=%d", e.scope.ID(), e.scope.Kind()),
		)
	}
	e.phase = executionPhaseFinalizing
	err := e.retireWorkflowLocked()
	e.authority.mu.Unlock()
	return err
}

func (e *execution) retireWorkflowLocked() error {
	workflowRef, hasWorkflow := e.scope.Workflow()
	if !hasWorkflow {
		return nil
	}
	workflowKey, err := workflowExecutionKeyFor(workflowRef)
	if err != nil {
		return e.authority.invariant(
			"retire Workflow execution association",
			fmt.Errorf("scope=%s: %w", e.scope.ID(), err),
		)
	}
	removed := e.authority.removeWorkflowExecutionLocked(workflowRef, workflowKey, e)
	if removed && e.resource != nil {
		e.resource.engine.EnterRetainedWorkflowControl()
	}
	return nil
}

func (e *execution) cleanup() error {
	promptErr := e.prompts.Close(context.Canceled)
	var bindingErr error
	if e.workflow != nil {
		bindingErr = e.workflow.Close()
		e.workflow = nil
	}
	if e.resource == nil {
		return errors.Join(promptErr, bindingErr)
	}
	resource := e.resource
	resource.mu.Lock()
	defer resource.mu.Unlock()
	if resource.current != e {
		return errors.Join(
			bindingErr,
			fmt.Errorf(
				"agent execution scope %s is not current for resource %s generation %d",
				e.scope.ID(),
				resource.ref.SessionID(),
				resource.ref.Generation(),
			),
		)
	}
	cleanupErr := errors.Join(promptErr, bindingErr)
	if resource.askBroker != nil {
		switch {
		case resource.askScope == nil:
			cleanupErr = errors.New("agent execution prompt binding is missing")
		case *resource.askScope != e.scope.ID():
			cleanupErr = fmt.Errorf(
				"agent execution prompt binding scope %s does not match finalizing scope %s",
				*resource.askScope,
				e.scope.ID(),
			)
		default:
			resource.askBroker.SetAskHandler(nil)
			resource.askScope = nil
		}
	}
	if resource.localTools != nil {
		cleanupErr = errors.Join(cleanupErr, resource.localTools.BindExecutionCorrelation(nil))
	}
	resource.current = nil
	resource.signalLocked()
	return cleanupErr
}

type executionPromptResult struct {
	resolution tools.AskQuestionResolution
	err        error
}

type executionPromptEntry struct {
	snapshot        ExecutionPromptSnapshot
	response        chan executionPromptResult
	publicationDone chan struct{}
}

type executionPromptClosure struct {
	err     error
	entries []*executionPromptEntry
}

type PromptBatchInvariantError struct {
	PromptID string
	Detail   string
}

func (e PromptBatchInvariantError) Error() string {
	return fmt.Sprintf("prepared question batch for prompt %q is invalid: %s", e.PromptID, e.Detail)
}

type executionPromptStore struct {
	authority *Authority
	// mu is the sole synchronization for pending prompts. Prompt lifecycle
	// mutations must not acquire Authority.mu because status snapshots hold it
	// while reading live execution state.
	mu              sync.RWMutex
	scope           ExecutionScope
	feed            ExecutionPromptFeed
	closed          bool
	pending         map[string]*executionPromptEntry
	promptFollowUps map[promptFollowUpKey]*promptFollowUpState
}

func newExecutionPromptStore(authority *Authority, scope ExecutionScope, feed ExecutionPromptFeed) executionPromptStore {
	return executionPromptStore{
		authority: authority,
		scope:     scope,
		feed:      feed,
		pending:   make(map[string]*executionPromptEntry),
	}
}

func (s *executionPromptStore) Await(ctx context.Context, req tools.AskQuestionRequest) (response tools.AskQuestionResolution, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestID := strings.TrimSpace(req.ID)
	if requestID == "" {
		return nil, errors.New("prompt request id is required")
	}
	snapshot := ExecutionPromptSnapshot{
		Scope:     s.scope,
		Request:   cloneExecutionPromptRequest(req),
		CreatedAt: time.Now().UTC(),
	}
	entry := &executionPromptEntry{
		snapshot:        snapshot,
		response:        make(chan executionPromptResult, 1),
		publicationDone: make(chan struct{}),
	}
	if s.authority == nil {
		return nil, errors.New("session runtime authority is required")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, context.Canceled
	}
	if _, exists := s.pending[requestID]; exists {
		s.mu.Unlock()
		return nil, fmt.Errorf("prompt %q is already pending", requestID)
	}
	s.pending[requestID] = entry
	s.mu.Unlock()
	if err := s.publishPending(snapshot); err != nil {
		s.mu.Lock()
		delete(s.pending, requestID)
		s.mu.Unlock()
		close(entry.publicationDone)
		return nil, err
	}
	close(entry.publicationDone)
	s.mu.Lock()
	s.observePromptFollowUpsLocked(req.StepID, requestID)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		current := s.pending[requestID]
		if current == entry {
			delete(s.pending, requestID)
		}
		s.mu.Unlock()
		if current == entry {
			if err := s.publishResolved(snapshot); err != nil && returnErr == nil {
				returnErr = err
			}
		}
	}()
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case result := <-entry.response:
		return result.resolution, result.err
	}
}

func (s *executionPromptStore) Close(err error) error {
	if err == nil {
		err = context.Canceled
	}
	if s.authority == nil {
		return nil
	}
	s.mu.Lock()
	closure := s.closeLocked(err)
	s.mu.Unlock()
	publicationErr := s.publishClosure(closure)
	s.releaseClosure(closure)
	return publicationErr
}

func (s *executionPromptStore) closeLocked(err error) executionPromptClosure {
	if err == nil {
		err = context.Canceled
	}
	if s.closed {
		return executionPromptClosure{}
	}
	s.closed = true
	s.closePromptFollowUpsLocked()
	closure := executionPromptClosure{
		err:     err,
		entries: make([]*executionPromptEntry, 0, len(s.pending)),
	}
	for requestID, entry := range s.pending {
		closure.entries = append(closure.entries, entry)
		delete(s.pending, requestID)
	}
	return closure
}

func (s *executionPromptStore) publishClosure(closure executionPromptClosure) error {
	var publicationErr error
	for _, entry := range closure.entries {
		<-entry.publicationDone
		publicationErr = errors.Join(publicationErr, s.publishResolved(entry.snapshot))
	}
	return publicationErr
}

func (s *executionPromptStore) releaseClosure(closure executionPromptClosure) {
	for _, entry := range closure.entries {
		entry.response <- executionPromptResult{
			err: closure.err,
		}
	}
}

func (s *executionPromptStore) hasPending() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pending) != 0
}

func (s *executionPromptStore) hasPendingID(requestID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists := s.pending[requestID]
	return exists
}

func (s *executionPromptStore) pendingReferences() ([]PendingPromptReference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pendingReferencesLocked()
}

func (s *executionPromptStore) tryPendingReferences() ([]PendingPromptReference, bool, error) {
	if !s.mu.TryRLock() {
		return nil, false, nil
	}
	defer s.mu.RUnlock()
	references, err := s.pendingReferencesLocked()
	return references, true, err
}

func (s *executionPromptStore) pendingReferencesLocked() ([]PendingPromptReference, error) {
	references := make([]PendingPromptReference, 0, len(s.pending))
	for requestID, entry := range s.pending {
		if entry == nil {
			return nil, errors.New("pending prompt store contains a nil entry")
		}
		reference := PendingPromptReference{
			ID: requestID,
		}
		if entry.snapshot.Request.Approval {
			reference.Kind = PendingPromptKindSessionApproval
		} else {
			reference.Kind = PendingPromptKindQuestion
		}
		references = append(references, reference)
	}
	sort.Slice(references, func(i, j int) bool {
		if references[i].ID != references[j].ID {
			return references[i].ID < references[j].ID
		}
		return references[i].Kind < references[j].Kind
	})
	return references, nil
}

func (s *executionPromptStore) publishPending(snapshot ExecutionPromptSnapshot) error {
	if s.feed == nil {
		return nil
	}
	return s.feed.PromptPendingScope(snapshot.Scope, cloneExecutionPromptRequest(snapshot.Request), snapshot.CreatedAt)
}

func (s *executionPromptStore) publishResolved(snapshot ExecutionPromptSnapshot) error {
	if s.feed == nil {
		return nil
	}
	return s.feed.PromptResolvedScope(snapshot.Scope, snapshot.Request.ID)
}

func (a *Authority) AwaitPromptResolution(
	ctx context.Context,
	scopeID runtimeids.ExecutionScopeID,
	req tools.AskQuestionRequest,
) (tools.AskQuestionResolution, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if _, err := runtimeids.ParseStepID(req.StepID); err != nil {
		return nil, fmt.Errorf("pending prompt step identity: %w", err)
	}
	a.mu.Lock()
	execution := a.byScope[scopeID]
	a.mu.Unlock()
	if execution == nil || execution.scope.Kind() != ExecutionScopeAgent {
		return nil, fmt.Errorf("execution scope %s is unavailable", scopeID)
	}
	return execution.prompts.Await(ctx, req)
}

func (a *Authority) sessionExecution(sessionID runtimeids.SessionID) *execution {
	if a == nil || sessionID.IsZero() {
		return nil
	}
	a.mu.Lock()
	resource := a.resources[sessionID]
	a.mu.Unlock()
	if resource == nil {
		return nil
	}
	resource.mu.Lock()
	execution := resource.current
	resource.mu.Unlock()
	return execution
}

func cloneExecutionPromptSnapshot(snapshot ExecutionPromptSnapshot) ExecutionPromptSnapshot {
	snapshot.Request = cloneExecutionPromptRequest(snapshot.Request)
	return snapshot
}

func cloneExecutionPromptRequest(req tools.AskQuestionRequest) tools.AskQuestionRequest {
	req.Suggestions = append([]string(nil), req.Suggestions...)
	req.ApprovalOptions = append([]tools.AskQuestionApprovalOption(nil), req.ApprovalOptions...)
	if req.QuestionBatch != nil {
		batch := *req.QuestionBatch
		batch.BatchPromptIDs = append([]string(nil), req.QuestionBatch.BatchPromptIDs...)
		req.QuestionBatch = &batch
	}
	if req.AttentionTarget != nil {
		target := *req.AttentionTarget
		if target.Focus != nil {
			focus := *target.Focus
			target.Focus = &focus
		}
		req.AttentionTarget = &target
	}
	return req
}
