package workflowsvc

import (
	"context"
	"core/internal/testharness/workflowtest"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/session"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestWorkflowSessionCannotStartItsOwnTask(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	sessionID := bindWorkflowServiceSessionToTask(
		t,
		service,
		metadataStore,
		binding,
		workflow.TaskID(task.Task.ID),
		started.CurrentNodes[0],
	)

	_, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:            task.Task.ID,
		InvokingSessionID: &sessionID,
		SetupOperationID:  serverapi.NewWorktreeSetupOperationID(),
	})
	var denied *serverapi.WorkflowTaskMutationSelfTargetError
	if !errors.As(err, &denied) || denied.TaskID != task.Task.ID {
		t.Fatalf("StartWorkflowTask error = %v, want self-target denial for %q", err, task.Task.ID)
	}
}

func TestWorkflowSessionCannotMoveItsOwnTask(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	sessionID := bindWorkflowServiceSessionToTask(
		t,
		service,
		metadataStore,
		binding,
		workflow.TaskID(task.Task.ID),
		started.CurrentNodes[0],
	)

	_, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:            task.Task.ID,
		InvokingSessionID: &sessionID,
		TargetNodeID:      "node-target",
	})
	var denied *serverapi.WorkflowTaskMutationSelfTargetError
	if !errors.As(err, &denied) || denied.TaskID != task.Task.ID {
		t.Fatalf("MoveWorkflowTask error = %v, want self-target denial for %q", err, task.Task.ID)
	}
}

func TestWorkflowSessionCannotApproveItsOwnTask(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	requireWorkflowServiceEdgeApproval(t, ctx, service, workflowID, "done")
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	sessionID := bindWorkflowServiceSessionToTask(
		t,
		service,
		metadataStore,
		binding,
		workflow.TaskID(task.Task.ID),
		started.CurrentNodes[0],
	)
	source := workflowServiceCurrentNodeReference(t, workflow.TaskID(task.Task.ID), started.CurrentNodes[0])
	completed, err := workflowtest.CompleteCurrentNode(service.store, ctx, workflowstore.CurrentNodeCompletionRequest{
		Source:       source,
		TransitionID: "done",
	})
	if err != nil || completed.PendingApproval == nil {
		t.Fatalf("CompleteCurrentNode = %+v, %v; want pending Approval", completed, err)
	}

	_, err = service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		ApprovalID:        completed.PendingApproval.ID.String(),
		InvokingSessionID: &sessionID,
	})
	var denied *serverapi.WorkflowTaskMutationSelfTargetError
	if !errors.As(err, &denied) || denied.TaskID != task.Task.ID {
		t.Fatalf("ApproveWorkflowTask error = %v, want self-target denial for %q", err, task.Task.ID)
	}
	if _, err := service.store.PendingApproval(ctx, completed.PendingApproval.ID); err != nil {
		t.Fatalf("pending Approval changed after denial: %v", err)
	}
}

func TestWorkflowSessionCannotInterruptOrResumeItsOwnTask(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	sessionID := bindWorkflowServiceSessionToTask(
		t,
		service,
		metadataStore,
		binding,
		workflow.TaskID(task.Task.ID),
		started.CurrentNodes[0],
	)
	execution := newTaskMutationAuthorizationExecutionStub(service)
	service.currentNodeExecution = execution

	_, interruptErr := service.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{
		TaskID:            task.Task.ID,
		InvokingSessionID: &sessionID,
	})
	var interruptDenied *serverapi.WorkflowTaskMutationSelfTargetError
	if !errors.As(interruptErr, &interruptDenied) || interruptDenied.TaskID != task.Task.ID {
		t.Fatalf("InterruptWorkflowTask error = %v, want self-target denial for %q", interruptErr, task.Task.ID)
	}

	_, resumeErr := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:            task.Task.ID,
		InvokingSessionID: &sessionID,
		SetupOperationID:  serverapi.NewWorktreeSetupOperationID(),
	})
	var resumeDenied *serverapi.WorkflowTaskMutationSelfTargetError
	if !errors.As(resumeErr, &resumeDenied) || resumeDenied.TaskID != task.Task.ID {
		t.Fatalf("ResumeWorkflowTask error = %v, want self-target denial for %q", resumeErr, task.Task.ID)
	}
	if len(execution.interrupts) != 0 || len(execution.resumedTaskIDs) != 0 {
		t.Fatalf("denied mutations reached execution: interrupts=%+v resumes=%+v", execution.interrupts, execution.resumedTaskIDs)
	}
}

func TestWorkflowSessionCanStartAnotherTask(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	ownedTask := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, ownedTask.Task.ID)
	sessionID := bindWorkflowServiceSessionToTask(
		t,
		service,
		metadataStore,
		binding,
		workflow.TaskID(ownedTask.Task.ID),
		started.CurrentNodes[0],
	)
	targetTask := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:            targetTask.Task.ID,
		InvokingSessionID: &sessionID,
		SetupOperationID:  serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if response.Applied == nil {
		t.Fatalf("StartWorkflowTask response = %+v, want applied", response)
	}
}

func TestWorkflowSessionCanMoveAnotherTask(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	ownedTask := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	ownedStarted := startWorkflowServiceTask(t, ctx, service, ownedTask.Task.ID)
	sessionID := bindWorkflowServiceSessionToTask(
		t,
		service,
		metadataStore,
		binding,
		workflow.TaskID(ownedTask.Task.ID),
		ownedStarted.CurrentNodes[0],
	)
	targetTask := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	targetNodeID := workflowServiceNodeIDByKind(t, definition.Definition, "agent")

	response, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:            targetTask.Task.ID,
		InvokingSessionID: &sessionID,
		TargetNodeID:      targetNodeID,
		SetupOperationID:  serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if response.Applied == nil {
		t.Fatalf("MoveWorkflowTask response = %+v, want applied", response)
	}
}

func TestWorkflowSessionCanApproveAnotherTask(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	requireWorkflowServiceEdgeApproval(t, ctx, service, workflowID, "done")
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	ownedTask := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	ownedStarted := startWorkflowServiceTask(t, ctx, service, ownedTask.Task.ID)
	sessionID := bindWorkflowServiceSessionToTask(
		t,
		service,
		metadataStore,
		binding,
		workflow.TaskID(ownedTask.Task.ID),
		ownedStarted.CurrentNodes[0],
	)
	targetTask := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	targetStarted := startWorkflowServiceTask(t, ctx, service, targetTask.Task.ID)
	source := workflowServiceCurrentNodeReference(t, workflow.TaskID(targetTask.Task.ID), targetStarted.CurrentNodes[0])
	completed, err := workflowtest.CompleteCurrentNode(service.store, ctx, workflowstore.CurrentNodeCompletionRequest{
		Source:       source,
		TransitionID: "done",
	})
	if err != nil || completed.PendingApproval == nil {
		t.Fatalf("CompleteCurrentNode = %+v, %v; want pending Approval", completed, err)
	}

	response, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		ApprovalID:        completed.PendingApproval.ID.String(),
		InvokingSessionID: &sessionID,
	})
	if err != nil {
		t.Fatalf("ApproveWorkflowTask: %v", err)
	}
	if response.Applied == nil || response.Applied.TaskID != targetTask.Task.ID {
		t.Fatalf("ApproveWorkflowTask response = %+v, want target Task applied", response)
	}
}

func TestWorkflowSessionCanInterruptAndResumeAnotherTask(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	ownedTask := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	ownedStarted := startWorkflowServiceTask(t, ctx, service, ownedTask.Task.ID)
	sessionID := bindWorkflowServiceSessionToTask(
		t,
		service,
		metadataStore,
		binding,
		workflow.TaskID(ownedTask.Task.ID),
		ownedStarted.CurrentNodes[0],
	)
	targetTask := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	execution := newTaskMutationAuthorizationExecutionStub(service)
	service.currentNodeExecution = execution

	if _, err := service.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{
		TaskID:            targetTask.Task.ID,
		InvokingSessionID: &sessionID,
	}); err != nil {
		t.Fatalf("InterruptWorkflowTask: %v", err)
	}
	if _, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:            targetTask.Task.ID,
		InvokingSessionID: &sessionID,
		SetupOperationID:  serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget:   &serverapi.WorkflowExecutionTargetSelection{Mode: serverapi.WorkflowExecutionTargetModeNone},
	}); err != nil {
		t.Fatalf("ResumeWorkflowTask: %v", err)
	}
	if len(execution.interrupts) != 1 || execution.interrupts[0].TaskID != workflow.TaskID(targetTask.Task.ID) {
		t.Fatalf("interrupts = %+v, want target Task", execution.interrupts)
	}
	if len(execution.resumedTaskIDs) != 1 || execution.resumedTaskIDs[0] != workflow.TaskID(targetTask.Task.ID) {
		t.Fatalf("resumed Task IDs = %+v, want target Task", execution.resumedTaskIDs)
	}
}

func TestWorkflowTaskMutationRejectsUnknownInvokingSession(t *testing.T) {
	ctx, service, binding, _ := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	unknownSessionID, err := runtimeids.ParseSessionID("unknown-session")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	execution := newTaskMutationAuthorizationExecutionStub(service)
	service.currentNodeExecution = execution

	if _, err := service.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{
		TaskID:            task.Task.ID,
		InvokingSessionID: &unknownSessionID,
	}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("InterruptWorkflowTask error = %v, want unknown Session failure", err)
	}
	if _, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:            task.Task.ID,
		InvokingSessionID: &unknownSessionID,
		SetupOperationID:  serverapi.NewWorktreeSetupOperationID(),
	}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("ResumeWorkflowTask error = %v, want unknown Session failure", err)
	}
	if len(execution.interrupts) != 0 || len(execution.resumedTaskIDs) != 0 {
		t.Fatalf("unknown Session mutations reached execution: interrupts=%+v resumes=%+v", execution.interrupts, execution.resumedTaskIDs)
	}

	_, err = service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:            task.Task.ID,
		InvokingSessionID: &unknownSessionID,
		SetupOperationID:  serverapi.NewWorktreeSetupOperationID(),
	})
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("StartWorkflowTask error = %v, want unknown Session failure", err)
	}

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("retry after unknown Session = %+v, %v; want unchanged Task to start", response, err)
	}
}

func TestUnboundSessionCanMutateAnyTask(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	requireWorkflowServiceEdgeApproval(t, ctx, service, workflowID, "done")
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	sessionID := createPersistedWorkflowServiceSession(t, metadataStore, binding)
	execution := newTaskMutationAuthorizationExecutionStub(service)
	service.currentNodeExecution = execution

	if _, err := service.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{
		TaskID:            task.Task.ID,
		InvokingSessionID: &sessionID,
	}); err != nil {
		t.Fatalf("InterruptWorkflowTask with unbound Session: %v", err)
	}
	if _, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:            task.Task.ID,
		InvokingSessionID: &sessionID,
		SetupOperationID:  serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget:   &serverapi.WorkflowExecutionTargetSelection{Mode: serverapi.WorkflowExecutionTargetModeNone},
	}); err != nil {
		t.Fatalf("ResumeWorkflowTask with unbound Session: %v", err)
	}

	moveTask := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	moveTargetNodeID := workflowServiceNodeIDByKind(t, definition.Definition, "agent")
	moveResponse, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:            moveTask.Task.ID,
		InvokingSessionID: &sessionID,
		TargetNodeID:      moveTargetNodeID,
		SetupOperationID:  serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil || moveResponse.Applied == nil {
		t.Fatalf("MoveWorkflowTask with unbound Session = %+v, %v; want applied", moveResponse, err)
	}

	approvalTask := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	approvalStarted := startWorkflowServiceTask(t, ctx, service, approvalTask.Task.ID)
	approvalSource := workflowServiceCurrentNodeReference(
		t,
		workflow.TaskID(approvalTask.Task.ID),
		approvalStarted.CurrentNodes[0],
	)
	completed, err := workflowtest.CompleteCurrentNode(service.store, ctx, workflowstore.CurrentNodeCompletionRequest{
		Source:       approvalSource,
		TransitionID: "done",
	})
	if err != nil || completed.PendingApproval == nil {
		t.Fatalf("CompleteCurrentNode = %+v, %v; want pending Approval", completed, err)
	}
	approvalResponse, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		ApprovalID:        completed.PendingApproval.ID.String(),
		InvokingSessionID: &sessionID,
	})
	if err != nil || approvalResponse.Applied == nil || approvalResponse.Applied.TaskID != approvalTask.Task.ID {
		t.Fatalf("ApproveWorkflowTask with unbound Session = %+v, %v; want target Task applied", approvalResponse, err)
	}

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:            task.Task.ID,
		InvokingSessionID: &sessionID,
		SetupOperationID:  serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("StartWorkflowTask = %+v, %v; want unbound Session allowed", response, err)
	}
}

type taskMutationAuthorizationExecutionStub struct {
	currentNodeCompletionExecutionStub
	interrupts     []workflowexecution.InterruptSelector
	resumedTaskIDs []workflow.TaskID
}

func newTaskMutationAuthorizationExecutionStub(service *Service) *taskMutationAuthorizationExecutionStub {
	return &taskMutationAuthorizationExecutionStub{
		currentNodeCompletionExecutionStub: currentNodeCompletionExecutionStub{store: service.store},
	}
}

func (s *taskMutationAuthorizationExecutionStub) Interrupt(_ context.Context, selector workflowexecution.InterruptSelector) error {
	s.interrupts = append(s.interrupts, selector)
	return nil
}

func (s *taskMutationAuthorizationExecutionStub) ResumeTask(_ context.Context, taskID workflow.TaskID) ([]workflow.CurrentNode, error) {
	s.resumedTaskIDs = append(s.resumedTaskIDs, taskID)
	return []workflow.CurrentNode{{
		Reference: workflow.CurrentNodeReference{
			TaskID: taskID,
			NodeID: workflow.NodeID("authorized-resume"),
		},
	}}, nil
}

func (s *taskMutationAuthorizationExecutionStub) ResumeTaskWithPreparation(
	_ context.Context,
	taskID workflow.TaskID,
	_ workflowexecution.TaskStartPreparation,
) ([]workflow.CurrentNode, error) {
	return s.ResumeTask(context.Background(), taskID)
}

func bindWorkflowServiceSessionToTask(
	t *testing.T,
	service *Service,
	metadataStore *metadata.Store,
	binding metadata.Binding,
	taskID workflow.TaskID,
	currentNode serverapi.WorkflowTaskCurrentNode,
) runtimeids.SessionID {
	t.Helper()
	sessionID := createPersistedWorkflowServiceSession(t, metadataStore, binding)
	reference := workflowServiceCurrentNodeReference(t, taskID, currentNode)
	if _, err := service.store.AssociateTaskSession(t.Context(), workflowstore.TaskSessionAssociationRequest{
		SessionID:    sessionID,
		CurrentNode:  reference,
		AssociatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AssociateTaskSession: %v", err)
	}
	return sessionID
}

func workflowServiceCurrentNodeReference(
	t *testing.T,
	taskID workflow.TaskID,
	currentNode serverapi.WorkflowTaskCurrentNode,
) workflow.CurrentNodeReference {
	t.Helper()
	var branchKey *workflow.TransitionBranchKey
	if currentNode.TransitionBranchKey != nil {
		value := workflow.TransitionBranchKey(*currentNode.TransitionBranchKey)
		branchKey = &value
	}
	reference, err := workflow.NewCurrentNodeReference(taskID, workflow.NodeID(currentNode.NodeID), branchKey)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	return reference
}

func createPersistedWorkflowServiceSession(
	t *testing.T,
	metadataStore *metadata.Store,
	binding metadata.Binding,
) runtimeids.SessionID {
	t.Helper()
	sessionRoot := filepath.Join(metadataStore.PersistenceRoot(), "projects", binding.ProjectID, "sessions")
	sessionStore, err := session.Create(
		sessionRoot,
		binding.WorkspaceName,
		binding.CanonicalRoot,
		sessioncontract.SessionCategoryMain,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := sessionStore.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	return sessionID
}
