package workflowsvc

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
	"core/server/worktree"
	"core/shared/serverapi"
)

type initialBranchControllerRunner struct{}

func (initialBranchControllerRunner) StartCurrentNode(
	context.Context,
	workflow.CurrentNodeReference,
	workflowruntime.TaskPromptDelivery,
	workflowexecution.CurrentNodeAssignmentSteer,
	sessionruntime.WorkflowExecutionLease,
	workflowruntime.Controller,
) error {
	return errors.New("runner must not start after branch preparation failure")
}

type initialBranchControllerSteerer struct{}

func (initialBranchControllerSteerer) SteerCurrentNodeAssignment(
	context.Context,
	workflow.CurrentNodeReference,
) (workflowexecution.CurrentNodeAssignmentSteer, error) {
	return initialBranchControllerSteer{}, nil
}

type initialBranchControllerSteer struct{}

func (initialBranchControllerSteer) Wait(context.Context) (session.CommitReceipt, error) {
	return session.CommitReceipt{Committed: true}, nil
}

func TestServiceTaskStartPersistsExplicitBranchBeforeConfiguredTargetResolution(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	preparations := make(chan workflowexecution.TaskStartPreparation, 1)
	service.currentNodeExecution = &currentNodeCompletionExecutionStub{
		store: service.store, startPreparations: preparations,
	}
	branchName := "feature/MBL-742"
	targets := &recordingExecutionTargetInfrastructure{
		resolveErr: &worktree.GitRevisionResolutionError{
			Kind:         worktree.GitRevisionResolutionErrorInvalidRevision,
			RequestedRef: "HEAD",
		},
	}
	service.executionTargets = targets

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
		BranchName:       &branchName,
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("StartWorkflowTask = %+v, %v; want applied placement", response, err)
	}
	var resolutionErr *worktree.GitRevisionResolutionError
	if err := (<-preparations)(ctx); !errors.As(err, &resolutionErr) {
		t.Fatalf("preparation error = %T %v, want configured target resolution error", err, err)
	}
	if targets.initialBranchInspection.BranchName != branchName ||
		targets.initialBranchInspection.SourceWorkspaceRoot != binding.CanonicalRoot {
		t.Fatalf("initial branch inspection = %+v, want branch %q in source workspace", targets.initialBranchInspection, branchName)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != branchName {
		t.Fatalf("pending initial branch = %v, want %q", targetContext.Task.PendingInitialManagedBranchName, branchName)
	}
	if targetContext.Task.ExecutionTarget != nil {
		t.Fatalf("execution target = %+v, want unlocked after resolution failure", targetContext.Task.ExecutionTarget)
	}
}

func TestServiceTaskResumeEligibilityRejectsExplicitBranchBeforePendingMutation(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	taskID := workflow.TaskID(task.Task.ID)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller, err := workflowexecution.NewCurrentNodeController(
		service.store,
		initialBranchControllerRunner{},
		authority,
		service.mutationPermit,
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: initialBranchControllerSteerer{},
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close CurrentNodeController: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close session runtime authority: %v", err)
		}
	})
	service.currentNodeExecution = controller
	targets := &recordingExecutionTargetInfrastructure{}
	service.executionTargets = targets
	branchName := "feature/ineligible-resume"

	_, err = service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &branchName,
	})

	var conflict *workflowexecution.TaskResumeConflictError
	if !errors.As(err, &conflict) || conflict.TaskID != taskID {
		t.Fatalf("ResumeWorkflowTask error = %T %v, want typed conflict", err, err)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != task.Task.ShortID {
		t.Fatalf("pending branch = %v, want unchanged %q", targetContext.Task.PendingInitialManagedBranchName, task.Task.ShortID)
	}
	if targets.initialBranchInspections != 0 || targets.materializeRequest.TaskID != "" {
		t.Fatalf("Execution Target infrastructure used before Resume eligibility: %+v", targets)
	}
}

func TestServiceTaskResumeEligibilityReturnsAllInvalidErrorBeforeBranchPreflight(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	taskID := workflow.TaskID(task.Task.ID)
	reference, err := workflow.NewCurrentNodeReference(taskID, workflow.NodeID("node-invalid"), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	validationErr := &workflowstore.CurrentNodeResumeValidationError{
		Diagnostics: []workflowstore.CurrentNodeResumeValidationDiagnostic{{
			Code:           workflowstore.CurrentNodeResumeParameterNotMaterializedCode,
			CurrentNode:    reference,
			EnteringEdgeID: workflow.EdgeID("edge-invalid"),
			ParameterKey:   "reviewer",
		}},
	}
	execution := &currentNodeCompletionExecutionStub{
		store:                service.store,
		resumeEligibilityErr: validationErr,
	}
	service.currentNodeExecution = execution
	targets := &recordingExecutionTargetInfrastructure{}
	service.executionTargets = targets
	branchName := "feature/all-invalid-resume"

	_, err = service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &branchName,
	})

	var typed *workflowstore.CurrentNodeResumeValidationError
	if !errors.As(err, &typed) || len(typed.Diagnostics) != 1 {
		t.Fatalf("ResumeWorkflowTask error = %T %v, want typed Resume validation error", err, err)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != task.Task.ShortID {
		t.Fatalf("pending branch = %v, want unchanged %q", targetContext.Task.PendingInitialManagedBranchName, task.Task.ShortID)
	}
	if targets.initialBranchInspections != 0 || targets.materializeRequest.TaskID != "" {
		t.Fatalf("Execution Target infrastructure used before Resume eligibility: %+v", targets)
	}
}

func TestServiceManualMoveNoOpRejectsExplicitBranchWithoutPendingMutation(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	taskID := workflow.TaskID(task.Task.ID)
	service.currentNodeExecution = newManualMoveExecutionStub(service)
	currentNodes, err := service.store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 {
		t.Fatalf("Current Nodes = %+v, want one Backlog node", currentNodes)
	}
	branchName := "feature/no-op-move"

	_, err = service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:       task.Task.ID,
		TargetNodeID: string(currentNodes[0].Reference.NodeID),
		BranchName:   &branchName,
	})

	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree ||
		branchErr.BranchName != branchName {
		t.Fatalf("MoveWorkflowTask error = %T %v, want operation-cannot-create-Worktree for %q", err, err, branchName)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != task.Task.ShortID {
		t.Fatalf("pending branch = %v, want unchanged %q", targetContext.Task.PendingInitialManagedBranchName, task.Task.ShortID)
	}
	after, err := service.store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after rejected move: %v", err)
	}
	if len(after) != 1 || after[0].Reference != currentNodes[0].Reference {
		t.Fatalf("Current Nodes after rejected move = %+v, want unchanged %+v", after, currentNodes)
	}
}

func TestServiceManualMoveNonExecutableRejectsExplicitBranchWithoutPendingMutation(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	taskID := workflow.TaskID(task.Task.ID)
	execution := newManualMoveExecutionStub(service)
	service.currentNodeExecution = execution
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	targetNodeID := workflowServiceNodeIDByKind(t, definition.Definition, "terminal")
	before, err := service.store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	branchName := "feature/non-executable-move"

	_, err = service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:       task.Task.ID,
		TargetNodeID: targetNodeID,
		BranchName:   &branchName,
	})

	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree ||
		branchErr.BranchName != branchName {
		t.Fatalf("MoveWorkflowTask error = %T %v, want operation-cannot-create-Worktree for %q", err, err, branchName)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != task.Task.ShortID {
		t.Fatalf("pending branch = %v, want unchanged %q", targetContext.Task.PendingInitialManagedBranchName, task.Task.ShortID)
	}
	after, err := service.store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after rejected move: %v", err)
	}
	if len(after) != len(before) || len(after) != 1 || after[0].Reference != before[0].Reference {
		t.Fatalf("Current Nodes after rejected move = %+v, want unchanged %+v", after, before)
	}
	if len(execution.interruptTaskIDs) != 0 || len(execution.started) != 0 {
		t.Fatalf("execution mutations after rejected move: interrupts=%v starts=%v", execution.interruptTaskIDs, execution.started)
	}
}

func TestServiceManualMoveSelectionRequiredReturnsBeforePendingBranchReplacement(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	service.currentNodeExecution = newManualMoveExecutionStub(service)
	targets := &recordingExecutionTargetInfrastructure{}
	service.executionTargets = targets
	branchName := "feature/move-awaiting-selection"

	response, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:           task.Task.ID,
		TargetNodeID:     workflowServiceNodeIDByKey(t, definition.Definition, "plan"),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &branchName,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired ||
		response.SelectionRequired == nil {
		t.Fatalf("Move response = %+v, want selection required", response)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != task.Task.ShortID {
		t.Fatalf("pending branch = %v, want unchanged %q", targetContext.Task.PendingInitialManagedBranchName, task.Task.ShortID)
	}
	if targets.initialBranchInspections != 0 || targets.materializeTaskID != "" {
		t.Fatalf("target infrastructure used before selection: %+v", targets)
	}
}

func TestServiceManualMoveDependencyConfirmationReturnsBeforePendingBranchReplacement(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	blocker := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	blocked := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	if _, err := service.AddWorkflowTaskDependency(ctx, serverapi.WorkflowTaskDependencyAddRequest{
		BlockerTaskID: blocker.Task.ID,
		BlockedTaskID: blocked.Task.ID,
	}); err != nil {
		t.Fatalf("AddWorkflowTaskDependency: %v", err)
	}
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	service.currentNodeExecution = newManualMoveExecutionStub(service)
	targets := &recordingExecutionTargetInfrastructure{}
	service.executionTargets = targets
	branchName := "feature/move-blocked"

	response, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:           blocked.Task.ID,
		TargetNodeID:     workflowServiceNodeIDByKey(t, definition.Definition, "plan"),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &branchName,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired ||
		response.UnsatisfiedDependencyCount == nil ||
		*response.UnsatisfiedDependencyCount != 1 {
		t.Fatalf("Move response = %+v, want dependency confirmation", response)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(blocked.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != blocked.Task.ShortID {
		t.Fatalf("pending branch = %v, want unchanged %q", targetContext.Task.PendingInitialManagedBranchName, blocked.Task.ShortID)
	}
	if targets.initialBranchInspections != 0 || targets.materializeTaskID != "" {
		t.Fatalf("target infrastructure used before dependency confirmation: %+v", targets)
	}
}

func TestServiceTaskStartReusesDefaultPendingBranch(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	preparations := make(chan workflowexecution.TaskStartPreparation, 1)
	service.currentNodeExecution = &currentNodeCompletionExecutionStub{
		store: service.store, startPreparations: preparations,
	}
	targets := &recordingExecutionTargetInfrastructure{
		resolveErr: &worktree.GitRevisionResolutionError{
			Kind:         worktree.GitRevisionResolutionErrorInvalidRevision,
			RequestedRef: "HEAD",
		},
	}
	service.executionTargets = targets

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("StartWorkflowTask = %+v, %v; want applied placement", response, err)
	}
	_ = (<-preparations)(ctx)
	if targets.initialBranchInspections != 1 ||
		targets.initialBranchInspection.BranchName != task.Task.ShortID {
		t.Fatalf("initial branch inspections = %d, last = %+v; want default %q", targets.initialBranchInspections, targets.initialBranchInspection, task.Task.ShortID)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != task.Task.ShortID {
		t.Fatalf("pending initial branch = %v, want unchanged default %q", targetContext.Task.PendingInitialManagedBranchName, task.Task.ShortID)
	}
}

func TestServiceTaskStartReturnsSelectionBeforeReplacingPendingBranch(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	branchName := "feature/not-yet-selected"
	targets := &recordingExecutionTargetInfrastructure{}
	service.executionTargets = targets

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
		BranchName:       &branchName,
	})
	if err != nil || response.SelectionRequired == nil {
		t.Fatalf("StartWorkflowTask = %+v, %v; want selection required", response, err)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != task.Task.ShortID {
		t.Fatalf("pending branch = %v, want unchanged %q", targetContext.Task.PendingInitialManagedBranchName, task.Task.ShortID)
	}
	if targets.initialBranchInspections != 0 {
		t.Fatalf("branch inspections = %d, want none before target selection", targets.initialBranchInspections)
	}
}

func TestServiceTaskStartRejectsExplicitBranchForNoManagedTargetBeforePlacement(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	branchName := "feature/no-target"

	_, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
		BranchName:       &branchName,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonNoManagedTarget {
		t.Fatalf("StartWorkflowTask error = %T %v, want no-managed-target branch error", err, err)
	}
	if err := service.store.ValidateTaskStart(ctx, workflow.TaskID(task.Task.ID)); err != nil {
		t.Fatalf("task left Backlog after rejected preflight: %v", err)
	}
}

func TestServiceTaskStartBranchCollisionReturnsBeforePlacement(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	branchName := "feature/collision"
	ref := "refs/heads/" + branchName
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		initialBranchErr: &serverapi.WorkflowTaskInitialBranchError{
			Reason: serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision, BranchName: branchName, Ref: &ref,
		},
	}

	_, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
		BranchName:       &branchName,
	})
	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision {
		t.Fatalf("StartWorkflowTask error = %T %v, want local collision", err, err)
	}
	if err := service.store.ValidateTaskStart(ctx, workflow.TaskID(task.Task.ID)); err != nil {
		t.Fatalf("task left Backlog after collision: %v", err)
	}
}

func TestServiceTaskStartFinalBranchCollisionOccursAfterPlacementAndRetainsPendingBranch(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller, err := workflowexecution.NewCurrentNodeController(
		service.store,
		initialBranchControllerRunner{},
		authority,
		service.mutationPermit,
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: initialBranchControllerSteerer{},
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close CurrentNodeController: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close session runtime authority: %v", err)
		}
	})
	service.currentNodeExecution = controller
	requestedRef := "HEAD"
	commitOID := strings.Repeat("e", 40)
	branchName := "feature/final-collision"
	ref := "refs/heads/" + branchName
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requestedRef,
			CommitOID: &commitOID, Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		materializeErr: &serverapi.WorkflowTaskInitialBranchError{
			Reason:     serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision,
			BranchName: branchName, Ref: &ref,
		},
	}

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
		BranchName:       &branchName,
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("StartWorkflowTask = %+v, %v; want placed task", response, err)
	}
	var currentNodes []workflow.CurrentNode
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 20*time.Millisecond, func() bool {
		var listErr error
		currentNodes, listErr = service.store.ListCurrentNodes(ctx, workflow.TaskID(task.Task.ID))
		return listErr == nil &&
			len(currentNodes) == 1 &&
			currentNodes[0].Scheduling != nil &&
			currentNodes[0].Scheduling.Interruption != nil
	}, "Current Node was not interrupted after final local branch collision")
	if currentNodes[0].Scheduling.Interruption.Reason != workflow.CurrentNodeInterruptionReason("workflow_runtime_start_failed") {
		t.Fatalf("interruption = %+v, want runtime-start failure", currentNodes[0].Scheduling.Interruption)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil ||
		targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != branchName {
		t.Fatalf("target context after collision = %+v, want unlocked with pending branch %q", targetContext.Task, branchName)
	}
}

func TestServiceTaskResumeReturnsAppliedBeforeFinalBranchCollisionInterruptsCurrentNode(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	taskID := workflow.TaskID(task.Task.ID)
	started, err := service.store.StartTask(ctx, taskID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	reference := started.Mutation.Created[0].Reference
	if err := service.store.InterruptCurrentNode(
		ctx,
		reference,
		workflow.CurrentNodeInterruptionReason("test_resume"),
		workflow.CurrentNodeInterruptionDetail{Code: "test_resume"},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller, err := workflowexecution.NewCurrentNodeController(
		service.store,
		initialBranchControllerRunner{},
		authority,
		service.mutationPermit,
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: initialBranchControllerSteerer{},
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	t.Cleanup(func() {
		if err := controller.Close(); err != nil {
			t.Errorf("close CurrentNodeController: %v", err)
		}
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close session runtime authority: %v", err)
		}
	})
	service.currentNodeExecution = controller
	requestedRef := "HEAD"
	commitOID := strings.Repeat("9", 40)
	branchName := "feature/resume-final-collision"
	ref := "refs/heads/" + branchName
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requestedRef,
			CommitOID: &commitOID, Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		materializeErr: &serverapi.WorkflowTaskInitialBranchError{
			Reason:     serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision,
			BranchName: branchName,
			Ref:        &ref,
		},
	}

	response, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &branchName,
	})
	if err != nil {
		t.Fatalf("ResumeWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied ||
		response.Applied == nil ||
		len(response.Applied.CurrentNodes) != 1 ||
		response.Applied.CurrentNodes[0].NodeID != string(reference.NodeID) {
		t.Fatalf("Resume response = %+v, want applied resumed Current Node", response)
	}
	var currentNodes []workflow.CurrentNode
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 20*time.Millisecond, func() bool {
		var listErr error
		currentNodes, listErr = service.store.ListCurrentNodes(ctx, taskID)
		return listErr == nil &&
			len(currentNodes) == 1 &&
			currentNodes[0].Scheduling != nil &&
			currentNodes[0].Scheduling.State == workflow.CurrentNodeSchedulingInterrupted &&
			currentNodes[0].Scheduling.Interruption != nil
	}, "resumed Current Node was not interrupted after final local branch collision")
	if currentNodes[0].Scheduling.Interruption.Reason != workflow.CurrentNodeInterruptionReason("workflow_runtime_start_failed") {
		t.Fatalf("interruption = %+v, want runtime-start failure", currentNodes[0].Scheduling.Interruption)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil ||
		targetContext.Task.ManagedWorktreeID != "" ||
		targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != branchName {
		t.Fatalf("target context after Resume collision = %+v, want unlocked without Worktree and pending %q", targetContext.Task, branchName)
	}
}

func TestServiceTaskResumeReplacesPendingBranchBeforeExecutablePreparation(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	taskID := workflow.TaskID(task.Task.ID)
	started, err := service.store.StartTask(ctx, taskID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := service.store.InterruptCurrentNode(
		ctx,
		started.Mutation.Created[0].Reference,
		workflow.CurrentNodeInterruptionReason("test_resume"),
		workflow.CurrentNodeInterruptionDetail{Code: "test_resume"},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	service.currentNodeExecution = &currentNodeCompletionExecutionStub{store: service.store}
	requestedRef := "HEAD"
	commitOID := strings.Repeat("8", 40)
	branchName := "feature/resume-replacement"
	targets := &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requestedRef,
			CommitOID: &commitOID, Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		materializeErr: errors.New("stop after branch replacement"),
	}
	service.executionTargets = targets

	_, err = service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &branchName,
	})
	if err == nil {
		t.Fatal("ResumeWorkflowTask succeeded, want preparation failure")
	}
	if targets.initialBranchInspection.BranchName != branchName ||
		targets.materializeRequest.InitialBranchAssertion == nil ||
		*targets.materializeRequest.InitialBranchAssertion != branchName {
		t.Fatalf("target branch handoff = inspection %+v, materialization %+v; want %q", targets.initialBranchInspection, targets.materializeRequest, branchName)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil ||
		targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != branchName {
		t.Fatalf("target context after failed Resume preparation = %+v, want pending %q", targetContext.Task, branchName)
	}
}

func TestServiceTaskResumeRejectsExplicitBranchForLockedNoneTarget(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	taskID := workflow.TaskID(task.Task.ID)
	started, err := service.store.StartTask(ctx, taskID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := service.store.LockTaskExecutionTarget(ctx, taskID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode:       workflow.ExecutionTargetModeNone,
			Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: binding.CanonicalRoot,
		},
	}); err != nil {
		t.Fatalf("LockTaskExecutionTarget: %v", err)
	}
	if err := service.store.InterruptCurrentNode(
		ctx,
		started.Mutation.Created[0].Reference,
		workflow.CurrentNodeInterruptionReason("test_resume"),
		workflow.CurrentNodeInterruptionDetail{Code: "test_resume"},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	execution := &currentNodeCompletionExecutionStub{store: service.store}
	service.currentNodeExecution = execution
	targets := &recordingExecutionTargetInfrastructure{}
	service.executionTargets = targets
	branchName := "feature/locked-none"

	_, err = service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &branchName,
	})
	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonNoManagedTarget ||
		branchErr.BranchName != branchName {
		t.Fatalf("ResumeWorkflowTask error = %T %v, want locked-none rejection", err, err)
	}
	currentNodes, err := service.store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
		t.Fatalf("Current Nodes after rejected Resume = %+v, want still interrupted", currentNodes)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil ||
		targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone ||
		targetContext.Task.PendingInitialManagedBranchName != nil {
		t.Fatalf("target context after rejected Resume = %+v, want locked none without pending branch", targetContext.Task)
	}
	if targets.initialBranchInspections != 0 || targets.restoreTaskID != "" {
		t.Fatalf("target infrastructure used for locked-none rejection: %+v", targets)
	}
}

func TestServiceTaskResumeAcceptsExactAssertionForLockedManagedWorktree(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	taskID := workflow.TaskID(task.Task.ID)
	started, err := service.store.StartTask(ctx, taskID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	worktreeID := "worktree-" + task.Task.ID
	worktreeRoot := filepath.Join(t.TempDir(), "task-worktree")
	if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID:            worktreeID,
		WorkspaceID:   binding.WorkspaceID,
		CanonicalRoot: worktreeRoot,
		Managed:       true,
		CreatedBranch: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	updated, err := metadataStore.Queries().BindInitialTaskManagedWorktree(ctx, sqlitegen.BindInitialTaskManagedWorktreeParams{
		ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
		TaskID:            task.Task.ID,
	})
	if err != nil || updated != 1 {
		t.Fatalf("BindInitialTaskManagedWorktree = %d, %v", updated, err)
	}
	requestedRef := "HEAD"
	commitOID := strings.Repeat("5", 40)
	if err := service.store.LockTaskExecutionTarget(ctx, taskID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requestedRef,
			CommitOID: &commitOID, Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID: binding.WorkspaceID, SourceWorkspaceRoot: binding.CanonicalRoot,
			Managed: &workflowstore.ManagedExecutionRoot{WorktreeID: worktreeID, Root: worktreeRoot},
		},
	}); err != nil {
		t.Fatalf("LockTaskExecutionTarget: %v", err)
	}
	if err := service.store.InterruptCurrentNode(
		ctx,
		started.Mutation.Created[0].Reference,
		workflow.CurrentNodeInterruptionReason("test_resume"),
		workflow.CurrentNodeInterruptionDetail{Code: "test_resume"},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	service.currentNodeExecution = &currentNodeCompletionExecutionStub{store: service.store}
	targets := &recordingExecutionTargetInfrastructure{}
	service.executionTargets = targets
	branchName := "feature/existing-branch"

	response, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &branchName,
	})
	if err != nil {
		t.Fatalf("ResumeWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied ||
		response.Applied == nil ||
		len(response.Applied.CurrentNodes) != 1 {
		t.Fatalf("Resume response = %+v, want applied", response)
	}
	if targets.restoreTaskID != taskID ||
		targets.restoreRequest.InitialBranchAssertion == nil ||
		*targets.restoreRequest.InitialBranchAssertion != branchName {
		t.Fatalf("restore request = %+v, want exact branch assertion %q", targets.restoreRequest, branchName)
	}
	if targets.initialBranchInspections != 0 || targets.materializeTaskID != "" {
		t.Fatalf("fresh branch infrastructure used for locked Worktree: %+v", targets)
	}
}

func TestServiceRejectsBranchAssertionForLockedManagedTargetWithoutWorktreeAuthority(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	worktreeID := "worktree-" + task.Task.ID
	worktreeRoot := filepath.Join(t.TempDir(), "task-worktree")
	if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID: worktreeID, WorkspaceID: binding.WorkspaceID,
		CanonicalRoot: worktreeRoot, Managed: true, CreatedBranch: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	updated, err := metadataStore.Queries().BindInitialTaskManagedWorktree(ctx, sqlitegen.BindInitialTaskManagedWorktreeParams{
		ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
		TaskID:            task.Task.ID,
	})
	if err != nil || updated != 1 {
		t.Fatalf("BindInitialTaskManagedWorktree = %d, %v", updated, err)
	}
	requestedRef := "HEAD"
	commitOID := strings.Repeat("f", 40)
	if err := service.store.LockTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID), &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requestedRef,
			CommitOID: &commitOID, Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID: binding.WorkspaceID, SourceWorkspaceRoot: binding.CanonicalRoot,
			Managed: &workflowstore.ManagedExecutionRoot{WorktreeID: worktreeID, Root: worktreeRoot},
		},
	}); err != nil {
		t.Fatalf("LockTaskExecutionTarget: %v", err)
	}
	updated, err = metadataStore.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ManagedWorktreeID: sql.NullString{},
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
		ID:                task.Task.ID,
	})
	if err != nil || updated != 1 {
		t.Fatalf("clear managed Worktree = %d, %v", updated, err)
	}
	targets := &recordingExecutionTargetInfrastructure{}
	service.executionTargets = targets
	branchName := "feature/cannot-assert-without-authority"

	_, err = service.preflightInitiatingActionTarget(
		ctx,
		workflow.TaskID(task.Task.ID),
		nil,
		&branchName,
	)
	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree {
		t.Fatalf("preflight error = %T %v, want operation-cannot-create-Worktree", err, err)
	}
	if targets.initialBranchInspections != 0 || targets.restoreTaskID != "" {
		t.Fatalf("target infrastructure used for rejected unbound assertion: %+v", targets)
	}
}

func TestServiceTaskStartMaterializesLatestTaskScopedPendingBranch(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	preparations := make(chan workflowexecution.TaskStartPreparation, 1)
	service.currentNodeExecution = &currentNodeCompletionExecutionStub{store: service.store, startPreparations: preparations}
	requestedRef := "HEAD"
	commitOID := strings.Repeat("c", 40)
	branchA := "feature/request-a"
	branchB := "feature/request-b"
	materializedBranch := ""
	worktreeID := "worktree-" + task.Task.ID
	targets := &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requestedRef,
			CommitOID: &commitOID, Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
	}
	targets.materialize = func(taskID workflow.TaskID) (ExecutionTargetMaterialization, error) {
		targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
		if err != nil {
			return ExecutionTargetMaterialization{}, err
		}
		materializedBranch = *targetContext.Task.PendingInitialManagedBranchName
		if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
			ID: worktreeID, WorkspaceID: binding.WorkspaceID,
			CanonicalRoot: filepath.Join(t.TempDir(), "task-worktree"), Managed: true, CreatedBranch: true,
		}); err != nil {
			return ExecutionTargetMaterialization{}, err
		}
		updated, err := metadataStore.Queries().BindInitialTaskManagedWorktree(ctx, sqlitegen.BindInitialTaskManagedWorktreeParams{
			ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
			UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(), TaskID: string(taskID),
		})
		if err != nil || updated != 1 {
			return ExecutionTargetMaterialization{}, errors.Join(err, errors.New("initial Worktree bind failed"))
		}
		root := workflowstore.ManagedExecutionRoot{WorktreeID: worktreeID, Root: filepath.Join(t.TempDir(), "retained")}
		return ExecutionTargetMaterialization{RetainedRoot: &root}, nil
	}
	service.executionTargets = targets

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID, BranchName: &branchA,
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("StartWorkflowTask = %+v, %v; want placed task", response, err)
	}
	if _, err := service.preflightInitiatingActionTarget(ctx, workflow.TaskID(task.Task.ID), nil, &branchB); err != nil {
		t.Fatalf("second eligible branch preflight: %v", err)
	}
	if err := (<-preparations)(context.Background()); err != nil {
		t.Fatalf("start preparation: %v", err)
	}
	if materializedBranch != branchB {
		t.Fatalf("materialized branch = %q, want latest task-scoped branch %q", materializedBranch, branchB)
	}
	if targets.materializeRequest.InitialBranchAssertion == nil ||
		*targets.materializeRequest.InitialBranchAssertion != branchA {
		t.Fatalf("materialization assertion = %v, want originating request %q", targets.materializeRequest.InitialBranchAssertion, branchA)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName != nil {
		t.Fatalf("pending branch after bind = %v, want consumed", targetContext.Task.PendingInitialManagedBranchName)
	}
	exact, err := service.preflightInitiatingActionTarget(ctx, workflow.TaskID(task.Task.ID), nil, &branchB)
	if err != nil {
		t.Fatalf("exact post-creation preflight: %v", err)
	}
	if _, err := service.initiatingActionTarget(ctx, workflow.TaskID(task.Task.ID), serverapi.NewWorktreeSetupOperationID(), exact); err != nil {
		t.Fatalf("exact post-creation assertion: %v", err)
	}
	if targets.restoreRequest.InitialBranchAssertion == nil ||
		*targets.restoreRequest.InitialBranchAssertion != branchB {
		t.Fatalf("restore assertion = %v, want exact branch %q", targets.restoreRequest.InitialBranchAssertion, branchB)
	}
	branchC := "feature/post-creation-rename"
	existingRef := "refs/heads/" + branchB
	targets.restoreErr = &serverapi.WorkflowTaskInitialBranchError{
		Reason:     serverapi.WorkflowTaskInitialBranchErrorReasonPostCreationMismatch,
		BranchName: branchC, Ref: &existingRef, ExistingBranchName: &branchB,
	}
	mismatchPreflight, err := service.preflightInitiatingActionTarget(ctx, workflow.TaskID(task.Task.ID), nil, &branchC)
	if err != nil {
		t.Fatalf("mismatched post-creation preflight: %v", err)
	}
	_, err = service.initiatingActionTarget(
		ctx,
		workflow.TaskID(task.Task.ID),
		serverapi.NewWorktreeSetupOperationID(),
		mismatchPreflight,
	)
	var mismatch *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &mismatch) ||
		mismatch.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonPostCreationMismatch {
		t.Fatalf("mismatched post-creation assertion error = %T %v", err, err)
	}
}

func TestServiceManualMoveCarriesBranchAssertionAndDoesNotApplyOnMismatch(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	execution := newManualMoveExecutionStub(service)
	service.currentNodeExecution = execution
	requestedRef := "HEAD"
	commitOID := strings.Repeat("d", 40)
	branchName := "feature/assertion-b"
	existingBranch := "feature/assertion-a"
	ref := "refs/heads/" + existingBranch
	targets := &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requestedRef,
			CommitOID: &commitOID, Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		materializeErr: &serverapi.WorkflowTaskInitialBranchError{
			Reason:     serverapi.WorkflowTaskInitialBranchErrorReasonPostCreationMismatch,
			BranchName: branchName, Ref: &ref, ExistingBranchName: &existingBranch,
		},
	}
	service.executionTargets = targets

	_, err = service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID: task.Task.ID, TargetNodeID: workflowServiceNodeIDByKey(t, definition.Definition, "plan"),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(), BranchName: &branchName,
	})
	var mismatch *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &mismatch) ||
		mismatch.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonPostCreationMismatch {
		t.Fatalf("MoveWorkflowTask error = %T %v, want post-creation mismatch", err, err)
	}
	if targets.materializeRequest.InitialBranchAssertion == nil ||
		*targets.materializeRequest.InitialBranchAssertion != branchName {
		t.Fatalf("materialization assertion = %v, want %q", targets.materializeRequest.InitialBranchAssertion, branchName)
	}
	if len(execution.interruptTaskIDs) != 0 || len(execution.started) != 0 {
		t.Fatalf("move mutated lifecycle after mismatch: interrupts=%v starts=%v", execution.interruptTaskIDs, execution.started)
	}
}

func TestServiceManualMoveCreatesFirstWorktreeFromExplicitPendingBranch(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	taskID := workflow.TaskID(task.Task.ID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	execution := newManualMoveExecutionStub(service)
	service.currentNodeExecution = execution
	requestedRef := "HEAD"
	commitOID := strings.Repeat("6", 40)
	branchName := "feature/manual-first-worktree"
	worktreeID := "worktree-" + task.Task.ID
	worktreeRoot := filepath.Join(t.TempDir(), "task-worktree")
	materializedBranch := ""
	targets := &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requestedRef,
			CommitOID: &commitOID, Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
	}
	targets.materialize = func(materializedTaskID workflow.TaskID) (ExecutionTargetMaterialization, error) {
		targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, materializedTaskID)
		if err != nil {
			return ExecutionTargetMaterialization{}, err
		}
		if targetContext.Task.PendingInitialManagedBranchName == nil {
			return ExecutionTargetMaterialization{}, errors.New("pending branch missing at materialization")
		}
		materializedBranch = *targetContext.Task.PendingInitialManagedBranchName
		if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
			ID:            worktreeID,
			WorkspaceID:   binding.WorkspaceID,
			CanonicalRoot: worktreeRoot,
			Managed:       true,
			CreatedBranch: true,
		}); err != nil {
			return ExecutionTargetMaterialization{}, err
		}
		updated, err := metadataStore.Queries().BindInitialTaskManagedWorktree(ctx, sqlitegen.BindInitialTaskManagedWorktreeParams{
			ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
			UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
			TaskID:            string(materializedTaskID),
		})
		if err != nil || updated != 1 {
			return ExecutionTargetMaterialization{}, errors.Join(err, errors.New("initial Worktree bind failed"))
		}
		return ExecutionTargetMaterialization{RetainedRoot: &workflowstore.ManagedExecutionRoot{
			WorktreeID: worktreeID,
			Root:       worktreeRoot,
		}}, nil
	}
	service.executionTargets = targets

	response, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:           task.Task.ID,
		TargetNodeID:     workflowServiceNodeIDByKey(t, definition.Definition, "plan"),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &branchName,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied ||
		response.Applied == nil ||
		len(response.Applied.CurrentNodes) != 1 {
		t.Fatalf("Move response = %+v, want applied first executable move", response)
	}
	if materializedBranch != branchName ||
		targets.initialBranchInspection.BranchName != branchName ||
		targets.materializeRequest.InitialBranchAssertion == nil ||
		*targets.materializeRequest.InitialBranchAssertion != branchName {
		t.Fatalf("branch handoff = materialized %q, inspection %+v, request %+v; want %q", materializedBranch, targets.initialBranchInspection, targets.materializeRequest, branchName)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil ||
		targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeHead ||
		targetContext.Task.ManagedWorktreeID != worktreeID ||
		targetContext.Task.PendingInitialManagedBranchName != nil {
		t.Fatalf("target context after move = %+v, want locked managed target with consumed pending branch", targetContext.Task)
	}
	if len(execution.interruptTaskIDs) != 1 || len(execution.started) != 1 {
		t.Fatalf("execution mutations = interrupts=%v starts=%v, want one each", execution.interruptTaskIDs, execution.started)
	}
}

func TestServiceManualMoveFinalBranchCollisionLeavesMoveUnappliedAndPendingBranch(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	taskID := workflow.TaskID(task.Task.ID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	before, err := service.store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	execution := newManualMoveExecutionStub(service)
	service.currentNodeExecution = execution
	requestedRef := "HEAD"
	commitOID := strings.Repeat("7", 40)
	branchName := "feature/move-final-collision"
	ref := "refs/heads/" + branchName
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requestedRef,
			CommitOID: &commitOID, Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		materializeErr: &serverapi.WorkflowTaskInitialBranchError{
			Reason:     serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision,
			BranchName: branchName,
			Ref:        &ref,
		},
	}

	_, err = service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:           task.Task.ID,
		TargetNodeID:     workflowServiceNodeIDByKey(t, definition.Definition, "plan"),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &branchName,
	})
	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonLocalCollision ||
		branchErr.BranchName != branchName {
		t.Fatalf("MoveWorkflowTask error = %T %v, want final local collision", err, err)
	}
	after, err := service.store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after collision: %v", err)
	}
	if len(after) != len(before) || len(after) != 1 || after[0].Reference != before[0].Reference {
		t.Fatalf("Current Nodes after collision = %+v, want unchanged %+v", after, before)
	}
	if len(execution.interruptTaskIDs) != 0 || len(execution.started) != 0 {
		t.Fatalf("execution mutations after collision: interrupts=%v starts=%v", execution.interruptTaskIDs, execution.started)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil ||
		targetContext.Task.ManagedWorktreeID != "" ||
		targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != branchName {
		t.Fatalf("target context after collision = %+v, want pending %q without target or Worktree", targetContext.Task, branchName)
	}
}
