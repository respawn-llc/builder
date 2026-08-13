package workflowsvc

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestCompleteWorkflowTaskConsumesCommittedResultBeforeReturningDiagnostic(t *testing.T) {
	source := currentNodeCompletionReference(t, "task-completion-diagnostic", "node-agent")
	approvalID := workflow.NewApprovalID()
	diagnostic := errors.New("successor assignment diagnostic")
	execution := &currentNodeCompletionExecutionStub{
		sessionResult: workflowstore.CurrentNodeCompletionResult{
			PendingApproval: &workflow.PendingApproval{
				ID:     approvalID,
				Source: source,
			},
		},
		sessionErr: diagnostic,
	}
	attention := &workflowAttentionRecorder{}
	service := currentNodeCompletionService(execution)
	service.attentionFinalizer = attention

	response, err := service.CompleteWorkflowTask(context.Background(), serverapi.WorkflowTaskCompleteRequest{
		ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
		AgentSessionID: runtimeids.NewSessionID().String(),
		TransitionID:   "done",
	})
	if !errors.Is(err, diagnostic) {
		t.Fatalf("CompleteWorkflowTask error = %v, want %v", err, diagnostic)
	}
	if response.TaskID != string(source.TaskID) ||
		response.PendingApprovalID == nil ||
		*response.PendingApprovalID != approvalID.String() {
		t.Fatalf("committed completion response = %+v, want pending Approval %q", response, approvalID)
	}
	if len(attention.pending) != 1 || attention.pending[0] != approvalID {
		t.Fatalf("published pending Approvals = %+v, want %q", attention.pending, approvalID)
	}
}

func TestApproveWorkflowTaskStartupFailureProjectsInterruptedResumeAcrossRestart(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	requireWorkflowServiceEdgeApproval(t, ctx, service, workflowID, "next")
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started, err := service.store.StartTask(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	completed, err := service.store.CompleteCurrentNode(ctx, workflowstore.CurrentNodeCompletionRequest{
		Source:       started.Mutation.Created[0].Reference,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "approved plan"},
	})
	if err != nil || completed.PendingApproval == nil {
		t.Fatalf("CompleteCurrentNode = %+v, %v; want pending Approval", completed, err)
	}
	approval := *completed.PendingApproval
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	newController := func(runtimeAuthority *sessionruntime.Authority) *workflowexecution.CurrentNodeController {
		next, controllerErr := workflowexecution.NewCurrentNodeController(
			service.store,
			initialBranchControllerRunner{},
			runtimeAuthority,
			service.taskMutations,
			workflowexecution.CurrentNodeControllerConfig{
				AgentConcurrency:  1,
				AssignmentSteerer: workflowServiceCommittedAssignmentSteerer{},
			},
		)
		if controllerErr != nil {
			t.Fatalf("NewCurrentNodeController: %v", controllerErr)
		}
		return next
	}
	controller := newController(authority)
	service.currentNodeExecution = controller
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})

	approved, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{ApprovalID: approval.ID.String()})
	if err != nil || approved.Applied == nil || len(approved.Applied.CurrentNodes) != 1 {
		t.Fatalf("ApproveWorkflowTask = %+v, %v; want consumed Approval and target", approved, err)
	}
	target, err := workflow.NewCurrentNodeReference(
		workflow.TaskID(task.Task.ID),
		workflow.NodeID(approved.Applied.CurrentNodes[0].NodeID),
		nil,
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	testsetup.RequireUntil(t, time.Now().Add(3*time.Second), 10*time.Millisecond, func() bool {
		nodes, listErr := service.store.ListCurrentNodes(ctx, workflow.TaskID(task.Task.ID))
		return listErr == nil &&
			len(nodes) == 1 &&
			nodes[0].Reference.Equal(target) &&
			nodes[0].Scheduling != nil &&
			nodes[0].Scheduling.Interruption != nil
	}, "approved target startup failure did not become interrupted")
	pending, err := service.store.ListPendingApprovals(ctx, workflow.TaskID(task.Task.ID))
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending Approvals after startup failure = %+v, %v; want none", pending, err)
	}
	detail, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: task.Task.ID})
	if err != nil ||
		detail.Task.Status.Kind != serverapi.WorkflowTaskStatusKindInterrupted ||
		!detail.Task.Actions.CanResume ||
		detail.Task.Actions.CanInterrupt {
		t.Fatalf("task detail after approved startup failure = %+v, %v", detail, err)
	}
	attention, err := service.ListWorkflowTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: task.Task.ID})
	if err != nil ||
		len(attention.Items) != 1 ||
		attention.Items[0].Kind != "interrupted_current_node" ||
		attention.Items[0].ApprovalID != nil ||
		attention.Items[0].DetailJSON == nil {
		t.Fatalf("Desktop attention after approved startup failure = %+v, %v", attention, err)
	}

	if recovered, err := controller.Recover(ctx); err != nil || recovered != 0 {
		t.Fatalf("Recover after approved startup failure = %d, %v; want no restart start/rewrite", recovered, err)
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("close pre-restart CurrentNodeController: %v", err)
	}
	if err := authority.Close(context.Background()); err != nil {
		t.Fatalf("close pre-restart Authority: %v", err)
	}
	authority = sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller = newController(authority)
	service.currentNodeExecution = controller
	resumed, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil || resumed.Applied == nil || len(resumed.Applied.CurrentNodes) != 1 {
		t.Fatalf("ResumeWorkflowTask after approved startup failure = %+v, %v", resumed, err)
	}
}

func TestResumeWorkflowTaskRepairsDirectlyConsistentRetainedSessionProvenance(t *testing.T) {
	shellPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh executable unavailable: %v", err)
	}
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started, err := service.store.StartTask(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	currentNode := started.Mutation.Created[0]
	sessionID := createPersistedWorkflowServiceSession(t, metadataStore, binding)
	if _, err := service.store.BindSessionToCurrentNode(ctx, workflowstore.CurrentNodeSessionBindingRequest{
		Association: workflowstore.TaskSessionAssociationRequest{
			SessionID:    sessionID,
			CurrentNode:  currentNode.Reference,
			AssociatedAt: time.UnixMilli(1_700_000_000_000).UTC(),
		},
	}); err != nil {
		t.Fatalf("BindSessionToCurrentNode: %v", err)
	}
	if err := service.store.InterruptCurrentNode(
		ctx,
		currentNode.Reference,
		"test_interruption",
		workflow.NewCurrentNodeInterruptionDetail("test_interruption", nil),
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	if _, err := metadataStore.DB().ExecContext(
		ctx,
		`DELETE FROM session_workflow_node_associations
WHERE session_id = ? AND node_id = ? AND transition_branch_key IS NULL`,
		sessionID.String(),
		currentNode.Reference.NodeID,
	); err != nil {
		t.Fatalf("delete exact Session provenance: %v", err)
	}

	var controller *workflowexecution.CurrentNodeController
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		ExecutionFinalized: sessionruntime.ExecutionFinalizedFunc(func(scope sessionruntime.ExecutionScope) {
			controller.ExecutionFinalized(scope)
		}),
	})
	runner := &workflowServiceValidatingRunner{
		store:     service.store,
		authority: authority,
		shellPath: shellPath,
		started:   make(chan workflow.CurrentNodeReference, 1),
	}
	controller, err = workflowexecution.NewCurrentNodeController(
		service.store,
		runner,
		authority,
		workflowexecution.NewTaskMutationCoordinator(),
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: workflowServiceCommittedAssignmentSteerer{},
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	service.currentNodeExecution = controller

	response, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("ResumeWorkflowTask = %+v, %v; want applied", response, err)
	}
	select {
	case resumed := <-runner.started:
		if !resumed.Equal(currentNode.Reference) {
			t.Fatalf("resumed Current Node = %v, want %v", resumed, currentNode.Reference)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("directly consistent retained Session did not start after Resume repair")
	}
	if err := service.store.ValidateCurrentNodeSessionBinding(ctx, sessionID, currentNode.Reference); err != nil {
		t.Fatalf("ResumeWorkflowTask did not repair exact Session provenance: %v", err)
	}
}

func TestCompleteWorkflowTaskMapsAmbiguousAndPendingSourceFailures(t *testing.T) {
	source := currentNodeCompletionReference(t, "task-completion-errors", "node-agent")
	sessionID := runtimeids.NewSessionID()
	tests := []struct {
		name      string
		resultErr error
		want      error
	}{
		{
			name:      "ambiguous",
			resultErr: workflowstore.ErrCurrentNodeCompletionSelectorAmbiguous,
			want:      serverapi.ErrWorkflowTaskCompleteSelectorAmbiguous,
		},
		{
			name:      "pending approval source",
			resultErr: workflowstore.ErrCurrentNodePendingApproval,
			want:      workflowstore.ErrCurrentNodePendingApproval,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execution := &currentNodeCompletionExecutionStub{idleErr: test.resultErr}
			_, err := currentNodeCompletionService(execution).CompleteWorkflowTask(context.Background(), serverapi.WorkflowTaskCompleteRequest{
				ActorKind:    serverapi.WorkflowTaskCompleteActorUser,
				Force:        true,
				TaskID:       string(source.TaskID),
				TransitionID: "done",
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("completion error = %v, want %v", err, test.want)
			}
		})
	}

	execution := &currentNodeCompletionExecutionStub{sessionErr: sessionruntime.ErrExecutionNoLongerLive}
	_, err := currentNodeCompletionService(execution).CompleteWorkflowTask(context.Background(), serverapi.WorkflowTaskCompleteRequest{
		ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
		AgentSessionID: sessionID.String(),
		TransitionID:   "done",
	})
	if !errors.Is(err, serverapi.ErrWorkflowTaskCompleteTargetNotFound) {
		t.Fatalf("live completion error = %v, want target-not-found", err)
	}
}

func TestWorkflowTaskCompleteContractHasNoRunOrPlacementFields(t *testing.T) {
	for _, contract := range []reflect.Type{
		reflect.TypeOf(serverapi.WorkflowTaskCompleteRequest{}),
		reflect.TypeOf(serverapi.WorkflowTaskCompleteResponse{}),
	} {
		for _, removed := range []string{"RunID", "RunIDs", "PlacementID", "PlacementIDs", "ProjectID", "ShortID"} {
			if _, exists := contract.FieldByName(removed); exists {
				t.Fatalf("%s still exposes removed completion field %s", contract.Name(), removed)
			}
		}
	}
}

func currentNodeCompletionService(execution *currentNodeCompletionExecutionStub) *Service {
	return &Service{
		readModels:           ReadModels{TaskDetail: currentNodeCompletionUnavailableTaskDetail{}},
		currentNodeExecution: execution,
	}
}

type currentNodeCompletionUnavailableTaskDetail struct{}

func (currentNodeCompletionUnavailableTaskDetail) GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("task detail unavailable")
}

func (currentNodeCompletionUnavailableTaskDetail) GetTaskByProjectShortID(context.Context, string, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("task detail unavailable")
}

func (currentNodeCompletionUnavailableTaskDetail) ListCurrentNodes(context.Context, string) ([]workflow.CurrentNode, error) {
	return nil, errors.New("current nodes unavailable")
}

func (currentNodeCompletionUnavailableTaskDetail) GetTaskByShortID(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("task detail unavailable")
}

type currentNodeCompletionTaskDetail struct {
	detail serverapi.WorkflowTaskDetail
}

type workflowServiceCommittedAssignmentSteerer struct{}

func (workflowServiceCommittedAssignmentSteerer) SteerCurrentNodeAssignment(
	context.Context,
	workflow.CurrentNodeReference,
) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	return workflowServiceCommittedAssignment{}, nil
}

type workflowServiceCommittedAssignment struct{}

func (workflowServiceCommittedAssignment) Wait(context.Context) (session.CommitReceipt, error) {
	return session.CommitReceipt{Committed: true}, nil
}

type workflowServiceValidatingRunner struct {
	store     *workflowstore.Store
	authority *sessionruntime.Authority
	shellPath string
	started   chan workflow.CurrentNodeReference
}

func (r *workflowServiceValidatingRunner) StartCurrentNode(
	ctx context.Context,
	reference workflow.CurrentNodeReference,
	_ workflowruntime.TaskPromptDelivery,
	_ *workflowexecution.CurrentNodeClassifiedAssignment,
	lease sessionruntime.WorkflowExecutionLease,
	_ workflowruntime.Controller,
) error {
	currentNodes, err := r.store.ListCurrentNodes(ctx, reference.TaskID)
	if err != nil {
		return err
	}
	var sessionID *runtimeids.SessionID
	for index := range currentNodes {
		if currentNodes[index].Reference.Equal(reference) {
			sessionID = currentNodes[index].SessionID
			break
		}
	}
	if sessionID == nil {
		return errors.New("resumed retained Current Node has no Session")
	}
	if err := r.store.ValidateCurrentNodeSessionBinding(ctx, *sessionID, reference); err != nil {
		return err
	}
	if _, err := r.authority.StartScriptExecution(context.Background(), sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command: sessionruntime.ScriptCommand{
			Path: r.shellPath,
			Args: []string{"-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
		},
	}); err != nil {
		return err
	}
	r.started <- reference
	return nil
}

func (d currentNodeCompletionTaskDetail) GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return d.detail, nil
}

func (d currentNodeCompletionTaskDetail) GetTaskByProjectShortID(context.Context, string, string) (serverapi.WorkflowTaskDetail, error) {
	return d.detail, nil
}

func (d currentNodeCompletionTaskDetail) ListCurrentNodes(context.Context, string) ([]workflow.CurrentNode, error) {
	return nil, nil
}

func (d currentNodeCompletionTaskDetail) GetTaskByShortID(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return d.detail, nil
}

func currentNodeCompletionReference(t *testing.T, taskID, nodeID string) workflow.CurrentNodeReference {
	t.Helper()
	reference, err := workflow.NewCurrentNodeReference(workflow.TaskID(taskID), workflow.NodeID(nodeID), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	return reference
}

type currentNodeCompletionExecutionStub struct {
	store                  *workflowstore.Store
	promoted               []workflow.CurrentNode
	promotionHandled       bool
	promotionErr           error
	resumePreflight        workflowexecution.TaskResumePreflight
	resumeEligibilityErr   error
	resumeEligibilityCalls int
	startPreparations      chan<- workflowexecution.TaskStartPreparation
	startFinalizers        chan<- workflowexecution.TaskPreparationFinalizer
	sessionID              runtimeids.SessionID
	sessionResult          workflowstore.CurrentNodeCompletionResult
	sessionErr             error
	idleSelector           workflowstore.IdleCurrentNodeSelector
	idleResult             workflowstore.CurrentNodeCompletionResult
	idleErr                error
	approvalResult         workflowstore.PendingApprovalApplyResult
	approvalErr            error
}

func (s *currentNodeCompletionExecutionStub) configuredResumePreflight(
	taskID workflow.TaskID,
) (workflowexecution.TaskResumePreflight, bool) {
	if s.resumePreflight.Outcome != "" {
		return s.resumePreflight, true
	}
	if s.resumePreflight.CurrentNodes == nil {
		return workflowexecution.TaskResumePreflight{}, false
	}
	preflight := s.resumePreflight
	preflight.CurrentNodes = append([]workflow.CurrentNode(nil), preflight.CurrentNodes...)
	for index := range preflight.CurrentNodes {
		preflight.CurrentNodes[index].Reference.TaskID = taskID
	}
	return preflight, true
}

func (s *currentNodeCompletionExecutionStub) PromoteConcurrencyQueuedTask(
	context.Context,
	workflow.TaskID,
) ([]workflow.CurrentNode, bool, error) {
	return append([]workflow.CurrentNode(nil), s.promoted...), s.promotionHandled, s.promotionErr
}

func (s *currentNodeCompletionExecutionStub) PreflightTaskResume(
	ctx context.Context,
	taskID workflow.TaskID,
) (workflowexecution.TaskResumePreflight, error) {
	s.resumeEligibilityCalls++
	if s.resumeEligibilityErr != nil {
		return workflowexecution.TaskResumePreflight{}, s.resumeEligibilityErr
	}
	if preflight, configured := s.configuredResumePreflight(taskID); configured {
		return preflight, nil
	}
	selected, err := s.store.InterruptedExecutableCurrentNodes(ctx, taskID)
	if err != nil {
		return workflowexecution.TaskResumePreflight{}, err
	}
	if len(selected) != 0 {
		return workflowexecution.TaskResumePreflight{
			Outcome:      workflowexecution.TaskResumePreflightResumable,
			CurrentNodes: selected,
		}, nil
	}
	currentNodes, err := s.store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		return workflowexecution.TaskResumePreflight{}, err
	}
	for _, currentNode := range currentNodes {
		if currentNode.Scheduling == nil {
			continue
		}
		switch currentNode.Scheduling.State {
		case workflow.CurrentNodeSchedulingReady, workflow.CurrentNodeSchedulingAdmitted:
			return workflowexecution.TaskResumePreflight{
				Outcome:      workflowexecution.TaskResumePreflightNoOp,
				CurrentNodes: currentNodes,
			}, nil
		}
	}
	return workflowexecution.TaskResumePreflight{}, &workflowexecution.TaskResumeConflictError{TaskID: taskID}
}

func (s *currentNodeCompletionExecutionStub) StartTask(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation workflowexecution.TaskStartPreparation,
	finalizer workflowexecution.TaskPreparationFinalizer,
) (workflowstore.StartTaskResult, error) {
	if s.store == nil {
		return workflowstore.StartTaskResult{}, errors.New("workflow store is required")
	}
	started, err := s.store.StartTask(ctx, taskID)
	if err != nil {
		return started, err
	}
	if s.startPreparations != nil {
		s.startPreparations <- preparation
		if s.startFinalizers != nil {
			s.startFinalizers <- finalizer
		}
		return started, nil
	}
	if err := preparation.Prepare(ctx); err != nil {
		finalizer(workflowexecution.TaskPreparationFinalization{
			Kind:  workflowexecution.TaskPreparationFailed,
			Cause: err,
		})
		return started, err
	}
	if err := preparation.Commit(ctx); err != nil {
		finalizer(workflowexecution.TaskPreparationFinalization{
			Kind:  workflowexecution.TaskPreparationFailed,
			Cause: err,
		})
		return started, err
	}
	finalizer(workflowexecution.TaskPreparationFinalization{Kind: workflowexecution.TaskPreparationHandedOff})
	return started, nil
}

func (s *currentNodeCompletionExecutionStub) ResumeTask(ctx context.Context, taskID workflow.TaskID) (workflowexecution.TaskResumeResult, error) {
	if s.store == nil {
		return workflowexecution.TaskResumeResult{}, errors.New("workflow store is required")
	}
	selected, err := s.store.InterruptedExecutableCurrentNodes(ctx, taskID)
	if err != nil {
		return workflowexecution.TaskResumeResult{}, err
	}
	for _, currentNode := range selected {
		if _, _, err := s.store.ResumeCurrentNode(ctx, currentNode.Reference); err != nil {
			return workflowexecution.TaskResumeResult{}, err
		}
	}
	return workflowexecution.TaskResumeResult{
		Outcome:      workflowexecution.TaskResumeApplied,
		CurrentNodes: selected,
	}, nil
}

func (s *currentNodeCompletionExecutionStub) ResumeTaskWithPreparation(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation workflowexecution.TaskStartPreparation,
	finalizer workflowexecution.TaskPreparationFinalizer,
) (workflowexecution.TaskResumeResult, error) {
	if s.store == nil {
		return workflowexecution.TaskResumeResult{}, errors.New("workflow store is required")
	}
	selected, err := s.store.InterruptedExecutableCurrentNodes(ctx, taskID)
	if err != nil {
		return workflowexecution.TaskResumeResult{}, err
	}
	if err := preparation.Prepare(ctx); err != nil {
		finalizer(workflowexecution.TaskPreparationFinalization{
			Kind:  workflowexecution.TaskPreparationFailed,
			Cause: err,
		})
		return workflowexecution.TaskResumeResult{}, err
	}
	if err := preparation.Commit(ctx); err != nil {
		finalizer(workflowexecution.TaskPreparationFinalization{
			Kind:  workflowexecution.TaskPreparationFailed,
			Cause: err,
		})
		return workflowexecution.TaskResumeResult{}, err
	}
	for _, currentNode := range selected {
		if _, _, err := s.store.ResumeCurrentNode(ctx, currentNode.Reference); err != nil {
			return workflowexecution.TaskResumeResult{}, err
		}
	}
	finalizer(workflowexecution.TaskPreparationFinalization{Kind: workflowexecution.TaskPreparationHandedOff})
	return workflowexecution.TaskResumeResult{
		Outcome:      workflowexecution.TaskResumeApplied,
		CurrentNodes: selected,
	}, nil
}

func (s *currentNodeCompletionExecutionStub) ApplyPendingApproval(ctx context.Context, approvalID workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error) {
	if s.approvalErr != nil {
		return s.approvalResult, s.approvalErr
	}
	if s.store == nil {
		return workflowstore.PendingApprovalApplyResult{}, errors.New("workflow store is required")
	}
	return s.store.ApplyPendingApproval(ctx, approvalID)
}

func (s *currentNodeCompletionExecutionStub) ApplyManualMove(
	ctx context.Context,
	prepared workflowstore.ManualMovePreparation,
	candidate *workflowstore.ExecutionTargetCandidate,
) (workflowstore.ManualMoveResult, error) {
	if s.store == nil {
		return workflowstore.ManualMoveResult{}, errors.New("workflow store is required")
	}
	return s.store.ApplyManualMove(ctx, prepared, candidate)
}

func (*currentNodeCompletionExecutionStub) Interrupt(context.Context, workflowexecution.InterruptSelector) error {
	return nil
}

func (*currentNodeCompletionExecutionStub) ManualMoveDisposition(workflow.TaskID) (workflowexecution.ManualMoveDisposition, error) {
	return workflowexecution.ManualMoveDispositionQuiescent, nil
}

func (*currentNodeCompletionExecutionStub) InterruptForManualMove(context.Context, workflow.TaskID, func() error) error {
	return nil
}

func (*currentNodeCompletionExecutionStub) EnsureTaskQuiescent(workflow.TaskID) error {
	return nil
}

func (s *currentNodeCompletionExecutionStub) CompleteSessionCurrentNode(_ context.Context, sessionID runtimeids.SessionID, _ string, _ map[string]string, _ string) (workflowstore.CurrentNodeCompletionResult, error) {
	s.sessionID = sessionID
	return s.sessionResult, s.sessionErr
}

func (s *currentNodeCompletionExecutionStub) CompleteIdleCurrentNode(_ context.Context, selector workflowstore.IdleCurrentNodeSelector, _ string, _ map[string]string, _ string) (workflowstore.CurrentNodeCompletionResult, error) {
	s.idleSelector = selector
	return s.idleResult, s.idleErr
}
