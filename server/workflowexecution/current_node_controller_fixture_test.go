package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

func newCurrentNodeControllerForTest(
	t *testing.T,
	store *currentNodeControllerStore,
	runner currentNodeTestRunner,
	authority *sessionruntime.Authority,
	concurrency int,
) *CurrentNodeController {
	return newCurrentNodeControllerWithAttentionForTest(t, store, runner, authority, concurrency, nil)
}

func newCurrentNodeControllerWithAttentionForTest(
	t *testing.T,
	store *currentNodeControllerStore,
	runner currentNodeTestRunner,
	authority *sessionruntime.Authority,
	concurrency int,
	attention CurrentNodeAttentionLifecycle,
) *CurrentNodeController {
	t.Helper()
	controller, err := NewCurrentNodeController(store, currentNodeTestPublicationRunner{
		runner: runner, authority: authority,
	}, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency:  concurrency,
		Attention:         attention,
		AssignmentSteerer: noOpCurrentNodeAssignmentSteerer{},
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	return controller
}

func newCurrentNodeControllerWithConfigForTest(
	t *testing.T,
	store *currentNodeControllerStore,
	runner currentNodeTestRunner,
	authority *sessionruntime.Authority,
	mutations *TaskMutationCoordinator,
	cfg CurrentNodeControllerConfig,
) *CurrentNodeController {
	t.Helper()
	controller, err := NewCurrentNodeController(store, currentNodeTestPublicationRunner{
		runner: runner, authority: authority,
	}, authority, mutations, cfg)
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	return controller
}

type workflowExecutionStart struct {
	state *workflowExecutionStartState
}

type workflowExecutionStartState struct {
	reference workflow.CurrentNodeReference
	admit     func() error
	published func(sessionruntime.ExecutionHandle)
	handle    sessionruntime.ExecutionHandle
	onRetire  func()
}

type currentNodeTestRunner interface {
	PublishCurrentNode(
		context.Context,
		workflow.CurrentNodeReference,
		workflowruntime.TaskPromptDelivery,
		CurrentNodeAssignmentSteer,
		workflowExecutionStart,
		workflowruntime.Controller,
	) error
}

type currentNodeTestPreparation interface {
	PrepareCurrentNode(context.Context, workflow.CurrentNodeReference, workflowruntime.TaskPromptDelivery) error
}

type currentNodeTestScriptRunner interface {
	UsesScriptPublication(workflow.CurrentNodeReference) bool
}

type currentNodeTestPublicationRunner struct {
	runner    currentNodeTestRunner
	authority *sessionruntime.Authority
}

func (r currentNodeTestPublicationRunner) PrepareScriptPublication(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	controller workflowruntime.Controller,
) (CurrentNodeScriptPublication, error) {
	if preparation, ok := r.runner.(CurrentNodeScriptPublicationPreparation); ok {
		return preparation.PrepareScriptPublication(ctx, reference, controller)
	}
	if scriptRunner, ok := r.runner.(currentNodeTestScriptRunner); ok && scriptRunner.UsesScriptPublication(reference) {
		if preparation, ok := r.runner.(currentNodeTestPreparation); ok {
			if err := preparation.PrepareCurrentNode(
				ctx,
				reference,
				workflowruntime.TaskPromptDeliveryAssignment,
			); err != nil {
				return nil, err
			}
		}
		return &currentNodeTestScriptPublication{
			runner: r.runner, authority: r.authority, reference: reference,
			controller: controller,
		}, nil
	}
	return nil, nil
}

func (r currentNodeTestPublicationRunner) StartAgentCurrentNode(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	delivery workflowruntime.TaskPromptDelivery,
	assignment CurrentNodeAssignmentSteer,
	onRetire func(),
	controller workflowruntime.Controller,
) (sessionruntime.ExecutionHandle, error) {
	if preparation, ok := r.runner.(currentNodeTestPreparation); ok {
		if err := preparation.PrepareCurrentNode(ctx, reference, delivery); err != nil {
			return nil, err
		}
	}
	state := &workflowExecutionStartState{
		reference: reference,
		admit:     func() error { return nil },
		onRetire:  onRetire,
	}
	if err := r.runner.PublishCurrentNode(
		ctx,
		reference,
		delivery,
		assignment,
		workflowExecutionStart{state: state},
		controller,
	); err != nil {
		return nil, err
	}
	if state.handle == nil {
		shellPath, err := exec.LookPath("sh")
		if err != nil {
			return nil, err
		}
		if _, err := startTestWorkflowScript(r.authority, workflowExecutionStart{state: state}, sessionruntime.ScriptExecutionRequest{
			Command: sessionruntime.ScriptCommand{Path: shellPath, Args: []string{"-c", "exit 0"}},
		}); err != nil {
			return nil, err
		}
	}
	if state.onRetire != nil {
		go func() {
			_, _ = state.handle.Wait(context.Background())
			state.onRetire()
		}()
	}
	return state.handle, nil
}

type currentNodeTestScriptPublication struct {
	runner     currentNodeTestRunner
	authority  *sessionruntime.Authority
	reference  workflow.CurrentNodeReference
	controller workflowruntime.Controller
}

func (p *currentNodeTestScriptPublication) Publish(
	_ context.Context,
	admit func() error,
	published func(sessionruntime.ExecutionHandle),
) (sessionruntime.ExecutionHandle, func(), error) {
	if err := admit(); err != nil {
		return nil, nil, err
	}
	state := &workflowExecutionStartState{
		reference: p.reference,
		admit:     func() error { return nil }, published: published,
	}
	if err := p.runner.PublishCurrentNode(
		context.Background(), p.reference, workflowruntime.TaskPromptDeliveryAssignment, nil,
		workflowExecutionStart{state: state}, p.controller,
	); err != nil {
		return nil, nil, err
	}
	if state.handle == nil {
		return nil, nil, errors.New("test Script publication returned no execution handle")
	}
	return state.handle, func() {}, nil
}

func (p *currentNodeTestScriptPublication) Cancel() {}

func startTestWorkflowScript(
	authority *sessionruntime.Authority,
	start workflowExecutionStart,
	request sessionruntime.ScriptExecutionRequest,
) (sessionruntime.ExecutionHandle, error) {
	if start.state == nil {
		return nil, errors.New("test Workflow execution start is required")
	}
	detached, err := authority.PrepareDetachedScriptExecution(context.Background(), sessionruntime.DetachedScriptExecutionRequest{
		Workflow: sessionruntime.WorkflowExecutionRef{
			ProjectID: "project-test", WorkflowID: currentNodeControllerTestWorkflowID,
			CurrentNode: start.state.reference,
		},
		Command: request.Command, Finalize: request.Finalize,
	})
	if err != nil {
		return nil, err
	}
	handle, launch, err := detached.Publish(context.Background(), func() error { return nil }, start.state.published)
	if err == nil {
		start.state.handle = handle
		launch()
	}
	return handle, err
}

func startLiveTestWorkflowScript(
	t *testing.T,
	controller *CurrentNodeController,
	authority *sessionruntime.Authority,
	reference workflow.CurrentNodeReference,
	request sessionruntime.ScriptExecutionRequest,
) sessionruntime.ExecutionHandle {
	t.Helper()
	detached, err := authority.PrepareDetachedScriptExecution(context.Background(), sessionruntime.DetachedScriptExecutionRequest{
		Workflow: sessionruntime.WorkflowExecutionRef{
			ProjectID: "project-test", WorkflowID: currentNodeControllerTestWorkflowID,
			CurrentNode: reference,
		},
		Command: request.Command, Finalize: request.Finalize,
	})
	if err != nil {
		t.Fatalf("prepare detached Script execution: %v", err)
	}
	handle, launch, err := detached.Publish(context.Background(), func() error { return nil }, nil)
	if err != nil {
		t.Fatalf("publish detached Script execution: %v", err)
	}
	launch()
	return handle
}

type noOpCurrentNodeAssignmentSteerer struct{}

func (noOpCurrentNodeAssignmentSteerer) SteerCurrentNodeAssignment(context.Context, workflow.CurrentNodeReference) (CurrentNodeAssignmentSteer, error) {
	return completedCurrentNodeAssignmentSteer{
		receipt: session.CommitReceipt{Committed: true},
	}, nil
}

type completedCurrentNodeAssignmentSteer struct {
	receipt session.CommitReceipt
	err     error
}

func completeCurrentNodeLifecycleForTest(
	ctx context.Context,
	controller *CurrentNodeController,
	scopeID runtimeids.ExecutionScopeID,
	transitionID string,
) (workflowstore.CurrentNodeCompletionResult, error) {
	result, err := controller.completeLiveCurrentNode(
		ctx,
		scopeID,
		transitionID,
		nil,
		"",
		func(commit func() (workflowruntime.CompletionResult, error)) (workflowruntime.CompletionResult, error) {
			return commit()
		},
	)
	return result.CommittedResult, err
}

func (s completedCurrentNodeAssignmentSteer) Wait(context.Context) (session.CommitReceipt, error) {
	return s.receipt, s.err
}

type deadlineRecordingCurrentNodeAssignmentSteerer struct {
	reference workflow.CurrentNodeReference
	deadline  chan<- time.Time
}

func (s deadlineRecordingCurrentNodeAssignmentSteerer) SteerCurrentNodeAssignment(
	_ context.Context,
	reference workflow.CurrentNodeReference,
) (CurrentNodeAssignmentSteer, error) {
	if !reference.Equal(s.reference) {
		return completedCurrentNodeAssignmentSteer{
			receipt: session.CommitReceipt{Committed: true},
		}, nil
	}
	return &deadlineRecordingCurrentNodeAssignmentSteer{deadline: s.deadline}, nil
}

type deadlineRecordingCurrentNodeAssignmentSteer struct {
	deadline chan<- time.Time
	mu       sync.Mutex
	recorded bool
}

func (s *deadlineRecordingCurrentNodeAssignmentSteer) Wait(ctx context.Context) (session.CommitReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recorded {
		return session.CommitReceipt{Committed: true}, nil
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return session.CommitReceipt{}, errors.New("assignment steer wait context has no deadline")
	}
	s.recorded = true
	s.deadline <- deadline
	return session.CommitReceipt{Committed: true}, nil
}

type lateCommitCurrentNodeAssignmentSteerer struct {
	release <-chan struct{}
	started chan struct{}
}

func (s lateCommitCurrentNodeAssignmentSteerer) SteerCurrentNodeAssignment(
	context.Context,
	workflow.CurrentNodeReference,
) (CurrentNodeAssignmentSteer, error) {
	return &lateCommitCurrentNodeAssignmentSteer{
		release: s.release,
		started: s.started,
	}, nil
}

type lateCommitCurrentNodeAssignmentSteer struct {
	release <-chan struct{}
	started chan struct{}
	once    sync.Once
}

func (s *lateCommitCurrentNodeAssignmentSteer) Wait(ctx context.Context) (session.CommitReceipt, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return session.CommitReceipt{Committed: true}, nil
	case <-ctx.Done():
		return session.CommitReceipt{}, context.Cause(ctx)
	}
}

type recordingCurrentNodeAssignmentSteerer struct {
	mu          sync.Mutex
	steered     []workflow.CurrentNodeReference
	outcomes    []currentNodeAssignmentSteerOutcome
	err         error
	waitReceipt session.CommitReceipt
	waitErr     error
}

type currentNodeAssignmentSteerOutcome struct {
	receipt  session.CommitReceipt
	steerErr error
	waitErr  error
}

func (s *recordingCurrentNodeAssignmentSteerer) SteerCurrentNodeAssignment(_ context.Context, reference workflow.CurrentNodeReference) (CurrentNodeAssignmentSteer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steered = append(s.steered, reference)
	index := len(s.steered) - 1
	if index < len(s.outcomes) {
		outcome := s.outcomes[index]
		if outcome.steerErr != nil {
			return nil, outcome.steerErr
		}
		return completedCurrentNodeAssignmentSteer{
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
	return completedCurrentNodeAssignmentSteer{receipt: receipt, err: s.waitErr}, nil
}

func (s *recordingCurrentNodeAssignmentSteerer) references() []workflow.CurrentNodeReference {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]workflow.CurrentNodeReference(nil), s.steered...)
}

func (s *recordingCurrentNodeAssignmentSteerer) setWaitError(err error) {
	s.mu.Lock()
	s.waitErr = err
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
	_, err := controller.StartTask(
		ctx,
		reference.TaskID,
		testTaskPreparation(func(context.Context) error { return nil }),
		noOpTaskPreparationFinalizer,
	)
	return err
}

func noOpTaskPreparationFinalizer(TaskPreparationFinalization) {}

func testTaskPreparation(prepare func(context.Context) error) TaskStartPreparation {
	return TaskStartPreparation{
		Prepare: prepare,
		Commit:  func(context.Context) error { return nil },
	}
}

func currentNodeReferenceForControllerTest(t *testing.T, taskID string, nodeID string) workflow.CurrentNodeReference {
	t.Helper()
	reference, err := workflow.NewCurrentNodeReference(workflow.TaskID(taskID), workflow.NodeID(nodeID), nil)
	if err != nil {
		t.Fatalf("new current node reference: %v", err)
	}
	return reference
}

func singleLiveScope(t *testing.T, authority *sessionruntime.Authority, reference workflow.CurrentNodeReference) runtimeids.ExecutionScopeID {
	t.Helper()
	handle, live := authority.ExecutionByCurrentNode("project-test", currentNodeControllerTestWorkflowID, reference)
	if live {
		return handle.Scope().ID()
	}
	t.Fatalf("no live execution for %v", reference)
	return runtimeids.ExecutionScopeID{}
}

func hasLiveCurrentNode(authority *sessionruntime.Authority, reference workflow.CurrentNodeReference) bool {
	_, live := authority.ExecutionByCurrentNode("project-test", currentNodeControllerTestWorkflowID, reference)
	return live
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

var currentNodeControllerTestWorkflowID = func() runtimeids.WorkflowID {
	workflowID, err := runtimeids.ParseWorkflowID("550e8400-e29b-41d4-a716-446655440201")
	if err != nil {
		panic(err)
	}
	return workflowID
}()

type currentNodeControllerStore struct {
	mu                    sync.Mutex
	started               workflowstore.StartTaskResult
	interrupted           []workflow.CurrentNode
	currentNodes          []workflow.CurrentNode
	pendingApproval       workflow.PendingApproval
	approvalApplied       workflowstore.PendingApprovalApplyResult
	manualMoved           workflowstore.ManualMoveResult
	admitted              []workflow.CurrentNodeReference
	admitStarted          chan struct{}
	admitRelease          chan struct{}
	admitSawCancellation  bool
	resumed               []workflow.CurrentNodeReference
	resumeErrors          map[workflow.CurrentNodeReferenceKey]error
	resumeClassifications []workflowstore.CurrentNodeResumeClassification
	preflightResumeCalls  int
	interruptions         map[workflow.CurrentNodeReferenceKey]currentNodeInterruptionRecord
	interruptionCalls     map[workflow.CurrentNodeReferenceKey]int
	recovered             []workflow.CurrentNodeReference
	completion            workflowstore.CurrentNodeCompletionResult
	completionDiagnostic  error
	completions           int
	startTaskStarted      chan struct{}
	startTaskRelease      chan struct{}
	startTaskOnce         sync.Once
	completionStarted     chan struct{}
	completionRelease     chan struct{}
	completionOnce        sync.Once
	sessionTaskID         *workflow.TaskID
	sessionAssociation    *workflowstore.TaskSessionAssociation
	bindingErr            error
	bindings              []currentNodeSessionBindingCall
	interruptStarted      chan struct{}
	interruptRelease      chan struct{}
	interruptOnce         sync.Once
	interruptionErr       error
	idleResolved          *workflow.CurrentNode
	idleResolvedSequence  []workflow.CurrentNode
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

func (s *currentNodeControllerStore) StartTask(ctx context.Context, _ workflow.TaskID) (workflowstore.StartTaskResult, error) {
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
	s.mu.Unlock()
	return started, nil
}

func (s *currentNodeControllerStore) InterruptedExecutableCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error) {
	return append([]workflow.CurrentNode(nil), s.interrupted...), nil
}

func (s *currentNodeControllerStore) ListCurrentNodes(context.Context, workflow.TaskID) ([]workflow.CurrentNode, error) {
	if s.currentNodes != nil {
		return append([]workflow.CurrentNode(nil), s.currentNodes...), nil
	}
	return append([]workflow.CurrentNode(nil), s.interrupted...), nil
}

func (s *currentNodeControllerStore) PreflightTaskResume(_ context.Context, _ workflow.TaskID) ([]workflowstore.CurrentNodeResumeClassification, error) {
	s.preflightResumeCalls++
	if len(s.resumeClassifications) > 0 {
		return append([]workflowstore.CurrentNodeResumeClassification(nil), s.resumeClassifications...), nil
	}
	classifications := make([]workflowstore.CurrentNodeResumeClassification, 0, len(s.interrupted))
	for _, currentNode := range s.interrupted {
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

func (s *currentNodeControllerStore) AdmitCurrentNode(ctx context.Context, reference workflow.CurrentNodeReference) (session.CommitReceipt, error) {
	if s.admitStarted != nil {
		close(s.admitStarted)
	}
	if s.admitRelease != nil {
		<-s.admitRelease
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admitSawCancellation = context.Cause(ctx) != nil
	s.admitted = append(s.admitted, reference)
	return session.CommitReceipt{Committed: true}, nil
}

func (s *currentNodeControllerStore) ResumeCurrentNode(_ context.Context, reference workflow.CurrentNodeReference) (workflowstore.InterruptedCurrentNodeAttentionProjection, bool, error) {
	s.mu.Lock()
	key, keyErr := reference.Key()
	if keyErr != nil {
		s.mu.Unlock()
		return workflowstore.InterruptedCurrentNodeAttentionProjection{}, false, keyErr
	}
	if err := s.resumeErrors[key]; err != nil {
		s.mu.Unlock()
		return workflowstore.InterruptedCurrentNodeAttentionProjection{}, false, err
	}
	s.resumed = append(s.resumed, reference)
	s.mu.Unlock()
	return workflowstore.InterruptedCurrentNodeAttentionProjection{
		CurrentNode:        reference,
		ProjectID:          "project-test",
		WorkflowID:         currentNodeControllerTestWorkflowID,
		InterruptionReason: "workflow_test_interruption",
		OccurredAtUnixMs:   1,
	}, true, nil
}

func (s *currentNodeControllerStore) InterruptAdmittedCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, reason workflow.CurrentNodeInterruptionReason, detail workflow.CurrentNodeInterruptionDetail) error {
	if s.interruptionErr != nil {
		return s.interruptionErr
	}
	key, err := reference.Key()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if len(s.idleResolvedSequence) != 0 {
		resolved := s.idleResolvedSequence[0]
		s.idleResolvedSequence = s.idleResolvedSequence[1:]
		return resolved, nil
	}
	if s.idleResolved == nil {
		return workflow.CurrentNode{}, sql.ErrNoRows
	}
	return *s.idleResolved, nil
}

func (s *currentNodeControllerStore) CompleteCurrentNode(ctx context.Context, _ workflowstore.CurrentNodeCompletionRequest) (workflowstore.CurrentNodeCompletionOutcome, error) {
	if s.completionStarted != nil {
		s.completionOnce.Do(func() {
			close(s.completionStarted)
		})
	}
	if s.completionRelease != nil {
		select {
		case <-s.completionRelease:
		case <-ctx.Done():
			return workflowstore.CurrentNodeCompletionOutcome{}, session.DefinitelyUncommittedMutation(context.Cause(ctx))
		}
	}
	s.mu.Lock()
	s.completions++
	s.mu.Unlock()
	return workflowstore.CurrentNodeCompletionOutcome{
		CommitReceipt:               session.CommitReceipt{Committed: true},
		CurrentNodeCompletionResult: s.completion,
		PostCommitDiagnostic:        s.completionDiagnostic,
	}, nil
}

func (s *currentNodeControllerStore) ValidateCurrentNodeSessionBinding(_ context.Context, sessionID runtimeids.SessionID, reference workflow.CurrentNodeReference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindings = append(s.bindings, currentNodeSessionBindingCall{sessionID: sessionID, reference: reference})
	return s.bindingErr
}

func (s *currentNodeControllerStore) ResolveCurrentSessionStartContext(
	_ context.Context,
	sessionID runtimeids.SessionID,
) (workflowstore.CurrentNodeStartContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionTaskID == nil || s.sessionAssociation == nil ||
		s.sessionAssociation.SessionID != sessionID {
		return workflowstore.CurrentNodeStartContext{}, workflowstore.ErrSessionNotCurrentWorkflowNode
	}
	for _, currentNode := range s.interrupted {
		if currentNode.Reference.Equal(s.sessionAssociation.CurrentNode) {
			return workflowstore.CurrentNodeStartContext{
				Task:        workflowstore.TaskRecord{ID: *s.sessionTaskID},
				CurrentNode: currentNode,
			}, nil
		}
	}
	for _, currentNode := range s.currentNodes {
		if currentNode.Reference.Equal(s.sessionAssociation.CurrentNode) {
			return workflowstore.CurrentNodeStartContext{
				Task:        workflowstore.TaskRecord{ID: *s.sessionTaskID},
				CurrentNode: currentNode,
			}, nil
		}
	}
	return workflowstore.CurrentNodeStartContext{}, workflowstore.ErrSessionNotCurrentWorkflowNode
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
	resolution askquestion.AskQuestionResolution
	err        error
}

func currentNodeQuestionAnswer(answer string) askquestion.AskQuestionAnswer {
	return askquestion.AskQuestionAnswer{
		Freeform: &answer,
	}
}

func (f currentNodeQuestionFixture) answerPromptBatch(
	ctx context.Context,
	pending currentNodePendingPrompt,
	stepID string,
	promptID string,
	answer askquestion.AskQuestionAnswer,
) (sessionruntime.PromptAnswerOutcome, error) {
	parsedStepID, err := runtimeids.ParseStepID(stepID)
	if err != nil {
		return "", err
	}
	results, err := f.authority.ResolvePromptBatch(ctx, pending.sessionID, parsedStepID, []sessionruntime.PromptAnswerCommand{{
		PromptID: clientui.PromptID(promptID),
		Payload:  sessionruntime.PromptQuestionAnswerCommand{Answer: answer},
	}})
	if err != nil {
		return "", err
	}
	if len(results) != 1 {
		return "", errors.New("one prompt answer result is required")
	}
	return results[0].Outcome, nil
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

type callbackCurrentNodeLLMClient struct {
	generate func(context.Context, llm.Request) (llm.Response, error)
}

func (c callbackCurrentNodeLLMClient) Generate(ctx context.Context, request llm.Request) (llm.Response, error) {
	return c.generate(ctx, request)
}

func (callbackCurrentNodeLLMClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}, nil
}

func newCurrentNodeQuestionFixture(t *testing.T) currentNodeQuestionFixture {
	return newCurrentNodeQuestionFixtureWithPromptFeed(t, nil)
}

func newCurrentNodeQuestionFixtureWithPromptFeed(
	t *testing.T,
	promptFeed sessionruntime.ExecutionPromptFeed,
) currentNodeQuestionFixture {
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
		PersistenceRoot: appCfg.PersistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
		PromptFeed:      promptFeed,
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
		resolution, askErr := f.authority.AwaitPromptResolution(ctx, scope.ID(), request)
		result <- currentNodePromptResult{resolution: resolution, err: askErr}
		return askErr
	})
	return currentNodePendingPrompt{handle: handle, sessionID: sessionID, result: result}
}

func (f currentNodeQuestionFixture) startQuestionExecution(
	t *testing.T,
	reference workflow.CurrentNodeReference,
	runner sessionruntime.AgentRunner,
) (sessionruntime.ExecutionHandle, runtimeids.SessionID) {
	handle, sessionID, _ := f.startAgentExecution(t, reference, runner)
	return handle, sessionID
}

func (f currentNodeQuestionFixture) startAgentExecution(
	t *testing.T,
	reference workflow.CurrentNodeReference,
	runner sessionruntime.AgentRunner,
) (sessionruntime.ExecutionHandle, runtimeids.SessionID, runtimeids.SessionResourceRef) {
	return f.startAgentExecutionWithClient(t, reference, currentNodeQuestionLLMClient{}, runner)
}

func (f currentNodeQuestionFixture) startAgentExecutionWithClient(
	t *testing.T,
	reference workflow.CurrentNodeReference,
	client llm.Client,
	runner sessionruntime.AgentRunner,
) (sessionruntime.ExecutionHandle, runtimeids.SessionID, runtimeids.SessionResourceRef) {
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
		Settings:              settings,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		FilesystemContext: func() askquestion.FilesystemContext {
			context, err := runtimewire.NewFilesystemContext(f.cfg.WorkspaceRoot, f.cfg.WorkspaceRoot, metadata.ProjectWorkspaceBoundary{ProjectID: "test"})
			if err != nil {
				t.Fatalf("NewFilesystemContext: %v", err)
			}
			return context
		}(),
		Client: client,
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	handle, err := f.authority.StartAgentExecution(context.Background(), sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Runtime:    &plan,
		Workflow: &sessionruntime.WorkflowAgentExecution{
			Reference: sessionruntime.WorkflowExecutionRef{
				ProjectID: "project-test", WorkflowID: currentNodeControllerTestWorkflowID,
				CurrentNode: reference,
			},
			Config: &workflowruntime.CurrentNodeExecutionConfig{
				Contract: workflowruntime.CompletionContract{
					Transitions: []workflowruntime.CompletionTransition{{ID: "next"}},
				},
				CompletionMode: workflowruntime.CompletionModeTool,
				Controller:     f.controller,
				Instructions:   workflowruntime.TaskInstructions{CurrentNode: reference},
			},
		},
		Resource: sessionruntime.OpenAgentResource{},
		Runner:   runner,
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	scope := handle.Scope()
	resource, ok := scope.Resource()
	if !ok {
		t.Fatal("detached Agent execution has no Session Resource")
	}
	return handle, sessionID, resource
}

func (f currentNodeQuestionFixture) waitForPendingPrompt(t *testing.T, taskID workflow.TaskID, askID string) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return f.pendingPromptCount(taskID, askID) >= 1
	}, "timed out waiting for workflow prompt %q on task %q", askID, taskID)
}

func (f currentNodeQuestionFixture) waitForAmbiguousPendingPrompt(t *testing.T, taskID workflow.TaskID, askID string) {
	t.Helper()
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		return f.pendingPromptCount(taskID, askID) >= 2
	}, "timed out waiting for ambiguous workflow prompt %q on task %q", askID, taskID)
}

func (f currentNodeQuestionFixture) pendingPromptCount(taskID workflow.TaskID, promptID string) int {
	snapshots, err := f.authority.CurrentWorkflowTaskExecutionSnapshots()
	if err != nil {
		return 0
	}
	snapshot, exists := snapshots[taskID]
	if !exists {
		return 0
	}
	count := 0
	for _, execution := range snapshot.Executions {
		for _, pending := range execution.PendingPrompts {
			if pending.ID == promptID {
				count++
			}
		}
	}
	return count
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

type controlledScriptPublication struct {
	detached    *sessionruntime.DetachedScriptExecution
	registered  chan struct{}
	returnStart chan struct{}
	handles     chan sessionruntime.ExecutionHandle
}

func (p *controlledScriptPublication) Publish(
	ctx context.Context,
	admit func() error,
	published func(sessionruntime.ExecutionHandle),
) (sessionruntime.ExecutionHandle, func(), error) {
	close(p.registered)
	<-p.returnStart
	handle, launch, err := p.detached.Publish(ctx, admit, published)
	if err == nil {
		p.handles <- handle
	}
	return handle, launch, err
}

func (p *controlledScriptPublication) Cancel() {
	if p != nil {
		p.detached.Cancel()
	}
}

func (r *controlledScriptRunner) PrepareScriptPublication(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.Controller,
) (CurrentNodeScriptPublication, error) {
	close(r.entered)
	<-r.startRunner
	detached, err := r.authority.PrepareDetachedScriptExecution(ctx, sessionruntime.DetachedScriptExecutionRequest{
		Workflow: sessionruntime.WorkflowExecutionRef{
			ProjectID:   "project-test",
			WorkflowID:  currentNodeControllerTestWorkflowID,
			CurrentNode: reference,
		},
		Command: r.command,
	})
	if err != nil {
		return nil, err
	}
	return &controlledScriptPublication{
		detached: detached, registered: r.registered,
		returnStart: r.returnStart, handles: r.handles,
	}, nil
}

func (r *controlledScriptRunner) PublishCurrentNode(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
	CurrentNodeAssignmentSteer,
	workflowExecutionStart,
	workflowruntime.Controller,
) error {
	return errors.New("generic Current Node publication must not be used for detached Script publication")
}

type failingCurrentNodeRunner struct {
	cause error
}

func (r failingCurrentNodeRunner) PrepareCurrentNode(context.Context, workflow.CurrentNodeReference, workflowruntime.TaskPromptDelivery) error {
	return r.cause
}

func (r failingCurrentNodeRunner) PublishCurrentNode(context.Context, workflow.CurrentNodeReference, workflowruntime.TaskPromptDelivery, CurrentNodeAssignmentSteer, workflowExecutionStart, workflowruntime.Controller) error {
	return nil
}

type blockingCurrentNodeRunner struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	cause   error
}

func (r *blockingCurrentNodeRunner) PublishCurrentNode(context.Context, workflow.CurrentNodeReference, workflowruntime.TaskPromptDelivery, CurrentNodeAssignmentSteer, workflowExecutionStart, workflowruntime.Controller) error {
	return nil
}

func (r *blockingCurrentNodeRunner) PrepareCurrentNode(context.Context, workflow.CurrentNodeReference, workflowruntime.TaskPromptDelivery) error {
	r.once.Do(func() {
		close(r.entered)
	})
	<-r.release
	if r.cause != nil {
		return r.cause
	}
	return errors.New("blocked current node setup released")
}

type countingCurrentNodeRunner struct {
	mu         sync.Mutex
	count      int
	deliveries []workflowruntime.TaskPromptDelivery
}

func (r *countingCurrentNodeRunner) PublishCurrentNode(_ context.Context, _ workflow.CurrentNodeReference, delivery workflowruntime.TaskPromptDelivery, _ CurrentNodeAssignmentSteer, _ workflowExecutionStart, _ workflowruntime.Controller) error {
	return nil
}

func (r *countingCurrentNodeRunner) PrepareCurrentNode(_ context.Context, _ workflow.CurrentNodeReference, delivery workflowruntime.TaskPromptDelivery) error {
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
	scripts   map[workflow.CurrentNodeReference]struct{}
	agents    map[workflow.CurrentNodeReference]struct{}
}

func (r *recordingScriptRunner) UsesScriptPublication(reference workflow.CurrentNodeReference) bool {
	if _, ok := r.agents[reference]; ok {
		return false
	}
	if len(r.scripts) == 0 {
		return true
	}
	_, ok := r.scripts[reference]
	return ok
}

type completingScriptRunner struct {
	authority *sessionruntime.Authority
	source    workflow.CurrentNodeReference
	shellPath string
	started   chan workflow.CurrentNodeReference
}

func (*completingScriptRunner) UsesScriptPublication(workflow.CurrentNodeReference) bool { return true }

type firstAdmissionBlockingScriptRunner struct {
	authority *sessionruntime.Authority
	shellPath string
	entered   chan workflow.CurrentNodeReference
	release   chan struct{}
}

func (*firstAdmissionBlockingScriptRunner) UsesScriptPublication(workflow.CurrentNodeReference) bool {
	return true
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

func (*parallelExplicitRunner) UsesScriptPublication(workflow.CurrentNodeReference) bool { return true }

type boundedExplicitAdmissionRunner struct {
	entered chan workflow.CurrentNodeReference
	release chan struct{}
}

func (r *boundedExplicitAdmissionRunner) PublishCurrentNode(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentSteer,
	_ workflowExecutionStart,
	_ workflowruntime.Controller,
) error {
	return nil
}

func (r *boundedExplicitAdmissionRunner) PrepareCurrentNode(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
) error {
	r.entered <- reference
	<-r.release
	return errors.New("explicit admission setup released")
}

type runningAndQueuedGateRunner struct {
	authority      *sessionruntime.Authority
	shellPath      string
	running        workflow.CurrentNodeReference
	runningStarted chan struct{}
	runningOnce    sync.Once
}

func (*runningAndQueuedGateRunner) UsesScriptPublication(workflow.CurrentNodeReference) bool {
	return false
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

func (*runningAndFinalizingScriptRunner) UsesScriptPublication(workflow.CurrentNodeReference) bool {
	return true
}

func (r *runningAndFinalizingScriptRunner) PublishCurrentNode(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentSteer,
	lease workflowExecutionStart,
	controller workflowruntime.Controller,
) error {
	switch {
	case reference.Equal(r.running):
		_, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{

			Command: sessionruntime.ScriptCommand{
				Path: r.shellPath,
				Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
			},
		})
		return err
	case reference.Equal(r.finalizing):
		_, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{

			Command: sessionruntime.ScriptCommand{Path: r.shellPath, Args: []string{"-c", "exit 0"}},
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
					_, completionErr = controller.CompleteScriptCurrentNode(ctx, workflowruntime.ScriptCompletionRequest{
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
		_, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{

			Command: sessionruntime.ScriptCommand{
				Path: r.shellPath,
				Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
			},
		})
		return err
	}
}

func (r *runningAndQueuedGateRunner) PublishCurrentNode(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentSteer,
	lease workflowExecutionStart,
	_ workflowruntime.Controller,
) error {
	_, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{

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
	}
	return nil
}

func (r *parallelExplicitRunner) PublishCurrentNode(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ CurrentNodeAssignmentSteer,
	lease workflowExecutionStart,
	_ workflowruntime.Controller,
) error {
	_, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{
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

func (r *parallelExplicitRunner) PrepareCurrentNode(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
) error {
	if reference.Equal(r.blocked) {
		r.blockedOnce.Do(func() {
			close(r.blockedEntered)
		})
		<-r.releaseBlocked
		return errors.New("first branch setup failed")
	}
	return nil
}

func (r *firstAdmissionBlockingScriptRunner) PublishCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ CurrentNodeAssignmentSteer, lease workflowExecutionStart, _ workflowruntime.Controller) error {
	_, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{

		Command: sessionruntime.ScriptCommand{Path: r.shellPath, Args: []string{"-c", "while :; do sleep 1; done"}},
	})
	return err
}

func (r *firstAdmissionBlockingScriptRunner) PrepareCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery) error {
	r.entered <- reference
	<-r.release
	return nil
}

func (r *completingScriptRunner) PublishCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ CurrentNodeAssignmentSteer, lease workflowExecutionStart, controller workflowruntime.Controller) error {
	if reference.Equal(r.source) {
		_, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{

			Command: sessionruntime.ScriptCommand{Path: r.shellPath, Args: []string{"-c", `printf '{"transition_id":"next"}'`}},
			Finalize: func(ctx context.Context, scope sessionruntime.ExecutionScope, result sessionruntime.ScriptResult, runErr error) error {
				if runErr != nil {
					return runErr
				}
				_, err := controller.CompleteScriptCurrentNode(ctx, workflowruntime.ScriptCompletionRequest{
					ScopeID:      scope.ID(),
					TransitionID: "next",
				})
				return err
			},
		})
		return err
	}
	_, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{

		Command: sessionruntime.ScriptCommand{Path: r.shellPath, Args: []string{"-c", "while :; do sleep 1; done"}},
	})
	if err == nil {
		r.started <- reference
	}
	return err
}

func (r *recordingScriptRunner) PublishCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ CurrentNodeAssignmentSteer, lease workflowExecutionStart, _ workflowruntime.Controller) error {
	_, err := startTestWorkflowScript(r.authority, lease, sessionruntime.ScriptExecutionRequest{

		Command: r.command,
	})
	if err == nil {
		r.started <- reference
	}
	return err
}
