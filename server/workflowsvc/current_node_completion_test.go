package workflowsvc

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

func TestCompleteWorkflowTaskReturnsPendingApprovalWithoutReplacingCurrentNode(t *testing.T) {
	source := currentNodeCompletionReference(t, "task-pending-approval", "node-agent")
	approvalID := workflow.NewApprovalID()
	execution := &currentNodeCompletionExecutionStub{
		sessionResult: workflowstore.CurrentNodeCompletionResult{
			PendingApproval: &workflow.PendingApproval{
				ID:     approvalID,
				Source: source,
			},
		},
	}
	service := currentNodeCompletionService(execution)
	sessionID := runtimeids.NewSessionID()

	response, err := service.CompleteWorkflowTask(context.Background(), serverapi.WorkflowTaskCompleteRequest{
		ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
		AgentSessionID: sessionID.String(),
		TransitionID:   "done",
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask: %v", err)
	}
	if response.TaskID != string(source.TaskID) {
		t.Fatalf("task id = %q, want %q", response.TaskID, source.TaskID)
	}
	if response.PendingApprovalID == nil || *response.PendingApprovalID != approvalID.String() {
		t.Fatalf("pending approval id = %v, want %q", response.PendingApprovalID, approvalID)
	}
	if len(response.CurrentNodes) != 0 {
		t.Fatalf("current nodes = %+v, want none while source remains pending approval", response.CurrentNodes)
	}
	if execution.sessionID != sessionID || execution.idleSelector.TaskID != nil || execution.idleSelector.SessionID != nil {
		t.Fatalf("completion dispatch = %+v, want live Session completion", execution)
	}
}

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

func TestApproveWorkflowTaskConsumesCommittedResultBeforeReturningDiagnostic(t *testing.T) {
	source := currentNodeCompletionReference(t, "task-approval-diagnostic", "node-agent")
	target := currentNodeCompletionReference(t, "task-approval-diagnostic", "node-review")
	approvalID := workflow.NewApprovalID()
	workflowID := runtimeids.NewWorkflowID()
	diagnostic := errors.New("approved successor assignment diagnostic")
	execution := &currentNodeCompletionExecutionStub{
		approvalResult: workflowstore.PendingApprovalApplyResult{
			Mutation: workflow.CurrentNodeMutationResult{
				Removed: []workflow.CurrentNodeReference{source},
				Created: []workflow.CurrentNode{{Reference: target}},
			},
			ResolvedApproval: workflow.PendingApproval{
				ID:     approvalID,
				Source: source,
			},
			TaskAttentionResolution: workflowstore.TaskAttentionResolution{
				Approvals: []workflowstore.ApprovalAttentionProjection{{
					ApprovalID: approvalID,
					Source:     source,
				}},
			},
		},
		approvalErr: diagnostic,
	}
	attention := &workflowAttentionRecorder{}
	service := currentNodeCompletionService(execution)
	service.readModels.TaskDetail = currentNodeCompletionTaskDetail{
		detail: serverapi.WorkflowTaskDetail{Summary: serverapi.WorkflowTaskSummary{
			ID:         string(source.TaskID),
			ProjectID:  "project-approval-diagnostic",
			WorkflowID: workflowID,
		}},
	}
	service.attentionFinalizer = attention
	service.events = newWorkflowProjectEventBroker()
	_, persistedService, _ := newWorkflowServiceTestContext(t)
	service.store = persistedService.store
	service.store.SetWorkflowEventPublisher(service.events)
	subscription, err := service.events.subscribe("project-approval-diagnostic", nil)
	if err != nil {
		t.Fatalf("subscribe Workflow project events: %v", err)
	}
	t.Cleanup(func() { _ = subscription.Close() })

	response, err := service.ApproveWorkflowTask(context.Background(), serverapi.WorkflowTaskApproveRequest{
		ApprovalID: approvalID.String(),
	})
	if !errors.Is(err, diagnostic) {
		t.Fatalf("ApproveWorkflowTask error = %v, want %v", err, diagnostic)
	}
	if response.Applied == nil ||
		response.Applied.TaskID != string(source.TaskID) ||
		len(response.Applied.CurrentNodes) != 1 ||
		response.Applied.CurrentNodes[0].NodeID != string(target.NodeID) {
		t.Fatalf("committed Approval response = %+v, want applied target %v", response, target)
	}
	if approvalIDs := attention.resolvedApprovalIDs(); len(approvalIDs) != 1 || approvalIDs[0] != approvalID {
		t.Fatalf("resolved Approval attention = %+v, want %q", approvalIDs, approvalID)
	}
	eventCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	event, eventErr := subscription.Next(eventCtx)
	if eventErr != nil {
		t.Fatalf("read approved Task event: %v", eventErr)
	}
	if event.Action != serverapi.WorkflowProjectEventActionApproved ||
		event.PrimaryEntityID != string(source.TaskID) {
		t.Fatalf("approved Task event = %+v", event)
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
		workflowexecution.NewMutationPermit(),
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

func TestCompleteWorkflowTaskUsesOnlyTaskOrSessionForcedSelector(t *testing.T) {
	source := currentNodeCompletionReference(t, "task-forced", "node-agent")
	result := workflowstore.CurrentNodeCompletionResult{
		Mutation: workflow.CurrentNodeMutationResult{
			Removed: []workflow.CurrentNodeReference{source},
			Created: []workflow.CurrentNode{{
				Reference: currentNodeCompletionReference(t, "task-forced", "node-done"),
			}},
		},
	}
	tests := []struct {
		name    string
		request serverapi.WorkflowTaskCompleteRequest
		assert  func(*testing.T, workflowstore.IdleCurrentNodeSelector)
	}{
		{
			name: "task",
			request: serverapi.WorkflowTaskCompleteRequest{
				ActorKind:    serverapi.WorkflowTaskCompleteActorUser,
				Force:        true,
				TaskID:       string(source.TaskID),
				TransitionID: "done",
			},
			assert: func(t *testing.T, selector workflowstore.IdleCurrentNodeSelector) {
				t.Helper()
				if selector.TaskID == nil || *selector.TaskID != source.TaskID || selector.SessionID != nil {
					t.Fatalf("idle selector = %+v, want task selector", selector)
				}
			},
		},
		{
			name: "session",
			request: serverapi.WorkflowTaskCompleteRequest{
				ActorKind:    serverapi.WorkflowTaskCompleteActorUser,
				Force:        true,
				SessionID:    runtimeids.NewSessionID().String(),
				TransitionID: "done",
			},
			assert: func(t *testing.T, selector workflowstore.IdleCurrentNodeSelector) {
				t.Helper()
				if selector.TaskID != nil || selector.SessionID == nil {
					t.Fatalf("idle selector = %+v, want Session selector", selector)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execution := &currentNodeCompletionExecutionStub{idleResult: result}
			response, err := currentNodeCompletionService(execution).CompleteWorkflowTask(context.Background(), test.request)
			if err != nil {
				t.Fatalf("CompleteWorkflowTask: %v", err)
			}
			test.assert(t, execution.idleSelector)
			if response.TaskID != string(source.TaskID) || len(response.CurrentNodes) != 1 || response.CurrentNodes[0].NodeID != "node-done" {
				t.Fatalf("response = %+v, want task and replacement Current Node", response)
			}
		})
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

func (s *currentNodeCompletionExecutionStub) EnsureTaskResumeEligible(context.Context, workflow.TaskID) error {
	s.resumeEligibilityCalls++
	return s.resumeEligibilityErr
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

func (s *currentNodeCompletionExecutionStub) ResumeTask(ctx context.Context, taskID workflow.TaskID) ([]workflow.CurrentNode, error) {
	if s.store == nil {
		return nil, errors.New("workflow store is required")
	}
	selected, err := s.store.InterruptedExecutableCurrentNodes(ctx, taskID)
	if err != nil {
		return nil, err
	}
	for _, currentNode := range selected {
		if _, _, err := s.store.ResumeCurrentNode(ctx, currentNode.Reference); err != nil {
			return nil, err
		}
	}
	return selected, nil
}

func (s *currentNodeCompletionExecutionStub) ResumeTaskWithPreparation(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation workflowexecution.TaskStartPreparation,
	finalizer workflowexecution.TaskPreparationFinalizer,
) ([]workflow.CurrentNode, error) {
	if s.store == nil {
		return nil, errors.New("workflow store is required")
	}
	selected, err := s.store.InterruptedExecutableCurrentNodes(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if err := preparation.Prepare(ctx); err != nil {
		finalizer(workflowexecution.TaskPreparationFinalization{
			Kind:  workflowexecution.TaskPreparationFailed,
			Cause: err,
		})
		return nil, err
	}
	if err := preparation.Commit(ctx); err != nil {
		finalizer(workflowexecution.TaskPreparationFinalization{
			Kind:  workflowexecution.TaskPreparationFailed,
			Cause: err,
		})
		return nil, err
	}
	for _, currentNode := range selected {
		if _, _, err := s.store.ResumeCurrentNode(ctx, currentNode.Reference); err != nil {
			return nil, err
		}
	}
	finalizer(workflowexecution.TaskPreparationFinalization{Kind: workflowexecution.TaskPreparationHandedOff})
	return selected, nil
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
