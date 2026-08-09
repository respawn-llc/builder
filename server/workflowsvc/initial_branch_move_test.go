package workflowsvc

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

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
		branchErr.BranchName != branchName ||
		branchErr.Ref == nil ||
		*branchErr.Ref != ref {
		t.Fatalf("MoveWorkflowTask error = %T %+v, want final local collision for %q", err, err, ref)
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
