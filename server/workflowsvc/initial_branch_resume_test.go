package workflowsvc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
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
