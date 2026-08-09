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
	"core/server/session"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowruntime"
	"core/server/workflowstore"
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
