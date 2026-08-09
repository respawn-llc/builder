package workflowsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	branchName := "feature/no-op-move"

	_, err = service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:       task.Task.ID,
		TargetNodeID: string(currentNodes[0].Reference.NodeID),
		BranchName:   &branchName,
	})

	var branchErr *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &branchErr) ||
		branchErr.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonOperationCannotCreateWorktree {
		t.Fatalf("MoveWorkflowTask error = %T %v, want operation-cannot-create-Worktree", err, err)
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

func TestServiceManualMoveConcurrentNoOpDoesNotSelectRequestedBranch(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	execution := newManualMoveExecutionStub(service)
	execution.mutationPermit = service.mutationPermit
	service.currentNodeExecution = execution
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	targetNodeID := workflowServiceNodeIDByKey(t, definition.Definition, "plan")
	requestedRef := "HEAD"
	commitOID := strings.Repeat("e", 40)
	requestedBranch := "feature/stale-manual-move"
	worktreeID := "worktree-" + task.Task.ID
	worktreeRoot := filepath.Join(t.TempDir(), "task-worktree")
	materialized := make(chan struct{}, 1)
	materializedBranch := ""
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
		if targetContext.Task.ManagedWorktreeID == "" {
			materializedBranch = *targetContext.Task.PendingInitialManagedBranchName
			if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
				ID: worktreeID, WorkspaceID: binding.WorkspaceID,
				CanonicalRoot: worktreeRoot, Managed: true, CreatedBranch: true,
			}); err != nil {
				return ExecutionTargetMaterialization{}, err
			}
			updated, err := metadataStore.Queries().BindInitialTaskManagedWorktree(ctx, sqlitegen.BindInitialTaskManagedWorktreeParams{
				ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
				UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
				TaskID:            string(taskID),
			})
			if err != nil {
				return ExecutionTargetMaterialization{}, err
			}
			if updated != 1 {
				return ExecutionTargetMaterialization{}, errors.New("initial Worktree bind did not update the Task")
			}
			select {
			case materialized <- struct{}{}:
			default:
			}
		}
		root := workflowstore.ManagedExecutionRoot{WorktreeID: worktreeID, Root: worktreeRoot}
		return ExecutionTargetMaterialization{RetainedRoot: &root}, nil
	}
	service.executionTargets = targets

	permitReady := make(chan struct{})
	runConcurrentMove := make(chan struct{})
	concurrentMoveDone := make(chan error, 1)
	permitDone := make(chan error, 1)
	go func() {
		permitDone <- service.mutationPermit.Run(context.Background(), func(permitCtx context.Context) error {
			close(permitReady)
			<-runConcurrentMove
			response, err := service.MoveWorkflowTask(permitCtx, serverapi.WorkflowTaskMoveRequest{
				TaskID: task.Task.ID, TargetNodeID: targetNodeID,
				SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
			})
			if err == nil && (response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied || response.Applied == nil) {
				err = fmt.Errorf("concurrent Manual Move response = %+v, want applied", response)
			}
			concurrentMoveDone <- err
			return nil
		})
	}()
	<-permitReady

	type moveResult struct {
		response serverapi.WorkflowTaskMoveResponse
		err      error
	}
	staleMoveDone := make(chan moveResult, 1)
	go func() {
		response, err := service.MoveWorkflowTask(context.Background(), serverapi.WorkflowTaskMoveRequest{
			TaskID: task.Task.ID, TargetNodeID: targetNodeID,
			SetupOperationID: serverapi.NewWorktreeSetupOperationID(), BranchName: &requestedBranch,
		})
		staleMoveDone <- moveResult{response: response, err: err}
	}()

	select {
	case <-materialized:
	case <-time.After(50 * time.Millisecond):
	}
	close(runConcurrentMove)
	if err := <-concurrentMoveDone; err != nil {
		t.Fatalf("concurrent MoveWorkflowTask: %v", err)
	}
	if err := <-permitDone; err != nil {
		t.Fatalf("hold workflow mutation permit: %v", err)
	}
	stale := <-staleMoveDone
	if stale.err != nil {
		t.Fatalf("stale MoveWorkflowTask: %v", stale.err)
	}
	if stale.response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeNoOp || stale.response.NoOp == nil {
		t.Fatalf("stale move response = %+v, want no-op", stale.response)
	}
	if materializedBranch != task.Task.ShortID {
		t.Fatalf("materialized branch = %q, want unchanged pending branch %q", materializedBranch, task.Task.ShortID)
	}
	if targets.initialBranchInspections != 1 {
		t.Fatalf("branch inspections = %d, want only the applied move's default branch inspection", targets.initialBranchInspections)
	}
}
