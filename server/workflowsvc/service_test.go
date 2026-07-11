package workflowsvc

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"core/prompts"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/requestmemo"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowattention"
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/server/workflowview"
	"core/server/worktree"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/serverapi"
)

func nextWorkflowProjectEvent(t *testing.T, sub serverapi.WorkflowProjectSubscription) serverapi.WorkflowProjectEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	event, err := sub.Next(ctx)
	if err != nil {
		t.Fatalf("subscription Next: %v", err)
	}
	return event
}

func waitForWorkflowServiceCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for workflow service condition")
}

func waitWorkflowProjectActions(t *testing.T, sub serverapi.WorkflowProjectSubscription, resource string, expected ...string) []serverapi.WorkflowProjectEvent {
	t.Helper()
	remaining := make(map[string]bool, len(expected))
	for _, action := range expected {
		remaining[action] = true
	}
	events := make([]serverapi.WorkflowProjectEvent, 0, len(expected))
	for attempts := 0; attempts < 10 && len(remaining) > 0; attempts++ {
		event := nextWorkflowProjectEvent(t, sub)
		events = append(events, event)
		if event.Resource == resource && remaining[event.Action] {
			delete(remaining, event.Action)
		}
	}
	if len(remaining) > 0 {
		t.Fatalf("events = %+v, missing actions %+v for resource %s", events, remaining, resource)
	}
	return events
}

func isWorkflowServiceRequestFieldError(err error, field string) bool {
	var validationErr serverapi.WorkflowRequestValidationError
	return errors.As(err, &validationErr) && validationErr.Field == field
}

func TestServiceCreatesValidatesLinksAndStartsDefaultWorkflowTask(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)

	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{
		Name:            "Workflow",
		ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyNone},
	})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	startID := workflowServiceNodeIDByKind(t, def.Definition, "start")
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	agentID := "node-agent"
	if _, err := service.AddWorkflowNode(ctx, serverapi.WorkflowNodeAddRequest{WorkflowID: created.Workflow.ID, NodeID: agentID, Key: "agent", Kind: "agent", DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddWorkflowNode: %v", err)
	}
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: "group-start", SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup start: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-start", TransitionGroupID: "group-start", Key: "start", TargetNodeID: agentID, ContextMode: "new_session", PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddWorkflowEdge start: %v", err)
	}
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: "group-done", SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup done: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-done", TransitionGroupID: "group-done", Key: "done", TargetNodeID: doneID, ContextMode: "new_session"}); err != nil {
		t.Fatalf("AddWorkflowEdge done: %v", err)
	}
	validated, err := service.ValidateWorkflow(ctx, serverapi.WorkflowValidateRequest{WorkflowID: created.Workflow.ID, Mode: serverapi.WorkflowValidationModeExecution})
	if err != nil {
		t.Fatalf("ValidateWorkflow: %v", err)
	}
	if !validated.Valid || len(validated.Errors) != 0 {
		t.Fatalf("validated = %+v, want valid", validated)
	}
	for _, mode := range []serverapi.WorkflowValidationMode{serverapi.WorkflowValidationModeDraft, serverapi.WorkflowValidationModeTaskCreation, serverapi.WorkflowValidationModeExecution} {
		if _, err := service.ValidateWorkflow(ctx, serverapi.WorkflowValidateRequest{WorkflowID: created.Workflow.ID, Mode: mode}); err != nil {
			t.Fatalf("ValidateWorkflow mode %q: %v", mode, err)
		}
	}
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, created.Workflow.ID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	if !strings.HasPrefix(task.Task.ShortID, "WOR-1") || task.Task.WorkflowID != created.Workflow.ID {
		t.Fatalf("task response = %+v", task.Task)
	}
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	if started.RunID == "" || started.PlacementID == "" {
		t.Fatalf("start response = %+v", started)
	}
}

func TestServiceStartAskPolicyRequiresDurableSelectionWithoutMutatingTask(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	updated, err := service.UpdateWorkflow(ctx, serverapi.WorkflowUpdateRequest{
		WorkflowID:      workflowID,
		Name:            "Workflow",
		ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyAsk},
	})
	if err != nil {
		t.Fatalf("UpdateWorkflow: %v", err)
	}
	if updated.Definition.Workflow.ExecutionPolicy.Mode != serverapi.WorkflowExecutionPolicyAsk {
		t.Fatalf("updated workflow = %+v, want ask execution policy", updated.Definition.Workflow)
	}
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)

	result, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if result.Outcome != serverapi.WorkflowTaskInitiatingActionOutcomeSelectionRequired || result.SelectionRequired == nil {
		t.Fatalf("start result = %+v, want selection_required", result)
	}
	requirement := result.SelectionRequired
	if requirement.TaskID != task.Task.ID ||
		requirement.SourceWorkspaceID != binding.WorkspaceID ||
		requirement.Source.Kind != serverapi.WorkflowTaskExecutionTargetSourceNonGit ||
		requirement.ConfiguredPolicy.Mode != serverapi.WorkflowExecutionPolicyAsk {
		t.Fatalf("selection requirement = %+v", requirement)
	}
	if len(requirement.SupportedSelections) != 4 {
		t.Fatalf("supported selections = %+v, want all concrete modes", requirement.SupportedSelections)
	}

	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", err)
	}
	if negotiation == nil ||
		negotiation.Generation != requirement.Generation ||
		negotiation.Action.Kind != workflow.ExecutionTargetNegotiationActionStart {
		t.Fatalf("negotiation = %+v, want persisted start fence", negotiation)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target != nil {
		t.Fatalf("target = %+v, want no materialized target", target)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want no scheduled run", runs)
	}
	placements, err := service.store.ListPlacements(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 1 || placements[0].State != "active" {
		t.Fatalf("placements = %+v, want unchanged active start placement", placements)
	}

	retry, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask retry: %v", err)
	}
	if retry.SelectionRequired == nil || retry.SelectionRequired.Generation != requirement.Generation {
		t.Fatalf("retry = %+v, want same durable selection requirement", retry)
	}

	started, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:              task.Task.ID,
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
		SelectionGeneration: &requirement.Generation,
		Selection:           &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask selection: %v", err)
	}
	if started.Started == nil || started.Started.RunID == "" {
		t.Fatalf("selected start result = %+v, want started", started)
	}
	target, err = service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget after selected start: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyNone || target.State != workflow.ExecutionTargetStateLocked {
		t.Fatalf("target after selected start = %+v, want locked none target", target)
	}
	negotiation, err = service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation after selected start: %v", err)
	}
	if negotiation != nil {
		t.Fatalf("negotiation after selected start = %+v, want cleared", negotiation)
	}
}

func TestServiceStartAskPolicySupersedesSelectionWhenGitFactsChange(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	resolver := &recordingTaskExecutionTargetResolver{
		resolutions: []worktree.ExecutionTargetResolution{
			namedExecutionTargetResolution("refs/heads/main", "commit-one"),
			namedExecutionTargetResolution("refs/heads/main", "commit-two"),
		},
	}
	service.targetResolver = resolver

	first, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask first: %v", err)
	}
	second, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask second: %v", err)
	}
	if first.SelectionRequired == nil || second.SelectionRequired == nil ||
		first.SelectionRequired.Generation == second.SelectionRequired.Generation ||
		second.SelectionRequired.Source.Commit == nil || *second.SelectionRequired.Source.Commit != "commit-two" {
		t.Fatalf("selection results = first:%+v second:%+v, want superseding source snapshot", first, second)
	}
	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", err)
	}
	if negotiation == nil || negotiation.Generation != second.SelectionRequired.Generation || negotiation.Source.Commit == nil || *negotiation.Source.Commit != "commit-two" {
		t.Fatalf("negotiation = %+v, want latest durable source facts", negotiation)
	}
}

func TestServicePreservesLegacyTargetlessExecutionBehavior(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)

	t.Run("managed attachment uses legacy ensure without target materialization", func(t *testing.T) {
		task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
		attachWorkflowServiceLegacyManagedWorktree(t, ctx, metadataStore, binding, task.Task.ID)
		ensurer := &recordingTaskWorktreeEnsurer{}
		service.taskWorktrees = ensurer

		started, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
			TaskID: task.Task.ID, SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		})
		if err != nil {
			t.Fatalf("StartWorkflowTask: %v", err)
		}
		if started.Started == nil || ensurer.taskID != task.Task.ID {
			t.Fatalf("start = %+v, ensured task = %q, want legacy start", started, ensurer.taskID)
		}
		target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
		if err != nil {
			t.Fatalf("GetTaskExecutionTarget: %v", err)
		}
		if target != nil {
			t.Fatalf("target = %+v, want no inferred legacy target", target)
		}
	})

	t.Run("historical task without attachment fails as typed RPC error", func(t *testing.T) {
		task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
		if _, err := service.store.StartTask(ctx, workflow.TaskID(task.Task.ID)); err != nil {
			t.Fatalf("StartTask: %v", err)
		}
		resolver := &recordingTaskExecutionTargetResolver{
			resolutions: []worktree.ExecutionTargetResolution{namedExecutionTargetResolution("refs/heads/main", "new-policy-commit")},
		}
		service.targetResolver = resolver

		_, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
			TaskID: task.Task.ID, SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		})
		if !errors.Is(err, serverapi.ErrWorkflowTaskLegacyExecutionTargetMissing) {
			t.Fatalf("StartWorkflowTask error = %v, want typed legacy-missing error", err)
		}
		var legacyMissing *serverapi.WorkflowTaskLegacyExecutionTargetMissingError
		if !errors.As(err, &legacyMissing) || legacyMissing.TaskID != task.Task.ID {
			t.Fatalf("StartWorkflowTask error = %v, want task-scoped legacy-missing error", err)
		}
		if resolver.calls != 0 {
			t.Fatalf("target resolver calls = %d, want no policy resolution", resolver.calls)
		}
		target, targetErr := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
		if targetErr != nil {
			t.Fatalf("GetTaskExecutionTarget: %v", targetErr)
		}
		if target != nil {
			t.Fatalf("target = %+v, want no replacement target", target)
		}
	})

	t.Run("unstarted task resolves current workflow policy", func(t *testing.T) {
		task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
		resolver := &recordingTaskExecutionTargetResolver{
			resolutions: []worktree.ExecutionTargetResolution{namedExecutionTargetResolution("refs/heads/main", "current-policy-commit")},
		}
		materializer := &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir()}
		service.targetResolver = resolver
		service.targetWorktrees = materializer

		started, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
			TaskID: task.Task.ID, SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		})
		if err != nil {
			t.Fatalf("StartWorkflowTask: %v", err)
		}
		if started.Started == nil {
			t.Fatalf("start = %+v, want current-policy start", started)
		}
		target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
		if err != nil {
			t.Fatalf("GetTaskExecutionTarget: %v", err)
		}
		if target == nil || target.Policy != workflow.ExecutionPolicyHead || target.ResolvedSource == nil || target.ResolvedSource.Commit != "current-policy-commit" {
			t.Fatalf("target = %+v, want current HEAD policy target", target)
		}
	})
}

func TestServiceSelectedManagedStartReturnsConflictWhenNegotiationFenceChanges(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	resolver := &recordingTaskExecutionTargetResolver{
		resolutions: []worktree.ExecutionTargetResolution{
			namedExecutionTargetResolution("refs/heads/main", "commit-one"),
		},
	}
	materializer := &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir()}
	service.targetResolver = resolver
	service.targetWorktrees = materializer

	required, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask selection requirement: %v", err)
	}
	if required.SelectionRequired == nil {
		t.Fatalf("selection requirement = %+v, want selection_required", required)
	}
	original, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation before race: %v", err)
	}
	if original == nil {
		t.Fatal("GetTaskExecutionTargetNegotiation before race = nil, want negotiation")
	}
	replacement := *original
	replacement.Generation = "concurrent-negotiation-generation"
	hookRan := false
	resolver.hook = func() {
		if hookRan {
			return
		}
		hookRan = true
		if err := service.store.SaveTaskExecutionTargetNegotiation(ctx, replacement); err != nil {
			t.Fatalf("SaveTaskExecutionTargetNegotiation concurrent replacement: %v", err)
		}
	}

	result, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:              task.Task.ID,
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
		SelectionGeneration: &required.SelectionRequired.Generation,
		Selection:           &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionHead},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask selected race: %v", err)
	}
	if !hookRan || result.Outcome != serverapi.WorkflowTaskInitiatingActionOutcomeConflict || result.Conflict == nil || result.Conflict.TaskID != task.Task.ID {
		t.Fatalf("selected race result = %+v, want conflict for task", result)
	}
	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation after race: %v", err)
	}
	if negotiation == nil || negotiation.Generation != replacement.Generation {
		t.Fatalf("negotiation after race = %+v, want preserved replacement", negotiation)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget after race: %v", err)
	}
	if target != nil {
		t.Fatalf("target after race = %+v, want nil", target)
	}
	if materializer.provision.WorkspaceID != "" || materializer.setup.WorkspaceID != "" {
		t.Fatalf("materializer requests = provision:%+v setup:%+v, want no worktree or setup", materializer.provision, materializer.setup)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns after race: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs after race = %+v, want none", runs)
	}
	placements, err := service.store.ListPlacements(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListPlacements after race: %v", err)
	}
	if len(placements) != 1 || placements[0].State != "active" {
		t.Fatalf("placements after race = %+v, want unchanged start placement", placements)
	}
}

func TestServiceAskSelectionRequirementReturnsConflictWhenNegotiationAppears(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	prepared, err := service.store.PrepareTaskStartExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("PrepareTaskStartExecutionTargetNegotiation: %v", err)
	}
	namedRef := "refs/heads/main"
	commit := "commit-one"
	replacement := workflow.ExecutionTargetNegotiation{
		TaskID:            workflow.TaskID(task.Task.ID),
		Generation:        "concurrent-negotiation-generation",
		WorkflowID:        workflow.WorkflowID(workflowID),
		SourceWorkspaceID: binding.WorkspaceID,
		Source: workflow.ExecutionTargetNegotiationSource{
			Kind:     workflow.ExecutionTargetNegotiationSourceNamedRef,
			NamedRef: &namedRef,
			Commit:   &commit,
		},
		Action: prepared.Action,
	}
	hookRan := false
	service.targetResolver = &recordingTaskExecutionTargetResolver{
		resolutions: []worktree.ExecutionTargetResolution{
			namedExecutionTargetResolution(namedRef, commit),
		},
		hook: func() {
			if hookRan {
				return
			}
			hookRan = true
			if err := service.store.SaveTaskExecutionTargetNegotiation(ctx, replacement); err != nil {
				t.Fatalf("SaveTaskExecutionTargetNegotiation concurrent creation: %v", err)
			}
		},
	}

	result, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask raced selection requirement: %v", err)
	}
	if !hookRan || result.Outcome != serverapi.WorkflowTaskInitiatingActionOutcomeConflict || result.Conflict == nil || result.Conflict.TaskID != task.Task.ID {
		t.Fatalf("selection requirement race result = %+v, want conflict for task", result)
	}
	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation after race: %v", err)
	}
	if negotiation == nil || negotiation.Generation != replacement.Generation {
		t.Fatalf("negotiation after race = %+v, want preserved replacement", negotiation)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget after race: %v", err)
	}
	if target != nil {
		t.Fatalf("target after race = %+v, want nil", target)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns after race: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs after race = %+v, want none", runs)
	}
}

func TestServiceStartAskNoneSelectionWithMissingSourceScriptLeavesNoTargetOrRun(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceScriptWorkflow(t, ctx, service, "scripts/missing")
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)

	required, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask requirement: %v", err)
	}
	if required.SelectionRequired == nil {
		t.Fatalf("start result = %+v, want selection_required", required)
	}
	_, err = service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:              task.Task.ID,
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
		SelectionGeneration: &required.SelectionRequired.Generation,
		Selection:           &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone},
	})
	if err == nil {
		t.Fatal("StartWorkflowTask missing source script succeeded")
	}
	target, targetErr := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if targetErr != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", targetErr)
	}
	if target != nil {
		t.Fatalf("target = %+v, want no target after validation failure", target)
	}
	runs, runsErr := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if runsErr != nil {
		t.Fatalf("ListRuns: %v", runsErr)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want no run after validation failure", runs)
	}
	negotiation, negotiationErr := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if negotiationErr != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", negotiationErr)
	}
	if negotiation != nil {
		t.Fatalf("negotiation = %+v, want no durable selection after validation failure", negotiation)
	}
}

func TestServiceStartAskHeadSelectionMaterializesManagedTargetBeforeStarting(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
	}}
	materializer := &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir()}
	service.targetWorktrees = materializer

	required, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask requirement: %v", err)
	}
	if required.SelectionRequired == nil {
		t.Fatalf("start result = %+v, want selection_required", required)
	}
	started, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:              task.Task.ID,
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
		SelectionGeneration: &required.SelectionRequired.Generation,
		Selection:           &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionHead},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask selection: %v", err)
	}
	if started.Started == nil {
		t.Fatalf("start result = %+v, want started", started)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyHead || target.State != workflow.ExecutionTargetStateLocked || target.SetupState != workflow.ExecutionTargetSetupSucceeded || target.ActiveClaim != nil {
		t.Fatalf("target = %+v, want locked setup-complete head target", target)
	}
	canonicalWorktreeRoot, err := config.CanonicalWorkspaceRoot(materializer.worktreeRoot)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot: %v", err)
	}
	if materializer.provision.TaskShortID != task.Task.ShortID || materializer.provision.ResolvedCommit != "deadbeef" || materializer.setup.WorktreeRoot != canonicalWorktreeRoot {
		t.Fatalf("materializer calls = provision:%+v setup:%+v", materializer.provision, materializer.setup)
	}
}

func TestServiceStartHeadPolicyMaterializesManagedTargetBeforeStarting(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
	}}
	materializer := &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir()}
	service.targetWorktrees = materializer

	started, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if started.Started == nil {
		t.Fatalf("start result = %+v, want started", started)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyHead || target.SetupState != workflow.ExecutionTargetSetupSucceeded {
		t.Fatalf("target = %+v, want materialized head target", target)
	}
	if materializer.provision.ResolvedCommit != "deadbeef" {
		t.Fatalf("provision request = %+v, want fixed-head resolved commit", materializer.provision)
	}
}

func TestServiceStartExplicitSelectionOverridesFixedPolicy(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)

	started, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		Selection:        &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if started.Started == nil {
		t.Fatalf("start result = %+v, want started", started)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyNone ||
		target.State != workflow.ExecutionTargetStateLocked ||
		target.SetupState != workflow.ExecutionTargetSetupNotApplicable {
		t.Fatalf("target = %+v, want durable none override", target)
	}
}

func TestServiceStartHeadPolicySetupFailureLocksTargetWithoutStarting(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
	}}
	setupErr := errors.New("setup failed")
	materializer := &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir(), setupErr: setupErr}
	service.targetWorktrees = materializer

	_, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if !errors.Is(err, setupErr) {
		t.Fatalf("StartWorkflowTask error = %v, want setup failure", err)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.State != workflow.ExecutionTargetStateLocked || target.SetupState != workflow.ExecutionTargetSetupFailed || target.ActiveClaim != nil {
		t.Fatalf("target = %+v, want attached locked target with failed setup", target)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want no start run after setup failure", runs)
	}
}

func TestServiceStartHeadPolicyProvisionFailureQueuesRecoveryAndRetriesWithoutRestart(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
	}}
	provisionErr := errors.New("worktree provision failed")
	materializer := &recordingTaskExecutionTargetWorktreeMaterializer{
		provisionErr: provisionErr,
		worktreeRoot: t.TempDir(),
	}
	service.targetWorktrees = materializer

	_, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if !errors.Is(err, provisionErr) {
		t.Fatalf("StartWorkflowTask error = %v, want provision failure", err)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.State != workflow.ExecutionTargetStateInitialProvisioning ||
		target.SetupState != workflow.ExecutionTargetSetupPending ||
		target.ActiveClaim == nil || target.ActiveClaim.Phase != workflow.ExecutionTargetClaimRecoveryQueued {
		t.Fatalf("target = %+v, want queued initial materialization recovery", target)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want no start run after provision failure", runs)
	}
	materializer.provisionErr = nil
	coordinator, err := service.StartExecutionTargetRecovery(ctx)
	if err != nil {
		t.Fatalf("StartExecutionTargetRecovery: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := coordinator.Close(); closeErr != nil {
			t.Fatalf("ExecutionTargetRecoveryCoordinator.Close: %v", closeErr)
		}
	})
	waitForWorkflowServiceCondition(t, func() bool {
		recovered, getErr := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
		return getErr == nil && recovered == nil
	})
	retry, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask retry: %v", err)
	}
	if retry.Started == nil {
		t.Fatalf("retry result = %+v, want started after live recovery", retry)
	}
}

func TestServiceStartReturnsInProgressWhileManagedTargetMaterializes(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	provisioningGeneration := "provisioning-generation"
	claimGeneration := "claim-generation"
	intendedWorktreeRoot := t.TempDir()
	target := workflow.ExecutionTarget{
		TaskID: workflow.TaskID(task.Task.ID),
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:     workflow.ExecutionTargetSourceNamedRef,
			NamedRef: stringPtr("refs/heads/main"),
			Commit:   "deadbeef",
		},
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		IntendedWorktreeRoot:        &intendedWorktreeRoot,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &workflow.ExecutionTargetClaim{Generation: claimGeneration, Phase: workflow.ExecutionTargetClaimMaterializing},
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := service.store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}

	result, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if result.Outcome != serverapi.WorkflowTaskInitiatingActionOutcomeInProgress || result.InProgress == nil ||
		result.InProgress.TaskID != task.Task.ID ||
		result.InProgress.Phase != serverapi.WorkflowTaskExecutionTargetMaterializationPhaseMaterializing {
		t.Fatalf("start result = %+v, want materializing in_progress", result)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want no run while target materializes", runs)
	}
}

func TestServiceStartManualRecoveryRequiresSelectionThenReplacesTarget(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	manualTarget := saveWorkflowServiceManualRecoveryTarget(t, ctx, service, task.Task.ID)
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "selection-commit"),
	}}

	required, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask selection requirement: %v", err)
	}
	if required.Outcome != serverapi.WorkflowTaskInitiatingActionOutcomeSelectionRequired ||
		required.SelectionRequired == nil ||
		required.SelectionRequired.RecoveryCause == nil ||
		*required.SelectionRequired.RecoveryCause != serverapi.WorkflowTaskExecutionTargetRecoveryCause(*manualTarget.RecoveryCause) {
		t.Fatalf("selection requirement = %+v, want manual-recovery selection with cause %q", required, *manualTarget.RecoveryCause)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget before selection: %v", err)
	}
	if target == nil || target.Policy != manualTarget.Policy || target.ResolvedSource == nil || target.ResolvedSource.Commit != "manual-recovery-commit" {
		t.Fatalf("target before selection = %+v, want original manual-recovery target", target)
	}
	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation before selection: %v", err)
	}
	if negotiation == nil || negotiation.Generation != required.SelectionRequired.Generation {
		t.Fatalf("negotiation before selection = %+v, want persisted selection fence", negotiation)
	}

	started, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:              task.Task.ID,
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
		SelectionGeneration: &required.SelectionRequired.Generation,
		Selection:           &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask manual-recovery selection: %v", err)
	}
	if started.Outcome != serverapi.WorkflowTaskInitiatingActionOutcomeStarted || started.Started == nil {
		t.Fatalf("start result = %+v, want started", started)
	}
	target, err = service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget after selection: %v", err)
	}
	if target == nil ||
		target.Policy != workflow.ExecutionPolicyNone ||
		target.RecoveryDisposition != workflow.ExecutionTargetRecoveryAvailable ||
		target.RecoveryCause != nil {
		t.Fatalf("target after selection = %+v, want replacement none target", target)
	}
	negotiation, err = service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation after selection: %v", err)
	}
	if negotiation != nil {
		t.Fatalf("negotiation after selection = %+v, want cleared", negotiation)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns after selection: %v", err)
	}
	if len(runs) != 1 || string(runs[0].ID) != started.Started.RunID {
		t.Fatalf("runs after selection = %+v, want the started run", runs)
	}
}

func TestServiceStartManualRecoveryManagedSelectionReplacesTarget(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	saveWorkflowServiceManualRecoveryTarget(t, ctx, service, task.Task.ID)
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "replacement-commit"),
	}}
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir()}

	required, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask selection requirement: %v", err)
	}
	if required.SelectionRequired == nil {
		t.Fatalf("selection requirement = %+v, want manual-recovery selection", required)
	}
	started, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:              task.Task.ID,
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
		SelectionGeneration: &required.SelectionRequired.Generation,
		Selection:           &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionHead},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask managed manual-recovery selection: %v", err)
	}
	if started.Started == nil {
		t.Fatalf("start result = %+v, want started", started)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget after selection: %v", err)
	}
	if target == nil ||
		target.Policy != workflow.ExecutionPolicyHead ||
		target.ResolvedSource == nil ||
		target.ResolvedSource.Commit != "replacement-commit" ||
		target.RecoveryDisposition != workflow.ExecutionTargetRecoveryAvailable ||
		target.ActiveClaim != nil {
		t.Fatalf("target after selection = %+v, want locked managed replacement", target)
	}
	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation after selection: %v", err)
	}
	if negotiation != nil {
		t.Fatalf("negotiation after selection = %+v, want cleared", negotiation)
	}
}

func TestServiceStartManualRecoveryNoneValidationFailurePreservesTargetAndClearsSelection(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceScriptWorkflow(t, ctx, service, "scripts/missing")
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	manualTarget := saveWorkflowServiceManualRecoveryTarget(t, ctx, service, task.Task.ID)
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "selection-commit"),
	}}

	required, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask selection requirement: %v", err)
	}
	if required.SelectionRequired == nil {
		t.Fatalf("selection requirement = %+v, want manual-recovery selection", required)
	}
	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:              task.Task.ID,
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
		SelectionGeneration: &required.SelectionRequired.Generation,
		Selection:           &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone},
	}); err == nil {
		t.Fatal("StartWorkflowTask missing source script succeeded")
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget after validation failure: %v", err)
	}
	if target == nil ||
		target.Policy != manualTarget.Policy ||
		target.ResolvedSource == nil ||
		target.ResolvedSource.Commit != manualTarget.ResolvedSource.Commit ||
		target.RecoveryDisposition != workflow.ExecutionTargetRecoveryManualRecovery {
		t.Fatalf("target after validation failure = %+v, want original manual-recovery target", target)
	}
	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation after validation failure: %v", err)
	}
	if negotiation != nil {
		t.Fatalf("negotiation after validation failure = %+v, want cleared", negotiation)
	}
}

func TestExecutionTargetRecoveryCoordinatorClaimsThenRequeuesOnClose(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	provisioningGeneration := "recovery-provisioning"
	queuedClaim := workflow.ExecutionTargetClaim{Generation: "queued-claim", Phase: workflow.ExecutionTargetClaimRecoveryQueued}
	intendedWorktreeRoot := filepath.Join(t.TempDir(), "recovery-root")
	target := workflow.ExecutionTarget{
		TaskID: workflow.TaskID(task.Task.ID),
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:     workflow.ExecutionTargetSourceNamedRef,
			NamedRef: stringPtr("refs/heads/main"),
			Commit:   "deadbeef",
		},
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		IntendedWorktreeRoot:        &intendedWorktreeRoot,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &queuedClaim,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := service.store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
	inspectionStarted := make(chan struct{})
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{
		inspect: func(ctx context.Context, _ worktree.InspectExecutionTargetWorktreeRequest) (worktree.ExecutionTargetWorktreeInspection, error) {
			close(inspectionStarted)
			<-ctx.Done()
			return worktree.ExecutionTargetWorktreeInspection{}, ctx.Err()
		},
	}
	coordinator, err := service.StartExecutionTargetRecovery(ctx)
	if err != nil {
		t.Fatalf("StartExecutionTargetRecovery: %v", err)
	}
	select {
	case <-inspectionStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for recovery inspection")
	}
	claimed, err := service.store.GetTaskExecutionTarget(ctx, target.TaskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget claimed: %v", err)
	}
	if claimed == nil || claimed.ActiveClaim == nil ||
		claimed.ActiveClaim.Phase != workflow.ExecutionTargetClaimRecovering ||
		claimed.ActiveClaim.Generation == queuedClaim.Generation {
		t.Fatalf("claimed recovery target = %+v, want a fresh recovering claim", claimed)
	}
	if err := coordinator.Close(); err != nil {
		t.Fatalf("ExecutionTargetRecoveryCoordinator.Close: %v", err)
	}
	requeued, err := service.store.GetTaskExecutionTarget(ctx, target.TaskID)
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget requeued: %v", err)
	}
	if requeued == nil || requeued.ActiveClaim == nil ||
		requeued.ActiveClaim.Phase != workflow.ExecutionTargetClaimRecoveryQueued ||
		requeued.ActiveClaim.Generation == claimed.ActiveClaim.Generation {
		t.Fatalf("requeued recovery target = %+v, want a fresh queued claim", requeued)
	}
}

func TestExecutionTargetRecoveryCoordinatorContinuesAfterTargetLocalFailure(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	first := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	second := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	failedTask, recoveredTask := first, second
	if first.Task.ID > second.Task.ID {
		failedTask, recoveredTask = second, first
	}
	failedWorktreeRoot := filepath.Join(t.TempDir(), "failed-recovery-root")
	failedBranchTip := "queued-recovery-commit"
	failedClaim := workflow.ExecutionTargetClaim{
		Generation: "queued-recovery-claim-" + failedTask.Task.ID,
		Phase:      workflow.ExecutionTargetClaimRecoveryQueued,
	}
	failedGeneration := "queued-recovery-provisioning"
	failedTarget := workflow.ExecutionTarget{
		TaskID: workflow.TaskID(failedTask.Task.ID),
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:     workflow.ExecutionTargetSourceNamedRef,
			NamedRef: stringPtr("refs/heads/main"),
			Commit:   failedBranchTip,
		},
		State:                       workflow.ExecutionTargetStateLocked,
		ProvisioningGeneration:      &failedGeneration,
		SetupProvisioningGeneration: &failedGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &failedClaim,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
		ExactBranchObservation:      &failedBranchTip,
		LinkedWorktreeOwnership: &workflow.ExecutionTargetLinkedWorktreeOwnership{
			CommonDir:  "/test/common-dir",
			AdminEntry: "worktrees/" + failedTask.Task.ShortID,
			GitDir:     filepath.Join(failedWorktreeRoot, ".git"),
			HeadRef:    "refs/heads/" + failedTask.Task.ShortID,
		},
	}
	if err := service.store.SaveTaskExecutionTarget(ctx, failedTarget); err != nil {
		t.Fatalf("SaveTaskExecutionTarget failed target: %v", err)
	}
	if _, err := service.store.AttachManagedExecutionTargetWorktree(ctx, workflowstore.AttachManagedExecutionTargetWorktreeRequest{
		Target:        failedTarget,
		ExpectedClaim: failedClaim,
		WorkspaceID:   binding.WorkspaceID,
		WorktreeRoot:  failedWorktreeRoot,
		CreatedBranch: true,
	}); err != nil {
		t.Fatalf("AttachManagedExecutionTargetWorktree failed target: %v", err)
	}
	saveQueuedInitialRecoveryTarget(t, ctx, service, recoveredTask.Task.ID)
	recoveryFailure := errors.New("transient recovery failure")
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{
		provisionErr: recoveryFailure,
		worktreeRoot: failedWorktreeRoot,
		inspect: func(_ context.Context, req worktree.InspectExecutionTargetWorktreeRequest) (worktree.ExecutionTargetWorktreeInspection, error) {
			if req.TaskShortID == failedTask.Task.ShortID {
				return worktree.ExecutionTargetWorktreeInspection{
					Kind:                    worktree.ExecutionTargetWorktreeInspectionExactMissingRoot,
					BranchName:              failedTask.Task.ShortID,
					ExactBranchObservation:  failedBranchTip,
					LinkedWorktreeOwnership: failedTarget.LinkedWorktreeOwnership,
				}, nil
			}
			return worktree.ExecutionTargetWorktreeInspection{Kind: worktree.ExecutionTargetWorktreeInspectionNoSideEffects}, nil
		},
	}
	coordinator, err := service.StartExecutionTargetRecovery(ctx)
	if err != nil {
		t.Fatalf("StartExecutionTargetRecovery: %v", err)
	}
	waitForWorkflowServiceCondition(t, func() bool {
		recovered, getErr := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(recoveredTask.Task.ID))
		return getErr == nil && recovered == nil
	})
	if closeErr := coordinator.Close(); closeErr != nil {
		t.Fatalf("ExecutionTargetRecoveryCoordinator.Close: %v", closeErr)
	}
	failed, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(failedTask.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget failed target: %v", err)
	}
	if failed == nil || failed.ActiveClaim == nil ||
		failed.ActiveClaim.Phase != workflow.ExecutionTargetClaimRecoveryQueued {
		t.Fatalf("failed target = %+v, want requeued target after local failure", failed)
	}
}

func TestExecutionTargetRecoveryCoordinatorDeadlineAfterLateNoSideEffectsMarksManualAndContinues(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	tasks := []serverapi.WorkflowTaskCreateResponse{
		createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID),
		createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID),
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Task.ID < tasks[j].Task.ID })
	blockedTask, recoveredTask := tasks[0], tasks[1]
	saveQueuedInitialRecoveryTarget(t, ctx, service, blockedTask.Task.ID)
	saveQueuedInitialRecoveryTarget(t, ctx, service, recoveredTask.Task.ID)
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{
		inspect: func(ctx context.Context, req worktree.InspectExecutionTargetWorktreeRequest) (worktree.ExecutionTargetWorktreeInspection, error) {
			if req.TaskShortID != blockedTask.Task.ShortID {
				return worktree.ExecutionTargetWorktreeInspection{Kind: worktree.ExecutionTargetWorktreeInspectionNoSideEffects}, nil
			}
			<-ctx.Done()
			return worktree.ExecutionTargetWorktreeInspection{Kind: worktree.ExecutionTargetWorktreeInspectionNoSideEffects}, nil
		},
	}

	coordinator, err := service.startExecutionTargetRecovery(ctx, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("startExecutionTargetRecovery: %v", err)
	}
	defer func() {
		if closeErr := coordinator.Close(); closeErr != nil {
			t.Fatalf("ExecutionTargetRecoveryCoordinator.Close: %v", closeErr)
		}
	}()

	waitForWorkflowServiceCondition(t, func() bool {
		blocked, blockedErr := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(blockedTask.Task.ID))
		recovered, recoveredErr := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(recoveredTask.Task.ID))
		return blockedErr == nil &&
			blocked != nil &&
			blocked.ActiveClaim == nil &&
			blocked.RecoveryDisposition == workflow.ExecutionTargetRecoveryManualRecovery &&
			blocked.RecoveryCause != nil &&
			*blocked.RecoveryCause == workflow.ExecutionTargetRecoveryCauseDeadlineExceeded &&
			recoveredErr == nil &&
			recovered == nil
	})
}

func TestExecutionTargetRecoveryCoordinatorPagesPastTwoHundredRecoverableFailures(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)

	tasks := make([]serverapi.WorkflowTaskCreateResponse, 0, executionTargetRecoveryFencePageSize+1)
	for range executionTargetRecoveryFencePageSize + 1 {
		tasks = append(tasks, createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID))
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Task.ID < tasks[j].Task.ID })
	for _, task := range tasks[:executionTargetRecoveryFencePageSize] {
		saveQueuedLockedExecutionTargetRecovery(t, ctx, service, binding, task)
	}
	healthyTask := tasks[executionTargetRecoveryFencePageSize]
	saveQueuedInitialRecoveryTarget(t, ctx, service, healthyTask.Task.ID)

	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{
		provisionErr: errors.New("transient recovery failure"),
		inspect: func(_ context.Context, req worktree.InspectExecutionTargetWorktreeRequest) (worktree.ExecutionTargetWorktreeInspection, error) {
			if req.TaskShortID == healthyTask.Task.ShortID {
				return worktree.ExecutionTargetWorktreeInspection{Kind: worktree.ExecutionTargetWorktreeInspectionNoSideEffects}, nil
			}
			return worktree.ExecutionTargetWorktreeInspection{
				Kind:       worktree.ExecutionTargetWorktreeInspectionExactMissingRoot,
				BranchName: req.TaskShortID,
			}, nil
		},
	}
	coordinator, err := service.StartExecutionTargetRecovery(ctx)
	if err != nil {
		t.Fatalf("StartExecutionTargetRecovery: %v", err)
	}
	defer func() {
		if closeErr := coordinator.Close(); closeErr != nil {
			t.Fatalf("ExecutionTargetRecoveryCoordinator.Close: %v", closeErr)
		}
	}()
	waitForWorkflowServiceCondition(t, func() bool {
		target, getErr := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(healthyTask.Task.ID))
		return getErr == nil && target == nil
	})
}

func TestExecutionTargetRecoveryCoordinatorDeletesUnprovisionedInitialTarget(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	provisioningGeneration := "recovery-provisioning"
	queuedClaim := workflow.ExecutionTargetClaim{Generation: "queued-claim", Phase: workflow.ExecutionTargetClaimRecoveryQueued}
	intendedWorktreeRoot := filepath.Join(t.TempDir(), "recovery-root")
	target := workflow.ExecutionTarget{
		TaskID: workflow.TaskID(task.Task.ID),
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:     workflow.ExecutionTargetSourceNamedRef,
			NamedRef: stringPtr("refs/heads/main"),
			Commit:   "deadbeef",
		},
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		IntendedWorktreeRoot:        &intendedWorktreeRoot,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &queuedClaim,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := service.store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{}
	coordinator, err := service.StartExecutionTargetRecovery(ctx)
	if err != nil {
		t.Fatalf("StartExecutionTargetRecovery: %v", err)
	}
	defer func() {
		if err := coordinator.Close(); err != nil {
			t.Fatalf("ExecutionTargetRecoveryCoordinator.Close: %v", err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recovered, err := service.store.GetTaskExecutionTarget(ctx, target.TaskID)
		if err != nil {
			t.Fatalf("GetTaskExecutionTarget: %v", err)
		}
		if recovered == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered execution target = %+v, want deleted after no-side-effect inspection", recovered)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecutionTargetRecoveryCoordinatorAttachesExactInitialTargetAndRecoversSetup(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	provisioningGeneration := "recovery-provisioning"
	queuedClaim := workflow.ExecutionTargetClaim{Generation: "queued-claim", Phase: workflow.ExecutionTargetClaimRecoveryQueued}
	intendedWorktreeRoot := filepath.Join(t.TempDir(), "recovery-root")
	target := workflow.ExecutionTarget{
		TaskID: workflow.TaskID(task.Task.ID),
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:     workflow.ExecutionTargetSourceNamedRef,
			NamedRef: stringPtr("refs/heads/main"),
			Commit:   "deadbeef",
		},
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		IntendedWorktreeRoot:        &intendedWorktreeRoot,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &queuedClaim,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := service.store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
	materializer := &recordingTaskExecutionTargetWorktreeMaterializer{
		inspect: func(_ context.Context, _ worktree.InspectExecutionTargetWorktreeRequest) (worktree.ExecutionTargetWorktreeInspection, error) {
			return worktree.ExecutionTargetWorktreeInspection{
				Kind:                   worktree.ExecutionTargetWorktreeInspectionExact,
				BranchName:             task.Task.ShortID,
				ExactBranchObservation: "deadbeef",
				LinkedWorktreeOwnership: &workflow.ExecutionTargetLinkedWorktreeOwnership{
					CommonDir:  "/test/common-dir",
					AdminEntry: "worktrees/" + task.Task.ShortID,
					GitDir:     filepath.Join(intendedWorktreeRoot, ".git"),
					HeadRef:    "refs/heads/" + task.Task.ShortID,
				},
			}, nil
		},
	}
	service.targetWorktrees = materializer
	coordinator, err := service.StartExecutionTargetRecovery(ctx)
	if err != nil {
		t.Fatalf("StartExecutionTargetRecovery: %v", err)
	}
	defer func() {
		if err := coordinator.Close(); err != nil {
			t.Fatalf("ExecutionTargetRecoveryCoordinator.Close: %v", err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recovered, err := service.store.GetTaskExecutionTarget(ctx, target.TaskID)
		if err != nil {
			t.Fatalf("GetTaskExecutionTarget: %v", err)
		}
		if recovered != nil &&
			recovered.State == workflow.ExecutionTargetStateLocked &&
			recovered.IntendedWorktreeRoot == nil &&
			recovered.SetupState == workflow.ExecutionTargetSetupSucceeded &&
			recovered.ActiveClaim == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered execution target = %+v, want locked setup-complete target", recovered)
		}
		time.Sleep(10 * time.Millisecond)
	}
	root, err := service.store.ResolveTaskExecutionRoot(ctx, target.TaskID)
	if err != nil {
		t.Fatalf("ResolveTaskExecutionRoot: %v", err)
	}
	if root.ManagedWorktree == nil || root.ManagedWorktree.Root != intendedWorktreeRoot ||
		materializer.setup.WorktreeRoot != intendedWorktreeRoot ||
		materializer.setup.BranchName != task.Task.ShortID {
		t.Fatalf("recovered execution root/setup = root:%+v setup:%+v, want exact attached root and task branch", root, materializer.setup)
	}
}

func TestExecutionTargetRecoveryCoordinatorMarksAmbiguousInitialTargetForManualRecovery(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	provisioningGeneration := "recovery-provisioning"
	queuedClaim := workflow.ExecutionTargetClaim{Generation: "queued-claim", Phase: workflow.ExecutionTargetClaimRecoveryQueued}
	intendedWorktreeRoot := filepath.Join(t.TempDir(), "recovery-root")
	target := workflow.ExecutionTarget{
		TaskID: workflow.TaskID(task.Task.ID),
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:     workflow.ExecutionTargetSourceNamedRef,
			NamedRef: stringPtr("refs/heads/main"),
			Commit:   "deadbeef",
		},
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		IntendedWorktreeRoot:        &intendedWorktreeRoot,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &queuedClaim,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := service.store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{
		inspect: func(_ context.Context, _ worktree.InspectExecutionTargetWorktreeRequest) (worktree.ExecutionTargetWorktreeInspection, error) {
			return worktree.ExecutionTargetWorktreeInspection{Kind: worktree.ExecutionTargetWorktreeInspectionAmbiguous}, nil
		},
	}
	coordinator, err := service.StartExecutionTargetRecovery(ctx)
	if err != nil {
		t.Fatalf("StartExecutionTargetRecovery: %v", err)
	}
	defer func() {
		if err := coordinator.Close(); err != nil {
			t.Fatalf("ExecutionTargetRecoveryCoordinator.Close: %v", err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recovered, err := service.store.GetTaskExecutionTarget(ctx, target.TaskID)
		if err != nil {
			t.Fatalf("GetTaskExecutionTarget: %v", err)
		}
		if recovered != nil &&
			recovered.ActiveClaim == nil &&
			recovered.RecoveryDisposition == workflow.ExecutionTargetRecoveryManualRecovery &&
			recovered.RecoveryCause != nil &&
			*recovered.RecoveryCause == workflow.ExecutionTargetRecoveryCauseAmbiguousProvisioning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered execution target = %+v, want manual recovery", recovered)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecutionTargetRecoveryCoordinatorResumesPendingSetupForAttachedLockedTarget(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	provisioningGeneration := "recovery-provisioning"
	queuedClaim := workflow.ExecutionTargetClaim{Generation: "queued-claim", Phase: workflow.ExecutionTargetClaimRecoveryQueued}
	intendedWorktreeRoot := filepath.Join(t.TempDir(), "recovery-root")
	target := workflow.ExecutionTarget{
		TaskID: workflow.TaskID(task.Task.ID),
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:     workflow.ExecutionTargetSourceNamedRef,
			NamedRef: stringPtr("refs/heads/main"),
			Commit:   "deadbeef",
		},
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		IntendedWorktreeRoot:        &intendedWorktreeRoot,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &queuedClaim,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := service.store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
	locked := target
	locked.State = workflow.ExecutionTargetStateLocked
	locked.IntendedWorktreeRoot = nil
	attached, err := service.store.AttachManagedExecutionTargetWorktree(ctx, workflowstore.AttachManagedExecutionTargetWorktreeRequest{
		Target:        locked,
		ExpectedClaim: queuedClaim,
		WorkspaceID:   binding.WorkspaceID,
		WorktreeRoot:  intendedWorktreeRoot,
		CreatedBranch: true,
	})
	if err != nil {
		t.Fatalf("AttachManagedExecutionTargetWorktree: %v", err)
	}
	materializer := &recordingTaskExecutionTargetWorktreeMaterializer{
		inspect: func(_ context.Context, _ worktree.InspectExecutionTargetWorktreeRequest) (worktree.ExecutionTargetWorktreeInspection, error) {
			return worktree.ExecutionTargetWorktreeInspection{
				Kind:       worktree.ExecutionTargetWorktreeInspectionExact,
				BranchName: task.Task.ShortID,
			}, nil
		},
	}
	service.targetWorktrees = materializer
	coordinator, err := service.StartExecutionTargetRecovery(ctx)
	if err != nil {
		t.Fatalf("StartExecutionTargetRecovery: %v", err)
	}
	defer func() {
		if err := coordinator.Close(); err != nil {
			t.Fatalf("ExecutionTargetRecoveryCoordinator.Close: %v", err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recovered, err := service.store.GetTaskExecutionTarget(ctx, target.TaskID)
		if err != nil {
			t.Fatalf("GetTaskExecutionTarget: %v", err)
		}
		if recovered != nil &&
			recovered.State == workflow.ExecutionTargetStateLocked &&
			recovered.SetupState == workflow.ExecutionTargetSetupSucceeded &&
			recovered.ActiveClaim == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered execution target = %+v, want locked setup-complete target", recovered)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if materializer.setup.WorktreeRoot != attached.Root ||
		materializer.setup.WorktreeID != attached.ID ||
		materializer.setup.BranchName != task.Task.ShortID {
		t.Fatalf("recovered setup = %+v, want attached root and task branch", materializer.setup)
	}
}

func TestExecutionTargetRecoveryCoordinatorReprovisionsExactMissingLockedRoot(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	provisioningGeneration := "recovery-provisioning"
	queuedClaim := workflow.ExecutionTargetClaim{Generation: "queued-claim", Phase: workflow.ExecutionTargetClaimRecoveryQueued}
	worktreeRoot := filepath.Join(t.TempDir(), "recovery-root")
	branchTip := "deadbeef"
	target := workflow.ExecutionTarget{
		TaskID: workflow.TaskID(task.Task.ID),
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:     workflow.ExecutionTargetSourceNamedRef,
			NamedRef: stringPtr("refs/heads/main"),
			Commit:   branchTip,
		},
		State:                       workflow.ExecutionTargetStateLocked,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &queuedClaim,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
		ExactBranchObservation:      &branchTip,
		LinkedWorktreeOwnership: &workflow.ExecutionTargetLinkedWorktreeOwnership{
			CommonDir:  "/test/common-dir",
			AdminEntry: "worktrees/" + task.Task.ShortID,
			GitDir:     filepath.Join(worktreeRoot, ".git"),
			HeadRef:    "refs/heads/" + task.Task.ShortID,
		},
	}
	if err := service.store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
	attached, err := service.store.AttachManagedExecutionTargetWorktree(ctx, workflowstore.AttachManagedExecutionTargetWorktreeRequest{
		Target:        target,
		ExpectedClaim: queuedClaim,
		WorkspaceID:   binding.WorkspaceID,
		WorktreeRoot:  worktreeRoot,
		CreatedBranch: true,
	})
	if err != nil {
		t.Fatalf("AttachManagedExecutionTargetWorktree: %v", err)
	}
	materializer := &recordingTaskExecutionTargetWorktreeMaterializer{
		worktreeRoot: worktreeRoot,
		inspect: func(_ context.Context, _ worktree.InspectExecutionTargetWorktreeRequest) (worktree.ExecutionTargetWorktreeInspection, error) {
			return worktree.ExecutionTargetWorktreeInspection{
				Kind:                    worktree.ExecutionTargetWorktreeInspectionExactMissingRoot,
				BranchName:              task.Task.ShortID,
				ExactBranchObservation:  branchTip,
				LinkedWorktreeOwnership: target.LinkedWorktreeOwnership,
			}, nil
		},
	}
	service.targetWorktrees = materializer
	coordinator, err := service.StartExecutionTargetRecovery(ctx)
	if err != nil {
		t.Fatalf("StartExecutionTargetRecovery: %v", err)
	}
	defer func() {
		if err := coordinator.Close(); err != nil {
			t.Fatalf("ExecutionTargetRecoveryCoordinator.Close: %v", err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recovered, getErr := service.store.GetTaskExecutionTarget(ctx, target.TaskID)
		if getErr != nil {
			t.Fatalf("GetTaskExecutionTarget: %v", getErr)
		}
		if recovered != nil && recovered.State == workflow.ExecutionTargetStateLocked && recovered.ActiveClaim == nil && recovered.SetupState == workflow.ExecutionTargetSetupSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered execution target = %+v, want reprovisioned locked target", recovered)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if materializer.provision.WorktreeRoot != worktreeRoot || materializer.setup.WorktreeRoot != attached.Root {
		t.Fatalf("reprovision/setup = provision:%+v setup:%+v, want durable root", materializer.provision, materializer.setup)
	}
	root, err := service.store.ResolveTaskExecutionRoot(ctx, target.TaskID)
	if err != nil {
		t.Fatalf("ResolveTaskExecutionRoot: %v", err)
	}
	if root.ManagedWorktree == nil || root.ManagedWorktree.ID != attached.ID {
		t.Fatalf("reprovisioned execution root = %+v, want existing worktree record %q", root, attached.ID)
	}
}

func TestExecutionTargetRecoveryCoordinatorReprovisionsExpectedDetachment(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	provisioningGeneration := "reprovisioning-generation"
	queuedClaim := workflow.ExecutionTargetClaim{Generation: "queued-claim", Phase: workflow.ExecutionTargetClaimRecoveryQueued}
	worktreeRoot := filepath.Join(t.TempDir(), "recovery-root")
	branchTip := "deadbeef"
	target := workflow.ExecutionTarget{
		TaskID: workflow.TaskID(task.Task.ID), Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind: workflow.ExecutionTargetSourceNamedRef, NamedRef: stringPtr("refs/heads/main"), Commit: branchTip,
		},
		State: workflow.ExecutionTargetStateLockedReprovisioning, IntendedWorktreeRoot: &worktreeRoot,
		ProvisioningGeneration: &provisioningGeneration, SetupProvisioningGeneration: &provisioningGeneration,
		SetupState: workflow.ExecutionTargetSetupPending, ActiveClaim: &queuedClaim,
		RecoveryDisposition:    workflow.ExecutionTargetRecoveryAvailable,
		ExactBranchObservation: &branchTip, ExpectedDetachmentCommit: &branchTip,
		LinkedWorktreeOwnership: &workflow.ExecutionTargetLinkedWorktreeOwnership{
			CommonDir: "/test/common-dir", AdminEntry: "worktrees/" + task.Task.ShortID,
			GitDir: filepath.Join(worktreeRoot, ".git"), HeadRef: "refs/heads/" + task.Task.ShortID,
		},
	}
	if err := service.store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
	materializer := &recordingTaskExecutionTargetWorktreeMaterializer{
		worktreeRoot: worktreeRoot,
		inspect: func(_ context.Context, req worktree.InspectExecutionTargetWorktreeRequest) (worktree.ExecutionTargetWorktreeInspection, error) {
			if req.ExpectedDetachment == nil || *req.ExpectedDetachment != branchTip {
				t.Fatalf("inspection request = %+v, want expected detachment %q", req, branchTip)
			}
			return worktree.ExecutionTargetWorktreeInspection{
				Kind: worktree.ExecutionTargetWorktreeInspectionExactMissingRoot, BranchName: task.Task.ShortID, ExactBranchObservation: branchTip,
			}, nil
		},
	}
	service.targetWorktrees = materializer
	coordinator, err := service.StartExecutionTargetRecovery(ctx)
	if err != nil {
		t.Fatalf("StartExecutionTargetRecovery: %v", err)
	}
	defer func() {
		if err := coordinator.Close(); err != nil {
			t.Fatalf("ExecutionTargetRecoveryCoordinator.Close: %v", err)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		recovered, getErr := service.store.GetTaskExecutionTarget(ctx, target.TaskID)
		if getErr != nil {
			t.Fatalf("GetTaskExecutionTarget: %v", getErr)
		}
		if recovered != nil && recovered.State == workflow.ExecutionTargetStateLocked &&
			recovered.ActiveClaim == nil && recovered.SetupState == workflow.ExecutionTargetSetupSucceeded &&
			recovered.ExpectedDetachmentCommit == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovered execution target = %+v, want locked reprovisioned target", recovered)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if materializer.provision.WorktreeRoot != worktreeRoot || materializer.setup.WorktreeRoot != worktreeRoot {
		t.Fatalf("reprovision/setup = provision:%+v setup:%+v, want durable root", materializer.provision, materializer.setup)
	}
}

func TestServiceStartHeadPolicyMissingScriptKeepsTargetWithoutStarting(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceScriptWorkflow(t, ctx, service, "scripts/missing")
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
	}}
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir()}

	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	}); err == nil {
		t.Fatal("StartWorkflowTask missing managed script succeeded")
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.State != workflow.ExecutionTargetStateLocked || target.SetupState != workflow.ExecutionTargetSetupSucceeded || target.ActiveClaim != nil {
		t.Fatalf("target = %+v, want locked setup-complete target after script validation failure", target)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want no start run after script validation failure", runs)
	}
}

func TestServiceStartNonGitHeadPolicyRequiresReplacementSelection(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)

	required, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if required.SelectionRequired == nil ||
		required.SelectionRequired.ConfiguredPolicy.Mode != serverapi.WorkflowExecutionPolicyHead ||
		required.SelectionRequired.Source.Kind != serverapi.WorkflowTaskExecutionTargetSourceNonGit {
		t.Fatalf("start result = %+v, want a non-git replacement selection requirement", required)
	}

	started, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:              task.Task.ID,
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
		SelectionGeneration: &required.SelectionRequired.Generation,
		Selection:           &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask replacement selection: %v", err)
	}
	if started.Started == nil {
		t.Fatalf("start result = %+v, want started", started)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyNone {
		t.Fatalf("target = %+v, want a locked none replacement target", target)
	}
}

func TestServiceMoveHeadPolicyMaterializesManagedTargetBeforeApplyingMove(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
	}}
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir()}

	moved, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:           task.Task.ID,
		TargetNodeID:     workflowServiceNodeIDByKind(t, def.Definition, "agent"),
		AllowMissingEdge: true,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if moved.Moved == nil {
		t.Fatalf("move result = %+v, want moved", moved)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyHead || target.SetupState != workflow.ExecutionTargetSetupSucceeded {
		t.Fatalf("target = %+v, want materialized head target", target)
	}
}

func TestServiceMoveHeadPolicyMissingScriptKeepsTargetWithoutApplyingMove(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceScriptWorkflow(t, ctx, service, "scripts/missing")
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
	}}
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir()}

	if _, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:           task.Task.ID,
		TargetNodeID:     workflowServiceNodeIDByKind(t, def.Definition, "script"),
		AllowMissingEdge: true,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	}); err == nil {
		t.Fatal("MoveWorkflowTask missing managed script succeeded")
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyHead || target.SetupState != workflow.ExecutionTargetSetupSucceeded {
		t.Fatalf("target = %+v, want locked setup-complete target", target)
	}
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 0 {
		t.Fatalf("transitions = %+v, want no move after script validation failure", transitions)
	}
}

func TestServiceMoveHeadPolicySetupFailureLocksTargetWithoutApplyingMove(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	setupErr := errors.New("setup failed")
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
	}}
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir(), setupErr: setupErr}

	_, err = service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:           task.Task.ID,
		TargetNodeID:     workflowServiceNodeIDByKind(t, definition.Definition, "agent"),
		AllowMissingEdge: true,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if !errors.Is(err, setupErr) {
		t.Fatalf("MoveWorkflowTask error = %v, want setup failure", err)
	}
	assertWorkflowServiceManagedSetupFailureLeavesNoAction(t, ctx, service, task.Task.ID)
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 0 {
		t.Fatalf("transitions = %+v, want no applied move", transitions)
	}
}

func TestServiceApprovalHeadPolicyMaterializesManagedTargetBeforeApplyingApproval(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	pending, err := service.store.ManualMoveTask(ctx, workflowstore.ManualMoveRequest{
		TaskID:           workflow.TaskID(task.Task.ID),
		TargetNodeID:     workflow.NodeID(workflowServiceNodeIDByKind(t, def.Definition, "agent")),
		AllowMissingEdge: true,
	})
	if err != nil {
		t.Fatalf("ManualMoveTask setup: %v", err)
	}
	if pending.State != "pending_approval" {
		t.Fatalf("setup move = %+v, want pending approval", pending)
	}
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
	}}
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir()}

	approved, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		TaskTransitionID: string(pending.TransitionID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("ApproveWorkflowTask: %v", err)
	}
	if approved.Approved == nil || approved.Approved.State != "approved" {
		t.Fatalf("approval result = %+v, want approved", approved)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyHead || target.SetupState != workflow.ExecutionTargetSetupSucceeded {
		t.Fatalf("target = %+v, want materialized head target", target)
	}
}

func TestServiceApprovalHeadPolicySetupFailureLocksTargetWithoutApplyingApproval(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	pending, err := service.store.ManualMoveTask(ctx, workflowstore.ManualMoveRequest{
		TaskID:           workflow.TaskID(task.Task.ID),
		TargetNodeID:     workflow.NodeID(workflowServiceNodeIDByKind(t, definition.Definition, "agent")),
		AllowMissingEdge: true,
	})
	if err != nil {
		t.Fatalf("ManualMoveTask setup: %v", err)
	}
	if pending.State != "pending_approval" {
		t.Fatalf("setup move = %+v, want pending approval", pending)
	}
	setupErr := errors.New("setup failed")
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
	}}
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir(), setupErr: setupErr}

	_, err = service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		TaskTransitionID: string(pending.TransitionID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if !errors.Is(err, setupErr) {
		t.Fatalf("ApproveWorkflowTask error = %v, want setup failure", err)
	}
	assertWorkflowServiceManagedSetupFailureLeavesNoAction(t, ctx, service, task.Task.ID)
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 1 || transitions[0].ID != pending.TransitionID || transitions[0].State != "pending_approval" {
		t.Fatalf("transitions = %+v, want pending approval", transitions)
	}
}

func TestServiceApprovalHeadPolicyMissingScriptKeepsTargetWithoutApplyingApproval(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceScriptWorkflow(t, ctx, service, "scripts/missing")
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	pending, err := service.store.ManualMoveTask(ctx, workflowstore.ManualMoveRequest{
		TaskID:           workflow.TaskID(task.Task.ID),
		TargetNodeID:     workflow.NodeID(workflowServiceNodeIDByKind(t, def.Definition, "script")),
		AllowMissingEdge: true,
	})
	if err != nil {
		t.Fatalf("ManualMoveTask setup: %v", err)
	}
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
	}}
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir()}

	if _, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		TaskTransitionID: string(pending.TransitionID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	}); err == nil {
		t.Fatal("ApproveWorkflowTask missing managed script succeeded")
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyHead || target.SetupState != workflow.ExecutionTargetSetupSucceeded {
		t.Fatalf("target = %+v, want locked setup-complete target", target)
	}
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 1 || transitions[0].State != "pending_approval" {
		t.Fatalf("transitions = %+v, want pending approval after script validation failure", transitions)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want no approval run after script validation failure", runs)
	}
}

func TestServiceExecutableManualMoveAskPolicyRequiresSelectionWithoutMutatingTask(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	targetNodeID := workflowServiceNodeIDByKind(t, def.Definition, "agent")

	result, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:           task.Task.ID,
		TargetNodeID:     targetNodeID,
		AllowMissingEdge: true,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if result.Outcome != serverapi.WorkflowTaskInitiatingActionOutcomeSelectionRequired || result.SelectionRequired == nil {
		t.Fatalf("move result = %+v, want selection_required", result)
	}
	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", err)
	}
	if negotiation == nil ||
		negotiation.Action.Kind != workflow.ExecutionTargetNegotiationActionManualMove ||
		negotiation.Action.MoveTargetNodeID == nil ||
		*negotiation.Action.MoveTargetNodeID != workflow.NodeID(targetNodeID) {
		t.Fatalf("negotiation = %+v, want manual-move fence", negotiation)
	}
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 0 {
		t.Fatalf("transitions = %+v, want no applied move", transitions)
	}
}

func TestServiceExecutableManualMoveAskNoneSelectionLocksTargetAndAppliesMove(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	targetNodeID := workflowServiceNodeIDByKind(t, def.Definition, "agent")
	request := serverapi.WorkflowTaskMoveRequest{
		TaskID:           task.Task.ID,
		TargetNodeID:     targetNodeID,
		AllowMissingEdge: true,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	}

	required, err := service.MoveWorkflowTask(ctx, request)
	if err != nil {
		t.Fatalf("MoveWorkflowTask requirement: %v", err)
	}
	if required.SelectionRequired == nil {
		t.Fatalf("move result = %+v, want selection_required", required)
	}
	request.SelectionGeneration = &required.SelectionRequired.Generation
	request.Selection = &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone}

	moved, err := service.MoveWorkflowTask(ctx, request)
	if err != nil {
		t.Fatalf("MoveWorkflowTask selection: %v", err)
	}
	if moved.Outcome != serverapi.WorkflowTaskInitiatingActionOutcomeMoved || moved.Moved == nil {
		t.Fatalf("move result = %+v, want moved", moved)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyNone || target.State != workflow.ExecutionTargetStateLocked {
		t.Fatalf("target = %+v, want locked none target", target)
	}
	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", err)
	}
	if negotiation != nil {
		t.Fatalf("negotiation = %+v, want cleared after target lock", negotiation)
	}
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 1 || string(transitions[0].ID) != moved.Moved.TransitionID {
		t.Fatalf("transitions = %+v, want exactly the applied move", transitions)
	}
}

func TestServiceExecutableManualMoveManualRecoveryNoneSelectionReplacesTargetAndAppliesMove(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	saveWorkflowServiceManualRecoveryTarget(t, ctx, service, task.Task.ID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "selection-commit"),
	}}
	request := serverapi.WorkflowTaskMoveRequest{
		TaskID:           task.Task.ID,
		TargetNodeID:     workflowServiceNodeIDByKind(t, def.Definition, "agent"),
		AllowMissingEdge: true,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	}

	required, err := service.MoveWorkflowTask(ctx, request)
	if err != nil {
		t.Fatalf("MoveWorkflowTask selection requirement: %v", err)
	}
	if required.SelectionRequired == nil {
		t.Fatalf("move result = %+v, want selection_required", required)
	}
	request.SelectionGeneration = &required.SelectionRequired.Generation
	request.Selection = &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone}
	moved, err := service.MoveWorkflowTask(ctx, request)
	if err != nil {
		t.Fatalf("MoveWorkflowTask manual-recovery selection: %v", err)
	}
	if moved.Moved == nil {
		t.Fatalf("move result = %+v, want moved", moved)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget after selection: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyNone || target.RecoveryDisposition != workflow.ExecutionTargetRecoveryAvailable {
		t.Fatalf("target after selection = %+v, want none replacement", target)
	}
	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation after selection: %v", err)
	}
	if negotiation != nil {
		t.Fatalf("negotiation after selection = %+v, want cleared", negotiation)
	}
}

func TestServiceExecutableManualMoveAskNoneMissingSourceScriptLeavesNoTargetOrMove(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceScriptWorkflow(t, ctx, service, "scripts/missing")
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	request := serverapi.WorkflowTaskMoveRequest{
		TaskID:           task.Task.ID,
		TargetNodeID:     workflowServiceNodeIDByKind(t, def.Definition, "script"),
		AllowMissingEdge: true,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	}
	required, err := service.MoveWorkflowTask(ctx, request)
	if err != nil {
		t.Fatalf("MoveWorkflowTask requirement: %v", err)
	}
	if required.SelectionRequired == nil {
		t.Fatalf("move result = %+v, want selection_required", required)
	}
	request.SelectionGeneration = &required.SelectionRequired.Generation
	request.Selection = &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone}
	if _, err := service.MoveWorkflowTask(ctx, request); err == nil {
		t.Fatal("MoveWorkflowTask missing source script succeeded")
	}
	target, targetErr := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if targetErr != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", targetErr)
	}
	if target != nil {
		t.Fatalf("target = %+v, want no target after validation failure", target)
	}
	negotiation, negotiationErr := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if negotiationErr != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", negotiationErr)
	}
	if negotiation != nil {
		t.Fatalf("negotiation = %+v, want no durable selection after validation failure", negotiation)
	}
	transitions, transitionsErr := service.store.ListTransitions(ctx, workflow.TaskID(task.Task.ID))
	if transitionsErr != nil {
		t.Fatalf("ListTransitions: %v", transitionsErr)
	}
	if len(transitions) != 0 {
		t.Fatalf("transitions = %+v, want no applied move", transitions)
	}
}

func TestServiceExecutableApprovalAskPolicyRequiresSelectionWithoutApplyingApproval(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	targetNodeID := workflowServiceNodeIDByKind(t, def.Definition, "agent")
	moved, err := service.store.ManualMoveTask(ctx, workflowstore.ManualMoveRequest{
		TaskID:           workflow.TaskID(task.Task.ID),
		TargetNodeID:     workflow.NodeID(targetNodeID),
		AllowMissingEdge: true,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask setup: %v", err)
	}
	if moved.State != "pending_approval" {
		t.Fatalf("setup move = %+v, want pending approval", moved)
	}
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)

	result, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		TaskTransitionID: string(moved.TransitionID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("ApproveWorkflowTask: %v", err)
	}
	if result.Outcome != serverapi.WorkflowTaskInitiatingActionOutcomeSelectionRequired || result.SelectionRequired == nil {
		t.Fatalf("approval result = %+v, want selection_required", result)
	}
	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", err)
	}
	if negotiation == nil ||
		negotiation.Action.Kind != workflow.ExecutionTargetNegotiationActionApproval ||
		negotiation.Action.ApprovalTransitionID == nil ||
		*negotiation.Action.ApprovalTransitionID != moved.TransitionID {
		t.Fatalf("negotiation = %+v, want approval fence", negotiation)
	}
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 1 || transitions[0].State != "pending_approval" {
		t.Fatalf("transitions = %+v, want unchanged pending approval", transitions)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want no approval run", runs)
	}
}

func TestServiceExecutableApprovalAskNoneSelectionLocksTargetAndAppliesApproval(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	targetNodeID := workflowServiceNodeIDByKind(t, def.Definition, "agent")
	moved, err := service.store.ManualMoveTask(ctx, workflowstore.ManualMoveRequest{
		TaskID:           workflow.TaskID(task.Task.ID),
		TargetNodeID:     workflow.NodeID(targetNodeID),
		AllowMissingEdge: true,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask setup: %v", err)
	}
	if moved.State != "pending_approval" {
		t.Fatalf("setup move = %+v, want pending approval", moved)
	}
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	request := serverapi.WorkflowTaskApproveRequest{
		TaskTransitionID: string(moved.TransitionID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	}

	required, err := service.ApproveWorkflowTask(ctx, request)
	if err != nil {
		t.Fatalf("ApproveWorkflowTask requirement: %v", err)
	}
	if required.SelectionRequired == nil {
		t.Fatalf("approval result = %+v, want selection_required", required)
	}
	request.SelectionGeneration = &required.SelectionRequired.Generation
	request.Selection = &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone}

	approved, err := service.ApproveWorkflowTask(ctx, request)
	if err != nil {
		t.Fatalf("ApproveWorkflowTask selection: %v", err)
	}
	if approved.Outcome != serverapi.WorkflowTaskInitiatingActionOutcomeApproved || approved.Approved == nil || approved.Approved.State != "approved" {
		t.Fatalf("approval result = %+v, want approved", approved)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyNone || target.State != workflow.ExecutionTargetStateLocked {
		t.Fatalf("target = %+v, want locked none target", target)
	}
	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", err)
	}
	if negotiation != nil {
		t.Fatalf("negotiation = %+v, want cleared after target lock", negotiation)
	}
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 1 || transitions[0].State != "approved" || string(transitions[0].ID) != approved.Approved.TransitionID {
		t.Fatalf("transitions = %+v, want exactly the approved transition", transitions)
	}
}

func TestServiceExecutableApprovalManualRecoveryNoneSelectionReplacesTargetAndAppliesApproval(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	pending, err := service.store.ManualMoveTask(ctx, workflowstore.ManualMoveRequest{
		TaskID:           workflow.TaskID(task.Task.ID),
		TargetNodeID:     workflow.NodeID(workflowServiceNodeIDByKind(t, def.Definition, "agent")),
		AllowMissingEdge: true,
	})
	if err != nil {
		t.Fatalf("ManualMoveTask setup: %v", err)
	}
	saveWorkflowServiceManualRecoveryTarget(t, ctx, service, task.Task.ID)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "selection-commit"),
	}}
	request := serverapi.WorkflowTaskApproveRequest{
		TaskTransitionID: string(pending.TransitionID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	}

	required, err := service.ApproveWorkflowTask(ctx, request)
	if err != nil {
		t.Fatalf("ApproveWorkflowTask selection requirement: %v", err)
	}
	if required.SelectionRequired == nil {
		t.Fatalf("approval result = %+v, want selection_required", required)
	}
	request.SelectionGeneration = &required.SelectionRequired.Generation
	request.Selection = &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone}
	approved, err := service.ApproveWorkflowTask(ctx, request)
	if err != nil {
		t.Fatalf("ApproveWorkflowTask manual-recovery selection: %v", err)
	}
	if approved.Approved == nil || approved.Approved.State != "approved" {
		t.Fatalf("approval result = %+v, want approved", approved)
	}
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget after selection: %v", err)
	}
	if target == nil || target.Policy != workflow.ExecutionPolicyNone || target.RecoveryDisposition != workflow.ExecutionTargetRecoveryAvailable {
		t.Fatalf("target after selection = %+v, want none replacement", target)
	}
	negotiation, err := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation after selection: %v", err)
	}
	if negotiation != nil {
		t.Fatalf("negotiation after selection = %+v, want cleared", negotiation)
	}
}

func TestServiceExecutableApprovalAskNoneMissingSourceScriptLeavesNoTargetOrApproval(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceScriptWorkflow(t, ctx, service, "scripts/missing")
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	scriptNodeID := workflowServiceNodeIDByKind(t, def.Definition, "script")
	moved, err := service.store.ManualMoveTask(ctx, workflowstore.ManualMoveRequest{
		TaskID:           workflow.TaskID(task.Task.ID),
		TargetNodeID:     workflow.NodeID(scriptNodeID),
		AllowMissingEdge: true,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask setup: %v", err)
	}
	if moved.State != "pending_approval" {
		t.Fatalf("setup move = %+v, want pending approval", moved)
	}
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)
	required, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		TaskTransitionID: string(moved.TransitionID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("ApproveWorkflowTask requirement: %v", err)
	}
	if required.SelectionRequired == nil {
		t.Fatalf("approval result = %+v, want selection_required", required)
	}
	_, err = service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		TaskTransitionID:    string(moved.TransitionID),
		SetupOperationID:    serverapi.NewWorktreeSetupOperationID(),
		SelectionGeneration: &required.SelectionRequired.Generation,
		Selection:           &serverapi.WorkflowTaskExecutionTargetSelection{Mode: serverapi.WorkflowTaskExecutionTargetSelectionNone},
	})
	if err == nil {
		t.Fatal("ApproveWorkflowTask missing source script succeeded")
	}
	target, targetErr := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if targetErr != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", targetErr)
	}
	if target != nil {
		t.Fatalf("target = %+v, want no target after validation failure", target)
	}
	negotiation, negotiationErr := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if negotiationErr != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", negotiationErr)
	}
	if negotiation != nil {
		t.Fatalf("negotiation = %+v, want no durable selection after validation failure", negotiation)
	}
	transitions, transitionsErr := service.store.ListTransitions(ctx, workflow.TaskID(task.Task.ID))
	if transitionsErr != nil {
		t.Fatalf("ListTransitions: %v", transitionsErr)
	}
	if len(transitions) != 1 || transitions[0].State != "pending_approval" {
		t.Fatalf("transitions = %+v, want unchanged pending approval", transitions)
	}
	runs, runsErr := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if runsErr != nil {
		t.Fatalf("ListRuns: %v", runsErr)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want no approval run", runs)
	}
}

func TestServiceExecutableFanoutApprovalAskNoneMissingSourceScriptLeavesNoTargetOrApproval(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceFanoutScriptApprovalWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	pending := createWorkflowServicePendingFanoutApproval(t, ctx, service, task.Task.ID)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyAsk)

	_, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		TaskTransitionID: string(pending.TransitionID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if !errors.Is(err, serverapi.ErrWorkflowTaskLegacyExecutionTargetMissing) {
		t.Fatalf("ApproveWorkflowTask error = %v, want typed legacy-missing error", err)
	}
	assertWorkflowServicePendingFanoutApprovalUntouched(t, ctx, service, task.Task.ID, pending.TransitionID)
	target, targetErr := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if targetErr != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", targetErr)
	}
	if target != nil {
		t.Fatalf("target = %+v, want no target after legacy-missing rejection", target)
	}
	negotiation, negotiationErr := service.store.GetTaskExecutionTargetNegotiation(ctx, workflow.TaskID(task.Task.ID))
	if negotiationErr != nil {
		t.Fatalf("GetTaskExecutionTargetNegotiation: %v", negotiationErr)
	}
	if negotiation != nil {
		t.Fatalf("negotiation = %+v, want no negotiation after legacy-missing rejection", negotiation)
	}
}

func TestServiceExecutableFanoutApprovalHeadMissingScriptKeepsTargetWithoutApplyingApproval(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceFanoutScriptApprovalWorkflow(t, ctx, service)
	setWorkflowServiceExecutionPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionPolicyHead)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	pending := createWorkflowServicePendingFanoutApproval(t, ctx, service, task.Task.ID)
	service.targetResolver = &recordingTaskExecutionTargetResolver{resolutions: []worktree.ExecutionTargetResolution{
		namedExecutionTargetResolution("refs/heads/main", "deadbeef"),
	}}
	service.targetWorktrees = &recordingTaskExecutionTargetWorktreeMaterializer{worktreeRoot: t.TempDir()}

	_, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		TaskTransitionID: string(pending.TransitionID),
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if !errors.Is(err, serverapi.ErrWorkflowTaskLegacyExecutionTargetMissing) {
		t.Fatalf("ApproveWorkflowTask error = %v, want typed legacy-missing error", err)
	}
	assertWorkflowServicePendingFanoutApprovalUntouched(t, ctx, service, task.Task.ID, pending.TransitionID)
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target != nil {
		t.Fatalf("target = %+v, want no target after legacy-missing rejection", target)
	}
}

func assertWorkflowServiceManagedSetupFailureLeavesNoAction(t *testing.T, ctx context.Context, service *Service, taskID string) {
	t.Helper()
	target, err := service.store.GetTaskExecutionTarget(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTarget: %v", err)
	}
	if target == nil || target.State != workflow.ExecutionTargetStateLocked || target.SetupState != workflow.ExecutionTargetSetupFailed || target.ActiveClaim != nil {
		t.Fatalf("target = %+v, want locked target with failed setup and no claim", target)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want no runs after setup failure", runs)
	}
}

func assertWorkflowServicePendingFanoutApprovalUntouched(t *testing.T, ctx context.Context, service *Service, taskID string, transitionID workflow.TransitionID) {
	t.Helper()
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 2 || transitions[1].ID != transitionID || transitions[1].State != "pending_approval" {
		t.Fatalf("transitions = %+v, want unchanged pending fan-out approval", transitions)
	}
	edges, err := service.store.ListTransitionEdges(ctx, transitionID)
	if err != nil {
		t.Fatalf("ListTransitionEdges: %v", err)
	}
	if len(edges) != 2 || edges[0].State != "pending" || edges[0].TargetPlacementID != "" || edges[1].State != "pending" || edges[1].TargetPlacementID != "" {
		t.Fatalf("approval edges = %+v, want two untouched pending fan-out edges", edges)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %+v, want only the completed source run", runs)
	}
	placements, err := service.store.ListPlacements(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("ListPlacements: %v", err)
	}
	if len(placements) != 2 {
		t.Fatalf("placements = %+v, want no target placements after approval validation failure", placements)
	}
}

func TestServiceValidateWorkflowReportsScriptPathDiagnostics(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{
		Name:            "Script Workflow",
		ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyHead},
	})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	startID := workflowServiceNodeIDByKind(t, def.Definition, "start")
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	if _, err := service.AddWorkflowNode(ctx, serverapi.WorkflowNodeAddRequest{WorkflowID: created.Workflow.ID, NodeID: "node-script", Key: "script", Kind: "script", DisplayName: "Script", ScriptPath: stringPtr("scripts/run")}); err != nil {
		t.Fatalf("AddWorkflowNode script: %v", err)
	}
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: "group-start", SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup start: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-start", TransitionGroupID: "group-start", Key: "start", TargetNodeID: "node-script", ContextMode: "new_session"}); err != nil {
		t.Fatalf("AddWorkflowEdge start: %v", err)
	}
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: "group-done", SourceNodeID: "node-script", TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup done: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-done", TransitionGroupID: "group-done", Key: "done", TargetNodeID: doneID, ContextMode: "new_session"}); err != nil {
		t.Fatalf("AddWorkflowEdge done: %v", err)
	}

	validated, err := service.ValidateWorkflow(ctx, serverapi.WorkflowValidateRequest{WorkflowID: created.Workflow.ID, Mode: serverapi.WorkflowValidationModeExecution})
	if err != nil {
		t.Fatalf("ValidateWorkflow: %v", err)
	}
	if !validated.Valid {
		t.Fatalf("validation = %+v, want valid with skipped diagnostic", validated)
	}
	if len(validated.Errors) != 1 || validated.Errors[0].Code != workflowscript.CodeRelativePathSkipped || validated.Errors[0].BlocksContext {
		t.Fatalf("validation errors = %+v, want nonblocking relative-path skipped diagnostic", validated.Errors)
	}
}

func TestServiceValidateWorkflowScriptPathReportsMissingPath(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceWorkflowWithScriptNode(t, ctx, service, "node-script", "scripts/run")

	validated, err := service.ValidateWorkflowScriptPath(ctx, serverapi.WorkflowScriptPathValidateRequest{
		WorkflowID: workflowID,
		NodeID:     "node-script",
		ScriptPath: "",
	})
	if err != nil {
		t.Fatalf("ValidateWorkflowScriptPath: %v", err)
	}
	if validated.Valid || len(validated.Errors) != 1 {
		t.Fatalf("validation = %+v, want one blocking missing-path diagnostic", validated)
	}
	got := validated.Errors[0]
	if got.Code != workflowscript.CodeMissingPath || got.WorkflowID != workflowID || got.NodeID != "node-script" || !got.BlocksContext {
		t.Fatalf("validation error = %+v, want blocking missing-path diagnostic scoped to script node", got)
	}
}

func TestServiceValidateWorkflowScriptPathReportsRelativeCheckSkippedWithoutWorktree(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceWorkflowWithScriptNode(t, ctx, service, "node-script", "scripts/run")

	validated, err := service.ValidateWorkflowScriptPath(ctx, serverapi.WorkflowScriptPathValidateRequest{
		WorkflowID: workflowID,
		NodeID:     "node-script",
		ScriptPath: "scripts/run",
	})
	if err != nil {
		t.Fatalf("ValidateWorkflowScriptPath: %v", err)
	}
	if !validated.Valid || len(validated.Errors) != 1 {
		t.Fatalf("validation = %+v, want valid response with one skipped diagnostic", validated)
	}
	if got := validated.Errors[0]; got.Code != workflowscript.CodeRelativePathSkipped || got.BlocksContext {
		t.Fatalf("validation error = %+v, want nonblocking relative-path skipped diagnostic", got)
	}
}

func TestServiceValidateWorkflowScriptPathAcceptsExecutableAbsolutePath(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceWorkflowWithScriptNode(t, ctx, service, "node-script", "scripts/run")
	scriptPath := filepath.Join(t.TempDir(), "run")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write executable script: %v", err)
	}

	validated, err := service.ValidateWorkflowScriptPath(ctx, serverapi.WorkflowScriptPathValidateRequest{
		WorkflowID: workflowID,
		NodeID:     "node-script",
		ScriptPath: scriptPath,
	})
	if err != nil {
		t.Fatalf("ValidateWorkflowScriptPath: %v", err)
	}
	if !validated.Valid || len(validated.Errors) != 0 {
		t.Fatalf("validation = %+v, want valid absolute executable path", validated)
	}
}

func TestServiceValidateWorkflowScriptPathSupportsDraftOnlyNodeID(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)

	validated, err := service.ValidateWorkflowScriptPath(ctx, serverapi.WorkflowScriptPathValidateRequest{
		WorkflowID: workflowID,
		NodeID:     "draft-script-node",
		ScriptPath: "scripts/run",
	})
	if err != nil {
		t.Fatalf("ValidateWorkflowScriptPath: %v", err)
	}
	if len(validated.Errors) != 1 || validated.Errors[0].NodeID != "draft-script-node" {
		t.Fatalf("validation = %+v, want diagnostics scoped to draft node id", validated)
	}
}

func TestServiceWorkflowGraphSavePreservesScriptPath(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Script Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow initial: %v", err)
	}
	startID := workflowServiceNodeIDByKind(t, def.Definition, "start")
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	if _, err := service.AddWorkflowNode(ctx, serverapi.WorkflowNodeAddRequest{WorkflowID: created.Workflow.ID, NodeID: "node-script", Key: "script", Kind: "script", DisplayName: "Script", ScriptPath: stringPtr("scripts/run")}); err != nil {
		t.Fatalf("AddWorkflowNode script: %v", err)
	}
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: "group-start", SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup start: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-start", TransitionGroupID: "group-start", Key: "start", TargetNodeID: "node-script", ContextMode: "new_session"}); err != nil {
		t.Fatalf("AddWorkflowEdge start: %v", err)
	}
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: "group-done", SourceNodeID: "node-script", TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup done: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-done", TransitionGroupID: "group-done", Key: "done", TargetNodeID: doneID, ContextMode: "new_session"}); err != nil {
		t.Fatalf("AddWorkflowEdge done: %v", err)
	}
	source, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow source: %v", err)
	}
	graph := renameWorkflowGraphDraftNode(workflowGraphDraftFromDefinition(source.Definition), "node-script", "Script Renamed")

	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      created.Workflow.ID,
		ExpectedVersion: source.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	if !saved.Saved {
		t.Fatalf("save result = %+v, want saved graph", saved)
	}
	updated, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow updated: %v", err)
	}
	node := workflowServiceNodeByID(t, updated.Definition, "node-script")
	if node.ScriptPath == nil || *node.ScriptPath != "scripts/run" {
		t.Fatalf("script path = %#v, want scripts/run", node.ScriptPath)
	}
}

func TestServiceListWorkflowTasksValidatesAndDelegates(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)

	blankProjectID := " "
	if _, err := service.ListWorkflowTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: &blankProjectID}); !isWorkflowServiceRequestFieldError(err, "project_id") {
		t.Fatalf("blank project error = %#v, want project_id validation", err)
	}
	resp, err := service.ListWorkflowTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: &binding.ProjectID})
	if err != nil {
		t.Fatalf("ListWorkflowTasks: %v", err)
	}
	if resp.WorkflowID != workflowID || len(resp.Tasks) != 1 || resp.Tasks[0].TaskID != task.Task.ID {
		t.Fatalf("task list response = %+v, want workflow %s task %s", resp, workflowID, task.Task.ID)
	}
}

func TestServiceCreatesAndUpdatesTaskSourceWorkspaceBeforeStart(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	source, err := metadataStore.AttachWorkspaceToProject(ctx, binding.ProjectID, t.TempDir())
	if err != nil {
		t.Fatalf("AttachWorkspaceToProject source: %v", err)
	}
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	workflowSub, err := service.SubscribeWorkflow(ctx, serverapi.WorkflowSubscribeRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("SubscribeWorkflow: %v", err)
	}
	defer func() { _ = workflowSub.Close() }()

	created := createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{ProjectID: binding.ProjectID, Title: "Task", Body: "Details", SourceWorkspaceID: source.WorkspaceID})
	if created.Task.SourceWorkspaceID != source.WorkspaceID || created.Task.BodyPreview != "Details" {
		t.Fatalf("created task = %+v", created.Task)
	}
	title := "Updated"
	body := "Updated details"
	updated, err := service.UpdateWorkflowTask(ctx, serverapi.WorkflowTaskUpdateRequest{TaskID: created.Task.ID, Title: &title, Body: &body, SourceWorkspaceID: binding.WorkspaceID})
	if err != nil {
		t.Fatalf("UpdateWorkflowTask: %v", err)
	}
	if updated.Task.Title != "Updated" || updated.Task.SourceWorkspaceID != binding.WorkspaceID || updated.Task.BodyPreview != "Updated details" {
		t.Fatalf("updated task = %+v", updated.Task)
	}
	retitled := "Retitled"
	titleOnly, err := service.UpdateWorkflowTask(ctx, serverapi.WorkflowTaskUpdateRequest{TaskID: created.Task.ID, Title: &retitled})
	if err != nil {
		t.Fatalf("UpdateWorkflowTask title only: %v", err)
	}
	if titleOnly.Task.Title != "Retitled" || titleOnly.Task.SourceWorkspaceID != binding.WorkspaceID || titleOnly.Task.BodyPreview != "Updated details" {
		t.Fatalf("title-only update = %+v, want previous body/source workspace preserved", titleOnly.Task)
	}
	bodyOnly := "Body only details"
	bodyOnlyUpdate, err := service.UpdateWorkflowTask(ctx, serverapi.WorkflowTaskUpdateRequest{TaskID: created.Task.ID, Body: &bodyOnly})
	if err != nil {
		t.Fatalf("UpdateWorkflowTask body only: %v", err)
	}
	if bodyOnlyUpdate.Task.Title != "Retitled" || bodyOnlyUpdate.Task.SourceWorkspaceID != binding.WorkspaceID || bodyOnlyUpdate.Task.BodyPreview != "Body only details" {
		t.Fatalf("body-only update = %+v, want previous title/source workspace preserved", bodyOnlyUpdate.Task)
	}
	started := startWorkflowServiceTask(t, ctx, service, created.Task.ID)
	if started.RunID == "" {
		t.Fatalf("start response = %+v", started)
	}
	startedTitle := "Started title"
	startedBody := "Started details"
	startedUpdate, err := service.UpdateWorkflowTask(ctx, serverapi.WorkflowTaskUpdateRequest{TaskID: created.Task.ID, Title: &startedTitle, Body: &startedBody})
	if err != nil {
		t.Fatalf("UpdateWorkflowTask after start: %v", err)
	}
	if startedUpdate.Task.Title != "Started title" || startedUpdate.Task.BodyPreview != "Started details" || startedUpdate.Task.SourceWorkspaceID != binding.WorkspaceID {
		t.Fatalf("started update = %+v", startedUpdate.Task)
	}
	tooLate := "Too late"
	if _, err := service.UpdateWorkflowTask(ctx, serverapi.WorkflowTaskUpdateRequest{TaskID: created.Task.ID, Title: &tooLate, SourceWorkspaceID: source.WorkspaceID}); !errors.Is(err, workflowstore.ErrSourceWorkspaceAfterAutomation) {
		t.Fatalf("UpdateWorkflowTask source after start error = %v", err)
	}
	waitWorkflowProjectActions(t, sub, "task", "created", "updated", "started")
}

func TestServiceCommentMutationsUpdateActivityAndPublishInvalidations(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	added, err := service.AddWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentAddRequest{TaskID: task.Task.ID, Body: "first", Author: "user", AuthorID: "nek"})
	if err != nil {
		t.Fatalf("AddWorkflowTaskComment: %v", err)
	}
	if added.Comment.CreatedAtUnixMs == 0 || added.Comment.UpdatedAt == 0 {
		t.Fatalf("added comment missing timestamps: %+v", added.Comment)
	}
	if err := service.ReplaceWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentReplaceRequest{CommentID: added.Comment.ID, Body: "updated"}); err != nil {
		t.Fatalf("ReplaceWorkflowTaskComment: %v", err)
	}
	activity, err := service.ListWorkflowTaskActivity(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("ListWorkflowTaskActivity: %v", err)
	}
	if len(activity.Items) == 0 || activity.Items[0].Type != "comment" || activity.Items[0].Comment == nil || activity.Items[0].Comment.Body != "updated" {
		t.Fatalf("activity after replace = %+v", activity.Items)
	}
	if err := service.DeleteWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentDeleteRequest{CommentID: added.Comment.ID}); err != nil {
		t.Fatalf("DeleteWorkflowTaskComment: %v", err)
	}
	activity, err = service.ListWorkflowTaskActivity(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("ListWorkflowTaskActivity after delete: %v", err)
	}
	for _, item := range activity.Items {
		if item.Type == "comment" && item.Comment != nil && item.Comment.ID == added.Comment.ID {
			t.Fatalf("deleted comment visible in activity: %+v", activity.Items)
		}
	}
	waitWorkflowProjectActions(t, sub, "task", "comment_added", "comment_updated", "comment_deleted")
}

func TestServiceAnswersTaskQuestionWithoutControllerLease(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	task, _, sessionID := createWorkflowServiceWaitingAsk(t, ctx, service, metadataStore, binding, "Question", "session-task-question", "ask-task-question")
	responder := &recordingPromptResponder{}
	service.prompts = responder

	req := serverapi.WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-question", TaskID: task.Task.ID, AskID: "ask-task-question", FreeformAnswer: "ship it"}
	if err := service.AnswerWorkflowTaskQuestion(ctx, req); err != nil {
		t.Fatalf("AnswerWorkflowTaskQuestion: %v", err)
	}
	if responder.sessionID != sessionID || responder.response.RequestID != "ask-task-question" || responder.response.FreeformAnswer != "ship it" {
		t.Fatalf("prompt response = session:%q response:%+v", responder.sessionID, responder.response)
	}
	if err := service.AnswerWorkflowTaskQuestion(ctx, req); err != nil {
		t.Fatalf("AnswerWorkflowTaskQuestion replay: %v", err)
	}
	req.FreeformAnswer = "different"
	if err := service.AnswerWorkflowTaskQuestion(ctx, req); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("AnswerWorkflowTaskQuestion mismatch error = %v", err)
	}
	if err := service.AnswerWorkflowTaskQuestion(ctx, serverapi.WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-bad", TaskID: task.Task.ID, AskID: "missing", FreeformAnswer: "nope"}); !errors.Is(err, workflowstore.ErrTaskAskNotPending) {
		t.Fatalf("AnswerWorkflowTaskQuestion missing ask error = %v", err)
	}
}

func TestServiceAnswersTaskApprovalQuestionWithoutControllerLease(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	sessionID := "session-task-question"
	task, _, _ := createWorkflowServiceWaitingAsk(t, ctx, service, metadataStore, binding, "Approval question", sessionID, "ask-task-approval")
	responder := &recordingPromptResponder{}
	service.prompts = responder

	req := serverapi.WorkflowTaskQuestionAnswerRequest{
		ClientRequestID: "req-approval",
		TaskID:          task.Task.ID,
		AskID:           "ask-task-approval",
		Approval:        &serverapi.WorkflowTaskQuestionApprovalAnswer{Decision: clientui.ApprovalDecisionAllowSession, Commentary: "trusted"},
	}
	if err := service.AnswerWorkflowTaskQuestion(ctx, req); err != nil {
		t.Fatalf("AnswerWorkflowTaskQuestion approval: %v", err)
	}
	if responder.sessionID != sessionID || responder.response.RequestID != "ask-task-approval" || responder.response.Approval == nil || responder.response.Approval.Decision != askquestion.AskQuestionApprovalDecisionAllowSession || responder.response.Approval.Commentary != "trusted" {
		t.Fatalf("prompt response = session:%q response:%+v", responder.sessionID, responder.response)
	}
	if err := service.AnswerWorkflowTaskQuestion(ctx, req); err != nil {
		t.Fatalf("AnswerWorkflowTaskQuestion approval replay: %v", err)
	}
	req.Approval.Commentary = "different"
	if err := service.AnswerWorkflowTaskQuestion(ctx, req); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("AnswerWorkflowTaskQuestion approval mismatch error = %v", err)
	}
}

func TestServiceTaskStartValidatesCurrentGraph(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: workflowID, GroupID: "group-invalid", SourceNodeID: doneID, TransitionID: "invalid", DisplayName: "Invalid"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup invalid: %v", err)
	}
	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID}); err == nil {
		t.Fatalf("expected current graph validation error, got %v", err)
	} else {
		var validationErr workflowstore.WorkflowValidationError
		if !errors.As(err, &validationErr) || !validationErr.HasCode(workflow.CodeTerminalHasOutgoingEdge) {
			t.Fatalf("expected current graph validation error, got %v", err)
		}
	}
}

func TestServiceTaskStartNonePolicySkipsTaskWorktreeBeforeRun(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	ensurer := &recordingTaskWorktreeEnsurer{}
	service.taskWorktrees = ensurer
	setupID := serverapi.NewWorktreeSetupOperationID()
	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{SetupOperationID: setupID, TaskID: task.Task.ID}); err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if ensurer.taskID != "" {
		t.Fatalf("ensured task id = %q, want no worktree ensure for none policy", ensurer.taskID)
	}
}

func TestServiceAllowsInvalidDefaultBacklogButRejectsUnlinkedWorkflow(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	unlinked, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Unlinked"})
	if err != nil {
		t.Fatalf("CreateWorkflow unlinked: %v", err)
	}
	if _, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{ProjectID: binding.ProjectID, WorkflowID: unlinked.Workflow.ID, Title: "Task", Body: "Body"}); err == nil {
		t.Fatalf("expected unlinked workflow task create to fail")
	}
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, unlinked.Workflow.ID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID}); !errors.Is(err, workflowstore.ErrWorkflowValidationFailed) {
		t.Fatalf("expected invalid default workflow start error, got %v", err)
	}
}

func TestServiceStartTaskAutomationValidatesEnsuresWorktreeAndRecordsRunnableRun(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	attachWorkflowServiceLegacyManagedWorktree(t, ctx, metadataStore, binding, task.Task.ID)
	ensurer := &recordingTaskWorktreeEnsurer{hook: func(taskID string) {
		runs, err := service.store.ListRuns(ctx, workflow.TaskID(taskID))
		if err != nil {
			t.Fatalf("ListRuns during ensure: %v", err)
		}
		if len(runs) != 0 {
			t.Fatalf("worktree ensure happened after automation intent: %+v", runs)
		}
	}}
	service.taskWorktrees = ensurer

	started, err := service.StartTaskAutomation(ctx, task.Task.ID)
	if err != nil {
		t.Fatalf("StartTaskAutomation: %v", err)
	}
	if ensurer.taskID != task.Task.ID {
		t.Fatalf("ensured task id = %q, want %q", ensurer.taskID, task.Task.ID)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns after automation: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != workflow.RunID(started.RunID) || runs[0].AutomationRequestedAt == nil {
		t.Fatalf("runs after automation = %+v", runs)
	}
	notifier := &recordingSchedulerNotifier{}
	service.schedulerWake = notifier
	if _, err := service.StartTaskAutomation(ctx, task.Task.ID); err == nil {
		t.Fatalf("expected second start to fail")
	}
	if notifier.count != 0 {
		t.Fatalf("scheduler notified on failed start")
	}
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 1 || transitions[0].TransitionID != "start" {
		t.Fatalf("start transition not applied: %+v", transitions)
	}
}

func TestServiceStartTaskAutomationNotifiesScheduler(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	notifier := &recordingSchedulerNotifier{}
	service.schedulerWake = notifier

	startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	if notifier.count != 1 {
		t.Fatalf("scheduler notifications = %d, want 1", notifier.count)
	}
}

func TestServiceMoveTaskRejectsMissingEdgeExecutableOverride(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	implementID := workflowServiceNodeIDByKey(t, def.Definition, "implement")

	_, err = service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID, TargetNodeID: implementID, AllowMissingEdge: true, AutoApprove: true, OutputValues: map[string]string{"prior_summary": "replacement"}})
	if !errors.Is(err, workflowstore.ErrManualMoveExecutableTargetNeedsEdge) {
		t.Fatalf("MoveWorkflowTask error = %v, want executable edge requirement", err)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs after rejected override = %+v, want none", runs)
	}
}

func TestServiceMoveTaskAutoApproveRunsScriptFromNoneExecutionRoot(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceScriptWorkflow(t, ctx, service, "scripts/complete")
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	scriptID := workflowServiceNodeIDByKey(t, def.Definition, "script")
	workspace, err := metadataStore.GetWorkspaceByID(ctx, binding.WorkspaceID)
	if err != nil {
		t.Fatalf("GetWorkspaceByID: %v", err)
	}
	scriptPath := filepath.Join(workspace.CanonicalRootPath, "scripts", "complete")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatalf("create script dir: %v", err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nprintf '{}'\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	setupID := serverapi.NewWorktreeSetupOperationID()
	moveResult, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{SetupOperationID: setupID, TaskID: task.Task.ID, TargetNodeID: scriptID, AllowMissingEdge: true, AutoApprove: true})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if moveResult.Moved == nil {
		t.Fatalf("MoveWorkflowTask result = %+v, want moved", moveResult)
	}
	moved := *moveResult.Moved
	if moved.State != "approved" || len(moved.PlacementIDs) != 1 || len(moved.RunIDs) != 1 || moved.ApprovalError != "" {
		t.Fatalf("auto-approved script move = %+v, want approved placement and run", moved)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].InterruptedAt != nil {
		t.Fatalf("script runs = %+v, want one non-interrupted run", runs)
	}
}

func TestServiceCompleteWorkflowTaskFromAgentSessionCompletesWithoutSchedulerWake(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	sessionID := "session-agent-complete"
	claimAndAttachWorkflowServiceRun(t, ctx, service, metadataStore, binding, started.RunID, sessionID)
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	notifier := &recordingSchedulerNotifier{}
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.schedulerWake = notifier
	service.attentionFinalizer = finalizer

	completed, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
		AgentSessionID: sessionID,
		Commentary:     "finished",
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask: %v", err)
	}
	if completed.TaskID != task.Task.ID || completed.RunID != started.RunID || completed.State != "applied" {
		t.Fatalf("complete response = %+v", completed)
	}
	if notifier.count != 0 {
		t.Fatalf("agent completion scheduler notifications = %d, want 0", notifier.count)
	}
	if len(finalizer.results) != 1 || finalizer.results[0].TransitionID != workflow.TransitionID(completed.TransitionID) || finalizer.results[0].State != "applied" {
		t.Fatalf("attention finalizer results = %+v", finalizer.results)
	}
	event := nextWorkflowProjectEvent(t, sub)
	if event.ProjectID != binding.ProjectID || event.WorkflowID != workflowID || event.Resource != "task" || event.Action != "completed" {
		t.Fatalf("completion event = %+v, want single store-owned task completed event", event)
	}
	noEventCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if extra, extraErr := sub.Next(noEventCtx); extraErr == nil {
		t.Fatalf("unexpected duplicate completion event: %+v", extra)
	} else if !errors.Is(extraErr, context.DeadlineExceeded) {
		t.Fatalf("waiting for duplicate completion event returned %v, want deadline", extraErr)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].CompletedAt == nil {
		t.Fatalf("runs after completion = %+v, want completed source run", runs)
	}
}

func TestServiceCompleteWorkflowTaskMapsMissingActiveTarget(t *testing.T) {
	ctx, service, _, _ := newWorkflowServiceTestContextWithMetadata(t)

	_, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
		AgentSessionID: "session-without-run",
	})
	if !errors.Is(err, serverapi.ErrWorkflowTaskCompleteTargetNotFound) {
		t.Fatalf("CompleteWorkflowTask missing target error = %v, want ErrWorkflowTaskCompleteTargetNotFound", err)
	}
}

func TestServiceCompleteWorkflowTaskRejectsAgentCrossSessionSelector(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	claimAndAttachWorkflowServiceRun(t, ctx, service, metadataStore, binding, started.RunID, "session-owner")

	_, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
		AgentSessionID: "session-other",
		RunID:          started.RunID,
	})
	if err == nil || err.Error() != prompts.WorkflowTaskCompleteAgentOwnershipErrorPrompt {
		t.Fatalf("cross-session completion error = %v, want ownership denial", err)
	}
	runs, listErr := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if listErr != nil {
		t.Fatalf("ListRuns: %v", listErr)
	}
	if len(runs) != 1 || runs[0].CompletedAt != nil {
		t.Fatalf("runs after rejected completion = %+v, want still active", runs)
	}
}

func TestServiceCompleteWorkflowTaskForceRequestsRuntimeCancelWithoutDirectSchedulerWake(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	claimAndAttachWorkflowServiceRun(t, ctx, service, metadataStore, binding, started.RunID, "session-human-force")
	notifier := &recordingSchedulerNotifier{}
	canceler := &recordingTaskRuntimeRunCancelRequester{active: true}
	service.schedulerWake = notifier
	service.runtimeCancel = canceler

	completed, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind: serverapi.WorkflowTaskCompleteActorUser,
		Force:     true,
		ProjectID: binding.ProjectID,
		RunID:     started.RunID,
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask force: %v", err)
	}
	if completed.RunID != started.RunID || completed.State != "applied" {
		t.Fatalf("force complete response = %+v", completed)
	}
	if len(canceler.requestedRunIDs) != 1 || canceler.requestedRunIDs[0] != workflow.RunID(started.RunID) {
		t.Fatalf("requested cancel run IDs = %+v, want %s", canceler.requestedRunIDs, started.RunID)
	}
	if len(canceler.runIDs) != 0 {
		t.Fatalf("blocking cancel run IDs = %+v, want none", canceler.runIDs)
	}
	if notifier.count != 0 {
		t.Fatalf("force completion scheduler notifications = %d, want 0 while runtime will publish RuntimeFinished", notifier.count)
	}
}

func TestServiceCompleteWorkflowTaskForceWakesSchedulerWhenNoRuntimeOwnsRun(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	claimAndAttachWorkflowServiceRun(t, ctx, service, metadataStore, binding, started.RunID, "session-human-force")
	notifier := &recordingSchedulerNotifier{}
	canceler := &recordingTaskRuntimeRunCancelRequester{active: false}
	service.schedulerWake = notifier
	service.runtimeCancel = canceler

	completed, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind: serverapi.WorkflowTaskCompleteActorUser,
		Force:     true,
		ProjectID: binding.ProjectID,
		RunID:     started.RunID,
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask force: %v", err)
	}
	if completed.RunID != started.RunID || completed.State != "applied" {
		t.Fatalf("force complete response = %+v", completed)
	}
	if len(canceler.requestedRunIDs) != 1 || canceler.requestedRunIDs[0] != workflow.RunID(started.RunID) {
		t.Fatalf("requested cancel run IDs = %+v, want %s", canceler.requestedRunIDs, started.RunID)
	}
	if len(canceler.runIDs) != 0 {
		t.Fatalf("blocking cancel run IDs = %+v, want none", canceler.runIDs)
	}
	if notifier.count != 1 {
		t.Fatalf("force completion scheduler notifications = %d, want 1", notifier.count)
	}
}

func TestServiceCompleteWorkflowTaskForceKeepsCompletionWhenRuntimeCancelFails(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	claimAndAttachWorkflowServiceRun(t, ctx, service, metadataStore, binding, started.RunID, "session-human-force")
	notifier := &recordingSchedulerNotifier{}
	canceler := &recordingTaskRuntimeCanceler{err: errors.New("runtime already gone")}
	service.schedulerWake = notifier
	service.runtimeCancel = canceler

	completed, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind: serverapi.WorkflowTaskCompleteActorUser,
		Force:     true,
		RunID:     started.RunID,
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask force with cancel failure: %v", err)
	}
	if completed.RunID != started.RunID || completed.State != "applied" {
		t.Fatalf("force complete response = %+v", completed)
	}
	if len(canceler.runIDs) != 1 || canceler.runIDs[0] != workflow.RunID(started.RunID) {
		t.Fatalf("canceled run IDs = %+v, want %s", canceler.runIDs, started.RunID)
	}
	if notifier.count != 1 {
		t.Fatalf("force completion scheduler notifications = %d, want 1", notifier.count)
	}
}

func TestServiceMoveTaskAutoApproveSurfacesCommittedPendingMoveWhenApprovalFails(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	planID := workflowServiceNodeIDByKey(t, def.Definition, "plan")
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.attentionFinalizer = finalizer
	service.approve = func(context.Context, workflow.TransitionID) (workflowstore.CompleteRunResult, error) {
		return workflowstore.CompleteRunResult{}, errors.New("approval failed")
	}

	moveResult, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID, TargetNodeID: planID, AllowMissingEdge: true, AutoApprove: true})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if moveResult.Moved == nil {
		t.Fatalf("MoveWorkflowTask result = %+v, want moved", moveResult)
	}
	moved := *moveResult.Moved
	if moved.State != "pending_approval" || moved.TransitionID == "" || moved.ApprovalError != "approval failed" {
		t.Fatalf("partial auto-approve response = %+v, want pending move with approval error", moved)
	}
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 1 || transitions[0].State != "pending_approval" {
		t.Fatalf("committed transition = %+v, want pending approval", transitions)
	}
	if len(finalizer.results) != 1 || finalizer.results[0].TransitionID != workflow.TransitionID(moved.TransitionID) || finalizer.results[0].State != "pending_approval" {
		t.Fatalf("attention finalizer results = %+v", finalizer.results)
	}
}

func TestServiceMoveTaskAutoApproveNoneTargetSkipsWorktreeSetup(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	planID := workflowServiceNodeIDByKey(t, def.Definition, "plan")
	setupErr := errors.New("setup failed")
	service.taskWorktrees = &recordingTaskWorktreeEnsurer{err: setupErr}

	moved, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID, TargetNodeID: planID, AllowMissingEdge: true, AutoApprove: true})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if moved.Moved == nil || moved.Moved.State != "approved" {
		t.Fatalf("move result = %+v, want approved move", moved)
	}
	if service.taskWorktrees.(*recordingTaskWorktreeEnsurer).taskID != "" {
		t.Fatalf("none target unexpectedly ensured a worktree for task %q", task.Task.ID)
	}
}

func TestServiceMoveTaskAutoApprovedReplacementResolvesOldPendingApproval(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	planID := workflowServiceNodeIDByKey(t, def.Definition, "plan")
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.attentionFinalizer = finalizer
	oldMoveResult, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID, TargetNodeID: planID, AllowMissingEdge: true})
	if err != nil {
		t.Fatalf("initial MoveWorkflowTask: %v", err)
	}
	if oldMoveResult.Moved == nil {
		t.Fatalf("initial MoveWorkflowTask result = %+v, want moved", oldMoveResult)
	}
	oldMove := *oldMoveResult.Moved
	if oldMove.State != "pending_approval" {
		t.Fatalf("initial move = %+v, want pending approval", oldMove)
	}

	replacementResult, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID, TargetNodeID: planID, AllowMissingEdge: true, AutoApprove: true})
	if err != nil {
		t.Fatalf("replacement MoveWorkflowTask: %v", err)
	}
	if replacementResult.Moved == nil {
		t.Fatalf("replacement MoveWorkflowTask result = %+v, want moved", replacementResult)
	}
	replacement := *replacementResult.Moved
	if replacement.State != "approved" {
		t.Fatalf("replacement move = %+v, want approved", replacement)
	}
	if len(finalizer.results) != 2 {
		t.Fatalf("attention finalizer results = %+v, want initial pending and approved replacement", finalizer.results)
	}
	resolved := finalizer.results[1].ResolvedApprovalTransitionIDs
	if len(resolved) != 1 || resolved[0] != workflow.TransitionID(oldMove.TransitionID) {
		t.Fatalf("replacement resolved approvals = %+v, want old transition %s", resolved, oldMove.TransitionID)
	}
}

func TestServiceMoveTaskAutoApproveDoesNotBypassApprovalGatedEdge(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	agentID := workflowServiceNodeIDByKey(t, def.Definition, "agent")
	var startEdge serverapi.WorkflowEdge
	for _, edge := range def.Definition.Edges {
		if edge.Key == "start" {
			startEdge = edge
			break
		}
	}
	if startEdge.ID == "" {
		t.Fatalf("missing start edge in %+v", def.Definition.Edges)
	}
	if _, err := service.store.UpdateEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID(startEdge.ID), WorkflowID: workflow.WorkflowID(workflowID), TransitionGroupID: workflow.TransitionGroupID(startEdge.TransitionGroupID), Key: workflow.ModelKey(startEdge.Key), TargetNodeID: workflow.NodeID(startEdge.TargetNodeID), RequiresApproval: true, ContextMode: workflow.ContextMode(startEdge.ContextMode), ContextSource: workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(startEdge.ContextSource.Kind), NodeKey: workflow.ModelKey(startEdge.ContextSource.NodeKey)}), PromptTemplate: startEdge.PromptTemplate, Parameters: domainParameters(startEdge.Parameters)}); err != nil {
		t.Fatalf("enable start edge approval: %v", err)
	}

	moveResult, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID, TargetNodeID: agentID, AllowMissingEdge: true, AutoApprove: true})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if moveResult.Moved == nil {
		t.Fatalf("MoveWorkflowTask result = %+v, want moved", moveResult)
	}
	moved := *moveResult.Moved
	if moved.State != "pending_approval" || len(moved.PlacementIDs) != 0 || len(moved.RunIDs) != 0 || moved.ApprovalError != "" {
		t.Fatalf("approval-gated move = %+v, want pending approval without automation", moved)
	}
}

func TestServiceApproveTerminalTransitionDoesNotEnsureWorktree(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	var doneEdge serverapi.WorkflowEdge
	for _, edge := range def.Definition.Edges {
		if edge.Key == "done" {
			doneEdge = edge
			break
		}
	}
	if doneEdge.ID == "" {
		t.Fatalf("missing done edge in %+v", def.Definition.Edges)
	}
	if _, err := service.store.UpdateEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID(doneEdge.ID), WorkflowID: workflow.WorkflowID(workflowID), TransitionGroupID: workflow.TransitionGroupID(doneEdge.TransitionGroupID), Key: workflow.ModelKey(doneEdge.Key), TargetNodeID: workflow.NodeID(doneEdge.TargetNodeID), RequiresApproval: true, ContextMode: workflow.ContextMode(doneEdge.ContextMode), ContextSource: workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(doneEdge.ContextSource.Kind), NodeKey: workflow.ModelKey(doneEdge.ContextSource.NodeKey)}), PromptTemplate: doneEdge.PromptTemplate, Parameters: domainParameters(doneEdge.Parameters)}); err != nil {
		t.Fatalf("enable done edge approval: %v", err)
	}
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	completed, err := service.store.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: workflow.RunID(started.RunID), TransitionID: "done", Actor: "agent"})
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	if completed.State != "pending_approval" {
		t.Fatalf("completion = %+v, want pending approval", completed)
	}
	service.taskWorktrees = &recordingTaskWorktreeEnsurer{hook: func(taskID string) {
		t.Fatalf("unexpected worktree ensure for terminal approval of task %s", taskID)
	}}

	approvedResult, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskTransitionID: string(completed.TransitionID)})
	if err != nil {
		t.Fatalf("ApproveWorkflowTask: %v", err)
	}
	if approvedResult.Approved == nil {
		t.Fatalf("ApproveWorkflowTask result = %+v, want approved", approvedResult)
	}
	approved := *approvedResult.Approved
	if approved.State != "approved" || len(approved.RunIDs) != 0 {
		t.Fatalf("terminal approval = %+v, want approved without run", approved)
	}
}

func TestServiceApproveExecutableTransitionForwardsSetupOperationID(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	attachWorkflowServiceLegacyManagedWorktree(t, ctx, metadataStore, binding, task.Task.ID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	var startEdge serverapi.WorkflowEdge
	for _, edge := range def.Definition.Edges {
		if edge.Key == "start" {
			startEdge = edge
			break
		}
	}
	if startEdge.ID == "" {
		t.Fatalf("missing start edge in %+v", def.Definition.Edges)
	}
	if _, err := service.store.UpdateEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID(startEdge.ID), WorkflowID: workflow.WorkflowID(workflowID), TransitionGroupID: workflow.TransitionGroupID(startEdge.TransitionGroupID), Key: workflow.ModelKey(startEdge.Key), TargetNodeID: workflow.NodeID(startEdge.TargetNodeID), RequiresApproval: true, ContextMode: workflow.ContextMode(startEdge.ContextMode), ContextSource: workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(startEdge.ContextSource.Kind), NodeKey: workflow.ModelKey(startEdge.ContextSource.NodeKey)}), PromptTemplate: startEdge.PromptTemplate, Parameters: domainParameters(startEdge.Parameters)}); err != nil {
		t.Fatalf("enable start edge approval: %v", err)
	}
	started, err := service.store.StartTask(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	ensurer := &recordingTaskWorktreeEnsurer{}
	service.taskWorktrees = ensurer
	setupID := serverapi.NewWorktreeSetupOperationID()
	approvedResult, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{SetupOperationID: setupID, TaskTransitionID: string(started.TransitionID)})
	if err != nil {
		t.Fatalf("ApproveWorkflowTask: %v", err)
	}
	if approvedResult.Approved == nil {
		t.Fatalf("ApproveWorkflowTask result = %+v, want approved", approvedResult)
	}
	approved := *approvedResult.Approved
	if approved.State != "applied" {
		t.Fatalf("approval = %+v, want applied", approved)
	}
	if ensurer.taskID != task.Task.ID || ensurer.setupOperationID != setupID {
		t.Fatalf("ensurer = task %q setup %s, want task %q setup %s", ensurer.taskID, ensurer.setupOperationID.String(), task.Task.ID, setupID.String())
	}
}

func TestServiceInterruptTaskTargetsRunAndCancelsRuntime(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	if _, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	canceler := &recordingTaskRuntimeCanceler{}
	service.runtimeCancel = canceler

	interrupted, err := service.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("InterruptWorkflowTask: %v", err)
	}
	if len(interrupted.Runs) != 1 {
		t.Fatalf("interrupt response=%+v, want one run", interrupted)
	}
	if len(canceler.taskIDs) != 1 || canceler.taskIDs[0] != workflow.TaskID(task.Task.ID) {
		t.Fatalf("canceled task runtimes=%+v, want task %s", canceler.taskIDs, task.Task.ID)
	}
}

func TestServiceInterruptTaskWithCustomReasonDoesNotSurfaceInterruptedRunAttention(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	if _, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}

	if _, err := service.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{
		TaskID: task.Task.ID,
		Reason: "operator paused this run",
	}); err != nil {
		t.Fatalf("InterruptWorkflowTask: %v", err)
	}

	attention, err := service.view.ListAttention(ctx, serverapi.WorkflowAttentionListRequest{ProjectID: binding.ProjectID}, service.roleResolver)
	if err != nil {
		t.Fatalf("ListAttention: %v", err)
	}
	for _, item := range attention.Items {
		if item.Kind == "interrupted_run" {
			t.Fatalf("custom user interrupt surfaced as attention: %+v", attention.Items)
		}
	}
}

func TestServiceCancelTaskCancelsActiveRuntime(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	canceler := &recordingTaskRuntimeCanceler{}
	service.runtimeCancel = canceler

	if err := service.CancelWorkflowTask(ctx, serverapi.WorkflowTaskCancelRequest{TaskID: task.Task.ID, Reason: "stop"}); err != nil {
		t.Fatalf("CancelWorkflowTask: %v", err)
	}
	if len(canceler.taskIDs) != 1 || canceler.taskIDs[0] != workflow.TaskID(task.Task.ID) {
		t.Fatalf("canceled tasks = %+v", canceler.taskIDs)
	}
}

func TestServiceCancelTaskResolvesPendingApprovalAttention(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	planID := workflowServiceNodeIDByKey(t, def.Definition, "plan")
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.attentionFinalizer = finalizer
	moveResult, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID, TargetNodeID: planID, AllowMissingEdge: true})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if moveResult.Moved == nil {
		t.Fatalf("MoveWorkflowTask result = %+v, want moved", moveResult)
	}
	moved := *moveResult.Moved
	if moved.State != "pending_approval" {
		t.Fatalf("setup move = %+v, want pending approval", moved)
	}

	if err := service.CancelWorkflowTask(ctx, serverapi.WorkflowTaskCancelRequest{TaskID: task.Task.ID, Reason: "stop"}); err != nil {
		t.Fatalf("CancelWorkflowTask: %v", err)
	}
	if len(finalizer.results) != 2 || len(finalizer.results[1].ResolvedApprovalTransitionIDs) != 1 || finalizer.results[1].ResolvedApprovalTransitionIDs[0] != workflow.TransitionID(moved.TransitionID) {
		t.Fatalf("attention finalizer results = %+v", finalizer.results)
	}
}

func TestServiceDeleteTaskCancelsRuntimeAndPublishesEvent(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	canceler := &recordingTaskRuntimeCanceler{}
	service.runtimeCancel = canceler
	worktreeCleanup := &recordingTaskWorktreeDeleter{}
	service.taskWorktreeCleanup = worktreeCleanup

	if err := service.DeleteWorkflowTask(ctx, serverapi.WorkflowTaskDeleteRequest{TaskID: task.Task.ID}); err != nil {
		t.Fatalf("DeleteWorkflowTask: %v", err)
	}
	if len(canceler.taskIDs) != 1 || canceler.taskIDs[0] != workflow.TaskID(task.Task.ID) {
		t.Fatalf("canceled tasks = %+v", canceler.taskIDs)
	}
	if len(worktreeCleanup.taskIDs) != 1 || worktreeCleanup.taskIDs[0] != task.Task.ID {
		t.Fatalf("worktree cleanup tasks = %+v", worktreeCleanup.taskIDs)
	}
	event := nextWorkflowProjectEvent(t, sub)
	if event.ProjectID != binding.ProjectID || event.WorkflowID != workflowID || event.Resource != "task" || event.Action != "deleted" || !sameStringSet(event.ChangedIDs, []string{task.Task.ID}) {
		t.Fatalf("delete event = %+v, want task deleted event", event)
	}
	if _, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: task.Task.ID}); err == nil {
		t.Fatalf("deleted workflow task should not remain readable")
	}
}

func TestServiceDeleteTaskResolvesPendingApprovalAttention(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	planID := workflowServiceNodeIDByKey(t, def.Definition, "plan")
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.attentionFinalizer = finalizer
	moveResult, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID, TargetNodeID: planID, AllowMissingEdge: true})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if moveResult.Moved == nil {
		t.Fatalf("MoveWorkflowTask result = %+v, want moved", moveResult)
	}
	moved := *moveResult.Moved
	if moved.State != "pending_approval" {
		t.Fatalf("setup move = %+v, want pending approval", moved)
	}

	if err := service.DeleteWorkflowTask(ctx, serverapi.WorkflowTaskDeleteRequest{TaskID: task.Task.ID}); err != nil {
		t.Fatalf("DeleteWorkflowTask: %v", err)
	}
	if len(finalizer.results) != 2 || len(finalizer.results[1].ResolvedApprovalProjections) != 1 || finalizer.results[1].ResolvedApprovalProjections[0].TransitionID != workflow.TransitionID(moved.TransitionID) {
		t.Fatalf("attention finalizer results = %+v", finalizer.results)
	}
}

func TestServiceDeleteWorkflowResolvesPendingApprovalAttention(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	planID := workflowServiceNodeIDByKey(t, def.Definition, "plan")
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.attentionFinalizer = finalizer
	moveResult, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID, TargetNodeID: planID, AllowMissingEdge: true})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if moveResult.Moved == nil {
		t.Fatalf("MoveWorkflowTask result = %+v, want moved", moveResult)
	}
	moved := *moveResult.Moved
	if moved.State != "pending_approval" {
		t.Fatalf("setup move = %+v, want pending approval", moved)
	}
	preview, err := service.PreviewWorkflowDelete(ctx, serverapi.WorkflowDeletePreviewRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}

	deleted, err := service.DeleteWorkflow(ctx, serverapi.WorkflowDeleteRequest{
		WorkflowID:           workflowID,
		Confirmed:            true,
		ExpectedVersion:      preview.Impact.Version,
		ExpectedProjectCount: preview.Impact.ProjectCount,
		ExpectedLinkCount:    preview.Impact.LinkCount,
		ExpectedTaskCount:    preview.Impact.TaskCount,
	})
	if err != nil {
		t.Fatalf("DeleteWorkflow: %v", err)
	}
	if !deleted.Deleted {
		t.Fatalf("delete response = %+v, want deleted", deleted)
	}
	if len(finalizer.results) != 2 {
		t.Fatalf("attention finalizer results = %+v, want pending setup and delete resolution", finalizer.results)
	}
	resolved := finalizer.results[1].ResolvedApprovalProjections
	if len(resolved) != 1 || resolved[0].TransitionID != workflow.TransitionID(moved.TransitionID) {
		t.Fatalf("delete resolved approvals = %+v, want transition %s", resolved, moved.TransitionID)
	}
}

func TestServiceDeleteWorkflowResolvesInterruptedRunAttention(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := service.store.InterruptRunGeneration(ctx, workflow.RunID(started.RunID), claimed.Generation, "workflow_runtime_failed", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.attentionFinalizer = finalizer
	preview, err := service.PreviewWorkflowDelete(ctx, serverapi.WorkflowDeletePreviewRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}

	deleted, err := service.DeleteWorkflow(ctx, serverapi.WorkflowDeleteRequest{
		WorkflowID:           workflowID,
		Confirmed:            true,
		ExpectedVersion:      preview.Impact.Version,
		ExpectedProjectCount: preview.Impact.ProjectCount,
		ExpectedLinkCount:    preview.Impact.LinkCount,
		ExpectedTaskCount:    preview.Impact.TaskCount,
	})
	if err != nil {
		t.Fatalf("DeleteWorkflow: %v", err)
	}
	if !deleted.Deleted {
		t.Fatalf("delete response = %+v, want deleted", deleted)
	}
	if len(finalizer.resolvedRuns) != 1 || finalizer.resolvedRuns[0] != workflow.RunID(started.RunID) {
		t.Fatalf("resolved interrupted runs = %+v, want %s", finalizer.resolvedRuns, started.RunID)
	}
}

func TestServiceWorkflowAttentionFinalizationIgnoresRequestCancellation(t *testing.T) {
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service := &Service{attentionFinalizer: finalizer}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	service.finalizeWorkflowAttention(ctx, workflowstore.CompleteRunResult{
		TransitionID: "transition-1",
		State:        "pending_approval",
	})

	if len(finalizer.transitionContextErrs) != 1 {
		t.Fatalf("finalizer context records = %+v, want one", finalizer.transitionContextErrs)
	}
	if finalizer.transitionContextErrs[0] != nil {
		t.Fatalf("finalizer context err = %v, want nil", finalizer.transitionContextErrs[0])
	}
}

func TestServiceWorkflowAttentionFinalizationPublishesInterruptedRuns(t *testing.T) {
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service := &Service{attentionFinalizer: finalizer}

	service.finalizeWorkflowAttention(context.Background(), workflowstore.CompleteRunResult{
		TransitionID:      "transition-1",
		State:             "applied",
		InterruptedRunIDs: []workflow.RunID{"run-script-1", "run-script-2"},
	})

	if len(finalizer.results) != 1 || finalizer.results[0].TransitionID != "transition-1" {
		t.Fatalf("finalized transitions = %+v, want transition-1", finalizer.results)
	}
	if len(finalizer.interruptedRuns) != 2 || finalizer.interruptedRuns[0] != "run-script-1" || finalizer.interruptedRuns[1] != "run-script-2" {
		t.Fatalf("finalized interrupted runs = %+v", finalizer.interruptedRuns)
	}
}

func TestServiceDeleteTaskPreflightBlockedDoesNotCancelRuns(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	canceler := &recordingTaskRuntimeCanceler{}
	service.runtimeCancel = canceler
	worktreeCleanup := &recordingTaskWorktreeDeleter{preflightErr: serverapi.ErrWorktreeBlocked}
	service.taskWorktreeCleanup = worktreeCleanup

	err := service.DeleteWorkflowTask(ctx, serverapi.WorkflowTaskDeleteRequest{TaskID: task.Task.ID})
	if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
		t.Fatalf("DeleteWorkflowTask error = %v, want ErrWorktreeBlocked", err)
	}
	if len(worktreeCleanup.preflightTaskIDs) != 1 || worktreeCleanup.preflightTaskIDs[0] != task.Task.ID {
		t.Fatalf("preflight tasks = %+v, want one preflight for %s", worktreeCleanup.preflightTaskIDs, task.Task.ID)
	}
	if len(canceler.taskIDs) != 0 {
		t.Fatalf("canceled tasks = %+v, want none when preflight blocks", canceler.taskIDs)
	}
	if len(worktreeCleanup.taskIDs) != 0 {
		t.Fatalf("worktree delete tasks = %+v, want none when preflight blocks", worktreeCleanup.taskIDs)
	}
	if _, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: task.Task.ID}); err != nil {
		t.Fatalf("blocked task should remain readable: %v", err)
	}
}

func TestServiceResumeTaskRequeuesRunAndNotifiesScheduler(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := service.store.InterruptRunGeneration(ctx, workflow.RunID(started.RunID), claimed.Generation, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	notifier := &recordingSchedulerNotifier{}
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.schedulerWake = notifier
	service.attentionFinalizer = finalizer

	resumed, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("ResumeWorkflowTask: %v", err)
	}
	if len(resumed.Runs) != 1 || resumed.Runs[0].Generation <= claimed.Generation || resumed.Runs[0].PlacementID == "" || resumed.Runs[0].NodeID == "" {
		t.Fatalf("resume response = %+v, want same run requeued", resumed)
	}
	if notifier.count != 1 {
		t.Fatalf("scheduler notifications = %d, want 1", notifier.count)
	}
	if len(finalizer.resolvedRuns) != 1 || finalizer.resolvedRuns[0] != workflow.RunID(started.RunID) {
		t.Fatalf("resolved interrupted runs = %+v, want %s", finalizer.resolvedRuns, started.RunID)
	}
}

type recordingSchedulerNotifier struct {
	count int
}

func (n *recordingSchedulerNotifier) Notify() {
	n.count++
}

type recordingWorkflowAttentionFinalizer struct {
	results               []workflowattention.TransitionResult
	interruptedRuns       []workflow.RunID
	resolvedRuns          []workflow.RunID
	transitionContextErrs []error
}

func (f *recordingWorkflowAttentionFinalizer) FinalizeTransition(ctx context.Context, result workflowattention.TransitionResult) {
	f.results = append(f.results, result)
	f.transitionContextErrs = append(f.transitionContextErrs, ctx.Err())
}

func (f *recordingWorkflowAttentionFinalizer) FinalizeInterruptedRun(_ context.Context, runID workflow.RunID) {
	f.interruptedRuns = append(f.interruptedRuns, runID)
}

func (f *recordingWorkflowAttentionFinalizer) ResolveInterruptedRun(_ context.Context, runID workflow.RunID) {
	f.resolvedRuns = append(f.resolvedRuns, runID)
}

type recordingTaskRuntimeCanceler struct {
	taskIDs []workflow.TaskID
	runIDs  []workflow.RunID
	err     error
}

func (c *recordingTaskRuntimeCanceler) CancelTaskRuns(_ context.Context, taskID workflow.TaskID) error {
	c.taskIDs = append(c.taskIDs, taskID)
	return c.err
}

func (c *recordingTaskRuntimeCanceler) CancelRun(_ context.Context, runID workflow.RunID) error {
	c.runIDs = append(c.runIDs, runID)
	return c.err
}

type recordingTaskRuntimeRunCancelRequester struct {
	recordingTaskRuntimeCanceler
	requestedRunIDs []workflow.RunID
	active          bool
}

func (c *recordingTaskRuntimeRunCancelRequester) RequestCancelRun(runID workflow.RunID) bool {
	c.requestedRunIDs = append(c.requestedRunIDs, runID)
	return c.active
}

type recordingTaskWorktreeEnsurer struct {
	taskID           string
	setupOperationID serverapi.WorktreeSetupOperationID
	hook             func(string)
	err              error
}

type recordingTaskExecutionTargetResolver struct {
	resolutions []worktree.ExecutionTargetResolution
	err         error
	calls       int
	hook        func()
}

type recordingTaskExecutionTargetWorktreeMaterializer struct {
	worktreeRoot string
	plannedRoot  string
	provision    worktree.ProvisionExecutionTargetWorktreeRequest
	setup        worktree.RunExecutionTargetSetupRequest
	provisionErr error
	setupErr     error
	inspect      func(context.Context, worktree.InspectExecutionTargetWorktreeRequest) (worktree.ExecutionTargetWorktreeInspection, error)
}

func (m *recordingTaskExecutionTargetWorktreeMaterializer) PlanExecutionTargetWorktreeRoot(_ string, _ string) (string, error) {
	if m.plannedRoot != "" {
		return m.plannedRoot, nil
	}
	if m.worktreeRoot != "" {
		return m.worktreeRoot, nil
	}
	return "/planned-execution-target-root", nil
}

func (m *recordingTaskExecutionTargetWorktreeMaterializer) ProvisionExecutionTargetWorktree(_ context.Context, req worktree.ProvisionExecutionTargetWorktreeRequest) (worktree.ProvisionExecutionTargetWorktreeResponse, error) {
	m.provision = req
	if m.provisionErr != nil {
		return worktree.ProvisionExecutionTargetWorktreeResponse{}, m.provisionErr
	}
	return worktree.ProvisionExecutionTargetWorktreeResponse{
		WorktreeRoot:           m.worktreeRoot,
		BranchName:             req.TaskShortID,
		CreatedBranch:          true,
		ExactBranchObservation: req.ResolvedCommit,
		LinkedWorktreeOwnership: &workflow.ExecutionTargetLinkedWorktreeOwnership{
			CommonDir:  "/test/common-dir",
			AdminEntry: "worktrees/" + req.TaskShortID,
			GitDir:     filepath.Join(m.worktreeRoot, ".git"),
			HeadRef:    "refs/heads/" + req.TaskShortID,
		},
	}, nil
}

func (m *recordingTaskExecutionTargetWorktreeMaterializer) ReprovisionExecutionTargetWorktree(ctx context.Context, req worktree.ProvisionExecutionTargetWorktreeRequest) (worktree.ProvisionExecutionTargetWorktreeResponse, error) {
	return m.ProvisionExecutionTargetWorktree(ctx, req)
}

func (m *recordingTaskExecutionTargetWorktreeMaterializer) InspectExecutionTargetWorktree(ctx context.Context, req worktree.InspectExecutionTargetWorktreeRequest) (worktree.ExecutionTargetWorktreeInspection, error) {
	if m.inspect != nil {
		return m.inspect(ctx, req)
	}
	return worktree.ExecutionTargetWorktreeInspection{Kind: worktree.ExecutionTargetWorktreeInspectionNoSideEffects}, nil
}

func (m *recordingTaskExecutionTargetWorktreeMaterializer) RunExecutionTargetSetup(_ context.Context, req worktree.RunExecutionTargetSetupRequest) error {
	m.setup = req
	return m.setupErr
}

func (r *recordingTaskExecutionTargetResolver) ResolveExecutionTarget(_ context.Context, _ string, _ workflow.ExecutionPolicyMode, _ *string) (worktree.ExecutionTargetResolution, error) {
	r.calls++
	if r.err != nil {
		return worktree.ExecutionTargetResolution{}, r.err
	}
	if len(r.resolutions) == 0 {
		return worktree.ExecutionTargetResolution{}, errors.New("execution target resolution is not configured")
	}
	index := r.calls - 1
	if index >= len(r.resolutions) {
		index = len(r.resolutions) - 1
	}
	if r.hook != nil {
		r.hook()
	}
	return r.resolutions[index], nil
}

func namedExecutionTargetResolution(namedRef string, commit string) worktree.ExecutionTargetResolution {
	return worktree.ExecutionTargetResolution{Source: workflow.ExecutionTargetResolvedSource{
		Kind:     workflow.ExecutionTargetSourceNamedRef,
		NamedRef: stringPtr(namedRef),
		Commit:   commit,
	}}
}

type recordingTaskWorktreeDeleter struct {
	taskIDs          []string
	preflightTaskIDs []string
	preflightErr     error
}

func (d *recordingTaskWorktreeDeleter) EnsureTaskWorktreeDeletable(_ context.Context, taskID string) error {
	d.preflightTaskIDs = append(d.preflightTaskIDs, taskID)
	return d.preflightErr
}

func (d *recordingTaskWorktreeDeleter) DeleteTaskWorktree(_ context.Context, taskID string) error {
	d.taskIDs = append(d.taskIDs, taskID)
	return nil
}

type recordingPromptResponder struct {
	sessionID string
	response  askquestion.AskQuestionResponse
	err       error
}

func (r *recordingPromptResponder) SubmitPromptResponse(sessionID string, resp askquestion.AskQuestionResponse, err error) error {
	r.sessionID = sessionID
	r.response = resp
	r.err = err
	return nil
}

func (e *recordingTaskWorktreeEnsurer) EnsureTaskWorktree(ctx context.Context, req TaskWorktreeEnsureRequest) error {
	e.taskID = string(req.TaskID)
	e.setupOperationID = req.SetupOperationID
	if e.hook != nil {
		e.hook(string(req.TaskID))
	}
	return e.err
}

func TestServiceDefaultWorkflowResolvesWithinProjectOnly(t *testing.T) {
	ctx, service, bindingA, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workspaceB := t.TempDir()
	cfgB, err := config.Load(workspaceB, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load B: %v", err)
	}
	bindingB, err := metadataStore.RegisterWorkspaceBinding(ctx, cfgB.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding B: %v", err)
	}
	if err := metadataStore.SetProjectKey(ctx, bindingB.ProjectID, "TWO"); err != nil {
		t.Fatalf("SetProjectKey B: %v", err)
	}
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, bindingA.ProjectID, workflowID)
	if _, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{ProjectID: bindingB.ProjectID, Title: "Task", Body: "Body"}); err == nil {
		t.Fatalf("expected project B task create to fail without project-scoped default workflow")
	}
}

func TestServiceWorkflowListPaginatesAndCreateLinkIsAtomic(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	for _, name := range []string{"Gamma", "Alpha", "Beta"} {
		if _, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: name}); err != nil {
			t.Fatalf("CreateWorkflow %q: %v", name, err)
		}
	}
	page1, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("ListWorkflows page1: %v", err)
	}
	if len(page1.Workflows) != 2 || page1.NextPageToken == "" {
		t.Fatalf("page1 = %+v", page1)
	}
	page2, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{PageSize: 2, PageToken: page1.NextPageToken})
	if err != nil {
		t.Fatalf("ListWorkflows page2: %v", err)
	}
	if len(page2.Workflows) != 1 || page2.NextPageToken != "" {
		t.Fatalf("page2 = %+v", page2)
	}
	seen := map[string]bool{}
	for _, record := range append(page1.Workflows, page2.Workflows...) {
		seen[record.Name] = true
	}
	for _, name := range []string{"Gamma", "Alpha", "Beta"} {
		if !seen[name] {
			t.Fatalf("paged workflows = %+v + %+v, missing %s", page1.Workflows, page2.Workflows, name)
		}
	}
	created, err := service.CreateAndLinkWorkflowToProject(ctx, serverapi.WorkflowCreateAndLinkProjectRequest{
		Name:          "Project Created",
		ProjectID:     binding.ProjectID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultIfProjectHasNone,
	})
	if err != nil {
		t.Fatalf("CreateAndLinkWorkflowToProject: %v", err)
	}
	if created.Workflow.ID == "" || created.Link.WorkflowID != created.Workflow.ID || !created.Link.Default {
		t.Fatalf("created = %+v, want first default link", created)
	}
	if _, err := service.CreateAndLinkWorkflowToProject(ctx, serverapi.WorkflowCreateAndLinkProjectRequest{
		Name:          "Broken",
		ProjectID:     "missing-project",
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultIfProjectHasNone,
	}); err == nil {
		t.Fatalf("expected invalid project create-and-link to fail")
	}
	filtered, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{PageSize: 10, Query: "Broken"})
	if err != nil {
		t.Fatalf("ListWorkflows filtered: %v", err)
	}
	if len(filtered.Workflows) != 0 {
		t.Fatalf("failed create-and-link left workflows: %+v", filtered.Workflows)
	}
}

func TestServiceWorkflowLinkFirstDefaultAndDuplicateIdempotency(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowA, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Workflow A"})
	if err != nil {
		t.Fatalf("CreateWorkflow A: %v", err)
	}
	workflowB, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Workflow B"})
	if err != nil {
		t.Fatalf("CreateWorkflow B: %v", err)
	}
	first := linkWorkflowServiceProject(t, ctx, service, serverapi.WorkflowLinkProjectRequest{
		ProjectID:     binding.ProjectID,
		WorkflowID:    workflowA.Workflow.ID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultIfProjectHasNone,
	})
	if !first.Link.Default {
		t.Fatalf("first link = %+v, want default", first)
	}
	duplicate := linkWorkflowServiceProject(t, ctx, service, serverapi.WorkflowLinkProjectRequest{
		ProjectID:     binding.ProjectID,
		WorkflowID:    workflowA.Workflow.ID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultIfProjectHasNone,
	})
	if duplicate.Link.ID != first.Link.ID || !duplicate.Link.Default {
		t.Fatalf("duplicate = %+v, want existing default link %+v", duplicate, first)
	}
	second := linkWorkflowServiceProject(t, ctx, service, serverapi.WorkflowLinkProjectRequest{
		ProjectID:     binding.ProjectID,
		WorkflowID:    workflowB.Workflow.ID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultIfProjectHasNone,
	})
	if second.Link.Default {
		t.Fatalf("second link = %+v, want non-default", second)
	}
}

func TestServiceWorkflowUnlinkRejectsTaskReferencesAndHardDeletesUnusedLinks(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	link := linkWorkflowServiceProject(t, ctx, service, serverapi.WorkflowLinkProjectRequest{
		ProjectID:     binding.ProjectID,
		WorkflowID:    workflowID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultAlways,
	})
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	blocked, err := service.UnlinkWorkflowFromProject(ctx, serverapi.WorkflowUnlinkProjectRequest{LinkID: link.Link.ID})
	if err != nil {
		t.Fatalf("task reference unlink guard should return typed blockers, got error: %v", err)
	}
	if blocked.Unlinked || !hasWorkflowUnlinkBlocker(blocked.Blockers, "task_references", 1) {
		t.Fatalf("blocked unlink = %+v, want task reference blocker", blocked)
	}
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	if _, err := service.store.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: workflow.RunID(started.RunID), TransitionID: "done"}); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	blocked, err = service.UnlinkWorkflowFromProject(ctx, serverapi.WorkflowUnlinkProjectRequest{LinkID: link.Link.ID})
	if err != nil {
		t.Fatalf("terminal history unlink guard should return typed blockers, got error: %v", err)
	}
	if blocked.Unlinked || !hasWorkflowUnlinkBlocker(blocked.Blockers, "task_references", 1) {
		t.Fatalf("blocked unlink = %+v, want terminal history blocker", blocked)
	}
	unusedWorkflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	unusedLink := linkWorkflowServiceProject(t, ctx, service, serverapi.WorkflowLinkProjectRequest{ProjectID: binding.ProjectID, WorkflowID: unusedWorkflowID})
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	if unlinked, err := service.UnlinkWorkflowFromProject(ctx, serverapi.WorkflowUnlinkProjectRequest{LinkID: unusedLink.Link.ID}); err != nil || !unlinked.Unlinked {
		t.Fatalf("unused link unlink: %v", err)
	}
	links, err := service.store.ListProjectWorkflowLinks(ctx, binding.ProjectID)
	if err != nil {
		t.Fatalf("ListProjectWorkflowLinks: %v", err)
	}
	for _, link := range links {
		if link.ID == unusedLink.Link.ID {
			t.Fatalf("unused link should be hard-deleted, links=%+v", links)
		}
	}
	events := waitWorkflowProjectActions(t, sub, "workflow_link", "unlinked")
	unlinkEvent := events[len(events)-1]
	if unlinkEvent.ProjectID != binding.ProjectID || unlinkEvent.WorkflowID != unusedWorkflowID {
		t.Fatalf("unlink event = %+v, want project/workflow identity", unlinkEvent)
	}
}

func TestServiceWorkflowDeletePreviewsBlocksAndPublishesDeletion(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	preview, err := service.PreviewWorkflowDelete(ctx, serverapi.WorkflowDeletePreviewRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}
	if preview.Impact.WorkflowID != workflowID || preview.Impact.ProjectCount != 1 || preview.Impact.LinkCount != 1 || preview.Impact.TaskCount != 1 {
		t.Fatalf("delete preview = %+v, want one project/link/task", preview)
	}
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	workflowSub, err := service.SubscribeWorkflow(ctx, serverapi.WorkflowSubscribeRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("SubscribeWorkflow: %v", err)
	}
	defer func() { _ = workflowSub.Close() }()

	blocked, err := service.DeleteWorkflow(ctx, serverapi.WorkflowDeleteRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("DeleteWorkflow unconfirmed: %v", err)
	}
	if blocked.Deleted || !hasWorkflowDeleteBlocker(blocked.Blockers, "confirmation_required", 1) {
		t.Fatalf("unconfirmed delete = %+v, want confirmation blocker", blocked)
	}

	deleted, err := service.DeleteWorkflow(ctx, serverapi.WorkflowDeleteRequest{
		WorkflowID:           workflowID,
		Confirmed:            true,
		ExpectedVersion:      preview.Impact.Version,
		ExpectedProjectCount: preview.Impact.ProjectCount,
		ExpectedLinkCount:    preview.Impact.LinkCount,
		ExpectedTaskCount:    preview.Impact.TaskCount,
	})
	if err != nil {
		t.Fatalf("DeleteWorkflow confirmed: %v", err)
	}
	if !deleted.Deleted || len(deleted.Blockers) != 0 {
		t.Fatalf("confirmed delete = %+v, want deleted without blockers", deleted)
	}
	event := nextWorkflowProjectEvent(t, sub)
	if event.ProjectID != binding.ProjectID || event.WorkflowID != workflowID || event.Resource != "workflow" || event.Action != "deleted" || !sameStringSet(event.ChangedIDs, []string{workflowID}) {
		t.Fatalf("event = %+v, want workflow deleted event", event)
	}
	eventCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	workflowEvent, err := workflowSub.Next(eventCtx)
	if err != nil {
		t.Fatalf("workflow subscription delete next: %v", err)
	}
	if workflowEvent.ProjectID != "" || workflowEvent.WorkflowID != workflowID || workflowEvent.Resource != "workflow" || workflowEvent.Action != "deleted" || !sameStringSet(workflowEvent.ChangedIDs, []string{workflowID}) {
		t.Fatalf("workflow-scoped delete event = %+v, want projectless workflow delete event", workflowEvent)
	}
	if _, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: task.Task.ID}); err == nil {
		t.Fatalf("deleted workflow task should not remain readable")
	}
}

func hasWorkflowUnlinkBlocker(blockers []serverapi.WorkflowUnlinkProjectBlocker, code string, count int) bool {
	for _, blocker := range blockers {
		if blocker.Code == code && blocker.Count == count {
			return true
		}
	}
	return false
}

func hasWorkflowDeleteBlocker(blockers []serverapi.WorkflowDeleteBlocker, code string, count int64) bool {
	for _, blocker := range blockers {
		if blocker.Code == code && blocker.Count == count {
			return true
		}
	}
	return false
}

func TestServiceCommentsAndReadModels(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	comment, err := service.AddWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentAddRequest{TaskID: task.Task.ID, Body: "note", Author: "user"})
	if err != nil {
		t.Fatalf("AddWorkflowTaskComment: %v", err)
	}
	comments, err := service.ListWorkflowTaskComments(ctx, serverapi.WorkflowTaskCommentListRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("ListWorkflowTaskComments: %v", err)
	}
	if len(comments.Comments) != 1 || comments.Comments[0].ID != comment.Comment.ID {
		t.Fatalf("comments = %+v", comments)
	}
	secondComment, err := service.AddWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentAddRequest{TaskID: task.Task.ID, Body: "second", Author: "user"})
	if err != nil {
		t.Fatalf("AddWorkflowTaskComment second: %v", err)
	}
	commentPage, err := service.ListWorkflowTaskComments(ctx, serverapi.WorkflowTaskCommentListRequest{TaskID: task.Task.ID, PageSize: 1})
	if err != nil {
		t.Fatalf("ListWorkflowTaskComments page 1: %v", err)
	}
	if len(commentPage.Comments) != 1 || commentPage.NextPageToken == "" {
		t.Fatalf("first comment page = %+v, want one comment with next token", commentPage)
	}
	nextCommentPage, err := service.ListWorkflowTaskComments(ctx, serverapi.WorkflowTaskCommentListRequest{TaskID: task.Task.ID, PageSize: 1, PageToken: commentPage.NextPageToken})
	if err != nil {
		t.Fatalf("ListWorkflowTaskComments page 2: %v", err)
	}
	if len(nextCommentPage.Comments) != 1 || nextCommentPage.NextPageToken != "" {
		t.Fatalf("second comment page = %+v, want one comment without next token", nextCommentPage)
	}
	gotPagedCommentIDs := map[string]int{
		commentPage.Comments[0].ID:     1,
		nextCommentPage.Comments[0].ID: 1,
	}
	if gotPagedCommentIDs[comment.Comment.ID] != 1 || gotPagedCommentIDs[secondComment.Comment.ID] != 1 || len(gotPagedCommentIDs) != 2 {
		t.Fatalf("paged comment ids = %+v, want both seeded comments exactly once", gotPagedCommentIDs)
	}
	for _, badToken := range []string{"garbage", "-1", "abc|def", "100"} {
		if _, err := service.ListWorkflowTaskComments(ctx, serverapi.WorkflowTaskCommentListRequest{TaskID: task.Task.ID, PageToken: badToken}); err == nil {
			t.Fatalf("ListWorkflowTaskComments accepted invalid page token %q", badToken)
		}
	}
	if _, err := service.ListWorkflowTaskComments(ctx, serverapi.WorkflowTaskCommentListRequest{TaskID: task.Task.ID, PageSize: serverapi.WorkflowTaskCommentListMaxPageSize + 1}); err == nil {
		t.Fatalf("ListWorkflowTaskComments accepted oversized page size")
	}
	board, err := service.GetWorkflowBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetWorkflowBoard: %v", err)
	}
	if len(board.Board.Cards) != 1 || len(board.Board.Columns) == 0 {
		t.Fatalf("board = %+v", board.Board)
	}
	backlogNodeID := ""
	for _, column := range board.Board.Columns {
		if column.IsBacklog {
			backlogNodeID = column.Node.NodeID
			break
		}
	}
	if backlogNodeID == "" {
		t.Fatalf("board columns missing backlog: %+v", board.Board.Columns)
	}
	cards, err := service.ListWorkflowBoardNodeCards(ctx, serverapi.WorkflowBoardNodeCardsListRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, NodeID: backlogNodeID})
	if err != nil {
		t.Fatalf("ListWorkflowBoardNodeCards: %v", err)
	}
	if len(cards.Cards) != 1 {
		t.Fatalf("node cards = %+v", cards)
	}
	detail, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("GetWorkflowTask: %v", err)
	}
	if detail.Task.Summary.ID != task.Task.ID || len(detail.Task.Comments) != 2 {
		t.Fatalf("detail = %+v", detail.Task)
	}
}

func TestServiceWorkflowProjectSubscriptionEmitsLiveEvents(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{
		Name:            "Workflow",
		ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyNone},
	})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}

	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, created.Workflow.ID)
	event := nextWorkflowProjectEvent(t, sub)
	if event.ProjectID != binding.ProjectID || event.WorkflowID != created.Workflow.ID || event.Resource != "workflow_link" || event.Action != "linked" {
		t.Fatalf("event = %+v, want workflow link event", event)
	}
	board, err := service.GetWorkflowBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("GetWorkflowBoard: %v", err)
	}
	if board.Board.SelectedWorkflow.WorkflowID != created.Workflow.ID {
		t.Fatalf("board selected workflow = %+v, want linked workflow", board.Board.SelectedWorkflow)
	}
}

func TestServiceWorkflowProjectSubscriptionEmitsRunCompletionEvent(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	completed, err := service.store.CompleteRun(ctx, workflowstore.CompleteRunRequest{RunID: workflow.RunID(started.RunID), TransitionID: "done"})
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	event := nextWorkflowProjectEvent(t, sub)
	if event.ProjectID != binding.ProjectID || event.WorkflowID != workflowID || event.Resource != "task" || event.Action != "completed" {
		t.Fatalf("event = %+v, want task completed event", event)
	}
	if !sameStringSet(event.ChangedIDs, []string{task.Task.ID, string(completed.TransitionID), started.RunID}) {
		t.Fatalf("changed IDs = %+v", event.ChangedIDs)
	}
	boardAfter, err := service.GetWorkflowBoard(ctx, serverapi.WorkflowBoardRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflowBoard after completion: %v", err)
	}
	if len(boardAfter.Board.DonePreview) != 1 {
		t.Fatalf("board done preview = %+v, want completed task", boardAfter.Board.DonePreview)
	}
}

func TestServiceWorkflowGraphMutationsPublishInvalidations(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, created.Workflow.ID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	startID := workflowServiceNodeIDByKind(t, def.Definition, "start")
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	if _, err := service.UpdateWorkflow(ctx, serverapi.WorkflowUpdateRequest{WorkflowID: created.Workflow.ID, Name: "Updated Workflow"}); err != nil {
		t.Fatalf("UpdateWorkflow: %v", err)
	}
	if _, err := service.AddWorkflowNode(ctx, serverapi.WorkflowNodeAddRequest{WorkflowID: created.Workflow.ID, NodeID: "node-agent-events", Key: "agent_events", Kind: "agent", DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddWorkflowNode: %v", err)
	}
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: "group-start-events", SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-start-events", TransitionGroupID: "group-start-events", Key: "start", TargetNodeID: doneID, ContextMode: "new_session"}); err != nil {
		t.Fatalf("AddWorkflowEdge: %v", err)
	}
	for _, event := range waitWorkflowProjectActions(t, sub, "workflow", "updated", "node_added", "transition_group_added", "edge_added") {
		if event.ProjectID != binding.ProjectID || event.WorkflowID != created.Workflow.ID {
			t.Fatalf("event = %+v, want linked project/workflow identity", event)
		}
	}
}

func TestServiceDeriveWorkflowGraphWiring(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	source, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow source: %v", err)
	}
	graph := workflowGraphDraftFromDefinition(source.Definition)
	derived, err := service.DeriveWorkflowGraphWiring(ctx, serverapi.WorkflowGraphDeriveWiringRequest{
		WorkflowID: workflowID,
		Graph:      graph,
	})
	if err != nil {
		t.Fatalf("DeriveWorkflowGraphWiring: %v", err)
	}
	if len(derived.DerivedWiring.Edges) != len(graph.Edges) {
		t.Fatalf("derived wiring edges = %+v, want one summary per draft edge", derived.DerivedWiring.Edges)
	}
}

func TestServiceWorkflowExecutionPolicyPublicRoundTrip(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	customRef := "refs/heads/release"
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{
		Name:            "Policy workflow",
		ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyCustomRef, CustomRef: &customRef},
	})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if created.Workflow.ExecutionPolicy.Mode != serverapi.WorkflowExecutionPolicyCustomRef || created.Workflow.ExecutionPolicy.CustomRef == nil || *created.Workflow.ExecutionPolicy.CustomRef != customRef {
		t.Fatalf("created policy = %+v", created.Workflow.ExecutionPolicy)
	}

	listed, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{})
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	if len(listed.Workflows) != 1 || listed.Workflows[0].ExecutionPolicy.Mode != serverapi.WorkflowExecutionPolicyCustomRef {
		t.Fatalf("listed workflow policies = %+v", listed.Workflows)
	}

	updated, err := service.UpdateWorkflow(ctx, serverapi.WorkflowUpdateRequest{
		WorkflowID:      created.Workflow.ID,
		Name:            created.Workflow.Name,
		ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyHead},
	})
	if err != nil {
		t.Fatalf("UpdateWorkflow: %v", err)
	}
	if updated.Definition.Workflow.ExecutionPolicy.Mode != serverapi.WorkflowExecutionPolicyHead || updated.Definition.Workflow.ExecutionPolicy.CustomRef != nil {
		t.Fatalf("updated policy = %+v", updated.Definition.Workflow.ExecutionPolicy)
	}

	graph := workflowGraphDraftFromDefinition(updated.Definition)
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      created.Workflow.ID,
		ExpectedVersion: updated.Definition.Workflow.Version,
		Metadata: &serverapi.WorkflowGraphMetadata{
			Name:            updated.Definition.Workflow.Name,
			Description:     updated.Definition.Workflow.Description,
			ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyDefaultBranch},
		},
		Graph: graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	if saved.Definition == nil || saved.Definition.Workflow.ExecutionPolicy.Mode != serverapi.WorkflowExecutionPolicyDefaultBranch {
		t.Fatalf("saved definition policy = %+v", saved.Definition)
	}
}

func TestServiceWorkflowGraphValidatePreviewAndSave(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	source, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow source: %v", err)
	}
	graph := workflowGraphDraftFromDefinition(source.Definition)
	validated, err := service.ValidateWorkflowGraphDraft(ctx, serverapi.WorkflowGraphValidateDraftRequest{
		WorkflowID: workflowID,
		Metadata:   &serverapi.WorkflowGraphMetadata{Name: "Draft Workflow Name", Description: source.Definition.Workflow.Description},
		Graph:      graph,
		Modes:      []serverapi.WorkflowValidationMode{serverapi.WorkflowValidationModeDraft, serverapi.WorkflowValidationModeExecution},
	})
	if err != nil {
		t.Fatalf("ValidateWorkflowGraphDraft: %v", err)
	}
	if len(validated.Results) != 2 || !validated.Results[serverapi.WorkflowValidationModeDraft].Valid || !validated.Results[serverapi.WorkflowValidationModeExecution].Valid {
		t.Fatalf("validated graph draft = %+v, want valid draft and execution results", validated)
	}
	if len(validated.DerivedWiring.Edges) != len(graph.Edges) {
		t.Fatalf("derived wiring edges = %+v, want one summary per draft edge", validated.DerivedWiring.Edges)
	}

	renamedGraph := renameWorkflowGraphDraftNode(graph, "node-agent-"+workflowID, "Preview Agent")
	renamedGraph = setWorkflowGraphDraftNodeCompletionMode(renamedGraph, "node-agent-"+workflowID, "tool")
	renamedGraph = setWorkflowGraphDraftEdgePrompt(renamedGraph, "edge-start-"+workflowID, "Saved edge prompt.")
	renamedGraph = setWorkflowGraphDraftTransitionDescription(renamedGraph, "group-start-"+workflowID, "Start implementation from the backlog.")
	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: source.Definition.Workflow.Version,
		Metadata:        &serverapi.WorkflowGraphMetadata{Name: "Preview Workflow", Description: "Preview only"},
		Graph:           renamedGraph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if !preview.CanSave || preview.ConfirmationRequired || len(preview.Blockers) != 0 {
		t.Fatalf("preview graph save = %+v, want savable preview without blockers", preview)
	}
	afterPreview, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow after preview: %v", err)
	}
	if afterPreview.Definition.Workflow.Version != source.Definition.Workflow.Version || afterPreview.Definition.Workflow.Name == "Preview Workflow" || workflowServiceNodeByID(t, afterPreview.Definition, "node-agent-"+workflowID).DisplayName == "Preview Agent" {
		t.Fatalf("preview mutated workflow definition = %+v", afterPreview.Definition)
	}

	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	workflowSub, err := service.SubscribeWorkflow(ctx, serverapi.WorkflowSubscribeRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("SubscribeWorkflow: %v", err)
	}
	defer func() { _ = workflowSub.Close() }()
	if _, err := service.SubscribeWorkflow(ctx, serverapi.WorkflowSubscribeRequest{WorkflowID: "workflow-missing"}); err == nil {
		t.Fatal("SubscribeWorkflow accepted missing workflow")
	}
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: source.Definition.Workflow.Version,
		Metadata:        &serverapi.WorkflowGraphMetadata{Name: "Saved Workflow", Description: "Saved metadata"},
		Graph:           renamedGraph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	if !saved.Saved || saved.Definition == nil || saved.CurrentVersion != source.Definition.Workflow.Version+1 {
		t.Fatalf("saved graph = %+v, want saved canonical definition with incremented version", saved)
	}
	if saved.Definition.Workflow.Name != "Saved Workflow" || saved.Definition.Workflow.Description != "Saved metadata" {
		t.Fatalf("saved workflow metadata = %+v, want combined metadata persisted", saved.Definition.Workflow)
	}
	if workflowServiceEdgeByID(t, *saved.Definition, "edge-start-"+workflowID).PromptTemplate != "Saved edge prompt." {
		t.Fatalf("saved response edge prompt = %q, want edited edge prompt", workflowServiceEdgeByID(t, *saved.Definition, "edge-start-"+workflowID).PromptTemplate)
	}
	if workflowServiceNodeByID(t, *saved.Definition, "node-agent-"+workflowID).CompletionMode != "tool" {
		t.Fatalf("saved response node completion mode = %q, want tool", workflowServiceNodeByID(t, *saved.Definition, "node-agent-"+workflowID).CompletionMode)
	}
	if workflowServiceTransitionGroupByID(t, *saved.Definition, "group-start-"+workflowID).Description != "Start implementation from the backlog." {
		t.Fatalf("saved response transition description = %q, want edited transition description", workflowServiceTransitionGroupByID(t, *saved.Definition, "group-start-"+workflowID).Description)
	}
	for _, event := range waitWorkflowProjectActions(t, sub, "workflow", "graph_saved") {
		if event.ProjectID != binding.ProjectID || event.WorkflowID != workflowID {
			t.Fatalf("event = %+v, want linked workflow event", event)
		}
	}
	eventCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	workflowEvent, err := workflowSub.Next(eventCtx)
	if err != nil {
		t.Fatalf("workflow subscription next: %v", err)
	}
	if workflowEvent.ProjectID != "" || workflowEvent.WorkflowID != workflowID || workflowEvent.Resource != "workflow" || workflowEvent.Action != "graph_saved" {
		t.Fatalf("workflow-scoped event = %+v, want graph_saved workflow event without project scope", workflowEvent)
	}
	canonical, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow canonical: %v", err)
	}
	if !reflect.DeepEqual(*saved.Definition, canonical.Definition) {
		t.Fatalf("saved definition = %+v, want canonical %+v", *saved.Definition, canonical.Definition)
	}
}

func TestServiceWorkflowGraphSaveAcceptsPreviousTargetOrNewContextSource(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	source, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow source: %v", err)
	}
	graph := workflowGraphDraftFromDefinition(source.Definition)
	graph = setWorkflowGraphDraftEdgeContext(graph, "edge-next-"+workflowID, "continue_session", serverapi.WorkflowContextSource{Kind: "previous_target_or_new"})

	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: source.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave previous_target_or_new: %v", err)
	}
	if !preview.CanSave || len(preview.Blockers) != 0 || !preview.ValidationResults[serverapi.WorkflowValidationModeExecution].Valid {
		t.Fatalf("previous_target_or_new preview = %+v, want savable valid graph", preview)
	}
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: source.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph previous_target_or_new: %v", err)
	}
	edge := workflowServiceEdgeByID(t, *saved.Definition, "edge-next-"+workflowID)
	if edge.ContextMode != "continue_session" || edge.ContextSource.Kind != "previous_target_or_new" || edge.ContextSource.NodeKey != "" {
		t.Fatalf("saved edge context = mode %q source %+v, want previous_target_or_new continuation", edge.ContextMode, edge.ContextSource)
	}
}

func TestServiceWorkflowGraphSaveDescriptionOnlyFeedsRuntimeTransitions(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	source, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow source: %v", err)
	}
	graph := workflowGraphDraftFromDefinition(source.Definition)
	description := "Use this transition when the agent has completed implementation."
	graph = setWorkflowGraphDraftTransitionDescription(graph, "group-done-"+workflowID, description)

	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: source.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph description-only: %v", err)
	}
	if !saved.Saved || saved.CurrentVersion != source.Definition.Workflow.Version+1 {
		t.Fatalf("description-only save = %+v, want saved version bump", saved)
	}

	reloaded, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow reloaded: %v", err)
	}
	if workflowServiceTransitionGroupByID(t, reloaded.Definition, "group-done-"+workflowID).Description != description {
		t.Fatalf("reloaded transition description = %q, want %q", workflowServiceTransitionGroupByID(t, reloaded.Definition, "group-done-"+workflowID).Description, description)
	}

	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	runContext, err := service.store.GetRunStartContext(ctx, workflow.RunID(started.RunID))
	if err != nil {
		t.Fatalf("GetRunStartContext: %v", err)
	}
	if len(runContext.TransitionOptions) != 1 || runContext.TransitionOptions[0].ID != "done" || runContext.TransitionOptions[0].Description != description {
		t.Fatalf("runtime transition options = %+v, want done description %q", runContext.TransitionOptions, description)
	}
}

func TestServiceWorkflowGraphSaveAllowsEmptyPromptButTaskStartRejects(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	source, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow source: %v", err)
	}
	graph := workflowGraphDraftFromDefinition(source.Definition)
	graph = setWorkflowGraphDraftEdgePrompt(graph, "edge-start-"+workflowID, "")

	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: source.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave empty prompt: %v", err)
	}
	if !preview.CanSave || len(preview.Blockers) != 0 {
		t.Fatalf("empty-prompt preview = %+v, want can save without blockers", preview)
	}
	if preview.ValidationResults[serverapi.WorkflowValidationModeDraft].Valid != true {
		t.Fatalf("empty-prompt preview draft validation = %+v, want valid", preview.ValidationResults[serverapi.WorkflowValidationModeDraft])
	}
	if preview.ValidationResults[serverapi.WorkflowValidationModeExecution].Valid {
		t.Fatalf("empty-prompt preview execution validation = %+v, want invalid", preview.ValidationResults[serverapi.WorkflowValidationModeExecution])
	}

	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: source.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph empty prompt: %v", err)
	}
	if !saved.Saved || saved.CurrentVersion != source.Definition.Workflow.Version+1 || len(saved.Blockers) != 0 {
		t.Fatalf("empty-prompt save = %+v, want saved without blockers", saved)
	}
	if saved.ValidationResults[serverapi.WorkflowValidationModeDraft].Valid != true {
		t.Fatalf("empty-prompt draft validation = %+v, want valid", saved.ValidationResults[serverapi.WorkflowValidationModeDraft])
	}
	if saved.ValidationResults[serverapi.WorkflowValidationModeExecution].Valid {
		t.Fatalf("empty-prompt execution validation = %+v, want invalid", saved.ValidationResults[serverapi.WorkflowValidationModeExecution])
	}

	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID}); err == nil {
		t.Fatalf("StartWorkflowTask empty prompt error = %v, want transition prompt required", err)
	} else {
		var validationErr workflowstore.WorkflowValidationError
		if !errors.As(err, &validationErr) || !validationErr.HasCode(workflow.CodeTransitionPromptRequired) {
			t.Fatalf("StartWorkflowTask empty prompt error = %v, want transition prompt required", err)
		}
	}
}

func TestWorkflowValidationResponsePreservesWorkflowIDFallback(t *testing.T) {
	resp := workflowValidationResponse("workflow-1", workflow.ValidationResult{Errors: []workflow.ValidationError{{
		Code:    workflow.CodeInvalidNodeKey,
		Message: "node key is invalid",
	}}})

	if len(resp.Errors) != 1 || resp.Errors[0].WorkflowID != "workflow-1" {
		t.Fatalf("validation response errors = %+v, want workflow id fallback", resp.Errors)
	}
}

func newWorkflowServiceTestService(t *testing.T) (*Service, metadata.Binding) {
	t.Helper()
	service, binding, _ := newWorkflowServiceTestServiceWithMetadata(t)
	return service, binding
}

func newWorkflowServiceTestContext(t *testing.T) (context.Context, *Service, metadata.Binding) {
	t.Helper()
	service, binding := newWorkflowServiceTestService(t)
	return context.Background(), service, binding
}

func newWorkflowServiceTestContextWithMetadata(t *testing.T) (context.Context, *Service, metadata.Binding, *metadata.Store) {
	t.Helper()
	service, binding, metadataStore := newWorkflowServiceTestServiceWithMetadata(t)
	return context.Background(), service, binding, metadataStore
}

func newWorkflowServiceTestServiceWithMetadata(t *testing.T) (*Service, metadata.Binding, *metadata.Store) {
	t.Helper()
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, filepath.Join(home, "kent-root"))
	cfg, err := config.Load(workspaceRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore, err := metadata.Open(cfg.PersistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "WOR"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	resolver := workflow.StaticRoleResolver{"coder": true}
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(resolver))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	view, err := workflowview.New(metadataStore)
	if err != nil {
		t.Fatalf("workflowview.New: %v", err)
	}
	service, err := New(store, view, resolver)
	if err != nil {
		t.Fatalf("workflowsvc.New: %v", err)
	}
	return service, binding, metadataStore
}

func stringPtr(value string) *string {
	return &value
}

func saveQueuedInitialRecoveryTarget(t *testing.T, ctx context.Context, service *Service, taskID string) {
	t.Helper()
	provisioningGeneration := "queued-recovery-provisioning"
	intendedWorktreeRoot := filepath.Join(t.TempDir(), "queued-recovery-root")
	target := workflow.ExecutionTarget{
		TaskID: workflow.TaskID(taskID),
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:     workflow.ExecutionTargetSourceNamedRef,
			NamedRef: stringPtr("refs/heads/main"),
			Commit:   "queued-recovery-commit",
		},
		State:                       workflow.ExecutionTargetStateInitialProvisioning,
		IntendedWorktreeRoot:        &intendedWorktreeRoot,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &workflow.ExecutionTargetClaim{Generation: "queued-recovery-claim-" + taskID, Phase: workflow.ExecutionTargetClaimRecoveryQueued},
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
	}
	if err := service.store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
}

func saveQueuedLockedExecutionTargetRecovery(t *testing.T, ctx context.Context, service *Service, binding metadata.Binding, task serverapi.WorkflowTaskCreateResponse) {
	t.Helper()
	worktreeRoot := filepath.Join(t.TempDir(), task.Task.ShortID)
	branchTip := "queued-recovery-commit"
	provisioningGeneration := "queued-recovery-provisioning-" + task.Task.ID
	queuedClaim := workflow.ExecutionTargetClaim{
		Generation: "queued-recovery-claim-" + task.Task.ID,
		Phase:      workflow.ExecutionTargetClaimRecoveryQueued,
	}
	target := workflow.ExecutionTarget{
		TaskID: workflow.TaskID(task.Task.ID),
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:     workflow.ExecutionTargetSourceNamedRef,
			NamedRef: stringPtr("refs/heads/main"),
			Commit:   branchTip,
		},
		State:                       workflow.ExecutionTargetStateLocked,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupPending,
		ActiveClaim:                 &queuedClaim,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryAvailable,
		ExactBranchObservation:      &branchTip,
		LinkedWorktreeOwnership: &workflow.ExecutionTargetLinkedWorktreeOwnership{
			CommonDir:  "/test/common-dir",
			AdminEntry: "worktrees/" + task.Task.ShortID,
			GitDir:     filepath.Join(worktreeRoot, ".git"),
			HeadRef:    "refs/heads/" + task.Task.ShortID,
		},
	}
	if err := service.store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget %s: %v", task.Task.ID, err)
	}
	if _, err := service.store.AttachManagedExecutionTargetWorktree(ctx, workflowstore.AttachManagedExecutionTargetWorktreeRequest{
		Target:        target,
		ExpectedClaim: queuedClaim,
		WorkspaceID:   binding.WorkspaceID,
		WorktreeRoot:  worktreeRoot,
		CreatedBranch: true,
	}); err != nil {
		t.Fatalf("AttachManagedExecutionTargetWorktree %s: %v", task.Task.ID, err)
	}
}

func saveWorkflowServiceManualRecoveryTarget(t *testing.T, ctx context.Context, service *Service, taskID string) workflow.ExecutionTarget {
	t.Helper()
	provisioningGeneration := "manual-recovery-provisioning"
	recoveryCause := workflow.ExecutionTargetRecoveryCauseAmbiguousWorktree
	target := workflow.ExecutionTarget{
		TaskID: workflow.TaskID(taskID),
		Policy: workflow.ExecutionPolicyHead,
		ResolvedSource: &workflow.ExecutionTargetResolvedSource{
			Kind:     workflow.ExecutionTargetSourceNamedRef,
			NamedRef: stringPtr("refs/heads/main"),
			Commit:   "manual-recovery-commit",
		},
		State:                       workflow.ExecutionTargetStateLocked,
		ProvisioningGeneration:      &provisioningGeneration,
		SetupProvisioningGeneration: &provisioningGeneration,
		SetupState:                  workflow.ExecutionTargetSetupFailed,
		RecoveryDisposition:         workflow.ExecutionTargetRecoveryManualRecovery,
		RecoveryCause:               &recoveryCause,
	}
	if err := service.store.SaveTaskExecutionTarget(ctx, target); err != nil {
		t.Fatalf("SaveTaskExecutionTarget: %v", err)
	}
	return target
}

func linkWorkflowServiceProject(t *testing.T, ctx context.Context, service *Service, req serverapi.WorkflowLinkProjectRequest) serverapi.WorkflowLinkProjectResponse {
	t.Helper()
	link, err := service.LinkWorkflowToProject(ctx, req)
	if err != nil {
		t.Fatalf("LinkWorkflowToProject: %v", err)
	}
	return link
}

func linkDefaultWorkflowServiceProject(t *testing.T, ctx context.Context, service *Service, projectID, workflowID string) {
	t.Helper()
	linkWorkflowServiceProject(t, ctx, service, serverapi.WorkflowLinkProjectRequest{
		ProjectID:     projectID,
		WorkflowID:    workflowID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultAlways,
	})
}

func createWorkflowServiceTask(t *testing.T, ctx context.Context, service *Service, req serverapi.WorkflowTaskCreateRequest) serverapi.WorkflowTaskCreateResponse {
	t.Helper()
	task, err := service.CreateWorkflowTask(ctx, req)
	if err != nil {
		t.Fatalf("CreateWorkflowTask: %v", err)
	}
	return task
}

func createDefaultWorkflowServiceTask(t *testing.T, ctx context.Context, service *Service, projectID string) serverapi.WorkflowTaskCreateResponse {
	t.Helper()
	return createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{ProjectID: projectID, Title: "Task", Body: "Body"})
}

func attachWorkflowServiceLegacyManagedWorktree(t *testing.T, ctx context.Context, metadataStore *metadata.Store, binding metadata.Binding, taskID string) {
	t.Helper()
	worktreeID := "legacy-worktree-" + taskID
	if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID: worktreeID, WorkspaceID: binding.WorkspaceID, CanonicalRoot: t.TempDir(), DisplayName: "legacy", Availability: "available", Managed: true,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	if _, err := metadataStore.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
		ID: taskID, ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true}, UpdatedAtUnixMs: time.Now().UTC().UnixMilli(),
	}); err != nil {
		t.Fatalf("UpdateTaskManagedWorktree: %v", err)
	}
}

func startWorkflowServiceTask(t *testing.T, ctx context.Context, service *Service, taskID string) serverapi.WorkflowTaskStartResponse {
	t.Helper()
	started, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: taskID})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if started.Started == nil {
		t.Fatalf("StartWorkflowTask result = %+v, want started", started)
	}
	return *started.Started
}

func claimAndAttachWorkflowServiceRun(t *testing.T, ctx context.Context, service *Service, metadataStore *metadata.Store, binding metadata.Binding, runID string, sessionID string) workflowstore.RunnableRunRecord {
	t.Helper()
	if _, err := metadataStore.DB().ExecContext(ctx, `INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, name, first_prompt_preview, input_draft, parent_session_id, created_at_unix_ms, updated_at_unix_ms, last_sequence, model_request_count, launch_visible, cwd_relpath, continuation_json, locked_json, usage_state_json, metadata_json) VALUES (?, ?, ?, ?, '', '', '', '', 1, 1, 0, 0, 1, '.', '{}', '{}', '{}', '{}')`, sessionID, binding.ProjectID, binding.WorkspaceID, "sessions/"+sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(runID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := service.store.AttachRunSession(ctx, workflow.RunID(runID), claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}
	return claimed
}

func createWorkflowServiceWaitingAsk(t *testing.T, ctx context.Context, service *Service, metadataStore *metadata.Store, binding metadata.Binding, title string, sessionID string, askID string) (serverapi.WorkflowTaskCreateResponse, string, string) {
	t.Helper()
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{ProjectID: binding.ProjectID, Title: title, Body: "Body"})
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	claimed := claimAndAttachWorkflowServiceRun(t, ctx, service, metadataStore, binding, started.RunID, sessionID)
	if err := service.store.SetRunWaitingAsk(ctx, workflow.RunID(started.RunID), claimed.Generation, askID); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
	return task, started.RunID, sessionID
}

func createWorkflowServiceValidWorkflow(t *testing.T, ctx context.Context, service *Service) string {
	t.Helper()
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{
		Name:            "Workflow",
		ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyNone},
	})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	startID := workflowServiceNodeIDByKind(t, def.Definition, "start")
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	if _, err := service.AddWorkflowNode(ctx, serverapi.WorkflowNodeAddRequest{WorkflowID: created.Workflow.ID, NodeID: "node-agent-" + created.Workflow.ID, Key: "agent", Kind: "agent", DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddWorkflowNode: %v", err)
	}
	agentID := "node-agent-" + created.Workflow.ID
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: "group-start-" + created.Workflow.ID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup start: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-start-" + created.Workflow.ID, TransitionGroupID: "group-start-" + created.Workflow.ID, Key: "start", TargetNodeID: agentID, ContextMode: "new_session", PromptTemplate: "Do work."}); err != nil {
		t.Fatalf("AddWorkflowEdge start: %v", err)
	}
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: "group-done-" + created.Workflow.ID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup done: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-done-" + created.Workflow.ID, TransitionGroupID: "group-done-" + created.Workflow.ID, Key: "done", TargetNodeID: doneID, ContextMode: "new_session"}); err != nil {
		t.Fatalf("AddWorkflowEdge done: %v", err)
	}
	return created.Workflow.ID
}

func setWorkflowServiceExecutionPolicy(t *testing.T, ctx context.Context, service *Service, workflowID string, mode serverapi.WorkflowExecutionPolicyMode) {
	t.Helper()
	current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if _, err := service.UpdateWorkflow(ctx, serverapi.WorkflowUpdateRequest{
		WorkflowID:      workflowID,
		Name:            current.Definition.Workflow.Name,
		Description:     current.Definition.Workflow.Description,
		ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: mode},
	}); err != nil {
		t.Fatalf("UpdateWorkflow execution policy: %v", err)
	}
}

func createWorkflowServiceWorkflowWithScriptNode(t *testing.T, ctx context.Context, service *Service, nodeID string, scriptPath string) string {
	t.Helper()
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{
		Name:            "Script Workflow",
		ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyNone},
	})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	if _, err := service.AddWorkflowNode(ctx, serverapi.WorkflowNodeAddRequest{
		WorkflowID:  created.Workflow.ID,
		NodeID:      nodeID,
		Key:         "script",
		Kind:        "script",
		DisplayName: "Script",
		ScriptPath:  stringPtr(scriptPath),
	}); err != nil {
		t.Fatalf("AddWorkflowNode script: %v", err)
	}
	return created.Workflow.ID
}

func createWorkflowServiceChainedWorkflow(t *testing.T, ctx context.Context, service *Service) string {
	t.Helper()
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{
		Name:            "Chained Workflow",
		ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyNone},
	})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	startID := workflowServiceNodeIDByKind(t, def.Definition, "start")
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	planID := "node-plan-" + created.Workflow.ID
	implementID := "node-implement-" + created.Workflow.ID
	if _, err := service.AddWorkflowNode(ctx, serverapi.WorkflowNodeAddRequest{WorkflowID: created.Workflow.ID, NodeID: planID, Key: "plan", Kind: "agent", DisplayName: "Plan", SubagentRole: "coder"}); err != nil {
		t.Fatalf("AddWorkflowNode plan: %v", err)
	}
	if _, err := service.AddWorkflowNode(ctx, serverapi.WorkflowNodeAddRequest{WorkflowID: created.Workflow.ID, NodeID: implementID, Key: "implement", Kind: "agent", DisplayName: "Implement", SubagentRole: "coder"}); err != nil {
		t.Fatalf("AddWorkflowNode implement: %v", err)
	}
	startGroup := "group-start-" + created.Workflow.ID
	nextGroup := "group-next-" + created.Workflow.ID
	doneGroup := "group-done-" + created.Workflow.ID
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: startGroup, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup start: %v", err)
	}
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: nextGroup, SourceNodeID: planID, TransitionID: "next", DisplayName: "Next"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup next: %v", err)
	}
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: doneGroup, SourceNodeID: implementID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup done: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-start-" + created.Workflow.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, ContextMode: "new_session", PromptTemplate: "Plan work."}); err != nil {
		t.Fatalf("AddWorkflowEdge start: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-next-" + created.Workflow.ID, TransitionGroupID: nextGroup, Key: "next", TargetNodeID: implementID, ContextMode: "new_session", PromptTemplate: "Implement {{.Params.prior_summary}}.", Parameters: []serverapi.WorkflowParameter{{Key: "prior_summary", Description: "Prior summary."}}}); err != nil {
		t.Fatalf("AddWorkflowEdge next: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-done-" + created.Workflow.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: doneID, ContextMode: "new_session"}); err != nil {
		t.Fatalf("AddWorkflowEdge done: %v", err)
	}
	return created.Workflow.ID
}

func createWorkflowServiceScriptWorkflow(t *testing.T, ctx context.Context, service *Service, scriptPath string) string {
	t.Helper()
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{
		Name:            "Script Workflow",
		ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyNone},
	})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	startID := workflowServiceNodeIDByKind(t, def.Definition, "start")
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	scriptID := "node-script-" + created.Workflow.ID
	if _, err := service.AddWorkflowNode(ctx, serverapi.WorkflowNodeAddRequest{WorkflowID: created.Workflow.ID, NodeID: scriptID, Key: "script", Kind: "script", DisplayName: "Script", ScriptPath: &scriptPath}); err != nil {
		t.Fatalf("AddWorkflowNode script: %v", err)
	}
	startGroup := "group-start-" + created.Workflow.ID
	doneGroup := "group-done-" + created.Workflow.ID
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: startGroup, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup start: %v", err)
	}
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: created.Workflow.ID, GroupID: doneGroup, SourceNodeID: scriptID, TransitionID: "done", DisplayName: "Done"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup done: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-start-" + created.Workflow.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: scriptID, ContextMode: "new_session"}); err != nil {
		t.Fatalf("AddWorkflowEdge start: %v", err)
	}
	if _, err := service.AddWorkflowEdge(ctx, serverapi.WorkflowEdgeAddRequest{WorkflowID: created.Workflow.ID, EdgeID: "edge-done-" + created.Workflow.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: doneID, ContextMode: "new_session"}); err != nil {
		t.Fatalf("AddWorkflowEdge done: %v", err)
	}
	return created.Workflow.ID
}

func createWorkflowServiceFanoutScriptApprovalWorkflow(t *testing.T, ctx context.Context, service *Service) string {
	t.Helper()
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{
		Name:            "Fan-out Script Approval Workflow",
		ExecutionPolicy: &serverapi.WorkflowExecutionPolicy{Mode: serverapi.WorkflowExecutionPolicyNone},
	})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	startID := workflowServiceNodeIDByKind(t, def.Definition, "start")
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	agentID := "node-agent-" + created.Workflow.ID
	validScriptID := "node-valid-script-" + created.Workflow.ID
	missingScriptID := "node-missing-script-" + created.Workflow.ID
	joinID := "node-join-" + created.Workflow.ID
	for _, node := range []serverapi.WorkflowNodeAddRequest{
		{WorkflowID: created.Workflow.ID, NodeID: agentID, Key: "agent", Kind: "agent", DisplayName: "Agent", SubagentRole: "coder", PromptTemplate: "Prepare work."},
		{WorkflowID: created.Workflow.ID, NodeID: validScriptID, Key: "valid_script", Kind: "script", DisplayName: "Valid script", ScriptPath: stringPtr("/bin/sh")},
		{WorkflowID: created.Workflow.ID, NodeID: missingScriptID, Key: "missing_script", Kind: "script", DisplayName: "Missing script", ScriptPath: stringPtr("scripts/missing")},
		{WorkflowID: created.Workflow.ID, NodeID: joinID, Key: "join", Kind: "join", DisplayName: "Join"},
	} {
		if _, err := service.AddWorkflowNode(ctx, node); err != nil {
			t.Fatalf("AddWorkflowNode %s: %v", node.Key, err)
		}
	}
	startGroup := "group-start-" + created.Workflow.ID
	splitGroup := "group-split-" + created.Workflow.ID
	validJoinGroup := "group-valid-join-" + created.Workflow.ID
	missingJoinGroup := "group-missing-join-" + created.Workflow.ID
	doneGroup := "group-join-done-" + created.Workflow.ID
	for _, group := range []serverapi.WorkflowTransitionGroupAddRequest{
		{WorkflowID: created.Workflow.ID, GroupID: startGroup, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		{WorkflowID: created.Workflow.ID, GroupID: splitGroup, SourceNodeID: agentID, TransitionID: "split", DisplayName: "Split"},
		{WorkflowID: created.Workflow.ID, GroupID: validJoinGroup, SourceNodeID: validScriptID, TransitionID: "join", DisplayName: "Join"},
		{WorkflowID: created.Workflow.ID, GroupID: missingJoinGroup, SourceNodeID: missingScriptID, TransitionID: "join", DisplayName: "Join"},
		{WorkflowID: created.Workflow.ID, GroupID: doneGroup, SourceNodeID: joinID, TransitionID: "done", DisplayName: "Done"},
	} {
		if _, err := service.AddWorkflowTransitionGroup(ctx, group); err != nil {
			t.Fatalf("AddWorkflowTransitionGroup %s: %v", group.TransitionID, err)
		}
	}
	for _, edge := range []serverapi.WorkflowEdgeAddRequest{
		{WorkflowID: created.Workflow.ID, EdgeID: "edge-start-" + created.Workflow.ID, TransitionGroupID: startGroup, Key: "start", TargetNodeID: agentID, ContextMode: "new_session", PromptTemplate: "Prepare work."},
		{WorkflowID: created.Workflow.ID, EdgeID: "edge-split-valid-" + created.Workflow.ID, TransitionGroupID: splitGroup, Key: "split_valid", TargetNodeID: validScriptID, RequiresApproval: true, ContextMode: "new_session"},
		{WorkflowID: created.Workflow.ID, EdgeID: "edge-split-missing-" + created.Workflow.ID, TransitionGroupID: splitGroup, Key: "split_missing", TargetNodeID: missingScriptID, RequiresApproval: true, ContextMode: "new_session"},
		{WorkflowID: created.Workflow.ID, EdgeID: "edge-valid-join-" + created.Workflow.ID, TransitionGroupID: validJoinGroup, Key: "join_valid", TargetNodeID: joinID, ContextMode: "new_session"},
		{WorkflowID: created.Workflow.ID, EdgeID: "edge-missing-join-" + created.Workflow.ID, TransitionGroupID: missingJoinGroup, Key: "join_missing", TargetNodeID: joinID, ContextMode: "new_session"},
		{WorkflowID: created.Workflow.ID, EdgeID: "edge-join-done-" + created.Workflow.ID, TransitionGroupID: doneGroup, Key: "done", TargetNodeID: doneID, ContextMode: "new_session"},
	} {
		if _, err := service.AddWorkflowEdge(ctx, edge); err != nil {
			t.Fatalf("AddWorkflowEdge %s: %v", edge.Key, err)
		}
	}
	return created.Workflow.ID
}

func createWorkflowServicePendingFanoutApproval(t *testing.T, ctx context.Context, service *Service, taskID string) workflowstore.CompleteRunResult {
	t.Helper()
	started, err := service.store.StartTask(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("StartTask source: %v", err)
	}
	pending, err := service.store.CompleteRun(ctx, workflowstore.CompleteRunRequest{
		RunID:        started.RunID,
		TransitionID: "split",
	})
	if err != nil {
		t.Fatalf("CompleteRun fan-out source: %v", err)
	}
	if pending.State != "pending_approval" || len(pending.PlacementIDs) != 0 || len(pending.RunIDs) != 0 {
		t.Fatalf("pending fan-out approval = %+v, want unapplied fan-out", pending)
	}
	return pending
}

func workflowServiceNodeIDByKind(t *testing.T, def serverapi.WorkflowDefinition, kind string) string {
	t.Helper()
	for _, node := range def.Nodes {
		if node.Kind == kind {
			return node.ID
		}
	}
	t.Fatalf("missing node kind %q in %+v", kind, def.Nodes)
	return ""
}

func workflowServiceNodeByID(t *testing.T, def serverapi.WorkflowDefinition, nodeID string) serverapi.WorkflowNode {
	t.Helper()
	for _, node := range def.Nodes {
		if node.ID == nodeID {
			return node
		}
	}
	t.Fatalf("missing node %q in %+v", nodeID, def.Nodes)
	return serverapi.WorkflowNode{}
}

func workflowServiceEdgeByID(t *testing.T, def serverapi.WorkflowDefinition, edgeID string) serverapi.WorkflowEdge {
	t.Helper()
	for _, edge := range def.Edges {
		if edge.ID == edgeID {
			return edge
		}
	}
	t.Fatalf("missing edge %q in %+v", edgeID, def.Edges)
	return serverapi.WorkflowEdge{}
}

func workflowServiceTransitionGroupByID(t *testing.T, def serverapi.WorkflowDefinition, groupID string) serverapi.WorkflowTransitionGroup {
	t.Helper()
	for _, group := range def.TransitionGroups {
		if group.ID == groupID {
			return group
		}
	}
	t.Fatalf("missing transition group %q in %+v", groupID, def.TransitionGroups)
	return serverapi.WorkflowTransitionGroup{}
}

func workflowGraphDraftFromDefinition(def serverapi.WorkflowDefinition) serverapi.WorkflowGraphDraft {
	graph := serverapi.WorkflowGraphDraft{
		NodeGroups:       make([]serverapi.WorkflowGraphDraftNodeGroup, 0, len(def.NodeGroups)),
		Nodes:            make([]serverapi.WorkflowGraphDraftNode, 0, len(def.Nodes)),
		TransitionGroups: make([]serverapi.WorkflowGraphDraftTransitionGroup, 0, len(def.TransitionGroups)),
		Edges:            make([]serverapi.WorkflowGraphDraftEdge, 0, len(def.Edges)),
	}
	for _, group := range def.NodeGroups {
		graph.NodeGroups = append(graph.NodeGroups, serverapi.WorkflowGraphDraftNodeGroup{ID: group.GroupID, Key: group.GroupKey, DisplayName: group.DisplayName})
	}
	for _, node := range def.Nodes {
		graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{ID: node.ID, Key: node.Key, Kind: node.Kind, DisplayName: node.DisplayName, GroupID: node.GroupID, GroupKey: node.GroupKey, SubagentRole: node.SubagentRole, PromptTemplate: node.PromptTemplate, CompletionMode: node.CompletionMode, ScriptPath: node.ScriptPath, InputFields: node.InputFields, JoinInputProviders: node.JoinInputProviders})
	}
	for _, group := range def.TransitionGroups {
		graph.TransitionGroups = append(graph.TransitionGroups, serverapi.WorkflowGraphDraftTransitionGroup{ID: group.ID, SourceNodeID: group.SourceNodeID, TransitionID: group.TransitionID, DisplayName: group.DisplayName, Description: group.Description})
	}
	for _, edge := range def.Edges {
		graph.Edges = append(graph.Edges, serverapi.WorkflowGraphDraftEdge{ID: edge.ID, TransitionGroupID: edge.TransitionGroupID, Key: edge.Key, TargetNodeID: edge.TargetNodeID, RequiresApproval: edge.RequiresApproval, ContextMode: edge.ContextMode, ContextSource: edge.ContextSource, PromptTemplate: edge.PromptTemplate, Parameters: edge.Parameters})
	}
	return graph
}

func renameWorkflowGraphDraftNode(graph serverapi.WorkflowGraphDraft, nodeID string, displayName string) serverapi.WorkflowGraphDraft {
	renamed := graph
	renamed.Nodes = make([]serverapi.WorkflowGraphDraftNode, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if node.ID == nodeID {
			node.DisplayName = displayName
		}
		renamed.Nodes = append(renamed.Nodes, node)
	}
	return renamed
}

func setWorkflowGraphDraftNodeCompletionMode(graph serverapi.WorkflowGraphDraft, nodeID string, completionMode string) serverapi.WorkflowGraphDraft {
	updated := graph
	updated.Nodes = make([]serverapi.WorkflowGraphDraftNode, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		if node.ID == nodeID {
			node.CompletionMode = completionMode
		}
		updated.Nodes = append(updated.Nodes, node)
	}
	return updated
}

func setWorkflowGraphDraftEdgePrompt(graph serverapi.WorkflowGraphDraft, edgeID string, promptTemplate string) serverapi.WorkflowGraphDraft {
	updated := graph
	updated.Edges = make([]serverapi.WorkflowGraphDraftEdge, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		if edge.ID == edgeID {
			edge.PromptTemplate = promptTemplate
		}
		updated.Edges = append(updated.Edges, edge)
	}
	return updated
}

func setWorkflowGraphDraftEdgeContext(graph serverapi.WorkflowGraphDraft, edgeID string, contextMode string, contextSource serverapi.WorkflowContextSource) serverapi.WorkflowGraphDraft {
	updated := graph
	updated.Edges = make([]serverapi.WorkflowGraphDraftEdge, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		if edge.ID == edgeID {
			edge.ContextMode = contextMode
			edge.ContextSource = contextSource
		}
		updated.Edges = append(updated.Edges, edge)
	}
	return updated
}

func setWorkflowGraphDraftTransitionDescription(graph serverapi.WorkflowGraphDraft, groupID string, description string) serverapi.WorkflowGraphDraft {
	updated := graph
	updated.TransitionGroups = make([]serverapi.WorkflowGraphDraftTransitionGroup, 0, len(graph.TransitionGroups))
	for _, group := range graph.TransitionGroups {
		if group.ID == groupID {
			group.Description = description
		}
		updated.TransitionGroups = append(updated.TransitionGroups, group)
	}
	return updated
}

func workflowServiceNodeIDByKey(t *testing.T, def serverapi.WorkflowDefinition, key string) string {
	t.Helper()
	for _, node := range def.Nodes {
		if node.Key == key {
			return node.ID
		}
	}
	t.Fatalf("missing node key %q in %+v", key, def.Nodes)
	return ""
}

func sameStringSet(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	if len(values) != len(left) {
		return false
	}
	for _, value := range right {
		if _, ok := values[value]; !ok {
			return false
		}
		delete(values, value)
	}
	return len(values) == 0
}
