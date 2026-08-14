package workflowsvc

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/sessionruntime"
	"core/server/workflow"
	"core/server/workflowexecution"
	"core/server/workflowstore"
	"core/server/worktree"
	"core/shared/serverapi"
	"core/shared/textutil"
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

func TestServiceConcurrentTaskResumeNoOpDoesNotReplacePendingBranch(t *testing.T) {
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
	firstBranch := "feature/first-resume"
	secondBranch := "feature/stale-resume"
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var releaseOnce sync.Once
	releaseMaterialization := func() {
		releaseOnce.Do(func() { close(release) })
	}
	t.Cleanup(releaseMaterialization)
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: textutil.Value("HEAD"),
			CommitOID:    textutil.Value(strings.Repeat("7", 40)),
			Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
		},
		materialize: func(workflow.TaskID) (ExecutionTargetMaterialization, error) {
			once.Do(func() { close(entered) })
			<-release
			return ExecutionTargetMaterialization{}, nil
		},
	}
	firstResponse := make(chan serverapi.WorkflowTaskResumeResponse, 1)
	firstErr := make(chan error, 1)
	go func() {
		response, resumeErr := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
			TaskID:           task.Task.ID,
			SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
			BranchName:       &firstBranch,
		})
		firstResponse <- response
		firstErr <- resumeErr
	}()
	<-entered
	if err := <-firstErr; err != nil {
		t.Fatalf("first ResumeWorkflowTask: %v", err)
	}
	if response := <-firstResponse; response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied {
		t.Fatalf("first ResumeWorkflowTask = %+v, want applied", response)
	}
	secondResponse := make(chan serverapi.WorkflowTaskResumeResponse, 1)
	secondErr := make(chan error, 1)
	go func() {
		response, resumeErr := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
			TaskID:           task.Task.ID,
			SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
			BranchName:       &secondBranch,
		})
		secondResponse <- response
		secondErr <- resumeErr
	}()
	if err := <-secondErr; err != nil {
		t.Fatalf("second ResumeWorkflowTask: %v", err)
	}
	if response := <-secondResponse; response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeNoOp {
		t.Fatalf("second ResumeWorkflowTask = %+v, want no-op", response)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != firstBranch {
		t.Fatalf(
			"pending branch after concurrent Resume = %v, want first branch %q",
			targetContext.Task.PendingInitialManagedBranchName,
			firstBranch,
		)
	}
	releaseMaterialization()
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
		targetContext.Task.ManagedWorktreeID != nil ||
		targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != branchName {
		t.Fatalf("target context after Resume collision = %+v, want unlocked without Worktree and pending %q", targetContext.Task, branchName)
	}
}

func TestServiceTaskResumePreflightsLockedBranchBeforeAsynchronousRestoration(t *testing.T) {
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
	reference := started.Mutation.Created[0].Reference
	if err := service.store.InterruptCurrentNode(
		ctx,
		reference,
		workflow.CurrentNodeInterruptionReason("test_resume"),
		workflow.CurrentNodeInterruptionDetail{Code: "test_resume"},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
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
	requestedRef := "HEAD"
	commitOID := strings.Repeat("8", 40)
	if err := service.store.LockTaskExecutionTarget(ctx, taskID, &workflowstore.ExecutionTargetCandidate{
		Snapshot: workflowstore.ExecutionTargetSnapshot{
			Mode: workflow.ExecutionTargetModeHead, RequestedRef: &requestedRef,
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
	restoreRequests := make(chan ExecutionTargetRestoreRequest, 1)
	targets := &recordingExecutionTargetInfrastructure{
		restoreRequests: restoreRequests,
		restoreErr: &worktree.LockedTaskWorktreeError{
			Cause: worktree.LockedTaskWorktreeCauseMissingBranch,
		},
	}
	service.executionTargets = targets

	existingBranchName := task.Task.ShortID
	requestedBranchName := "feature/attempted-rename"
	existingBranchRef := "refs/heads/" + existingBranchName
	targets.initialBranchAssertionErr = &serverapi.WorkflowTaskInitialBranchError{
		Reason:             serverapi.WorkflowTaskInitialBranchErrorReasonPostCreationMismatch,
		BranchName:         requestedBranchName,
		Ref:                &existingBranchRef,
		ExistingBranchName: &existingBranchName,
	}
	response, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &requestedBranchName,
	})
	var mismatch *serverapi.WorkflowTaskInitialBranchError
	if !errors.As(err, &mismatch) ||
		mismatch.Reason != serverapi.WorkflowTaskInitialBranchErrorReasonPostCreationMismatch ||
		mismatch.BranchName != requestedBranchName ||
		mismatch.ExistingBranchName == nil ||
		*mismatch.ExistingBranchName != existingBranchName {
		t.Fatalf("ResumeWorkflowTask mismatch error = %T %+v, want %q versus %q mismatch", err, err, requestedBranchName, existingBranchName)
	}
	if response.Outcome != "" || response.Applied != nil {
		t.Fatalf("Resume response = %+v, want unapplied branch mismatch", response)
	}
	if targets.initialBranchAssertions != 1 ||
		targets.initialBranchAssertion.TaskID != taskID ||
		targets.initialBranchAssertion.BranchName != requestedBranchName {
		t.Fatalf("initial branch assertions = %d %+v, want one preflight assertion", targets.initialBranchAssertions, targets.initialBranchAssertion)
	}
	select {
	case restored := <-restoreRequests:
		t.Fatalf("locked-target restoration queued before branch mismatch rejection: %+v", restored)
	default:
	}
	interruptedNodes, err := service.store.ListCurrentNodes(ctx, taskID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after mismatch: %v", err)
	}
	if len(interruptedNodes) != 1 ||
		interruptedNodes[0].Scheduling == nil ||
		interruptedNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
		t.Fatalf("Current Nodes after mismatch = %+v, want original interruption", interruptedNodes)
	}

	targets.initialBranchAssertionErr = nil
	response, err = service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("ResumeWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied ||
		response.Applied == nil ||
		len(response.Applied.CurrentNodes) != 1 ||
		response.Applied.CurrentNodes[0].NodeID != string(reference.NodeID) {
		t.Fatalf("Resume response = %+v, want applied before restoration", response)
	}
	select {
	case restored := <-restoreRequests:
		if restored.TaskID != taskID {
			t.Fatalf("restored Task = %q, want %q", restored.TaskID, taskID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("locked-target restoration was not queued")
	}
	var currentNodes []workflow.CurrentNode
	testsetup.RequireUntil(t, time.Now().Add(5*time.Second), 20*time.Millisecond, func() bool {
		var listErr error
		currentNodes, listErr = service.store.ListCurrentNodes(ctx, taskID)
		return listErr == nil &&
			len(currentNodes) == 1 &&
			currentNodes[0].Scheduling != nil &&
			currentNodes[0].Scheduling.State == workflow.CurrentNodeSchedulingInterrupted
	}, "locked-target restoration failure did not interrupt the requeued Current Node")
}
