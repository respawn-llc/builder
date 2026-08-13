package workflowexecution

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	controller, err := NewCurrentNodeController(store, runner, authority, NewTaskMutationCoordinator(), CurrentNodeControllerConfig{
		AgentConcurrency:  concurrency,
		Attention:         attention,
		AssignmentSteerer: noOpCurrentNodeAssignmentSteerer{},
	})
	if err != nil {
		t.Fatalf("new current node controller: %v", err)
	}
	return controller
}

type noOpCurrentNodeAssignmentSteerer struct{}

func (noOpCurrentNodeAssignmentSteerer) SteerCurrentNodeAssignment(context.Context, workflow.CurrentNodeReference) (CurrentNodeAssignmentSteer, error) {
	return completedCurrentNodeAssignmentSteer{
		receipt: session.CommitReceipt{Committed: true},
	}, nil
}

func (noOpCurrentNodeAssignmentSteerer) PrepareManualMoveAssignments(
	_ context.Context,
	inputs []workflowstore.CurrentNodeStartContext,
) (
	workflowstore.ManualMoveTargetAssignmentPreparation,
	map[workflow.CurrentNodeReferenceKey]CurrentNodeAssignmentSteer,
	error,
) {
	assignments := make([]workflowstore.ManualMoveTargetAssignment, 0, len(inputs))
	steers := make(map[workflow.CurrentNodeReferenceKey]CurrentNodeAssignmentSteer, len(inputs))
	for _, input := range inputs {
		if input.Node.Kind != workflow.NodeKindAgent {
			continue
		}
		key, err := input.CurrentNode.Reference.Key()
		if err != nil {
			return workflowstore.ManualMoveTargetAssignmentPreparation{}, nil, err
		}
		sessionID := runtimeids.NewSessionID()
		assignments = append(assignments, workflowstore.ManualMoveTargetAssignment{
			CurrentNode: input.CurrentNode.Reference,
			SessionID:   sessionID,
		})
		steers[key] = completedCurrentNodeAssignmentSteer{
			receipt: session.CommitReceipt{Committed: true},
		}
	}
	return workflowstore.ManualMoveTargetAssignmentPreparation{Assignments: assignments}, steers, nil
}

func prepareNoOpManualMoveAssignments(
	ctx context.Context,
	inputs []workflowstore.CurrentNodeStartContext,
) (
	workflowstore.ManualMoveTargetAssignmentPreparation,
	map[workflow.CurrentNodeReferenceKey]CurrentNodeAssignmentSteer,
	error,
) {
	return noOpCurrentNodeAssignmentSteerer{}.PrepareManualMoveAssignments(ctx, inputs)
}

type completedCurrentNodeAssignmentSteer struct {
	receipt session.CommitReceipt
	err     error
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

func (deadlineRecordingCurrentNodeAssignmentSteerer) PrepareManualMoveAssignments(
	ctx context.Context,
	inputs []workflowstore.CurrentNodeStartContext,
) (workflowstore.ManualMoveTargetAssignmentPreparation, map[workflow.CurrentNodeReferenceKey]CurrentNodeAssignmentSteer, error) {
	return prepareNoOpManualMoveAssignments(ctx, inputs)
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
	delayed *workflow.CurrentNodeReference
	release <-chan struct{}
	started chan struct{}
	resumed chan struct{}
	receipt session.CommitReceipt
	err     error
}

func (s lateCommitCurrentNodeAssignmentSteerer) SteerCurrentNodeAssignment(
	_ context.Context,
	reference workflow.CurrentNodeReference,
) (CurrentNodeAssignmentSteer, error) {
	if s.delayed != nil && !reference.Equal(*s.delayed) {
		return noOpCurrentNodeAssignmentSteerer{}.SteerCurrentNodeAssignment(context.Background(), reference)
	}
	return &lateCommitCurrentNodeAssignmentSteer{
		release: s.release,
		started: s.started,
		resumed: s.resumed,
		receipt: s.receipt,
		err:     s.err,
	}, nil
}

func (lateCommitCurrentNodeAssignmentSteerer) PrepareManualMoveAssignments(
	ctx context.Context,
	inputs []workflowstore.CurrentNodeStartContext,
) (workflowstore.ManualMoveTargetAssignmentPreparation, map[workflow.CurrentNodeReferenceKey]CurrentNodeAssignmentSteer, error) {
	return prepareNoOpManualMoveAssignments(ctx, inputs)
}

type lateCommitCurrentNodeAssignmentSteer struct {
	release    <-chan struct{}
	started    chan struct{}
	resumed    chan struct{}
	receipt    session.CommitReceipt
	err        error
	startOnce  sync.Once
	resumeOnce sync.Once
	waits      atomic.Int32
}

func (s *lateCommitCurrentNodeAssignmentSteer) Wait(ctx context.Context) (session.CommitReceipt, error) {
	s.startOnce.Do(func() { close(s.started) })
	if s.waits.Add(1) > 1 && s.resumed != nil {
		s.resumeOnce.Do(func() { close(s.resumed) })
	}
	select {
	case <-s.release:
		receipt := s.receipt
		if s.err == nil {
			receipt.Committed = true
		}
		return receipt, s.err
	case <-ctx.Done():
		return session.CommitReceipt{}, context.Cause(ctx)
	}
}

type blockingCurrentNodeAssignmentSteerer struct {
	blocked         workflow.CurrentNodeReference
	release         <-chan struct{}
	started         chan struct{}
	siblingPrepared chan struct{}
	receipt         session.CommitReceipt
	err             error
	startedOnce     sync.Once
	siblingOnce     sync.Once
}

func (s *blockingCurrentNodeAssignmentSteerer) SteerCurrentNodeAssignment(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
) (CurrentNodeAssignmentSteer, error) {
	if reference.Equal(s.blocked) {
		s.startedOnce.Do(func() { close(s.started) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
		return completedCurrentNodeAssignmentSteer{
			receipt: s.receipt,
			err:     s.err,
		}, nil
	}
	s.siblingOnce.Do(func() { close(s.siblingPrepared) })
	return completedCurrentNodeAssignmentSteer{
		receipt: session.CommitReceipt{Committed: true},
	}, nil
}

func (*blockingCurrentNodeAssignmentSteerer) PrepareManualMoveAssignments(
	ctx context.Context,
	inputs []workflowstore.CurrentNodeStartContext,
) (workflowstore.ManualMoveTargetAssignmentPreparation, map[workflow.CurrentNodeReferenceKey]CurrentNodeAssignmentSteer, error) {
	return prepareNoOpManualMoveAssignments(ctx, inputs)
}

type recordingCurrentNodeAssignmentSteerer struct {
	mu          sync.Mutex
	steered     []workflow.CurrentNodeReference
	byReference map[workflow.CurrentNodeReferenceKey]currentNodeAssignmentSteerOutcome
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
	if key, err := reference.Key(); err == nil {
		if outcome, exists := s.byReference[key]; exists {
			if outcome.steerErr != nil {
				return nil, outcome.steerErr
			}
			return completedCurrentNodeAssignmentSteer{
				receipt: outcome.receipt,
				err:     outcome.waitErr,
			}, nil
		}
	}
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

func (*recordingCurrentNodeAssignmentSteerer) PrepareManualMoveAssignments(
	ctx context.Context,
	inputs []workflowstore.CurrentNodeStartContext,
) (workflowstore.ManualMoveTargetAssignmentPreparation, map[workflow.CurrentNodeReferenceKey]CurrentNodeAssignmentSteer, error) {
	return prepareNoOpManualMoveAssignments(ctx, inputs)
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
	handle, live := authority.ExecutionByWorkflow(sessionruntime.WorkflowExecutionRef{
		ProjectID: "project-test", WorkflowID: currentNodeControllerTestWorkflowID, CurrentNode: reference,
	})
	if live {
		return handle.Scope().ID()
	}
	t.Fatalf("no live execution for %v", reference)
	return runtimeids.ExecutionScopeID{}
}

func hasLiveCurrentNode(authority *sessionruntime.Authority, reference workflow.CurrentNodeReference) bool {
	_, live := authority.ExecutionByWorkflow(sessionruntime.WorkflowExecutionRef{
		ProjectID: "project-test", WorkflowID: currentNodeControllerTestWorkflowID, CurrentNode: reference,
	})
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
	mu                        sync.Mutex
	started                   workflowstore.StartTaskResult
	interrupted               []workflow.CurrentNode
	pendingApproval           workflow.PendingApproval
	approvalApplied           workflowstore.PendingApprovalApplyResult
	manualMoved               workflowstore.ManualMoveResult
	admitted                  []workflow.CurrentNodeReference
	resumed                   []workflow.CurrentNodeReference
	resumeErrors              map[workflow.CurrentNodeReferenceKey]error
	resumeClassifications     []workflowstore.CurrentNodeResumeClassification
	preflightResumeCalls      int
	interruptions             map[workflow.CurrentNodeReferenceKey]currentNodeInterruptionRecord
	interruptionCalls         map[workflow.CurrentNodeReferenceKey]int
	interruptErr              error
	replaceInterruptErr       error
	admittedInterruptErr      error
	interruptAttempts         int
	admittedInterruptAttempts int
	recovered                 []workflow.CurrentNodeReference
	recoveryErr               error
	completion                workflowstore.CurrentNodeCompletionResult
	completions               int
	startTaskStarted          chan struct{}
	startTaskRelease          chan struct{}
	startTaskOnce             sync.Once
	completionStarted         chan struct{}
	completionRelease         chan struct{}
	completionOnce            sync.Once
	bindingErr                error
	bindings                  []currentNodeSessionBindingCall
	interruptStarted          chan struct{}
	interruptRelease          chan struct{}
	interruptOnce             sync.Once
	idleResolved              *workflow.CurrentNode
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

func (s *currentNodeControllerStore) ApplyManualMoveWithTargetAssignments(
	context.Context,
	workflowstore.ManualMovePreparation,
	*workflowstore.ExecutionTargetCandidate,
	workflowstore.ManualMoveTargetAssignmentPreparer,
) (workflowstore.ManualMoveResult, error) {
	return s.manualMoved, nil
}

type currentNodeInterruptionRecord struct {
	reason workflow.CurrentNodeInterruptionReason
	detail workflow.CurrentNodeInterruptionDetail
}

func currentNodeInterruptionPostCommitDiagnosticForTest(
	reference workflow.CurrentNodeReference,
	cause error,
) error {
	return errors.Join(
		&workflowstore.CurrentNodeInterruptionPostCommitDiagnostic{Reference: reference},
		cause,
	)
}

type currentNodeSessionBindingCall struct {
	sessionID runtimeids.SessionID
	reference workflow.CurrentNodeReference
}

func (s *currentNodeControllerStore) AdmitCurrentNode(_ context.Context, reference workflow.CurrentNodeReference) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admitted = append(s.admitted, reference)
	return nil
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
	s.mu.Lock()
	s.admittedInterruptAttempts++
	err := s.admittedInterruptErr
	s.mu.Unlock()
	committed, diagnostic := classifyCurrentNodeInterruption(err)
	if !committed {
		return err
	}
	key, keyErr := reference.Key()
	if keyErr != nil {
		return keyErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.interruptions == nil {
		s.interruptions = make(map[workflow.CurrentNodeReferenceKey]currentNodeInterruptionRecord)
	}
	if _, exists := s.interruptions[key]; exists {
		return sql.ErrNoRows
	}
	if s.interruptionCalls == nil {
		s.interruptionCalls = make(map[workflow.CurrentNodeReferenceKey]int)
	}
	s.interruptions[key] = currentNodeInterruptionRecord{reason: reason, detail: detail}
	s.interruptionCalls[key]++
	return diagnostic
}

func (s *currentNodeControllerStore) InterruptCurrentNode(ctx context.Context, reference workflow.CurrentNodeReference, reason workflow.CurrentNodeInterruptionReason, detail workflow.CurrentNodeInterruptionDetail) error {
	s.mu.Lock()
	s.interruptAttempts++
	err := s.interruptErr
	s.mu.Unlock()
	committed, diagnostic := classifyCurrentNodeInterruption(err)
	if !committed {
		return err
	}
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
	admittedErr := s.InterruptAdmittedCurrentNode(ctx, reference, reason, detail)
	if admittedErr != nil {
		return errors.Join(diagnostic, admittedErr)
	}
	return diagnostic
}

func (s *currentNodeControllerStore) ReplaceUserInterruptionWithAssignmentFailure(
	_ context.Context,
	reference workflow.CurrentNodeReference,
	detail workflow.CurrentNodeInterruptionDetail,
) error {
	committed, diagnostic := classifyCurrentNodeInterruption(s.replaceInterruptErr)
	if !committed {
		return s.replaceInterruptErr
	}
	key, err := reference.Key()
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.interruptions[key]
	if !exists || current.reason != workflow.CurrentNodeInterruptionReasonUserInterrupt {
		return sql.ErrNoRows
	}
	s.interruptions[key] = currentNodeInterruptionRecord{
		reason: reasonCurrentNodeRuntimeStartFailed,
		detail: detail,
	}
	return diagnostic
}

func (s *currentNodeControllerStore) interruptionAttemptCount(admitted bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if admitted {
		return s.admittedInterruptAttempts
	}
	return s.interruptAttempts
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
	return append([]workflow.CurrentNodeReference(nil), s.recovered...), s.recoveryErr
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

func (*currentNodeControllerStore) RepairCurrentNodeSessionProvenanceForResume(
	context.Context,
	workflow.CurrentNode,
) error {
	return nil
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
		Client: currentNodeQuestionLLMClient{},
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
	key, err := reference.Key()
	if err != nil {
		t.Fatalf("current node key: %v", err)
	}
	f.controller.mu.Lock()
	f.controller.live[lease.ScopeID()] = currentNodeLiveScope{reference: reference, lease: lease}
	f.controller.liveByNode[key] = lease.ScopeID()
	f.controller.mu.Unlock()
	return handle, sessionID
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

func (r *controlledScriptRunner) StartCurrentNode(_ context.Context, _ workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ *CurrentNodeClassifiedAssignment, lease sessionruntime.WorkflowExecutionLease, _ workflowruntime.Controller) error {
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

func (r failingCurrentNodeRunner) StartCurrentNode(context.Context, workflow.CurrentNodeReference, workflowruntime.TaskPromptDelivery, *CurrentNodeClassifiedAssignment, sessionruntime.WorkflowExecutionLease, workflowruntime.Controller) error {
	return r.cause
}

type blockingCurrentNodeRunner struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingCurrentNodeRunner) StartCurrentNode(context.Context, workflow.CurrentNodeReference, workflowruntime.TaskPromptDelivery, *CurrentNodeClassifiedAssignment, sessionruntime.WorkflowExecutionLease, workflowruntime.Controller) error {
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

func (r *countingCurrentNodeRunner) StartCurrentNode(_ context.Context, _ workflow.CurrentNodeReference, delivery workflowruntime.TaskPromptDelivery, _ *CurrentNodeClassifiedAssignment, _ sessionruntime.WorkflowExecutionLease, _ workflowruntime.Controller) error {
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
	_ *CurrentNodeClassifiedAssignment,
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
	queued           workflow.CurrentNodeReference
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
	_ *CurrentNodeClassifiedAssignment,
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
	_ *CurrentNodeClassifiedAssignment,
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
	if !reference.Equal(r.queued) {
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
	_ *CurrentNodeClassifiedAssignment,
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

func (r *firstAdmissionBlockingScriptRunner) StartCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ *CurrentNodeClassifiedAssignment, lease sessionruntime.WorkflowExecutionLease, _ workflowruntime.Controller) error {
	r.entered <- reference
	<-r.release
	_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command:  sessionruntime.ScriptCommand{Path: r.shellPath, Args: []string{"-c", "while :; do sleep 1; done"}},
	})
	return err
}

func (r *completingScriptRunner) StartCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ *CurrentNodeClassifiedAssignment, lease sessionruntime.WorkflowExecutionLease, controller workflowruntime.Controller) error {
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

func (r *recordingScriptRunner) StartCurrentNode(_ context.Context, reference workflow.CurrentNodeReference, _ workflowruntime.TaskPromptDelivery, _ *CurrentNodeClassifiedAssignment, lease sessionruntime.WorkflowExecutionLease, _ workflowruntime.Controller) error {
	_, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command:  r.command,
	})
	if err == nil {
		r.started <- reference
	}
	return err
}
