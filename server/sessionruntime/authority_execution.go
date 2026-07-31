package sessionruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"core/server/runtime"
	"core/server/tools"
	"core/server/workflow"
	"core/shared/runtimeids"
	"core/shared/serverapi"
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

type ExecutionPromptSnapshot struct {
	Scope     ExecutionScope
	Request   tools.AskQuestionRequest
	CreatedAt time.Time
}

// WorkflowPromptResolution is the exact volatile prompt scope selected for a
// Task-owned question answer. It is valid only while the Authority retains the
// matching execution and pending prompt.
type WorkflowPromptResolution struct {
	ScopeID     runtimeids.ExecutionScopeID
	SessionID   runtimeids.SessionID
	CurrentNode workflow.CurrentNodeReference
}

// ErrWorkflowPromptAmbiguous means more than one exact live workflow scope has
// the requested pending prompt. The caller must not choose one arbitrarily.
var ErrWorkflowPromptAmbiguous = errors.New("workflow prompt is ambiguous")

type ExecutionPromptFeed interface {
	PromptPending(runtimeids.SessionResourceRef, runtimeids.ExecutionScopeID, tools.AskQuestionRequest, time.Time)
	PromptResolved(runtimeids.SessionResourceRef, runtimeids.ExecutionScopeID, string)
}

type execution struct {
	authority *Authority
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

	closeResource bool
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
	select {
	case <-h.execution.done:
		return false
	default:
		h.execution.cancel()
		return true
	}
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
	drainErr := e.drainQueuedWorkBeforeRetirement(runErr, stopErr)
	cleanupErr := e.cleanup()
	e.retire()

	authority := e.authority
	executionErr := errors.Join(runErr, drainErr)
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
	}
	e.resultMu.Lock()
	e.result = result
	e.runErr = errors.Join(executionErr, cleanupErr, closeErr)
	e.stopErr = errors.Join(stopErr, drainErr, cleanupErr, closeErr)
	e.resultMu.Unlock()
	if _, hasWorkflow := e.scope.Workflow(); hasWorkflow && authority.executionFinalized != nil {
		authority.executionFinalized.ExecutionFinalized(e.scope)
	}
	close(e.done)
}

func (e *execution) drainQueuedWorkBeforeRetirement(runErr error, stopErr error) error {
	if e.resource == nil ||
		!e.closeResource ||
		runErr != nil ||
		stopErr != nil ||
		context.Cause(e.ctx) != nil {
		return nil
	}
	return e.resource.withEngine(e.ctx, e.resource.ref, func(ctx context.Context, engine *runtime.Engine) error {
		return engine.DrainQueuedUserMessagesBeforeClose(ctx)
	})
}

func (e *execution) retire() {
	authority := e.authority
	authority.mu.Lock()
	if authority.byScope[e.scope.ID()] == e {
		delete(authority.byScope, e.scope.ID())
	}
	e.retireWorkflowLocked()
	authority.mu.Unlock()
}

// beginWorkflowFinalization removes a terminal Script from workflow liveness
// indexes while retaining its Exact Execution Scope for its completion
// finalizer. A Script becomes terminal when its process exits or Start fails.
// Its finalizer can still prove Current Node ownership, but no longer
// authorizes Interrupt or appears in queued/running read models.
func (e *execution) beginWorkflowFinalization() {
	e.authority.mu.Lock()
	if e.authority.byScope[e.scope.ID()] != e {
		e.authority.mu.Unlock()
		return
	}
	if e.phase != executionPhaseQueued && e.phase != executionPhaseRunning {
		e.authority.mu.Unlock()
		panic(fmt.Sprintf(
			"workflow execution scope %s began finalization from phase %d",
			e.scope.ID(),
			e.phase,
		))
	}
	e.phase = executionPhaseFinalizing
	e.retireWorkflowLocked()
	e.authority.mu.Unlock()
}

func (e *execution) retireWorkflowLocked() {
	workflowRef, hasWorkflow := e.scope.Workflow()
	if !hasWorkflow {
		return
	}
	workflowKey, err := workflowExecutionKeyFor(workflowRef)
	if err != nil {
		panic(fmt.Sprintf("retire workflow execution scope %s: %v", e.scope.ID(), err))
	}
	e.authority.removeWorkflowExecutionLocked(workflowRef, workflowKey, e)
}

func (e *execution) cleanup() error {
	e.prompts.Close(context.Canceled)
	if e.resource == nil {
		return nil
	}
	resource := e.resource
	resource.mu.Lock()
	defer resource.mu.Unlock()
	if resource.current != e {
		return fmt.Errorf(
			"agent execution scope %s is not current for resource %s generation %d",
			e.scope.ID(),
			resource.ref.SessionID(),
			resource.ref.Generation(),
		)
	}
	var cleanupErr error
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
	if e.workflow != nil {
		cleanupErr = errors.Join(cleanupErr, e.workflow.Close())
		e.workflow = nil
	}
	resource.current = nil
	resource.signalLocked()
	return cleanupErr
}

type executionPromptResult struct {
	response tools.AskQuestionResponse
	err      error
}

type executionPromptEntry struct {
	snapshot ExecutionPromptSnapshot
	response chan executionPromptResult
}

type executionPromptStore struct {
	mu      sync.RWMutex
	scope   ExecutionScope
	feed    ExecutionPromptFeed
	closed  bool
	pending map[string]*executionPromptEntry
}

func newExecutionPromptStore(scope ExecutionScope, feed ExecutionPromptFeed) executionPromptStore {
	return executionPromptStore{
		scope:   scope,
		feed:    feed,
		pending: make(map[string]*executionPromptEntry),
	}
}

func (s *executionPromptStore) Await(ctx context.Context, req tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestID := strings.TrimSpace(req.ID)
	if requestID == "" {
		return tools.AskQuestionResponse{}, errors.New("prompt request id is required")
	}
	snapshot := ExecutionPromptSnapshot{
		Scope:     s.scope,
		Request:   cloneExecutionPromptRequest(req),
		CreatedAt: time.Now().UTC(),
	}
	entry := &executionPromptEntry{
		snapshot: snapshot,
		response: make(chan executionPromptResult, 1),
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return tools.AskQuestionResponse{}, context.Canceled
	}
	if _, exists := s.pending[requestID]; exists {
		s.mu.Unlock()
		return tools.AskQuestionResponse{}, fmt.Errorf("prompt %q is already pending", requestID)
	}
	s.pending[requestID] = entry
	s.mu.Unlock()
	s.publishPending(snapshot)
	defer func() {
		s.mu.Lock()
		current := s.pending[requestID]
		if current == entry {
			delete(s.pending, requestID)
		}
		s.mu.Unlock()
		if current == entry {
			s.publishResolved(snapshot)
		}
	}()
	select {
	case <-ctx.Done():
		return tools.AskQuestionResponse{}, context.Cause(ctx)
	case result := <-entry.response:
		return result.response, result.err
	}
}

func (s *executionPromptStore) Submit(resp tools.AskQuestionResponse, submitErr error) error {
	requestID := strings.TrimSpace(resp.RequestID)
	if requestID == "" {
		return errors.New("prompt response request id is required")
	}
	s.mu.Lock()
	entry := s.pending[requestID]
	if entry == nil {
		s.mu.Unlock()
		return fmt.Errorf("prompt %q not found: %w", requestID, serverapi.ErrPromptNotFound)
	}
	if submitErr == nil {
		if err := tools.ValidateAskQuestionResponse(entry.snapshot.Request, resp); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	delete(s.pending, requestID)
	s.mu.Unlock()
	entry.response <- executionPromptResult{response: resp, err: submitErr}
	s.publishResolved(entry.snapshot)
	return nil
}

func (s *executionPromptStore) Close(err error) {
	if err == nil {
		err = context.Canceled
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	entries := make([]*executionPromptEntry, 0, len(s.pending))
	for requestID, entry := range s.pending {
		entries = append(entries, entry)
		delete(s.pending, requestID)
	}
	s.mu.Unlock()
	for _, entry := range entries {
		entry.response <- executionPromptResult{err: err}
		s.publishResolved(entry.snapshot)
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

func (s *executionPromptStore) publishPending(snapshot ExecutionPromptSnapshot) {
	if s.feed == nil {
		return
	}
	resource, ok := snapshot.Scope.Resource()
	if !ok {
		panic(fmt.Sprintf("agent execution prompt scope %s has no session resource", snapshot.Scope.ID()))
	}
	s.feed.PromptPending(resource, snapshot.Scope.ID(), cloneExecutionPromptRequest(snapshot.Request), snapshot.CreatedAt)
}

func (s *executionPromptStore) publishResolved(snapshot ExecutionPromptSnapshot) {
	if s.feed == nil {
		return
	}
	resource, ok := snapshot.Scope.Resource()
	if !ok {
		panic(fmt.Sprintf("agent execution prompt scope %s has no session resource", snapshot.Scope.ID()))
	}
	s.feed.PromptResolved(resource, snapshot.Scope.ID(), snapshot.Request.ID)
}

func (a *Authority) AwaitPromptResponse(ctx context.Context, scopeID runtimeids.ExecutionScopeID, req tools.AskQuestionRequest) (tools.AskQuestionResponse, error) {
	if a == nil {
		return tools.AskQuestionResponse{}, errors.New("session runtime authority is required")
	}
	if _, err := runtimeids.ParseStepID(req.StepID); err != nil {
		return tools.AskQuestionResponse{}, fmt.Errorf("pending prompt step identity: %w", err)
	}
	a.mu.Lock()
	execution := a.byScope[scopeID]
	a.mu.Unlock()
	if execution == nil || execution.scope.Kind() != ExecutionScopeAgent {
		return tools.AskQuestionResponse{}, fmt.Errorf("execution scope %s is unavailable", scopeID)
	}
	return execution.prompts.Await(ctx, req)
}

func (a *Authority) SubmitPromptResponse(sessionID runtimeids.SessionID, resp tools.AskQuestionResponse, err error) error {
	execution := a.sessionExecution(sessionID)
	if execution == nil {
		return fmt.Errorf("session %s has no active execution", sessionID)
	}
	return execution.prompts.Submit(resp, err)
}

// ResolvePendingWorkflowPrompt selects one exact live Agent scope by Task and
// prompt ID. It deliberately reads only Authority-owned volatile execution
// state; persisted Run history is not part of prompt ownership.
func (a *Authority) ResolvePendingWorkflowPrompt(taskID workflow.TaskID, askID string) (WorkflowPromptResolution, error) {
	if a == nil {
		return WorkflowPromptResolution{}, errors.New("session runtime authority is required")
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return WorkflowPromptResolution{}, errors.New("workflow task id is required")
	}
	askID = strings.TrimSpace(askID)
	if askID == "" {
		return WorkflowPromptResolution{}, errors.New("prompt request id is required")
	}

	a.mu.Lock()
	executions := make([]*execution, 0, len(a.byScope))
	for _, candidate := range a.byScope {
		executions = append(executions, candidate)
	}
	a.mu.Unlock()

	var resolved *WorkflowPromptResolution
	for _, candidate := range executions {
		if candidate.scope.Kind() != ExecutionScopeAgent {
			continue
		}
		workflowRef, isWorkflow := candidate.scope.Workflow()
		if !isWorkflow || workflowRef.CurrentNode.TaskID != taskID || !candidate.prompts.hasPendingID(askID) {
			continue
		}
		resource, isAgent := candidate.scope.Resource()
		if !isAgent {
			panic(fmt.Sprintf("workflow agent scope %s has no session resource", candidate.scope.ID()))
		}
		next := WorkflowPromptResolution{
			ScopeID:     candidate.scope.ID(),
			SessionID:   resource.SessionID(),
			CurrentNode: workflowRef.CurrentNode,
		}
		if resolved != nil {
			return WorkflowPromptResolution{}, ErrWorkflowPromptAmbiguous
		}
		resolved = &next
	}
	if resolved == nil {
		return WorkflowPromptResolution{}, serverapi.ErrPromptNotFound
	}
	return *resolved, nil
}

// SubmitPromptResponseForScope delivers an answer only to its previously
// resolved exact scope. A scope retirement between resolve and submit is a
// stale prompt, never a reason to redirect by Session.
func (a *Authority) SubmitPromptResponseForScope(scopeID runtimeids.ExecutionScopeID, resp tools.AskQuestionResponse, err error) error {
	if a == nil {
		return errors.New("session runtime authority is required")
	}
	if scopeID.IsZero() {
		return errors.New("execution scope id is required")
	}
	a.mu.Lock()
	execution := a.byScope[scopeID]
	a.mu.Unlock()
	if execution == nil || execution.scope.Kind() != ExecutionScopeAgent {
		return serverapi.ErrPromptNotFound
	}
	return execution.prompts.Submit(resp, err)
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
