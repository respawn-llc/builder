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
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
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
		BranchName: &branchName,
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

func TestServiceManualMoveAcceptedBranchReturnsConflictWhenFinalRevalidationBecomesNoOp(t *testing.T) {
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
	targetNodeID := workflow.NodeID(workflowServiceNodeIDByKey(t, definition.Definition, "plan"))
	execution := newManualMoveExecutionStub(service)
	execution.manualMoveAssignments = workflowServiceTestManualMoveAssignments(t, metadataStore)
	service.currentNodeExecution = execution
	requestedRef := "HEAD"
	commitOID := strings.Repeat("e", 40)
	branchName := "feature/stale-manual-move"
	worktreeID := "worktree-" + task.Task.ID
	worktreeRoot := filepath.Join(t.TempDir(), "task-worktree")
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
		if targetContext.Task.PendingInitialManagedBranchName == nil {
			return ExecutionTargetMaterialization{}, errors.New("pending initial managed branch is absent")
		}
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
		root := workflowstore.ManagedExecutionRoot{WorktreeID: worktreeID, Root: worktreeRoot}
		return ExecutionTargetMaterialization{RetainedRoot: &root}, nil
	}
	service.executionTargets = targets
	execution.interruptHook = func() {
		prepared, err := service.store.PrepareManualMove(ctx, workflowstore.ManualMoveRequest{
			TaskID: taskID, TargetNodeID: targetNodeID,
		})
		if err != nil {
			t.Errorf("prepare concurrent Manual Move: %v", err)
			return
		}
		targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
		if err != nil {
			t.Errorf("GetTaskExecutionTargetContext for concurrent Manual Move: %v", err)
			return
		}
		candidate := &workflowstore.ExecutionTargetCandidate{
			Snapshot: targets.resolution,
			Root: workflowstore.ExecutionRoot{
				SourceWorkspaceID:   targetContext.SourceWorkspaceID,
				SourceWorkspaceRoot: targetContext.SourceWorkspaceRoot,
				Managed: &workflowstore.ManagedExecutionRoot{
					WorktreeID: worktreeID,
					Root:       worktreeRoot,
				},
			},
		}
		moved, err := execution.ApplyManualMove(ctx, prepared, candidate)
		if err != nil {
			t.Errorf("apply concurrent Manual Move: %v", err)
			return
		}
		if moved.Outcome != workflowstore.ManualMoveResultOutcomeApplied {
			t.Errorf("concurrent Manual Move outcome = %q, want applied", moved.Outcome)
		}
	}

	response, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID: task.Task.ID, TargetNodeID: string(targetNodeID),
		BranchName: &branchName,
	})

	if !errors.Is(err, workflowexecution.ErrManualMoveLifecycleConflict) {
		t.Fatalf("MoveWorkflowTask error = %T %v, want Manual Move lifecycle conflict", err, err)
	}
	if response.Outcome != "" {
		t.Fatalf("MoveWorkflowTask response = %+v, want no successful outcome", response)
	}
	if materializedBranch != branchName {
		t.Fatalf("materialized branch = %q, want accepted branch %q", materializedBranch, branchName)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName != nil ||
		targetContext.Task.ManagedWorktreeID == nil ||
		*targetContext.Task.ManagedWorktreeID != worktreeID ||
		targetContext.Task.ExecutionTarget == nil {
		t.Fatalf("target context after concurrent move = %+v, want accepted branch lifecycle consumed by applied move", targetContext.Task)
	}
	if len(execution.interruptTaskIDs) != 0 {
		t.Fatalf("stale Manual Move interruptions = %v, want none", execution.interruptTaskIDs)
	}
}

func workflowServiceTestManualMoveAssignments(
	t *testing.T,
	metadataStore *metadata.Store,
) workflowstore.ManualMoveTargetAssignmentPreparer {
	t.Helper()
	return func(
		_ context.Context,
		inputs []workflowstore.CurrentNodeStartContext,
	) (workflowstore.ManualMoveTargetAssignmentPreparation, error) {
		assignments := make([]workflowstore.ManualMoveTargetAssignment, 0, len(inputs))
		for _, input := range inputs {
			if input.Node.Kind != workflow.NodeKindAgent {
				continue
			}
			if input.CurrentNode.SessionID != nil {
				assignments = append(assignments, workflowstore.ManualMoveTargetAssignment{
					CurrentNode: input.CurrentNode.Reference,
					SessionID:   *input.CurrentNode.SessionID,
				})
				continue
			}
			sessionStore, err := session.Create(
				filepath.Join(metadataStore.PersistenceRoot(), "projects", input.Task.ProjectID, "sessions"),
				filepath.Base(input.ExecutionRoot.SourceWorkspaceRoot),
				input.ExecutionRoot.SourceWorkspaceRoot,
				sessioncontract.SessionCategoryMain,
				metadataStore.AuthoritativeSessionStoreOptions()...,
			)
			if err != nil {
				return workflowstore.ManualMoveTargetAssignmentPreparation{}, err
			}
			if err := sessionStore.EnsureDurable(); err != nil {
				return workflowstore.ManualMoveTargetAssignmentPreparation{}, err
			}
			sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
			if err != nil {
				return workflowstore.ManualMoveTargetAssignmentPreparation{}, err
			}
			assignments = append(assignments, workflowstore.ManualMoveTargetAssignment{
				CurrentNode: input.CurrentNode.Reference,
				SessionID:   sessionID,
			})
		}
		return workflowstore.ManualMoveTargetAssignmentPreparation{Assignments: assignments}, nil
	}
}
