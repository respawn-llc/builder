package workflowsvc

import (
	"context"
	"errors"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
	"core/shared/workflowcontract"
)

type countingTaskDependencyReadModel struct {
	count              int
	unsatisfiedBlocker *int
}

func (r *countingTaskDependencyReadModel) GetTaskDependencies(context.Context, string) (serverapi.WorkflowTaskDependencies, error) {
	r.count++
	return serverapi.WorkflowTaskDependencies{}, errors.New("unexpected dependency projection")
}

func (r *countingTaskDependencyReadModel) CountUnsatisfiedBlockers(context.Context, string) (int, error) {
	r.count++
	if r.unsatisfiedBlocker != nil {
		return *r.unsatisfiedBlocker, nil
	}
	return 0, errors.New("unexpected dependency count")
}

func (r *countingTaskDependencyReadModel) ListTaskDependencies(context.Context, string, *serverapi.WorkflowTaskDependencyDirection) (serverapi.WorkflowTaskDependencyListResponse, error) {
	r.count++
	return serverapi.WorkflowTaskDependencyListResponse{}, errors.New("unexpected dependency list")
}

func TestStartDependencyPreflightWarnsBeforeExecutionTargetWorkAndProceedSkipsRecheck(t *testing.T) {
	ctx, service, projectID, workflowID, _ := newWorkflowServiceOrdinaryTaskFixture(t)
	blocker, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID: projectID, WorkflowID: &workflowID, Title: "blocker", LabelIDs: []string{},
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	blocked, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID: projectID, WorkflowID: &workflowID, Title: "blocked", LabelIDs: []string{},
	})
	if err != nil {
		t.Fatalf("create blocked: %v", err)
	}
	if _, err := service.AddWorkflowTaskDependency(ctx, serverapi.WorkflowTaskDependencyAddRequest{
		BlockerTaskID: blocker.Task.ID, BlockedTaskID: blocked.Task.ID,
	}); err != nil {
		t.Fatalf("add dependency: %v", err)
	}
	targets := &recordingExecutionTargetInfrastructure{}
	service.executionTargets = targets
	branchName := "feature/dependency-warning"

	warning, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           blocked.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &branchName,
	})
	if err != nil {
		t.Fatalf("start warning: %v", err)
	}
	if warning.Outcome != serverapi.WorkflowTaskActionOutcomeDependencyConfirmationRequired ||
		warning.UnsatisfiedDependencyCount == nil || *warning.UnsatisfiedDependencyCount != 1 {
		t.Fatalf("warning = %+v", warning)
	}
	if err := warning.Validate(); err != nil {
		t.Fatalf("warning Validate: %v", err)
	}
	if targets.resolveSelection != (workflowcontract.ExecutionTargetSelection{}) || targets.materializeTaskID != "" || targets.restoreTaskID != "" {
		t.Fatalf("target infrastructure used during warning: %+v", targets)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(blocked.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != blocked.Task.ShortID {
		t.Fatalf("pending branch after dependency warning = %v, want unchanged %q", targetContext.Task.PendingInitialManagedBranchName, blocked.Task.ShortID)
	}

	proceeded, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:                     blocked.Task.ID,
		SetupOperationID:           serverapi.NewWorktreeSetupOperationID(),
		ProceedDespiteDependencies: true,
	})
	if err != nil {
		t.Fatalf("proceeded start: %v", err)
	}
	if proceeded.Outcome == serverapi.WorkflowTaskActionOutcomeDependencyConfirmationRequired {
		t.Fatalf("proceeded start returned dependency warning: %+v", proceeded)
	}
}

func TestStartRequestValidationReturnsBeforeDependencyReader(t *testing.T) {
	ctx, service, _, _, _ := newWorkflowServiceOrdinaryTaskFixture(t)
	reader := &countingTaskDependencyReadModel{}
	service.readModels.TaskDependencies = reader
	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{}); err == nil {
		t.Fatal("invalid start request returned nil error")
	}
	if reader.count != 0 {
		t.Fatalf("dependency reader count = %d, want 0", reader.count)
	}
}

func TestStartDependencyPreflightReturnsBeforeTargetInfrastructure(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	unsatisfied := 1
	reader := &countingTaskDependencyReadModel{unsatisfiedBlocker: &unsatisfied}
	service.readModels.TaskDependencies = reader
	service.executionTargets = nil

	_, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           taskID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &workflowcontract.ExecutionTargetSelection{
			Mode: workflowcontract.ExecutionTargetModeHead,
		},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if reader.count != 1 {
		t.Fatalf("dependency reader count = %d, want 1", reader.count)
	}
}

func TestExecutableMoveWithProceedSkipsDependencyReaderBeforeTargetInfrastructurePreflight(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	reader := &countingTaskDependencyReadModel{}
	service.readModels.TaskDependencies = reader
	service.currentNodeExecution = newManualMoveExecutionStub(service)
	service.executionTargets = nil

	_, err = service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:                     task.Task.ID,
		TargetNodeID:               workflowServiceNodeIDByKey(t, definition.Definition, "plan"),
		ProceedDespiteDependencies: true,
		ExecutionTarget: &workflowcontract.ExecutionTargetSelection{
			Mode: workflowcontract.ExecutionTargetModeHead,
		},
	})
	if !errors.Is(err, errExecutionTargetInfrastructureRequired) {
		t.Fatalf("MoveWorkflowTask error = %v, want required target infrastructure", err)
	}
	if reader.count != 0 {
		t.Fatalf("dependency reader count = %d, want 0", reader.count)
	}
}

func TestExecutableMoveDependencyPreflightRequiresExplicitProceed(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
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
	targetNodeID := workflowServiceNodeIDByKey(t, definition.Definition, "plan")
	execution := newManualMoveExecutionStub(service)
	service.currentNodeExecution = execution
	targets := &recordingExecutionTargetInfrastructure{}
	service.executionTargets = targets

	warning, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:       blocked.Task.ID,
		TargetNodeID: targetNodeID,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask warning: %v", err)
	}
	if warning.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeDependencyConfirmationRequired ||
		warning.UnsatisfiedDependencyCount == nil || *warning.UnsatisfiedDependencyCount != 1 {
		t.Fatalf("warning = %+v", warning)
	}
	if err := warning.Validate(); err != nil {
		t.Fatalf("warning Validate: %v", err)
	}
	if len(execution.interruptTaskIDs) != 0 || len(execution.started) != 0 {
		t.Fatalf("execution used during dependency warning: interrupts=%v starts=%v", execution.interruptTaskIDs, execution.started)
	}
	if targets.resolveSelection != (workflowcontract.ExecutionTargetSelection{}) || targets.materializeTaskID != "" {
		t.Fatalf("target infrastructure used during dependency warning: %+v", targets)
	}

	proceeded, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:                     blocked.Task.ID,
		TargetNodeID:               targetNodeID,
		ExecutionTarget:            &workflowcontract.ExecutionTargetSelection{Mode: workflowcontract.ExecutionTargetModeNone},
		ProceedDespiteDependencies: true,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask proceeded: %v", err)
	}
	if proceeded.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied ||
		proceeded.Applied == nil || len(proceeded.Applied.CurrentNodes) != 1 {
		t.Fatalf("proceeded response = %+v", proceeded)
	}
	if proceeded.UnsatisfiedDependencyCount != nil {
		t.Fatalf("proceeded response retained dependency count: %+v", proceeded)
	}
}

func TestExecutableMoveWithProceedSkipsDependencyReaderBeforeTargetCompatibilityPreflight(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	reader := &countingTaskDependencyReadModel{}
	service.readModels.TaskDependencies = reader
	service.currentNodeExecution = newManualMoveExecutionStub(service)

	_, err = service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:                     task.Task.ID,
		TargetNodeID:               workflowServiceNodeIDByKey(t, definition.Definition, "implement"),
		ProceedDespiteDependencies: true,
		Values: map[string]map[string]string{
			"plan": {"prior_summary": "manual plan"},
		},
		ExecutionTarget: &workflowcontract.ExecutionTargetSelection{
			Mode: workflowcontract.ExecutionTargetModeNone,
		},
	})
	if !errors.Is(err, workflowstore.ErrExecutionTargetAlreadyLocked) {
		t.Fatalf("MoveWorkflowTask error = %v, want locked-target compatibility error", err)
	}
	if reader.count != 0 {
		t.Fatalf("dependency reader count = %d, want 0", reader.count)
	}
}
