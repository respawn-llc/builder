package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func newCurrentNodeControllerForTest(
	t *testing.T,
	store *currentNodeControllerStore,
	runner CurrentNodeRunner,
	authority *sessionruntime.Authority,
	concurrency int,
) *CurrentNodeController {
	return newCurrentNodeControllerWithAttentionForTest(t, store, runner, authority, concurrency, nil)
}

func newCurrentNodeControllerWithAttentionForTest(
	t *testing.T,
	store *currentNodeControllerStore,
	runner CurrentNodeRunner,
	authority *sessionruntime.Authority,
	concurrency int,
	attention CurrentNodeAttentionLifecycle,
) *CurrentNodeController {
	t.Helper()
	controller, err := NewCurrentNodeController(store, runner, authority, NewMutationPermit(), CurrentNodeControllerConfig{
		AgentConcurrency:  concurrency,
		Attention:         attention,
		AssignmentEnsurer: noOpCurrentNodeAssignmentEnsurer{},
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	return controller
}

type noOpCurrentNodeAssignmentEnsurer struct{}

func (noOpCurrentNodeAssignmentEnsurer) EnsureCurrentNodeAssignment(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
) (CurrentNodeAssignmentEnsure, error) {
	return completedCurrentNodeAssignmentEnsure{
		receipt: session.CommitReceipt{Committed: true},
	}, nil
}

type completedCurrentNodeAssignmentEnsure struct {
	receipt session.CommitReceipt
	err     error
}

func (s completedCurrentNodeAssignmentEnsure) Wait(context.Context) (session.CommitReceipt, error) {
	return s.receipt, s.err
}

type deadlineRecordingCurrentNodeAssignmentEnsurer struct {
	reference workflow.CurrentNodeReference
	deadline  chan<- time.Time
}

func (s deadlineRecordingCurrentNodeAssignmentEnsurer) EnsureCurrentNodeAssignment(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
) (CurrentNodeAssignmentEnsure, error) {
	if !reference.Equal(s.reference) {
		return completedCurrentNodeAssignmentEnsure{
			receipt: session.CommitReceipt{Committed: true},
		}, nil
	}
	return &deadlineRecordingCurrentNodeAssignmentEnsure{deadline: s.deadline}, nil
}

type deadlineRecordingCurrentNodeAssignmentEnsure struct {
	deadline chan<- time.Time
	mu       sync.Mutex
	recorded bool
}

func (s *deadlineRecordingCurrentNodeAssignmentEnsure) Wait(ctx context.Context) (session.CommitReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recorded {
		return session.CommitReceipt{Committed: true}, nil
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return session.CommitReceipt{}, errors.New("assignment ensure wait context has no deadline")
	}
	s.recorded = true
	s.deadline <- deadline
	return session.CommitReceipt{Committed: true}, nil
}

type lateCommitCurrentNodeAssignmentEnsurer struct {
	release <-chan struct{}
	started chan struct{}
}

func (s lateCommitCurrentNodeAssignmentEnsurer) EnsureCurrentNodeAssignment(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
) (CurrentNodeAssignmentEnsure, error) {
	return &lateCommitCurrentNodeAssignmentEnsure{
		release: s.release,
		started: s.started,
	}, nil
}

type lateCommitCurrentNodeAssignmentEnsure struct {
	release <-chan struct{}
	started chan struct{}
	once    sync.Once
}

func (s *lateCommitCurrentNodeAssignmentEnsure) Wait(ctx context.Context) (session.CommitReceipt, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return session.CommitReceipt{Committed: true}, nil
	case <-ctx.Done():
		return session.CommitReceipt{}, context.Cause(ctx)
	}
}

type recordingCurrentNodeAssignmentEnsurer struct {
	mu          sync.Mutex
	steered     []workflow.CurrentNodeReference
	outcomes    []currentNodeAssignmentEnsureOutcome
	errors      map[workflow.CurrentNodeReferenceKey]error
	waitErrors  map[workflow.CurrentNodeReferenceKey]error
	err         error
	waitReceipt session.CommitReceipt
	waitErr     error
}

type currentNodeAssignmentEnsureOutcome struct {
	receipt   session.CommitReceipt
	ensureErr error
	waitErr   error
}

func (s *recordingCurrentNodeAssignmentEnsurer) EnsureCurrentNodeAssignment(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
) (CurrentNodeAssignmentEnsure, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steered = append(s.steered, reference)
	key, keyErr := reference.Key()
	if keyErr != nil {
		return nil, keyErr
	}
	if ensureErr := s.errors[key]; ensureErr != nil {
		return nil, ensureErr
	}
	if waitErr := s.waitErrors[key]; waitErr != nil {
		return completedCurrentNodeAssignmentEnsure{err: waitErr}, nil
	}
	index := len(s.steered) - 1
	if index < len(s.outcomes) {
		outcome := s.outcomes[index]
		if outcome.ensureErr != nil {
			return nil, outcome.ensureErr
		}
		return completedCurrentNodeAssignmentEnsure{
			receipt: outcome.receipt,
			err:     outcome.waitErr,
		}, nil
	}
	if s.err != nil {
		return nil, s.err
	}
	receipt := s.waitReceipt
	if s.waitErr == nil {
		receipt.Committed = true
	}
	return completedCurrentNodeAssignmentEnsure{receipt: receipt, err: s.waitErr}, nil
}

func (s *recordingCurrentNodeAssignmentEnsurer) references() []workflow.CurrentNodeReference {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]workflow.CurrentNodeReference(nil), s.steered...)
}

func (s *recordingCurrentNodeAssignmentEnsurer) setWaitError(err error) {
	s.mu.Lock()
	s.waitErr = err
	s.mu.Unlock()
}

func (s *recordingCurrentNodeAssignmentEnsurer) setError(
	reference workflow.CurrentNodeReference,
	err error,
) {
	key, keyErr := reference.Key()
	if keyErr != nil {
		panic(fmt.Sprintf("set assignment ensure error for %v: %v", reference, keyErr))
	}
	s.mu.Lock()
	if s.errors == nil {
		s.errors = make(map[workflow.CurrentNodeReferenceKey]error)
	}
	s.errors[key] = err
	s.mu.Unlock()
}

func (s *recordingCurrentNodeAssignmentEnsurer) setWaitErrorFor(
	reference workflow.CurrentNodeReference,
	err error,
) {
	key, keyErr := reference.Key()
	if keyErr != nil {
		panic(fmt.Sprintf("set assignment ensure wait error for %v: %v", reference, keyErr))
	}
	s.mu.Lock()
	if s.waitErrors == nil {
		s.waitErrors = make(map[workflow.CurrentNodeReferenceKey]error)
	}
	s.waitErrors[key] = err
	s.mu.Unlock()
}

func startCurrentNodeForControllerTest(
	ctx context.Context,
	controller *CurrentNodeController,
	store *currentNodeControllerStore,
	reference workflow.CurrentNodeReference,
) error {
	store.mu.Lock()
	store.started = workflowstore.StartTaskResult{Mutation: workflow.CurrentNodeMutationResult{
		Created: []workflow.CurrentNode{{
			Reference:  reference,
			Scheduling: &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
		}},
	}}
	store.mu.Unlock()
	_, err := controller.StartTask(ctx, reference.TaskID, func(context.Context) error { return nil })
	return err
}

func currentNodeReferenceForControllerTest(t *testing.T, taskID string, nodeID string) workflow.CurrentNodeReference {
	t.Helper()
	reference, err := workflow.NewCurrentNodeReference(workflow.TaskID(taskID), workflow.NodeID(nodeID), nil)
	if err != nil {
		t.Fatalf("new current node reference: %v", err)
	}
	return reference
}

func installCurrentNodeRunLockedForTest(
	controller *CurrentNodeController,
	run *currentNodeRun,
) workflow.CurrentNodeReferenceKey {
	registered, _, err := controller.runs.register(run)
	if err != nil {
		panic(fmt.Sprintf("install current node Run for test: %v", err))
	}
	return mustCurrentNodeRunKey(registered)
}

func publishCurrentNodeRunForControllerTest(
	t *testing.T,
	controller *CurrentNodeController,
	reference workflow.CurrentNodeReference,
) {
	t.Helper()
	delta, err := workflowstore.NewTaskLifecycleDelta(reference.TaskID, []workflowstore.LifecycleRunDelta{{
		CurrentNode: reference,
		Expect:      workflowstore.LifecycleFieldAbsent,
		Next:        workflowstore.LifecycleFieldPresent,
	}}, nil)
	if err != nil {
		t.Fatalf("new Current Node Run lifecycle delta: %v", err)
	}
	if err := controller.publication.Publish(context.Background(), delta); err != nil {
		t.Fatalf("publish Current Node Run: %v", err)
	}
}

func installLiveCurrentNodeRunLockedForTest(
	controller *CurrentNodeController,
	reference workflow.CurrentNodeReference,
	nodeKind workflow.NodeKind,
	policy currentNodeAdmissionPolicy,
	lease sessionruntime.WorkflowExecutionLease,
) {
	run := newCurrentNodeRun(reference, nodeKind, policy)
	run.phase = currentNodeRunRunning
	if err := run.transitionDisposition(currentNodeRunDispositionRunning, nil); err != nil {
		panic(fmt.Sprintf("install live current node Run for test: %v", err))
	}
	run.executionLease = &lease
	key := installCurrentNodeRunLockedForTest(controller, run)
	controller.exactScopes[lease.ScopeID()] = key
	if publication, ok := controller.publication.(*currentNodeControllerLifecyclePublication); ok {
		publication.mu.Lock()
		if publication.root == nil {
			publication.root = make(map[workflow.TaskID][]workflow.CurrentNodeReference)
		}
		if publication.exact == nil {
			publication.exact = make(map[workflow.TaskID][]workflowstore.LifecycleExactExecution)
		}
		publication.root[reference.TaskID] = append(publication.root[reference.TaskID], reference)
		publication.exact[reference.TaskID] = append(
			publication.exact[reference.TaskID],
			workflowstore.LifecycleExactExecution{CurrentNode: reference, ScopeID: lease.ScopeID()},
		)
		publication.mu.Unlock()
	}
}

func singleLiveScope(t *testing.T, controller *CurrentNodeController, reference workflow.CurrentNodeReference) runtimeids.ExecutionScopeID {
	t.Helper()
	snapshot := controller.Snapshot()
	for _, scope := range snapshot.LiveScopes {
		if scope.CurrentNode.Equal(reference) {
			return scope.ScopeID
		}
	}
	t.Fatalf("snapshot %+v has no live scope for %v", snapshot, reference)
	return runtimeids.ExecutionScopeID{}
}

func hasLiveCurrentNode(snapshot CurrentNodeExecutionSnapshot, reference workflow.CurrentNodeReference) bool {
	for _, live := range snapshot.LiveScopes {
		if live.CurrentNode.Equal(reference) {
			return true
		}
	}
	return false
}

func waitForRunningCurrentNode(
	t *testing.T,
	authority *sessionruntime.Authority,
	reference workflow.CurrentNodeReference,
) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		snapshots, err := authority.CurrentWorkflowTaskExecutionSnapshots()
		if err != nil {
			return false
		}
		for _, execution := range snapshots[reference.TaskID].Executions {
			if execution.Ref.CurrentNode.Equal(reference) &&
				!execution.Queued &&
				len(execution.PendingPrompts) == 0 {
				return true
			}
		}
		return false
	}, "current node %v did not begin running", reference)
}

func hasAutomaticCurrentNodeIntent(snapshot CurrentNodeExecutionSnapshot, reference workflow.CurrentNodeReference) bool {
	for _, intent := range snapshot.AutomaticIntents {
		if intent.CurrentNode.Equal(reference) {
			return true
		}
	}
	return false
}

var currentNodeControllerTestWorkflowID = func() runtimeids.WorkflowID {
	workflowID, err := runtimeids.ParseWorkflowID("550e8400-e29b-41d4-a716-446655440201")
	if err != nil {
		panic(err)
	}
	return workflowID
}()

type currentNodeControllerStore struct {
	mu                     sync.Mutex
	started                workflowstore.StartTaskResult
	startedByTask          map[workflow.TaskID]workflowstore.StartTaskResult
	interrupted            []workflow.CurrentNode
	pendingApproval        workflow.PendingApproval
	approvalApplied        workflowstore.PendingApprovalApplyResult
	manualMoved            workflowstore.ManualMoveResult
	admitted               []workflow.CurrentNodeReference
	admissionErrors        map[workflow.CurrentNodeReferenceKey]error
	resumed                []workflow.CurrentNodeReference
	resumeErrors           map[workflow.CurrentNodeReferenceKey]error
	resumeClassifications  []workflowstore.CurrentNodeResumeClassification
	resumeCommitStarted    chan struct{}
	resumeCommitRelease    chan struct{}
	resumeCommitOnce       sync.Once
	resumeCommitWon        chan struct{}
	resumeCommitWinRelease chan struct{}
	resumeCommitWinOnce    sync.Once
	resumePrepareStarted   chan struct{}
	resumePrepareRelease   chan struct{}
	resumePrepareOnce      sync.Once
	resumePrepareCanceled  chan struct{}
	resumeCancelOnce       sync.Once
	resumeCommitErr        error
	publication            *currentNodeControllerLifecyclePublication
	interruptions          map[workflow.CurrentNodeReferenceKey]currentNodeInterruptionRecord
	interruptionCalls      map[workflow.CurrentNodeReferenceKey]int
	interruptErr           error
	recovered              []workflow.CurrentNodeReference
	completion             workflowstore.CurrentNodeCompletionResult
	completions            int
	startTaskStarted       chan struct{}
	startTaskRelease       chan struct{}
	startTaskOnce          sync.Once
	startTaskCalls         chan workflow.TaskID
	startTaskHook          func(context.Context, workflow.TaskID) error
	completionStarted      chan struct{}
	completionRelease      chan struct{}
	completionOnce         sync.Once
	publicationStarted     chan struct{}
	publicationRelease     chan struct{}
	publicationOnce        sync.Once
	bindingErr             error
	bindings               []currentNodeSessionBindingCall
	taskBySession          map[runtimeids.SessionID]*workflow.TaskID
	currentSessionContexts map[runtimeids.SessionID]workflowstore.CurrentNodeStartContext
	currentSessionErrors   map[runtimeids.SessionID]error
	interruptStarted       chan struct{}
	interruptRelease       chan struct{}
	interruptOnce          sync.Once
	idleResolved           *workflow.CurrentNode
}

type currentNodeAttentionRecorder struct {
	mu          sync.Mutex
	pending     []workflow.CurrentNodeReference
	resolutions []workflowstore.TaskAttentionResolution
}

func (r *currentNodeAttentionRecorder) PublishPendingInterruptedCurrentNode(_ context.Context, reference workflow.CurrentNodeReference) {
	r.mu.Lock()
	r.pending = append(r.pending, reference)
	r.mu.Unlock()
}

func (r *currentNodeAttentionRecorder) FinalizeTaskResolution(resolution workflowstore.TaskAttentionResolution) {
	r.mu.Lock()
	r.resolutions = append(r.resolutions, resolution)
	r.mu.Unlock()
}

func (r *currentNodeAttentionRecorder) pendingCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pending)
}

func (r *currentNodeAttentionRecorder) resolvedInterruptions() []workflowstore.InterruptedCurrentNodeAttentionProjection {
	r.mu.Lock()
	defer r.mu.Unlock()
	var projections []workflowstore.InterruptedCurrentNodeAttentionProjection
	for _, resolution := range r.resolutions {
		projections = append(projections, resolution.InterruptedCurrentNodes...)
	}
	return projections
}

func (*currentNodeControllerStore) TaskExecutionScope(context.Context, workflow.TaskID) (workflowstore.TaskExecutionScope, error) {
	return workflowstore.TaskExecutionScope{ProjectID: "project-test", WorkflowID: currentNodeControllerTestWorkflowID}, nil
}

func (*currentNodeControllerStore) CurrentNodeKind(
	context.Context,
	workflow.CurrentNodeReference,
) (workflow.NodeKind, error) {
	return workflow.NodeKindAgent, nil
}

func (s *currentNodeControllerStore) TaskIDForSession(
	_ context.Context,
	sessionID runtimeids.SessionID,
) (*workflow.TaskID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	taskID := s.taskBySession[sessionID]
	if taskID == nil {
		return nil, nil
	}
	cloned := *taskID
	return &cloned, nil
}

func (s *currentNodeControllerStore) ResolveCurrentSessionStartContext(
	_ context.Context,
	sessionID runtimeids.SessionID,
) (workflowstore.CurrentNodeStartContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.currentSessionErrors[sessionID]; err != nil {
		return workflowstore.CurrentNodeStartContext{}, err
	}
	input, exists := s.currentSessionContexts[sessionID]
	if !exists {
		return workflowstore.CurrentNodeStartContext{}, workflowstore.ErrSessionNotCurrentWorkflowNode
	}
	return input, nil
}

func (s *currentNodeControllerStore) StartTask(ctx context.Context, taskID workflow.TaskID) (workflowstore.StartTaskResult, error) {
	if s.startTaskCalls != nil {
		s.startTaskCalls <- taskID
	}
	if s.startTaskHook != nil {
		if err := s.startTaskHook(ctx, taskID); err != nil {
			return workflowstore.StartTaskResult{}, err
		}
	}
	if s.startTaskStarted != nil {
		s.startTaskOnce.Do(func() {
			close(s.startTaskStarted)
		})
	}
	if s.startTaskRelease != nil {
		select {
		case <-s.startTaskRelease:
		case <-ctx.Done():
			return workflowstore.StartTaskResult{}, context.Cause(ctx)
		}
	}
	s.mu.Lock()
	started := s.started
	if byTask, exists := s.startedByTask[taskID]; exists {
		started = byTask
	}
	s.mu.Unlock()
	return started, nil
}

func (s *currentNodeControllerStore) InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error) {
	return append([]workflow.CurrentNode(nil), s.interrupted...), nil
}

func (s *currentNodeControllerStore) PreflightTaskResume(_ context.Context, _ workflow.TaskID) ([]workflowstore.CurrentNodeResumeClassification, error) {
	if len(s.resumeClassifications) > 0 {
		return append([]workflowstore.CurrentNodeResumeClassification(nil), s.resumeClassifications...), nil
	}
	classifications := make([]workflowstore.CurrentNodeResumeClassification, 0, len(s.interrupted))
	for _, currentNode := range s.interrupted {
		if currentNode.Scheduling == nil ||
			currentNode.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
			continue
		}
		classification := workflowstore.CurrentNodeResumeClassification{CurrentNode: currentNode}
		if key, err := currentNode.Reference.Key(); err == nil {
			if validationErr := s.resumeErrors[key]; validationErr != nil {
				var typed *workflowstore.CurrentNodeResumeValidationError
				if errors.As(validationErr, &typed) {
					classification.Diagnostics = append(classification.Diagnostics, typed.Diagnostics...)
				}
			}
		}
		classifications = append(classifications, classification)
	}
	return classifications, nil
}

func (s *currentNodeControllerStore) PendingApproval(context.Context, workflow.ApprovalID) (workflow.PendingApproval, error) {
	return s.pendingApproval, nil
}

func (s *currentNodeControllerStore) ApplyPendingApproval(context.Context, workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error) {
	return s.approvalApplied, nil
}

func (s *currentNodeControllerStore) ApplyManualMove(context.Context, workflowstore.ManualMovePreparation, *workflowstore.ExecutionTargetCandidate) (workflowstore.ManualMoveResult, error) {
	return s.manualMoved, nil
}

type currentNodeInterruptionRecord struct {
	reason workflow.CurrentNodeInterruptionReason
	detail workflow.CurrentNodeInterruptionDetail
}

type currentNodeSessionBindingCall struct {
	sessionID runtimeids.SessionID
	reference workflow.CurrentNodeReference
}

func (s *currentNodeControllerStore) AdmitCurrentNode(_ context.Context, reference workflow.CurrentNodeReference) error {
	key, err := reference.Key()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.admissionErrors[key]; err != nil {
		return err
	}
	s.admitted = append(s.admitted, reference)
	return nil
}

func (s *currentNodeControllerStore) lifecyclePublication() LifecyclePublication {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.publication == nil {
		s.publication = &currentNodeControllerLifecyclePublication{
			store: s,
			root:  make(map[workflow.TaskID][]workflow.CurrentNodeReference),
		}
	}
	return s.publication
}

type currentNodeControllerLifecyclePublication struct {
	mu     sync.RWMutex
	store  *currentNodeControllerStore
	root   map[workflow.TaskID][]workflow.CurrentNodeReference
	exact  map[workflow.TaskID][]workflowstore.LifecycleExactExecution
	closed bool
}

func (p *currentNodeControllerLifecyclePublication) Publish(
	_ context.Context,
	delta workflowstore.TaskLifecycleDelta,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return workflowstore.ErrLifecyclePublicationClosed
	}
	if p.root == nil {
		p.root = make(map[workflow.TaskID][]workflow.CurrentNodeReference)
	}
	if p.exact == nil {
		p.exact = make(map[workflow.TaskID][]workflowstore.LifecycleExactExecution)
	}
	p.store.mu.Lock()
	publicationStarted := p.store.publicationStarted
	publicationRelease := p.store.publicationRelease
	p.store.mu.Unlock()
	if publicationStarted != nil {
		p.store.publicationOnce.Do(func() {
			close(publicationStarted)
		})
	}
	if publicationRelease != nil {
		<-publicationRelease
	}
	taskID := delta.TaskID()
	for _, change := range delta.RunChanges() {
		present := false
		for _, reference := range p.root[taskID] {
			if reference.Equal(change.CurrentNode) {
				present = true
				break
			}
		}
		if present != (change.Expect == workflowstore.LifecycleFieldPresent) {
			return errors.New("lifecycle Run predecessor conflict")
		}
		if change.Next == workflowstore.LifecycleFieldPresent && !present {
			p.root[taskID] = append(p.root[taskID], change.CurrentNode)
		} else if change.Next == workflowstore.LifecycleFieldAbsent {
			filtered := p.root[taskID][:0]
			for _, reference := range p.root[taskID] {
				if !reference.Equal(change.CurrentNode) {
					filtered = append(filtered, reference)
				}
			}
			p.root[taskID] = filtered
		}
	}
	for _, change := range delta.ExactChanges() {
		found := false
		filtered := p.exact[taskID][:0]
		for _, exact := range p.exact[taskID] {
			if !exact.CurrentNode.Equal(change.CurrentNode) {
				filtered = append(filtered, exact)
				continue
			}
			if change.ExpectScope == nil || exact.ScopeID != *change.ExpectScope {
				return errors.New("lifecycle Exact predecessor conflict")
			}
			found = true
		}
		if change.ExpectScope != nil && !found {
			return errors.New("lifecycle Exact predecessor conflict")
		}
		if change.ExpectScope == nil && len(filtered) != len(p.exact[taskID]) {
			return errors.New("lifecycle Exact predecessor conflict")
		}
		if change.Next != nil {
			filtered = append(filtered, *change.Next)
		}
		p.exact[taskID] = filtered
	}
	return nil
}

func (p *currentNodeControllerLifecyclePublication) PublishCurrentNodeAdmission(
	_ context.Context,
	reference workflow.CurrentNodeReference,
) error {
	key, err := reference.Key()
	if err != nil {
		return err
	}
	p.store.mu.Lock()
	if err := p.store.admissionErrors[key]; err != nil {
		p.store.mu.Unlock()
		return err
	}
	p.store.admitted = append(p.store.admitted, reference)
	p.store.mu.Unlock()
	return nil
}

func (p *currentNodeControllerLifecyclePublication) PublishExactRegistration(
	_ context.Context,
	exact workflowstore.LifecycleExactExecution,
) error {
	reference := exact.CurrentNode
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return workflowstore.ErrLifecyclePublicationClosed
	}
	if p.exact == nil {
		p.exact = make(map[workflow.TaskID][]workflowstore.LifecycleExactExecution)
	}
	for _, current := range p.exact[reference.TaskID] {
		if current.CurrentNode.Equal(reference) {
			return errors.New("lifecycle Exact predecessor conflict")
		}
	}
	p.exact[reference.TaskID] = append(p.exact[reference.TaskID], exact)
	return nil
}

func (p *currentNodeControllerLifecyclePublication) PublishExactPromptPending(
	_ context.Context,
	scopeID runtimeids.ExecutionScopeID,
	prompt workflowstore.LifecyclePendingPrompt,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for taskID, executions := range p.exact {
		for index := range executions {
			if executions[index].ScopeID != scopeID {
				continue
			}
			executions[index].PendingPrompts = append(executions[index].PendingPrompts, prompt)
			p.exact[taskID] = executions
			return nil
		}
	}
	return sessionruntime.ErrExecutionNoLongerLive
}

func (p *currentNodeControllerLifecyclePublication) PublishExactPromptResolved(
	_ context.Context,
	scopeID runtimeids.ExecutionScopeID,
	promptID string,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for taskID, executions := range p.exact {
		for index := range executions {
			if executions[index].ScopeID != scopeID {
				continue
			}
			prompts := executions[index].PendingPrompts
			for promptIndex, prompt := range prompts {
				if prompt.ID == promptID {
					executions[index].PendingPrompts = append(
						prompts[:promptIndex:promptIndex],
						prompts[promptIndex+1:]...,
					)
					p.exact[taskID] = executions
					return nil
				}
			}
		}
	}
	return sessionruntime.ErrExecutionNoLongerLive
}

func (p *currentNodeControllerLifecyclePublication) PublishExactFinalizing(
	_ context.Context,
	scopeID runtimeids.ExecutionScopeID,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for taskID, executions := range p.exact {
		for index := range executions {
			if executions[index].ScopeID == scopeID {
				executions[index].Phase = workflowstore.LifecycleExactExecutionFinalizing
				p.exact[taskID] = executions
				return nil
			}
		}
	}
	return sessionruntime.ErrExecutionNoLongerLive
}

func (p *currentNodeControllerLifecyclePublication) PublishCurrentNodeInterruption(
	ctx context.Context,
	references []workflow.CurrentNodeReference,
	predecessor workflowstore.CurrentNodeInterruptionPredecessor,
	expectedRun workflowstore.LifecycleFieldPresence,
	reason workflow.CurrentNodeInterruptionReason,
	detail workflow.CurrentNodeInterruptionDetail,
	expectedExact []workflowstore.LifecycleExactExecution,
) error {
	for _, reference := range references {
		var err error
		switch predecessor {
		case workflowstore.CurrentNodeInterruptionFromAdmitted:
			err = p.store.InterruptAdmittedCurrentNode(ctx, reference, reason, detail)
		case workflowstore.CurrentNodeInterruptionFromReadyOrAdmitted:
			err = p.store.InterruptCurrentNode(ctx, reference, reason, detail)
		default:
			return errors.New("current node interruption predecessor is invalid")
		}
		if err != nil {
			return err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return workflowstore.ErrLifecyclePublicationClosed
	}
	expectedByNode := make(map[workflow.CurrentNodeReferenceKey]runtimeids.ExecutionScopeID, len(expectedExact))
	for _, exact := range expectedExact {
		key, err := exact.CurrentNode.Key()
		if err != nil {
			return err
		}
		expectedByNode[key] = exact.ScopeID
	}
	taskID := references[0].TaskID
	if p.exact == nil {
		p.exact = make(map[workflow.TaskID][]workflowstore.LifecycleExactExecution)
	}
	for _, reference := range references {
		key, err := reference.Key()
		if err != nil {
			return err
		}
		runPresent := false
		for _, queued := range p.root[taskID] {
			if queued.Equal(reference) {
				runPresent = true
				break
			}
		}
		if runPresent != (expectedRun == workflowstore.LifecycleFieldPresent) {
			return errors.New("lifecycle Run predecessor conflict")
		}
		currentExact := p.exact[taskID]
		foundExact := false
		filteredExact := currentExact[:0]
		for _, exact := range currentExact {
			if !exact.CurrentNode.Equal(reference) {
				filteredExact = append(filteredExact, exact)
				continue
			}
			expected, expectsExact := expectedByNode[key]
			if !expectsExact || expected != exact.ScopeID {
				return errors.New("lifecycle Exact predecessor conflict")
			}
			foundExact = true
		}
		if expected, expectsExact := expectedByNode[key]; expectsExact && (!foundExact || expected.IsZero()) {
			return errors.New("lifecycle Exact predecessor conflict")
		}
		p.exact[taskID] = filteredExact
		filteredRuns := p.root[taskID][:0]
		for _, queued := range p.root[taskID] {
			if !queued.Equal(reference) {
				filteredRuns = append(filteredRuns, queued)
			}
		}
		p.root[taskID] = filteredRuns
	}
	return nil
}

func (p *currentNodeControllerLifecyclePublication) PublishTaskStart(
	ctx context.Context,
	taskID workflow.TaskID,
	stage workflowstore.TaskStartPublicationStage,
) (workflowstore.StartTaskResult, error) {
	started, err := p.store.StartTask(ctx, taskID)
	if err != nil {
		return workflowstore.StartTaskResult{}, err
	}
	delta, rollback, err := stage(started)
	if err != nil {
		return workflowstore.StartTaskResult{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		err := workflowstore.ErrLifecyclePublicationClosed
		if rollback != nil {
			rollback(err)
		}
		return workflowstore.StartTaskResult{}, err
	}
	candidate := make(map[workflow.TaskID][]workflow.CurrentNodeReference, len(p.root)+1)
	for id, queued := range p.root {
		candidate[id] = append([]workflow.CurrentNodeReference(nil), queued...)
	}
	for _, change := range delta.RunChanges() {
		switch change.Next {
		case workflowstore.LifecycleFieldPresent:
			candidate[taskID] = append(candidate[taskID], change.CurrentNode)
		case workflowstore.LifecycleFieldAbsent:
			filtered := candidate[taskID][:0]
			for _, reference := range candidate[taskID] {
				if !reference.Equal(change.CurrentNode) {
					filtered = append(filtered, reference)
				}
			}
			candidate[taskID] = filtered
		}
	}
	p.root = candidate
	return started, nil
}

func (p *currentNodeControllerLifecyclePublication) PublishCurrentNodeCompletion(
	ctx context.Context,
	req workflowstore.CurrentNodeCompletionRequest,
	stage workflowstore.CurrentNodeCompletionPublicationStage,
) (workflowstore.CurrentNodeCompletionResult, error) {
	prepared, err := p.PrepareCurrentNodeCompletion(ctx, req, stage)
	if err != nil {
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	return prepared.Publish(ctx)
}

type preparedCurrentNodeControllerCompletionPublication struct {
	publication *currentNodeControllerLifecyclePublication
	request     workflowstore.CurrentNodeCompletionRequest
	result      workflowstore.CurrentNodeCompletionResult
	delta       workflowstore.TaskLifecycleDelta
	rollback    func(error)
}

func (p *preparedCurrentNodeControllerCompletionPublication) Result() workflowstore.CurrentNodeCompletionResult {
	return p.result
}

func (p *preparedCurrentNodeControllerCompletionPublication) Publish(
	ctx context.Context,
) (workflowstore.CurrentNodeCompletionResult, error) {
	completed, err := p.publication.store.CompleteCurrentNode(ctx, p.request)
	if err != nil {
		if p.rollback != nil {
			p.rollback(err)
		}
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	if err := p.publication.Publish(ctx, p.delta); err != nil {
		if p.rollback != nil {
			p.rollback(err)
		}
		return workflowstore.CurrentNodeCompletionResult{}, err
	}
	return completed, nil
}

func (p *preparedCurrentNodeControllerCompletionPublication) Rollback(cause error) error {
	if p.rollback != nil {
		p.rollback(cause)
	}
	return nil
}

func (p *currentNodeControllerLifecyclePublication) PrepareCurrentNodeCompletion(
	_ context.Context,
	req workflowstore.CurrentNodeCompletionRequest,
	stage workflowstore.CurrentNodeCompletionPublicationStage,
) (workflowstore.PreparedCurrentNodeCompletionPublication, error) {
	p.store.mu.Lock()
	result := p.store.completion
	p.store.mu.Unlock()
	delta, rollback, err := stage(result)
	if err != nil {
		return nil, err
	}
	return &preparedCurrentNodeControllerCompletionPublication{
		publication: p,
		request:     req,
		result:      result,
		delta:       delta,
		rollback:    rollback,
	}, nil
}

func (p *currentNodeControllerLifecyclePublication) PreviewCurrentNodeCompletion(
	_ context.Context,
	_ workflowstore.CurrentNodeCompletionRequest,
) (workflowstore.CurrentNodeCompletionResult, error) {
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	return p.store.completion, nil
}

func (p *currentNodeControllerLifecyclePublication) PublishPendingApproval(
	ctx context.Context,
	approvalID workflow.ApprovalID,
	stage workflowstore.PendingApprovalPublicationStage,
) (workflowstore.PendingApprovalApplyResult, error) {
	applied, err := p.store.ApplyPendingApproval(ctx, approvalID)
	if err != nil {
		return workflowstore.PendingApprovalApplyResult{}, err
	}
	delta, rollback, err := stage(applied)
	if err != nil {
		return workflowstore.PendingApprovalApplyResult{}, err
	}
	if err := p.Publish(ctx, delta); err != nil {
		if rollback != nil {
			rollback(err)
		}
		return workflowstore.PendingApprovalApplyResult{}, err
	}
	return applied, nil
}

func (p *currentNodeControllerLifecyclePublication) PublishManualMove(
	ctx context.Context,
	prepared workflowstore.ManualMovePreparation,
	candidate *workflowstore.ExecutionTargetCandidate,
	stage workflowstore.ManualMovePublicationStage,
) (workflowstore.ManualMoveResult, error) {
	moved, err := p.store.ApplyManualMove(ctx, prepared, candidate)
	if err != nil {
		return workflowstore.ManualMoveResult{}, err
	}
	if moved.Outcome == workflowstore.ManualMoveResultOutcomeNoOp {
		return moved, nil
	}
	delta, rollback, err := stage(moved)
	if err != nil {
		return workflowstore.ManualMoveResult{}, err
	}
	if err := p.Publish(ctx, delta); err != nil {
		if rollback != nil {
			rollback(err)
		}
		return workflowstore.ManualMoveResult{}, err
	}
	return moved, nil
}

func (p *currentNodeControllerLifecyclePublication) PublishResume(
	ctx context.Context,
	delta workflowstore.QueuedTaskLifecycleDelta,
) ([]workflowstore.InterruptedCurrentNodeAttentionProjection, error) {
	references := delta.QueuedCurrentNodes()
	p.store.mu.Lock()
	for _, reference := range references {
		key, err := reference.Key()
		if err != nil {
			p.store.mu.Unlock()
			return nil, err
		}
		if err := p.store.resumeErrors[key]; err != nil {
			p.store.mu.Unlock()
			return nil, err
		}
	}
	p.store.mu.Unlock()
	if p.store.resumePrepareStarted != nil {
		p.store.resumePrepareOnce.Do(func() {
			close(p.store.resumePrepareStarted)
		})
	}
	if p.store.resumePrepareRelease != nil {
		select {
		case <-p.store.resumePrepareRelease:
		case <-ctx.Done():
			if p.store.resumePrepareCanceled != nil {
				p.store.resumeCancelOnce.Do(func() {
					close(p.store.resumePrepareCanceled)
				})
			}
			return nil, context.Cause(ctx)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil, workflowstore.ErrLifecyclePublicationClosed
	}
	candidate := make(map[workflow.TaskID][]workflow.CurrentNodeReference, len(p.root)+1)
	for taskID, queued := range p.root {
		candidate[taskID] = append([]workflow.CurrentNodeReference(nil), queued...)
	}
	taskID := delta.TaskID()
	for _, reference := range references {
		for _, current := range candidate[taskID] {
			if current.Equal(reference) {
				return nil, errors.New("lifecycle Run predecessor conflict")
			}
		}
		candidate[taskID] = append(candidate[taskID], reference)
	}
	if p.store.resumeCommitStarted != nil {
		p.store.resumeCommitOnce.Do(func() {
			close(p.store.resumeCommitStarted)
		})
	}
	if p.store.resumeCommitRelease != nil {
		select {
		case <-p.store.resumeCommitRelease:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	if p.store.resumeCommitWon != nil {
		p.store.resumeCommitWinOnce.Do(func() {
			close(p.store.resumeCommitWon)
		})
	}
	if p.store.resumeCommitWinRelease != nil {
		<-p.store.resumeCommitWinRelease
	}
	if p.store.resumeCommitErr != nil {
		return nil, p.store.resumeCommitErr
	}
	attention := make([]workflowstore.InterruptedCurrentNodeAttentionProjection, 0, len(references))
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	p.store.resumed = append(p.store.resumed, references...)
	for index := range p.store.interrupted {
		for _, reference := range references {
			if !p.store.interrupted[index].Reference.Equal(reference) {
				continue
			}
			p.store.interrupted[index].Scheduling = &workflow.CurrentNodeScheduling{
				State: workflow.CurrentNodeSchedulingReady,
			}
			attention = append(attention, workflowstore.InterruptedCurrentNodeAttentionProjection{
				CurrentNode:        reference,
				ProjectID:          "project-test",
				WorkflowID:         currentNodeControllerTestWorkflowID,
				InterruptionReason: "workflow_test_interruption",
				OccurredAtUnixMs:   1,
			})
		}
	}
	p.root = candidate
	return attention, nil
}

func (p *currentNodeControllerLifecyclePublication) PublishTaskDeletion(
	context.Context,
	workflow.TaskID,
) (workflowstore.DeleteTaskResult, error) {
	return workflowstore.DeleteTaskResult{}, errors.New("Task deletion is unavailable in the controller fixture")
}

func (p *currentNodeControllerLifecyclePublication) PublishWorkflowDeletion(
	context.Context,
	workflowstore.WorkflowDeleteRequest,
) (workflowstore.WorkflowDeleteResult, error) {
	return workflowstore.WorkflowDeleteResult{}, errors.New("Workflow deletion is unavailable in the controller fixture")
}

func (p *currentNodeControllerLifecyclePublication) PublishProjectDeletion(
	context.Context,
	workflowstore.ProjectDeleteRequest,
) ([]serverapi.ProjectDeleteBlocker, error) {
	return nil, errors.New("Project deletion is unavailable in the controller fixture")
}

func (p *currentNodeControllerLifecyclePublication) Capture(
	context.Context,
) (workflowstore.LifecycleCapture, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return nil, workflowstore.ErrLifecyclePublicationClosed
	}
	p.store.mu.Lock()
	currentNodes := append([]workflow.CurrentNode(nil), p.store.interrupted...)
	p.store.mu.Unlock()
	return &currentNodeControllerLifecycleCapture{
		currentNodes: currentNodes,
		root:         p.root,
		exact:        p.exact,
	}, nil
}

func (p *currentNodeControllerLifecyclePublication) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.root = nil
	p.exact = nil
	return nil
}

type currentNodeControllerLifecycleCapture struct {
	mu           sync.Mutex
	currentNodes []workflow.CurrentNode
	root         map[workflow.TaskID][]workflow.CurrentNodeReference
	exact        map[workflow.TaskID][]workflowstore.LifecycleExactExecution
	closed       bool
}

func (c *currentNodeControllerLifecycleCapture) CurrentNodes(
	_ context.Context,
	taskID workflow.TaskID,
) ([]workflow.CurrentNode, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("lifecycle capture is closed")
	}
	var currentNodes []workflow.CurrentNode
	for _, currentNode := range c.currentNodes {
		if currentNode.Reference.TaskID == taskID {
			currentNodes = append(currentNodes, currentNode)
		}
	}
	return currentNodes, nil
}

func (c *currentNodeControllerLifecycleCapture) TaskIDs() []workflow.TaskID {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	taskSet := make(map[workflow.TaskID]struct{}, len(c.root)+len(c.exact))
	for taskID := range c.root {
		taskSet[taskID] = struct{}{}
	}
	for taskID := range c.exact {
		taskSet[taskID] = struct{}{}
	}
	taskIDs := make([]workflow.TaskID, 0, len(taskSet))
	for taskID := range taskSet {
		taskIDs = append(taskIDs, taskID)
	}
	sort.Slice(taskIDs, func(i, j int) bool {
		return taskIDs[i] < taskIDs[j]
	})
	return taskIDs
}

func (c *currentNodeControllerLifecycleCapture) QueuedCurrentNodes(
	taskID workflow.TaskID,
) []workflow.CurrentNodeReference {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	return append([]workflow.CurrentNodeReference(nil), c.root[taskID]...)
}

func (c *currentNodeControllerLifecycleCapture) ExactExecutions(
	taskID workflow.TaskID,
) []workflowstore.LifecycleExactExecution {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	return append([]workflowstore.LifecycleExactExecution(nil), c.exact[taskID]...)
}

func (c *currentNodeControllerLifecycleCapture) WithQueries(
	operation func(*sqlitegen.Queries) error,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("lifecycle capture is closed")
	}
	return operation(nil)
}

func (c *currentNodeControllerLifecycleCapture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.currentNodes = nil
	c.root = nil
	c.exact = nil
	return nil
}

func (s *currentNodeControllerStore) InterruptAdmittedCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, reason workflow.CurrentNodeInterruptionReason, detail workflow.CurrentNodeInterruptionDetail) error {
	key, err := reference.Key()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interruptErr != nil {
		return s.interruptErr
	}
	if s.interruptions == nil {
		s.interruptions = make(map[workflow.CurrentNodeReferenceKey]currentNodeInterruptionRecord)
	}
	if s.interruptionCalls == nil {
		s.interruptionCalls = make(map[workflow.CurrentNodeReferenceKey]int)
	}
	s.interruptions[key] = currentNodeInterruptionRecord{reason: reason, detail: detail}
	s.interruptionCalls[key]++
	return nil
}

func (s *currentNodeControllerStore) InterruptCurrentNode(ctx context.Context, reference workflow.CurrentNodeReference, reason workflow.CurrentNodeInterruptionReason, detail workflow.CurrentNodeInterruptionDetail) error {
	if s.interruptStarted != nil {
		s.interruptOnce.Do(func() {
			close(s.interruptStarted)
		})
	}
	if s.interruptRelease != nil {
		select {
		case <-s.interruptRelease:
		case <-ctx.Done():
			return context.Cause(ctx)
		}
	}
	s.mu.Lock()
	if s.interruptErr != nil {
		err := s.interruptErr
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	return s.InterruptAdmittedCurrentNode(ctx, reference, reason, detail)
}

func (s *currentNodeControllerStore) InterruptCurrentNodes(ctx context.Context, references []workflow.CurrentNodeReference, reason workflow.CurrentNodeInterruptionReason, detail workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error) {
	interrupted := make([]workflow.CurrentNodeReference, 0, len(references))
	for _, reference := range references {
		if err := s.InterruptCurrentNode(ctx, reference, reason, detail); err != nil {
			return interrupted, err
		}
		interrupted = append(interrupted, reference)
	}
	return interrupted, nil
}

func (s *currentNodeControllerStore) RecoverExecutableCurrentNodes(context.Context, workflow.CurrentNodeInterruptionReason, workflow.CurrentNodeInterruptionDetail) ([]workflow.CurrentNodeReference, error) {
	return append([]workflow.CurrentNodeReference(nil), s.recovered...), nil
}

func (s *currentNodeControllerStore) ResolveIdleExecutableCurrentNode(context.Context, workflowstore.IdleCurrentNodeSelector) (workflow.CurrentNode, error) {
	if s.idleResolved == nil {
		return workflow.CurrentNode{}, sql.ErrNoRows
	}
	return *s.idleResolved, nil
}

func (s *currentNodeControllerStore) CompleteCurrentNode(ctx context.Context, _ workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionResult, error) {
	if s.completionStarted != nil {
		s.completionOnce.Do(func() {
			close(s.completionStarted)
		})
	}
	if s.completionRelease != nil {
		select {
		case <-s.completionRelease:
		case <-ctx.Done():
			return workflowstore.CurrentNodeCompletionResult{}, context.Cause(ctx)
		}
	}
	s.mu.Lock()
	s.completions++
	s.mu.Unlock()
	return s.completion, nil
}

func (s *currentNodeControllerStore) ValidateCurrentNodeSessionBinding(_ context.Context, sessionID runtimeids.SessionID, reference workflow.CurrentNodeReference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings = append(s.bindings, currentNodeSessionBindingCall{sessionID: sessionID, reference: reference})
	return s.bindingErr
}

func (s *currentNodeControllerStore) admitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.admitted)
}

func (s *currentNodeControllerStore) interruption(reference workflow.CurrentNodeReference) (currentNodeInterruptionRecord, bool) {
	key, err := reference.Key()
	if err != nil {
		return currentNodeInterruptionRecord{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.interruptions[key]
	return value, ok
}

func (s *currentNodeControllerStore) interruptionCount(reference workflow.CurrentNodeReference) int {
	key, err := reference.Key()
	if err != nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interruptionCalls[key]
}

func (s *currentNodeControllerStore) setBindingError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindingErr = err
}

func (s *currentNodeControllerStore) bindingCalls() []currentNodeSessionBindingCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]currentNodeSessionBindingCall(nil), s.bindings...)
}

func (s *currentNodeControllerStore) completionCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completions
}

type currentNodeQuestionFixture struct {
	cfg        config.App
	metadata   interface{ AuthoritativeSessionStoreOptions() []session.StoreOption }
	authority  *sessionruntime.Authority
	controller *CurrentNodeController
	store      *currentNodeControllerStore
	sessionDir string
}

type currentNodePendingPrompt struct {
	handle    sessionruntime.ExecutionHandle
	sessionID runtimeids.SessionID
	result    <-chan currentNodePromptResult
}

type currentNodePromptResult struct {
	response askquestion.AskQuestionResponse
	err      error
}

type currentNodeQuestionLLMClient struct{}

func (currentNodeQuestionLLMClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("question fixture model must not generate")
}

func (currentNodeQuestionLLMClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}, nil
}

func (f currentNodeQuestionFixture) answerWorkflowQuestion(
	ctx context.Context,
	taskID workflow.TaskID,
	askID string,
	response askquestion.AskQuestionResponse,
	submitErr error,
) error {
	acceptance, err := f.controller.AcceptWorkflowQuestion(ctx, taskID, askID, response, submitErr)
	if err != nil {
		return err
	}
	return acceptance.AwaitSuccessor(ctx)
}

func newCurrentNodeQuestionFixture(t *testing.T) currentNodeQuestionFixture {
	t.Helper()
	home := t.TempDir()
	workspace := t.TempDir()
	t.Setenv("HOME", home)
	appCfg, err := config.Load(workspace, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore := testsetup.OpenStore(t, appCfg.PersistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), appCfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	store := &currentNodeControllerStore{}
	var controller *CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			if controller != nil {
				controller.ExecutionFinalized(scope)
			}
		}),
		PersistenceRoot: appCfg.PersistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
	})
	controller = newCurrentNodeControllerForTest(t, store, &countingCurrentNodeRunner{}, authority, 1)
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close controller: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	return currentNodeQuestionFixture{
		cfg:        appCfg,
		metadata:   metadataStore,
		authority:  authority,
		controller: controller,
		store:      store,
		sessionDir: filepath.Join(appCfg.PersistenceRoot, "projects", binding.ProjectID, "sessions"),
	}
}

func (f currentNodeQuestionFixture) startPendingPrompt(t *testing.T, reference workflow.CurrentNodeReference, request askquestion.AskQuestionRequest) currentNodePendingPrompt {
	t.Helper()
	result := make(chan currentNodePromptResult, 1)
	handle, sessionID := f.startQuestionExecution(t, reference, func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
		response, askErr := f.authority.AwaitPromptResponse(ctx, scope.ID(), request)
		result <- currentNodePromptResult{response: response, err: askErr}
		return askErr
	})
	return currentNodePendingPrompt{handle: handle, sessionID: sessionID, result: result}
}

func (f currentNodeQuestionFixture) startQuestionExecution(
	t *testing.T,
	reference workflow.CurrentNodeReference,
	runner sessionruntime.AgentRunner,
) (sessionruntime.ExecutionHandle, runtimeids.SessionID) {
	t.Helper()
	store, err := session.Create(
		f.sessionDir,
		filepath.Base(f.sessionDir),
		f.cfg.WorkspaceRoot,
		sessioncontract.SessionCategorySubagent,
		f.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("NewOpenSessionDescriptor: %v", err)
	}
	settings := f.cfg.Settings
	settings.Model = "gpt-5"
	settings.ModelContextWindow = 200_000
	settings.Reviewer.Frequency = "off"
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings,
		Workdir:  f.cfg.WorkspaceRoot,
		Client:   currentNodeQuestionLLMClient{},
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	lease, err := f.authority.NewWorkflowExecutionLease(sessionruntime.WorkflowExecutionRef{ProjectID: "project-test", WorkflowID: currentNodeControllerTestWorkflowID, CurrentNode: reference})
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	lease.Release()
	handle, err := f.authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Runtime:    &plan,
		Workflow:   &lease,
		Resource:   sessionruntime.OpenAgentResource{},
		Runner:     runner,
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	f.controller.mu.Lock()
	installLiveCurrentNodeRunLockedForTest(
		f.controller,
		reference,
		workflow.NodeKindAgent,
		currentNodeAdmissionExplicitOverride,
		lease,
	)
	f.controller.mu.Unlock()
	return handle, sessionID
}

func (f currentNodeQuestionFixture) waitForPendingPrompt(t *testing.T, taskID workflow.TaskID, askID string) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, err := f.authority.ResolvePendingWorkflowPrompt(taskID, askID)
		return err == nil || errors.Is(err, sessionruntime.ErrWorkflowPromptAmbiguous)
	}, "timed out waiting for workflow prompt %q on task %q", askID, taskID)
}

func (f currentNodeQuestionFixture) waitForAmbiguousPendingPrompt(t *testing.T, taskID workflow.TaskID, askID string) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		_, err := f.authority.ResolvePendingWorkflowPrompt(taskID, askID)
		return errors.Is(err, sessionruntime.ErrWorkflowPromptAmbiguous)
	}, "timed out waiting for ambiguous workflow prompt %q on task %q", askID, taskID)
}

type controlledScriptRunner struct {
	authority   *sessionruntime.Authority
	command     sessionruntime.ScriptCommand
	entered     chan struct{}
	startRunner chan struct{}
	registered  chan struct{}
	returnStart chan struct{}
	handles     chan sessionruntime.ExecutionHandle
}

func (r *controlledScriptRunner) StartCurrentNode(_ context.Context, _ workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ CurrentNodeAssignmentEnsure, lease sessionruntime.WorkflowExecutionLease, _ workflowruntime.Controller) error {
	close(r.entered)
	<-r.startRunner
	handle, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command:  r.command,
	})
	if err != nil {
		return err
	}
	r.handles <- handle
	close(r.registered)
	<-r.returnStart
	return nil
}

type failingCurrentNodeRunner struct {
	cause error
}

func (r failingCurrentNodeRunner) StartCurrentNode(context.Context, workflow.CurrentNodeReference, workflowruntime.TaskPromptDelivery, CurrentNodeAssignmentEnsure, sessionruntime.WorkflowExecutionLease, workflowruntime.Controller) error {
	return r.cause
}

type blockingCurrentNodeRunner struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingCurrentNodeRunner) StartCurrentNode(context.Context, workflow.CurrentNodeReference, workflowruntime.TaskPromptDelivery, CurrentNodeAssignmentEnsure, sessionruntime.WorkflowExecutionLease, workflowruntime.Controller) error {
	r.once.Do(func() {
		close(r.entered)
	})
	<-r.release
	return errors.New("blocked current node setup released")
}

type countingCurrentNodeRunner struct {
	mu         sync.Mutex
	count      int
	deliveries []workflowruntime.TaskPromptDelivery
}

func (r *countingCurrentNodeRunner) StartCurrentNode(_ context.Context, _ workflow.CurrentNodeReference, delivery workflowruntime.TaskPromptDelivery, _ CurrentNodeAssignmentEnsure, _ sessionruntime.WorkflowExecutionLease, _ workflowruntime.Controller) error {
	r.mu.Lock()
	r.count++
	r.deliveries = append(r.deliveries, delivery)
	r.mu.Unlock()
	return nil
}

func (r *countingCurrentNodeRunner) starts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.count
}

func (r *countingCurrentNodeRunner) promptDeliveries() []workflowruntime.TaskPromptDelivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]workflowruntime.TaskPromptDelivery(nil), r.deliveries...)
}

type recordingScriptRunner struct {
	authority *sessionruntime.Authority
	command   sessionruntime.ScriptCommand
	started   chan workflow.CurrentNodeReference
}

type completingScriptRunner struct {
	authority *sessionruntime.Authority
	source    workflow.CurrentNodeReference
	shellPath string
	started   chan workflow.CurrentNodeReference
}

type firstAdmissionBlockingScriptRunner struct {
	authority *sessionruntime.Authority
	shellPath string
	entered   chan workflow.CurrentNodeReference
	release   chan struct{}
}

type parallelExplicitRunner struct {
	authority      *sessionruntime.Authority
	shellPath      string
	blocked        workflow.CurrentNodeReference
	blockedEntered chan struct{}
	releaseBlocked chan struct{}
	siblingStarted chan workflow.CurrentNodeReference
	blockedOnce    sync.Once
}

type boundedExplicitAdmissionRunner struct {
	entered chan workflow.CurrentNodeReference
	release chan struct{}
}

func (r *boundedExplicitAdmissionRunner) StartCurrentNode(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentEnsure,
	_ sessionruntime.WorkflowExecutionLease,
	_ workflowruntime.Controller,
) error {
	r.entered <- reference
	<-r.release
	return errors.New("explicit admission setup released")
}

type runningAndQueuedGateRunner struct {
	authority        *sessionruntime.Authority
	shellPath        string
	running          workflow.CurrentNodeReference
	runningStarted   chan struct{}
	queuedRegistered chan struct{}
	returnQueued     chan struct{}
	runningOnce      sync.Once
	queuedOnce       sync.Once
}

type runningAndFinalizingScriptRunner struct {
	authority           *sessionruntime.Authority
	shellPath           string
	running             workflow.CurrentNodeReference
	finalizing          workflow.CurrentNodeReference
	finalizerEntered    chan struct{}
	releaseFinalizer    chan struct{}
	finalizerCompletion chan error
	successorStarted    chan struct{}
	finalizerOnce       sync.Once
	successorOnce       sync.Once
	finalize            func(context.Context, sessionruntime.ExecutionScope, *CurrentNodeController) error
}

func (r *runningAndFinalizingScriptRunner) StartCurrentNode(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentEnsure,
	lease sessionruntime.WorkflowExecutionLease,
	controller workflowruntime.Controller,
) error {
	switch {
	case reference.Equal(r.running):
		_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
			Workflow: &lease,
			Command: sessionruntime.ScriptCommand{
				Path: r.shellPath,
				Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
			},
		})
		return err
	case reference.Equal(r.finalizing):
		_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
			Workflow: &lease,
			Command:  sessionruntime.ScriptCommand{Path: r.shellPath, Args: []string{"-c", "exit 0"}},
			Finalize: func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.ScriptResult, runErr error) error {
				if runErr != nil {
					r.finalizerCompletion <- runErr
					return runErr
				}
				r.finalizerOnce.Do(func() {
					close(r.finalizerEntered)
				})
				<-r.releaseFinalizer
				var completionErr error
				if r.finalize != nil {
					currentController, ok := controller.(*CurrentNodeController)
					if !ok {
						completionErr = errors.New("finalizing Script controller is not CurrentNodeController")
					} else {
						completionErr = r.finalize(ctx, scope, currentController)
					}
				} else {
					_, completionErr = controller.CompleteCurrentNode(ctx, workflowruntime.CompletionRequest{
						ScopeID:      scope.ID(),
						TransitionID: "next",
					})
				}
				r.finalizerCompletion <- completionErr
				return completionErr
			},
		})
		return err
	default:
		r.successorOnce.Do(func() {
			r.successorStarted <- struct{}{}
		})
		_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
			Workflow: &lease,
			Command: sessionruntime.ScriptCommand{
				Path: r.shellPath,
				Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
			},
		})
		return err
	}
}

func (r *runningAndQueuedGateRunner) StartCurrentNode(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentEnsure,
	lease sessionruntime.WorkflowExecutionLease,
	_ workflowruntime.Controller,
) error {
	_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command: sessionruntime.ScriptCommand{
			Path: r.shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
	})
	if err != nil {
		return err
	}
	if reference.Equal(r.running) {
		r.runningOnce.Do(func() {
			close(r.runningStarted)
		})
		return nil
	}
	r.queuedOnce.Do(func() {
		close(r.queuedRegistered)
	})
	<-r.returnQueued
	return nil
}

func (r *parallelExplicitRunner) StartCurrentNode(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentEnsure,
	lease sessionruntime.WorkflowExecutionLease,
	_ workflowruntime.Controller,
) error {
	if reference.Equal(r.blocked) {
		r.blockedOnce.Do(func() {
			close(r.blockedEntered)
		})
		<-r.releaseBlocked
		return errors.New("first branch setup failed")
	}
	_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command: sessionruntime.ScriptCommand{
			Path: r.shellPath,
			Args: []string{"-c", "while :; do sleep 1; done"},
		},
	})
	if err == nil {
		r.siblingStarted <- reference
	}
	return err
}

func (r *firstAdmissionBlockingScriptRunner) StartCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ CurrentNodeAssignmentEnsure, lease sessionruntime.WorkflowExecutionLease, _ workflowruntime.Controller) error {
	r.entered <- reference
	<-r.release
	_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command:  sessionruntime.ScriptCommand{Path: r.shellPath, Args: []string{"-c", "while :; do sleep 1; done"}},
	})
	return err
}

func (r *completingScriptRunner) StartCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ CurrentNodeAssignmentEnsure, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
	if reference.Equal(r.source) {
		_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
			Workflow: &lease,
			Command:  sessionruntime.ScriptCommand{Path: r.shellPath, Args: []string{"-c", `printf '{"transition_id":"next"}'`}},
			Finalize: func(ctx context.Context, scope sessionruntime.ExecutionScope, result sessionruntime.ScriptResult, runErr error) error {
				if runErr != nil {
					return runErr
				}
				_, err := controller.CompleteCurrentNode(ctx, workflowruntime.CompletionRequest{
					ScopeID:      scope.ID(),
					TransitionID: "next",
				})
				return err
			},
		})
		return err
	}
	_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command:  sessionruntime.ScriptCommand{Path: r.shellPath, Args: []string{"-c", "while :; do sleep 1; done"}},
	})
	if err == nil {
		r.started <- reference
	}
	return err
}

func (r *recordingScriptRunner) StartCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ CurrentNodeAssignmentEnsure, lease sessionruntime.WorkflowExecutionLease, _ workflowruntime.Controller) error {
	_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command:  r.command,
	})
	if err == nil {
		r.started <- reference
	}
	return err
}
