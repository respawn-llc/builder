package workflowsvc

import (
	"context"
	"errors"
	"reflect"
	"testing"

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
		RunID:          "11111111-1111-4111-8111-111111111111",
		StepID:         "22222222-2222-4222-8222-222222222222",
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
	if execution.sessionID != sessionID {
		t.Fatalf("completion dispatch = %+v, want live Session completion", execution)
	}
}

func TestCompleteWorkflowTaskReturnsAcceptedCompletionDespitePostCommitDiagnostic(t *testing.T) {
	source := currentNodeCompletionReference(t, "task-accepted-diagnostic", "node-agent")
	publicationErr := errors.New("completion event publication failed")
	execution := &currentNodeCompletionExecutionStub{
		sessionResult: workflowstore.CurrentNodeCompletionResult{
			PendingApproval: &workflow.PendingApproval{
				ID:     workflow.NewApprovalID(),
				Source: source,
			},
		},
		sessionDiagnostic: publicationErr,
	}
	response, err := currentNodeCompletionService(execution).CompleteWorkflowTask(
		context.Background(),
		serverapi.WorkflowTaskCompleteRequest{
			ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
			AgentSessionID: runtimeids.NewSessionID().String(),
			RunID:          "11111111-1111-4111-8111-111111111111",
			StepID:         "22222222-2222-4222-8222-222222222222",
			TransitionID:   "done",
		},
	)
	if err != nil {
		t.Fatalf("CompleteWorkflowTask: %v", err)
	}
	if response.TaskID != string(source.TaskID) {
		t.Fatalf("accepted completion response = %+v, want Task %s", response, source.TaskID)
	}
}

func TestCompleteWorkflowTaskForceComposesInterruptThenManualMove(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	execution := newManualMoveExecutionStub(service)
	service.currentNodeExecution = execution
	startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	execution.calls = nil

	response, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind:    serverapi.WorkflowTaskCompleteActorUser,
		Force:        true,
		TaskID:       task.Task.ID,
		TransitionID: "next",
		OutputValues: map[string]string{"prior_summary": "planned"},
		Commentary:   "Proceed with implementation.",
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask: %v", err)
	}
	if !reflect.DeepEqual(execution.calls, []string{"interrupt", "manual_move"}) {
		t.Fatalf("forced completion operations = %v, want Interrupt then Manual Move", execution.calls)
	}
	if len(execution.interruptTaskIDs) != 2 ||
		execution.interruptTaskIDs[0] != workflow.TaskID(task.Task.ID) ||
		execution.interruptTaskIDs[1] != workflow.TaskID(task.Task.ID) {
		t.Fatalf("forced completion interrupt selections = %v, want Task Interrupt then Manual Move interruption", execution.interruptTaskIDs)
	}
	if response.TaskID != task.Task.ID ||
		len(response.CurrentNodes) != 1 ||
		response.Handoff.SourceNodeDisplayName != "Plan" ||
		response.Handoff.DestinationDisplayName != "Implement" {
		t.Fatalf("forced completion response = %+v", response)
	}
}

func TestCompleteWorkflowTaskMapsMissingLiveSourceFailure(t *testing.T) {
	sessionID := runtimeids.NewSessionID()
	execution := &currentNodeCompletionExecutionStub{sessionErr: sessionruntime.ErrExecutionNoLongerLive}
	_, err := currentNodeCompletionService(execution).CompleteWorkflowTask(context.Background(), serverapi.WorkflowTaskCompleteRequest{
		ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
		AgentSessionID: sessionID.String(),
		RunID:          "11111111-1111-4111-8111-111111111111",
		StepID:         "22222222-2222-4222-8222-222222222222",
		TransitionID:   "done",
	})
	if !errors.Is(err, serverapi.ErrWorkflowTaskCompleteTargetNotFound) {
		t.Fatalf("live completion error = %v, want target-not-found", err)
	}
}

func TestWorkflowTaskCompleteContractHasExactAgentProvenanceAndNoPlacementFields(t *testing.T) {
	for _, contract := range []reflect.Type{
		reflect.TypeOf(serverapi.WorkflowTaskCompleteRequest{}),
		reflect.TypeOf(serverapi.WorkflowTaskCompleteResponse{}),
	} {
		for _, removed := range []string{"RunIDs", "PlacementID", "PlacementIDs", "ProjectID", "ShortID"} {
			if _, exists := contract.FieldByName(removed); exists {
				t.Fatalf("%s still exposes removed completion field %s", contract.Name(), removed)
			}
		}
	}
	request := reflect.TypeOf(serverapi.WorkflowTaskCompleteRequest{})
	for _, required := range []string{"RunID", "StepID"} {
		if _, exists := request.FieldByName(required); !exists {
			t.Fatalf("WorkflowTaskCompleteRequest lacks %s provenance", required)
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
	sessionDiagnostic      error
	sessionErr             error
	manualMoveAssignments  workflowstore.ManualMoveTargetAssignmentPreparer
}

func (s *currentNodeCompletionExecutionStub) configuredResumePreflight(
	taskID workflow.TaskID,
) (workflowexecution.TaskResumePreflight, bool) {
	if s.resumePreflight.Outcome == "" && s.resumePreflight.CurrentNodes == nil {
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
	if s.manualMoveAssignments != nil {
		return s.store.ApplyManualMoveWithTargetAssignments(ctx, prepared, candidate, s.manualMoveAssignments)
	}
	return s.store.ApplyManualMove(ctx, prepared, candidate)
}

func (s *currentNodeCompletionExecutionStub) Interrupt(context.Context, workflowexecution.InterruptSelector) error {
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

func (s *currentNodeCompletionExecutionStub) CompleteSessionCurrentNode(
	_ context.Context,
	sessionID runtimeids.SessionID,
	_ runtimeids.RunID,
	_ runtimeids.StepID,
	_ string,
	_ map[string]string,
	_ string,
) (workflowruntime.CompletionOutcome, error) {
	s.sessionID = sessionID
	if s.sessionErr != nil {
		return workflowruntime.RejectedCompletionOutcome(s.sessionErr), s.sessionErr
	}
	return workflowruntime.AcceptedCompletionOutcome(workflowruntime.AcceptedCompletion{
		Result:     workflowruntime.CompletionResult{CommittedResult: s.sessionResult},
		Diagnostic: s.sessionDiagnostic,
	}), nil
}
