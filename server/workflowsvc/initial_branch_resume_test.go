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
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

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
