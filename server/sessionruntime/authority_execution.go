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
	e.exactMu.Lock()
	defer e.exactMu.Unlock()
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
	promptErr := e.prompts.Close(context.Canceled)
	var bindingErr error
	if e.workflow != nil {
		bindingErr = e.workflow.Close()
		e.workflow = nil
	} else if e.resource != nil {
		bindingErr = e.resource.engine.FinishCurrentNodeExecutionActivation()
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
	response tools.AskQuestionResponse
	err      error
}

// PromptResponseAcceptance is returned after a prompt response mutation has
// been accepted. Successor observation is latched, so every retry sees the
// same accepted result even after the successor has already been resolved.
type PromptResponseAcceptance struct {
	observation *promptSuccessorObservation
}

func (a PromptResponseAcceptance) AwaitSuccessor(ctx context.Context) error {
	if a.observation == nil {
		return errors.New("prompt response acceptance is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-a.observation.done:
		return nil
	default:
	}
	select {
	case <-a.observation.done:
		return nil
	case <-ctx.Done():
		select {
		case <-a.observation.done:
			return nil
		default:
			return context.Cause(ctx)
		}
	}
}

type promptSuccessorObservation struct {
	done         chan struct{}
	observed     bool
	successorIDs []string
}

type executionPromptEntry struct {
	snapshot        ExecutionPromptSnapshot
	response        chan executionPromptResult
	publicationDone chan struct{}
}

type executionPromptClosure struct {
	err          error
	entries      []*executionPromptEntry
	observations []*promptSuccessorObservation
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
	mu                    sync.RWMutex
	scope                 ExecutionScope
	feed                  ExecutionPromptFeed
	closed                bool
	pending               map[string]*executionPromptEntry
	successorObservations map[string]map[*promptSuccessorObservation]struct{}
}

func newExecutionPromptStore(authority *Authority, scope ExecutionScope, feed ExecutionPromptFeed) executionPromptStore {
	return executionPromptStore{
		authority: authority,
		scope:     scope,
		feed:      feed,
		pending:   make(map[string]*executionPromptEntry),
	}
}

func (s *executionPromptStore) Await(ctx context.Context, req tools.AskQuestionRequest) (response tools.AskQuestionResponse, returnErr error) {
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
		snapshot:        snapshot,
		response:        make(chan executionPromptResult, 1),
		publicationDone: make(chan struct{}),
	}
	if s.authority == nil {
		return tools.AskQuestionResponse{}, errors.New("session runtime authority is required")
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
	if err := s.publishPending(snapshot); err != nil {
		s.mu.Lock()
		delete(s.pending, requestID)
		s.mu.Unlock()
		close(entry.publicationDone)
		return tools.AskQuestionResponse{}, err
	}
	close(entry.publicationDone)
	s.mu.Lock()
	s.observePromptSuccessorLocked(requestID)
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
		return tools.AskQuestionResponse{}, context.Cause(ctx)
	case result := <-entry.response:
		return result.response, result.err
	}
}

func (s *executionPromptStore) Submit(resp tools.AskQuestionResponse, submitErr error) error {
	_, err := s.submit(resp, submitErr, false)
	return err
}

func (s *executionPromptStore) Accept(
	resp tools.AskQuestionResponse,
	submitErr error,
) (PromptResponseAcceptance, error) {
	acceptance, err := s.submit(resp, submitErr, true)
	if err != nil {
		return PromptResponseAcceptance{}, err
	}
	return acceptance, nil
}

func (s *executionPromptStore) submit(
	resp tools.AskQuestionResponse,
	submitErr error,
	latchSuccessor bool,
) (PromptResponseAcceptance, error) {
	requestID := strings.TrimSpace(resp.RequestID)
	if requestID == "" {
		return PromptResponseAcceptance{}, errors.New("prompt response request id is required")
	}
	if s.authority == nil {
		return PromptResponseAcceptance{}, errors.New("session runtime authority is required")
	}
	s.mu.Lock()
	entry := s.pending[requestID]
	if entry == nil {
		s.mu.Unlock()
		return PromptResponseAcceptance{}, fmt.Errorf(
			"prompt %q not found: %w",
			requestID,
			serverapi.ErrPromptNotFound,
		)
	}
	if submitErr == nil {
		if err := tools.ValidateAskQuestionResponse(entry.snapshot.Request, resp); err != nil {
			s.mu.Unlock()
			return PromptResponseAcceptance{}, err
		}
	}
	successorIDs, err := preparedSuccessorPromptIDs(entry.snapshot.Request, submitErr)
	if err != nil {
		delete(s.pending, requestID)
		s.mu.Unlock()
		<-entry.publicationDone
		publicationErr := s.publishResolved(entry.snapshot)
		entry.response <- executionPromptResult{err: err}
		return PromptResponseAcceptance{}, errors.Join(err, publicationErr)
	}
	acceptance := PromptResponseAcceptance{}
	if latchSuccessor {
		observation := &promptSuccessorObservation{
			done:         make(chan struct{}),
			successorIDs: successorIDs,
		}
		acceptance.observation = observation
		s.registerPromptSuccessorObservationLocked(observation)
	}
	delete(s.pending, requestID)
	s.mu.Unlock()
	<-entry.publicationDone
	publicationErr := s.publishResolved(entry.snapshot)
	entry.response <- executionPromptResult{response: resp, err: submitErr}
	return acceptance, publicationErr
}

func preparedSuccessorPromptIDs(req tools.AskQuestionRequest, submitErr error) ([]string, error) {
	batch := req.QuestionBatch
	if batch == nil {
		return nil, nil
	}
	invalid := func(detail string) ([]string, error) {
		return nil, PromptBatchInvariantError{PromptID: req.ID, Detail: detail}
	}
	if batch.Origin != tools.AskQuestionOriginModelTool {
		return invalid(fmt.Sprintf("origin is %q", batch.Origin))
	}
	if batch.Origin != req.Origin {
		return invalid(fmt.Sprintf("origin %q does not match request origin %q", batch.Origin, req.Origin))
	}
	if strings.TrimSpace(batch.RunID) == "" || batch.RunID != req.RunID {
		return invalid(fmt.Sprintf("run id %q does not match request run id %q", batch.RunID, req.RunID))
	}
	if strings.TrimSpace(batch.StepID) == "" || batch.StepID != req.StepID {
		return invalid(fmt.Sprintf("step id %q does not match request step id %q", batch.StepID, req.StepID))
	}
	if strings.TrimSpace(batch.BatchID) == "" || strings.TrimSpace(batch.BatchID) != batch.BatchID {
		return invalid("batch id is blank or not normalized")
	}
	if batch.PromptID != req.ID {
		return invalid(fmt.Sprintf("metadata prompt id %q does not match request id", batch.PromptID))
	}
	if batch.PreparedPromptCount != len(batch.BatchPromptIDs) {
		return invalid(fmt.Sprintf(
			"prepared prompt count %d does not match prompt id count %d",
			batch.PreparedPromptCount,
			len(batch.BatchPromptIDs),
		))
	}
	if batch.CandidateOrdinal < 0 || batch.CandidateOrdinal >= len(batch.BatchPromptIDs) {
		return invalid(fmt.Sprintf(
			"candidate ordinal %d is outside %d prompt ids",
			batch.CandidateOrdinal,
			len(batch.BatchPromptIDs),
		))
	}
	seen := make(map[string]struct{}, len(batch.BatchPromptIDs))
	for index, raw := range batch.BatchPromptIDs {
		promptID := strings.TrimSpace(raw)
		if promptID == "" || promptID != raw {
			return invalid(fmt.Sprintf("prompt id at index %d is blank or not normalized", index))
		}
		if _, exists := seen[promptID]; exists {
			return invalid(fmt.Sprintf("prompt id %q is duplicated", promptID))
		}
		seen[promptID] = struct{}{}
	}
	if batch.BatchPromptIDs[batch.CandidateOrdinal] != req.ID {
		return invalid(fmt.Sprintf(
			"prompt id at candidate ordinal %d is %q",
			batch.CandidateOrdinal,
			batch.BatchPromptIDs[batch.CandidateOrdinal],
		))
	}
	if submitErr != nil {
		return nil, nil
	}
	return append([]string(nil), batch.BatchPromptIDs[batch.CandidateOrdinal+1:]...), nil
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
	closure := executionPromptClosure{
		err:     err,
		entries: make([]*executionPromptEntry, 0, len(s.pending)),
	}
	for requestID, entry := range s.pending {
		closure.entries = append(closure.entries, entry)
		delete(s.pending, requestID)
	}
	observations := make(map[*promptSuccessorObservation]struct{})
	for _, byObservation := range s.successorObservations {
		for observation := range byObservation {
			observations[observation] = struct{}{}
		}
	}
	closure.observations = make([]*promptSuccessorObservation, 0, len(observations))
	for observation := range observations {
		closure.observations = append(closure.observations, observation)
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
		entry.response <- executionPromptResult{err: closure.err}
	}
	s.mu.Lock()
	for _, observation := range closure.observations {
		s.markPromptSuccessorObservedLocked(observation)
	}
	s.mu.Unlock()
}

func (s *executionPromptStore) registerPromptSuccessorObservationLocked(
	observation *promptSuccessorObservation,
) {
	if observation == nil {
		panic("prompt successor observation is required")
	}
	if len(observation.successorIDs) == 0 || s.closed {
		s.markPromptSuccessorObservedLocked(observation)
		return
	}
	for _, requestID := range observation.successorIDs {
		if _, pending := s.pending[requestID]; pending {
			s.markPromptSuccessorObservedLocked(observation)
			return
		}
	}
	if s.successorObservations == nil {
		s.successorObservations = make(map[string]map[*promptSuccessorObservation]struct{})
	}
	for _, requestID := range observation.successorIDs {
		observations := s.successorObservations[requestID]
		if observations == nil {
			observations = make(map[*promptSuccessorObservation]struct{})
			s.successorObservations[requestID] = observations
		}
		observations[observation] = struct{}{}
	}
}

func (s *executionPromptStore) observePromptSuccessorLocked(requestID string) {
	observations := s.successorObservations[requestID]
	if len(observations) == 0 {
		return
	}
	pending := make([]*promptSuccessorObservation, 0, len(observations))
	for observation := range observations {
		pending = append(pending, observation)
	}
	for _, observation := range pending {
		s.markPromptSuccessorObservedLocked(observation)
	}
}

func (s *executionPromptStore) markPromptSuccessorObservedLocked(
	observation *promptSuccessorObservation,
) {
	if observation == nil || observation.observed {
		return
	}
	observation.observed = true
	close(observation.done)
	for _, requestID := range observation.successorIDs {
		observations := s.successorObservations[requestID]
		delete(observations, observation)
		if len(observations) == 0 {
			delete(s.successorObservations, requestID)
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

func (a *Authority) AcceptPromptResponse(
	sessionID runtimeids.SessionID,
	resp tools.AskQuestionResponse,
	err error,
) (PromptResponseAcceptance, error) {
	execution := a.sessionExecution(sessionID)
	if execution == nil {
		return PromptResponseAcceptance{}, fmt.Errorf("session %s has no active execution", sessionID)
	}
	return execution.prompts.Accept(resp, err)
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
	defer a.mu.Unlock()
	var resolved *WorkflowPromptResolution
	for _, candidate := range a.byScope {
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
	execution, resolveErr := a.agentExecutionByScope(scopeID)
	if resolveErr != nil {
		return resolveErr
	}
	return execution.prompts.Submit(resp, err)
}

func (a *Authority) AcceptPromptResponseForScope(
	scopeID runtimeids.ExecutionScopeID,
	resp tools.AskQuestionResponse,
	err error,
) (PromptResponseAcceptance, error) {
	execution, resolveErr := a.agentExecutionByScope(scopeID)
	if resolveErr != nil {
		return PromptResponseAcceptance{}, resolveErr
	}
	return execution.prompts.Accept(resp, err)
}

func (a *Authority) agentExecutionByScope(scopeID runtimeids.ExecutionScopeID) (*execution, error) {
	if a == nil {
		return nil, errors.New("session runtime authority is required")
	}
	if scopeID.IsZero() {
		return nil, errors.New("execution scope id is required")
	}
	a.mu.Lock()
	execution := a.byScope[scopeID]
	a.mu.Unlock()
	if execution == nil || execution.scope.Kind() != ExecutionScopeAgent {
		return nil, serverapi.ErrPromptNotFound
	}
	return execution, nil
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
