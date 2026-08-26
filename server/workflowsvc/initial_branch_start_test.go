package workflowsvc

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/serverapi"
	"core/shared/workflowcontract"
)

func TestServiceTaskStartMaterializesLatestTaskScopedPendingBranch(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: workflowcontract.ExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	preparations := make(chan workflowexecution.TaskStartPreparation, 1)
	service.currentNodeExecution = &currentNodeCompletionExecutionStub{
		store: service.store, startPreparations: preparations,
	}
	requestedRef := "HEAD"
	commitOID := strings.Repeat("c", 40)
	branchA := "feature/request-a"
	branchB := "feature/request-b"
	materializedBranch := ""
	worktreeID := "worktree-" + task.Task.ID
	targets := &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode: workflowcontract.ExecutionTargetModeHead, RequestedRef: &requestedRef,
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
	preparation := <-preparations
	if err := preparation.Prepare(context.Background()); err != nil {
		t.Fatalf("start preparation: %v", err)
	}
	if err := preparation.Commit(context.Background()); err != nil {
		t.Fatalf("start preparation commit: %v", err)
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
}

func TestServiceTaskStartRestoresAlreadyLockedExecutionTarget(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: workflowcontract.ExecutionTargetModeDefaultBranch,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	taskID := workflow.TaskID(task.Task.ID)
	worktreeID := "worktree-" + task.Task.ID
	worktreeRoot := filepath.Join(t.TempDir(), "task-worktree")
	if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID: worktreeID, WorkspaceID: binding.WorkspaceID,
		CanonicalRoot: worktreeRoot, Managed: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	updated, err := metadataStore.Queries().BindInitialTaskManagedWorktree(ctx, sqlitegen.BindInitialTaskManagedWorktreeParams{
		ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
		UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
		TaskID:            task.Task.ID,
	})
	if err != nil {
		t.Fatalf("BindInitialTaskManagedWorktree: %v", err)
	}
	if updated != 1 {
		t.Fatalf("BindInitialTaskManagedWorktree updated %d rows, want 1", updated)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	requestedRef := "refs/remotes/origin/main"
	commitOID := strings.Repeat("d", 40)
	if err := service.store.LockTaskExecutionTarget(ctx, taskID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode: workflowcontract.ExecutionTargetModeDefaultBranch, RequestedRef: &requestedRef,
			CommitOID: &commitOID, Provenance: workflowstore.ExecutionTargetProvenanceResolved,
		},
		Root: workflowstore.ExecutionRoot{
			SourceWorkspaceID:   targetContext.SourceWorkspaceID,
			SourceWorkspaceRoot: targetContext.SourceWorkspaceRoot,
			Managed: &workflowstore.ManagedExecutionRoot{
				WorktreeID: worktreeID,
				Root:       worktreeRoot,
			},
		},
	}); err != nil {
		t.Fatalf("LockTaskExecutionTarget: %v", err)
	}
	preparations := make(chan workflowexecution.TaskStartPreparation, 1)
	service.currentNodeExecution = &currentNodeCompletionExecutionStub{
		store: service.store, startPreparations: preparations,
	}
	targets := &recordingExecutionTargetInfrastructure{}
	service.executionTargets = targets

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("StartWorkflowTask = %+v, %v; want placed task", response, err)
	}
	preparation := <-preparations
	if err := preparation.Prepare(ctx); err != nil {
		t.Fatalf("start preparation: %v", err)
	}
	if err := preparation.Commit(ctx); err != nil {
		t.Fatalf("start preparation commit: %v", err)
	}
	if targets.restoreTaskID != taskID {
		t.Fatalf("restored Task = %q, want %q", targets.restoreTaskID, taskID)
	}
	if targets.materializeTaskID != "" || targets.resolveSelection != (workflowcontract.ExecutionTargetSelection{}) {
		t.Fatalf("locked target was resolved or materialized again: %+v", targets)
	}
}
