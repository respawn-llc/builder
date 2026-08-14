package workflowsvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
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
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/server/workflowview"
	"core/server/worktree"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/worktreecontract"
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

func waitWorkflowProjectActions(t *testing.T, sub serverapi.WorkflowProjectSubscription, resource serverapi.WorkflowProjectEventResource, expected ...serverapi.WorkflowProjectEventAction) []serverapi.WorkflowProjectEvent {
	t.Helper()
	remaining := make(map[serverapi.WorkflowProjectEventAction]bool, len(expected))
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

var workflowServiceGraphEntityIDs sync.Map

func workflowServiceGraphEntityID(alias string) string {
	if _, err := runtimeids.GraphEntityIDBlob(alias); err == nil {
		return alias
	}
	generated, _ := workflowServiceGraphEntityIDs.LoadOrStore(alias, runtimeids.NewGraphEntityID())
	return generated.(string)
}

func TestServiceCreatesValidatesLinksAndStartsDefaultWorkflowTask(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)

	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	startID := workflowServiceNodeIDByKind(t, def.Definition, "start")
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	agentID := workflowServiceGraphEntityID("node-agent")
	startGroupID := workflowServiceGraphEntityID("group-start")
	doneGroupID := workflowServiceGraphEntityID("group-done")
	startEdgeID := workflowServiceGraphEntityID("edge-start")
	doneEdgeID := workflowServiceGraphEntityID("edge-done")
	graph := serverapi.WorkflowGraphDraftFromDefinition(def.Definition)
	graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
		ID: agentID, Key: "agent", Kind: "agent", DisplayName: "Agent", SubagentRole: "coder",
	})
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: startGroupID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: doneGroupID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		serverapi.WorkflowGraphDraftEdge{ID: startEdgeID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: agentID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session", PromptTemplate: "Do work."},
		serverapi.WorkflowGraphDraftEdge{ID: doneEdgeID, TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: doneID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session"},
	)
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: created.Workflow.ID, ExpectedVersion: def.Definition.Workflow.Version, Graph: graph,
	})
	if err != nil || !saved.Saved {
		t.Fatalf("SaveWorkflowGraph fixture = %+v, err = %v", saved, err)
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
	if len(started.CurrentNodes) != 1 || strings.TrimSpace(started.CurrentNodes[0].NodeID) == "" {
		t.Fatalf("start response = %+v, want one Current Node", started)
	}
	_, err = service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	var conflict *serverapi.WorkflowTaskStartConflictError
	if !errors.As(err, &conflict) ||
		conflict.TaskID != task.Task.ID ||
		conflict.Reason != serverapi.WorkflowTaskStartConflictAlreadyStarted {
		t.Fatalf("StartWorkflowTask error = %T %+v, want public already-started conflict", err, err)
	}
}

func TestServiceValidateWorkflowScriptPathReportsMissingPath(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	scriptNodeID := workflowServiceGraphEntityID("node-script")
	workflowID := createWorkflowServiceWorkflowWithScriptNode(t, ctx, service, scriptNodeID, "scripts/run")

	validated, err := service.ValidateWorkflowScriptPath(ctx, serverapi.WorkflowScriptPathValidateRequest{
		WorkflowID: workflowID,
		NodeID:     scriptNodeID,
		ScriptPath: "",
	})
	if err != nil {
		t.Fatalf("ValidateWorkflowScriptPath: %v", err)
	}
	if validated.Valid || len(validated.Errors) != 1 {
		t.Fatalf("validation = %+v, want one blocking missing-path diagnostic", validated)
	}
	got := validated.Errors[0]
	if got.Code != workflowscript.CodeMissingPath || got.WorkflowID == nil || *got.WorkflowID != workflowID || got.NodeID == nil || *got.NodeID != scriptNodeID || !got.BlocksContext {
		t.Fatalf("validation error = %+v, want blocking missing-path diagnostic scoped to script node", got)
	}
}

func TestServiceListWorkflowTasksValidatesAndDelegates(t *testing.T) {
	ctx, service, projectID, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)

	blankProjectID := " "
	if _, err := service.ListWorkflowTasks(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone}, ProjectID: &blankProjectID}); !isWorkflowServiceRequestFieldError(err, "project_id") {
		t.Fatalf("blank project error = %#v, want project_id validation", err)
	}
	resp, err := service.ListWorkflowTasks(ctx, serverapi.WorkflowTaskListRequest{
		LabelFilter: serverapi.WorkflowTaskLabelFilter{Kind: serverapi.WorkflowTaskLabelFilterKindNone},
		ProjectID:   &projectID,
		WorkflowID:  &workflowID,
	})
	if err != nil {
		t.Fatalf("ListWorkflowTasks: %v", err)
	}
	if resp.Scope.WorkflowID == nil || *resp.Scope.WorkflowID != workflowID || len(resp.Tasks) != 1 || resp.Tasks[0].TaskID != taskID {
		t.Fatalf("task list response = %+v, want workflow %s task %s", resp, workflowID, taskID)
	}
}

func TestServiceCommentMutationsUpdateActivityAndPublishInvalidations(t *testing.T) {
	ctx, service, projectID, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: projectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	added, err := service.AddWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentAddRequest{TaskID: taskID, Body: "first", Author: "user", AuthorID: "nek"})
	if err != nil {
		t.Fatalf("AddWorkflowTaskComment: %v", err)
	}
	if added.Comment.CreatedAtUnixMs == 0 || added.Comment.UpdatedAt == 0 {
		t.Fatalf("added comment missing timestamps: %+v", added.Comment)
	}
	if err := service.ReplaceWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentReplaceRequest{CommentID: added.Comment.ID, Body: "updated"}); err != nil {
		t.Fatalf("ReplaceWorkflowTaskComment: %v", err)
	}
	activity, err := service.ListWorkflowTaskActivity(ctx, serverapi.WorkflowTaskOffsetPageRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("ListWorkflowTaskActivity: %v", err)
	}
	if len(activity.Items) == 0 || activity.Items[0].Type != "comment" || activity.Items[0].Comment == nil || activity.Items[0].Comment.Body != "updated" {
		t.Fatalf("activity after replace = %+v", activity.Items)
	}
	if err := service.DeleteWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentDeleteRequest{CommentID: added.Comment.ID}); err != nil {
		t.Fatalf("DeleteWorkflowTaskComment: %v", err)
	}
	activity, err = service.ListWorkflowTaskActivity(ctx, serverapi.WorkflowTaskOffsetPageRequest{TaskID: taskID})
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

func TestServiceTaskCommentListPaginatesOffsetWindows(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	for _, body := range []string{"first", "second", "third"} {
		if _, err := service.AddWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentAddRequest{
			TaskID: taskID,
			Body:   body,
			Author: "user",
		}); err != nil {
			t.Fatalf("AddWorkflowTaskComment %q: %v", body, err)
		}
	}
	offset := 0
	limit := 2
	first, err := service.ListWorkflowTaskComments(ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: taskID,
		Offset: &offset,
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("ListWorkflowTaskComments first page: %v", err)
	}
	if len(first.Items) != 2 || first.NextOffset == nil || *first.NextOffset != 2 || first.TotalCount != 3 {
		t.Fatalf("first comment page = %+v", first)
	}
	second, err := service.ListWorkflowTaskComments(ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: taskID,
		Offset: first.NextOffset,
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("ListWorkflowTaskComments continued page: %v", err)
	}
	if len(second.Items) != 1 || second.NextOffset != nil || second.TotalCount != 3 {
		t.Fatalf("continued comment page = %+v", second)
	}
	beyondEnd := 3
	empty, err := service.ListWorkflowTaskComments(ctx, serverapi.WorkflowTaskOffsetPageRequest{
		TaskID: taskID,
		Offset: &beyondEnd,
		Limit:  &limit,
	})
	if err != nil {
		t.Fatalf("ListWorkflowTaskComments beyond end: %v", err)
	}
	if len(empty.Items) != 0 || empty.NextOffset != nil || empty.TotalCount != 3 {
		t.Fatalf("beyond-end comment page = %+v", empty)
	}
	negativeOffset := -1
	zeroLimit := 0
	aboveLimit := serverapi.WorkflowPaginationMaxLimit + 1
	for _, tt := range []struct {
		name   string
		offset *int
		limit  *int
		field  string
	}{
		{name: "negative offset", offset: &negativeOffset, limit: &limit, field: "offset"},
		{name: "zero limit", offset: &offset, limit: &zeroLimit, field: "limit"},
		{name: "limit above maximum", offset: &offset, limit: &aboveLimit, field: "limit"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service.ListWorkflowTaskComments(ctx, serverapi.WorkflowTaskOffsetPageRequest{
				TaskID: taskID,
				Offset: tt.offset,
				Limit:  tt.limit,
			})
			if !isWorkflowServiceRequestFieldError(err, tt.field) {
				t.Fatalf("ListWorkflowTaskComments error = %T %v, want %s validation", err, err, tt.field)
			}
		})
	}
}

func TestServiceTaskStartValidatesCurrentGraph(t *testing.T) {
	ctx, service, _, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	startID := workflowServiceNodeIDByKind(t, def.Definition, "start")
	agentID := workflowServiceNodeIDByKey(t, def.Definition, "agent")
	graph := serverapi.WorkflowGraphDraftFromDefinition(def.Definition)
	invalidGroupID := workflowServiceGraphEntityID("group-invalid")
	graph.TransitionGroups = append(graph.TransitionGroups, serverapi.WorkflowGraphDraftTransitionGroup{
		ID: invalidGroupID, SourceNodeID: startID, TransitionID: "invalid", DisplayName: "Invalid",
	})
	graph.Edges = append(graph.Edges, workflowGraphAtomicEdge(workflowServiceGraphEntityID("edge-invalid"), invalidGroupID, "invalid", agentID, "Invalid."))
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: workflowID, ExpectedVersion: def.Definition.Workflow.Version, Graph: graph,
	})
	if err != nil || !saved.Saved {
		t.Fatalf("SaveWorkflowGraph invalid execution draft = %+v, err = %v", saved, err)
	}
	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: taskID}); err == nil {
		t.Fatalf("expected current graph validation error, got %v", err)
	} else {
		var validationErr workflowstore.WorkflowValidationError
		if !errors.As(err, &validationErr) || !validationErr.HasCode(workflow.CodeInvalidStartOutgoingShape) {
			t.Fatalf("expected current graph validation error, got %v", err)
		}
	}
}

func TestServiceTaskStartRequiresSelectionWithoutApplyingAction(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           taskID,
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowTaskActionOutcomeSelectionRequired ||
		response.Applied != nil ||
		response.SelectionRequired == nil ||
		response.SelectionRequired.Reason != serverapi.WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection {
		t.Fatalf("start response = %+v, want policy selection requirement", response)
	}
}

func TestServiceManualMoveExecutableSelectsTargetThenStartsCurrentNode(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	targetNodeID := workflowServiceNodeIDByKey(t, definition.Definition, "plan")
	execution := newManualMoveExecutionStub(service)
	execution.manualMoveAssignments = workflowServiceTestManualMoveAssignments(t, metadataStore)
	service.currentNodeExecution = execution

	selectionRequired, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:       task.Task.ID,
		TargetNodeID: targetNodeID,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask selection: %v", err)
	}
	if selectionRequired.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired ||
		selectionRequired.Applied != nil ||
		selectionRequired.SelectionRequired == nil ||
		selectionRequired.SelectionRequired.Reason != serverapi.WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection {
		t.Fatalf("selection response = %+v, want execution-target selection", selectionRequired)
	}
	if len(execution.interruptTaskIDs) != 0 {
		t.Fatalf("interruptions before execution-target selection = %v, want none", execution.interruptTaskIDs)
	}

	applied, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:       task.Task.ID,
		TargetNodeID: targetNodeID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask retry: %v", err)
	}
	if applied.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied ||
		applied.Applied == nil ||
		len(applied.Applied.CurrentNodes) != 1 ||
		applied.Applied.CurrentNodes[0].NodeID != targetNodeID {
		t.Fatalf("applied move response = %+v, want started target Current Node", applied)
	}
	if len(execution.started) != 1 || execution.started[0].NodeID != workflow.NodeID(targetNodeID) {
		t.Fatalf("execution starts = %+v, want target Current Node", execution.started)
	}
	if len(execution.interruptTaskIDs) != 1 || execution.interruptTaskIDs[0] != workflow.TaskID(task.Task.ID) {
		t.Fatalf("interruptions after target selection = %v, want selected task", execution.interruptTaskIDs)
	}
}

func TestServicePreviewManualMoveMapsOutcomesAndLiveBlockers(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	execution := newManualMoveExecutionStub(service)
	service.currentNodeExecution = execution
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	currentNodeID := started.CurrentNodes[0].NodeID
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	terminalID := workflowServiceNodeIDByKind(t, definition.Definition, "terminal")
	implementID := workflowServiceNodeIDByKey(t, definition.Definition, "implement")

	noOp, err := service.PreviewWorkflowTaskMove(ctx, serverapi.WorkflowTaskMovePreviewRequest{
		TaskID: task.Task.ID, TargetNodeID: currentNodeID,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowTaskMove no-op: %v", err)
	}
	if noOp.Outcome != serverapi.WorkflowTaskMovePreviewOutcomeNoOp ||
		noOp.NoOp == nil || len(noOp.NoOp.CurrentNodes) != 1 ||
		noOp.NoOp.CurrentNodes[0].NodeID != currentNodeID || execution.dispositionCalls != 0 {
		t.Fatalf("no-op preview = %+v, disposition calls = %d", noOp, execution.dispositionCalls)
	}

	direct, err := service.PreviewWorkflowTaskMove(ctx, serverapi.WorkflowTaskMovePreviewRequest{
		TaskID: task.Task.ID, TargetNodeID: terminalID,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowTaskMove direct: %v", err)
	}
	if direct.Outcome != serverapi.WorkflowTaskMovePreviewOutcomeDirect || direct.Direct == nil {
		t.Fatalf("direct preview = %+v", direct)
	}

	transition, err := service.PreviewWorkflowTaskMove(ctx, serverapi.WorkflowTaskMovePreviewRequest{
		TaskID: task.Task.ID, TargetNodeID: implementID,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowTaskMove transition: %v", err)
	}
	if transition.Outcome != serverapi.WorkflowTaskMovePreviewOutcomeTransition ||
		transition.Transition == nil || len(transition.Transition.Choices) != 1 ||
		transition.Transition.Choices[0].TransitionKey != "next" ||
		transition.Transition.Choices[0].SourceNodeDisplayName != "Plan" {
		t.Fatalf("transition preview = %+v", transition)
	}

	execution.disposition = workflowexecution.ManualMoveDispositionAutoInterruptible
	interruptible, err := service.PreviewWorkflowTaskMove(ctx, serverapi.WorkflowTaskMovePreviewRequest{
		TaskID: task.Task.ID, TargetNodeID: terminalID,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowTaskMove auto-interruptible: %v", err)
	}
	if interruptible.Outcome != serverapi.WorkflowTaskMovePreviewOutcomeDirect ||
		interruptible.Direct == nil {
		t.Fatalf("auto-interruptible preview = %+v, want direct move", interruptible)
	}
	execution.disposition = workflowexecution.ManualMoveDispositionLifecycleConflict
	blocked, err := service.PreviewWorkflowTaskMove(ctx, serverapi.WorkflowTaskMovePreviewRequest{
		TaskID: task.Task.ID, TargetNodeID: terminalID,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowTaskMove lifecycle conflict: %v", err)
	}
	if blocked.Outcome != serverapi.WorkflowTaskMovePreviewOutcomeBlocked ||
		blocked.Blocked == nil ||
		blocked.Blocked.Reason != serverapi.WorkflowTaskMovePreviewBlockerLifecycleConflict {
		t.Fatalf("lifecycle-conflict preview = %+v", blocked)
	}
}

func TestServiceManualMoveNoOpSkipsInterruptionAttentionAndEvent(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	execution := newManualMoveExecutionStub(service)
	service.currentNodeExecution = execution
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	recorder := &workflowAttentionRecorder{}
	service.attentionFinalizer = recorder
	subscription, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{
		ProjectID: binding.ProjectID,
	})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = subscription.Close() }()

	moved, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID: task.Task.ID, TargetNodeID: started.CurrentNodes[0].NodeID,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask no-op: %v", err)
	}
	if err := moved.Validate(); err != nil {
		t.Fatalf("no-op response validation: %v", err)
	}
	if moved.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeNoOp ||
		moved.NoOp == nil || len(moved.NoOp.CurrentNodes) != 1 ||
		moved.NoOp.CurrentNodes[0].NodeID != started.CurrentNodes[0].NodeID {
		t.Fatalf("no-op move response = %+v", moved)
	}
	if len(execution.interruptTaskIDs) != 0 {
		t.Fatalf("no-op interruption calls = %v, want none", execution.interruptTaskIDs)
	}
	if len(recorder.resolutions) != 0 {
		t.Fatalf("no-op attention resolutions = %+v, want none", recorder.resolutions)
	}
	eventContext, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := subscription.Next(eventContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("no-op event = %v, want context deadline", err)
	}
}

func TestServiceManualMoveStaleFinalRevalidationReturnsNoOpWithoutSideEffects(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	execution := newManualMoveExecutionStub(service)
	service.currentNodeExecution = execution
	startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	terminalID := workflowServiceNodeIDByKind(t, definition.Definition, "terminal")
	execution.started = nil
	recorder := &workflowAttentionRecorder{}
	service.attentionFinalizer = recorder
	subscription, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{
		ProjectID: binding.ProjectID,
	})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = subscription.Close() }()
	execution.interruptHook = func() {
		if _, err := service.store.ManualMoveTask(ctx, workflowstore.ManualMoveRequest{
			TaskID:       workflow.TaskID(task.Task.ID),
			TargetNodeID: workflow.NodeID(terminalID),
		}); err != nil {
			t.Errorf("stale move setup: %v", err)
		}
	}

	moved, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID: task.Task.ID, TargetNodeID: terminalID,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask stale no-op: %v", err)
	}
	if err := moved.Validate(); err != nil {
		t.Fatalf("stale no-op response validation: %v", err)
	}
	if moved.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeNoOp ||
		moved.NoOp == nil || len(moved.NoOp.CurrentNodes) != 1 ||
		moved.NoOp.CurrentNodes[0].NodeID != terminalID {
		t.Fatalf("stale no-op response = %+v", moved)
	}
	if len(execution.started) != 0 {
		t.Fatalf("stale no-op explicit starts = %v, want none", execution.started)
	}
	if len(execution.interruptTaskIDs) != 0 {
		t.Fatalf("stale no-op interruption calls = %v, want none", execution.interruptTaskIDs)
	}
	if len(recorder.resolutions) != 0 {
		t.Fatalf("stale no-op attention resolutions = %+v, want none", recorder.resolutions)
	}
	eventContext, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := subscription.Next(eventContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stale no-op event = %v, want context deadline", err)
	}
}

func TestServiceManualMoveApprovalAppliesImmediately(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	requireWorkflowServiceEdgeApproval(t, ctx, service, workflowID, "next")
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	targetNodeID := workflowServiceNodeIDByKey(t, definition.Definition, "implement")
	execution := newManualMoveExecutionStub(service)
	execution.manualMoveAssignments = workflowServiceTestManualMoveAssignments(t, metadataStore)
	service.currentNodeExecution = execution
	startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	execution.started = nil

	moved, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:       task.Task.ID,
		TargetNodeID: targetNodeID,
		Values:       map[string]map[string]string{"plan": {"prior_summary": "manual plan"}},
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if err := moved.Validate(); err != nil {
		t.Fatalf("MoveWorkflowTask response validation: %v", err)
	}
	if moved.Applied == nil ||
		len(moved.Applied.CurrentNodes) != 1 ||
		moved.Applied.CurrentNodes[0].NodeID != targetNodeID {
		t.Fatalf("move response = %+v, want target Current Node", moved)
	}
	if len(execution.started) != 1 || execution.started[0].NodeID != workflow.NodeID(targetNodeID) {
		t.Fatalf("execution starts = %+v, want target Current Node", execution.started)
	}
	approvals, err := service.store.ListPendingApprovals(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("pending Approvals = %+v, want none after manual Approval", approvals)
	}
}

func TestServiceManualMoveRevalidatesTaskQuiescenceBeforeDurableApply(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	targetNodeID := workflowServiceNodeIDByKey(t, definition.Definition, "plan")
	execution := newManualMoveExecutionStub(service)
	execution.quiescentErrors = []error{workflowexecution.ErrTaskExecutionNotQuiescent}
	service.currentNodeExecution = execution

	_, err = service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		TaskID:       task.Task.ID,
		TargetNodeID: targetNodeID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if !errors.Is(err, workflowexecution.ErrTaskExecutionNotQuiescent) {
		t.Fatalf("MoveWorkflowTask quiescence error = %v, want %v", err, workflowexecution.ErrTaskExecutionNotQuiescent)
	}
	currentNodes, err := service.store.ListCurrentNodes(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	if len(currentNodes) != 1 || currentNodes[0].Reference.NodeID == workflow.NodeID(targetNodeID) {
		t.Fatalf("current nodes after rejected move = %+v, want original backlog Current Node", currentNodes)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil {
		t.Fatalf("rejected move execution target = %+v, want target unlocked", targetContext.Task.ExecutionTarget)
	}
	if len(execution.quiescentTaskIDs) != 1 {
		t.Fatalf("quiescence checks = %v, want durable revalidation only", execution.quiescentTaskIDs)
	}
}

func TestServiceGraphMutationsUseStoreEditPolicyInsteadOfTaskWideQuiescence(t *testing.T) {
	service, binding, _ := newWorkflowServiceTestServiceWithRoleResolver(t, testsetup.QuestionsEnabled("coder", "explorer"))
	ctx := context.Background()
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	startWorkflowServiceTask(t, ctx, service, task.Task.ID)

	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	agentID := workflowServiceNodeIDByKey(t, definition.Definition, "agent")
	execution := newManualMoveExecutionStub(service)
	execution.quiescentErr = workflowexecution.ErrTaskExecutionNotQuiescent
	service.currentNodeExecution = execution
	graph := serverapi.WorkflowGraphDraftFromDefinition(definition.Definition)
	for index := range graph.Nodes {
		if graph.Nodes[index].ID == agentID {
			graph.Nodes[index].SubagentRole = "explorer"
		}
	}
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: definition.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph role during active Task: %v", err)
	}
	if !saved.Saved {
		t.Fatalf("SaveWorkflowGraph response = %+v, want saved role change", saved)
	}
	if len(execution.quiescentTaskIDs) != 0 {
		t.Fatalf("graph mutations checked Task-wide Quiescence: %v", execution.quiescentTaskIDs)
	}
}

func TestServiceSaveWorkflowGraphProjectsSelectorApplicabilityWithRoleCatalog(t *testing.T) {
	service, binding, _ := newWorkflowServiceTestServiceWithRoleResolver(t, testsetup.QuestionsEnabled("coder", "reviewer"))
	ctx := context.Background()
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	source, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	graph := serverapi.WorkflowGraphDraftFromDefinition(source.Definition)
	edgeID := workflowServiceGraphEntityID("edge-next-" + workflowID.String())
	var selectedEdge *serverapi.WorkflowGraphDraftEdge
	for index := range graph.Edges {
		if graph.Edges[index].ID != edgeID {
			continue
		}
		graph.Edges[index].AssigneeSelection = "previous_node"
		graph.Edges[index].Parameters = append(graph.Edges[index].Parameters, serverapi.WorkflowParameter{
			Key:     "role",
			Purpose: "target_assignee",
		})
		selectedEdge = &graph.Edges[index]
		break
	}
	if selectedEdge == nil {
		t.Fatalf("graph edge %q not found", edgeID)
	}
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: source.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	if !saved.Saved || saved.Definition == nil {
		t.Fatalf("SaveWorkflowGraph response = %+v, want saved definition", saved)
	}
	for _, derivedEdge := range saved.Definition.DerivedWiring.Edges {
		if derivedEdge.EdgeID != edgeID {
			continue
		}
		applicability := derivedEdge.AssigneeSelectionApplicability
		if !applicability.Available ||
			!applicability.ParameterVisible ||
			applicability.Reason != serverapi.WorkflowSelectorApplicabilityReasonEligible {
			t.Fatalf("saved selector applicability = %+v, want catalog-backed eligible selector", applicability)
		}
		return
	}
	t.Fatalf("saved definition omitted derived wiring for edge %q", edgeID)
}

func TestServiceWorkflowDeleteRevalidatesWorkflowTasksAtCommit(t *testing.T) {
	ctx, service, _, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	preview, err := service.PreviewWorkflowDelete(ctx, serverapi.WorkflowDeletePreviewRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}
	execution := newManualMoveExecutionStub(service)
	execution.quiescentErr = workflowexecution.ErrTaskExecutionNotQuiescent
	service.currentNodeExecution = execution

	_, err = service.DeleteWorkflow(ctx, serverapi.WorkflowDeleteRequest{
		WorkflowID:           workflowID,
		Confirmed:            true,
		ExpectedVersion:      preview.Impact.Version,
		ExpectedProjectCount: preview.Impact.ProjectCount,
		ExpectedLinkCount:    preview.Impact.LinkCount,
		ExpectedTaskCount:    preview.Impact.TaskCount,
	})
	if !errors.Is(err, workflowexecution.ErrTaskExecutionNotQuiescent) {
		t.Fatalf("DeleteWorkflow error = %v, want %v", err, workflowexecution.ErrTaskExecutionNotQuiescent)
	}
	if len(execution.quiescentTaskIDs) != 1 || execution.quiescentTaskIDs[0] != workflow.TaskID(taskID) {
		t.Fatalf("quiescence checks = %v, want task %s before workflow delete", execution.quiescentTaskIDs, taskID)
	}
	if _, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID}); err != nil {
		t.Fatalf("GetWorkflow after rejected mutations: %v", err)
	}
	if _, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: taskID}); err != nil {
		t.Fatalf("GetWorkflowTask after rejected delete: %v", err)
	}
}

func TestServiceWorkflowDeleteWaitsForAffectedTaskMutation(t *testing.T) {
	ctx, service, _, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	preview, err := service.PreviewWorkflowDelete(ctx, serverapi.WorkflowDeletePreviewRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}
	waitForTaskMutation(t, service, workflow.TaskID(taskID), func() error {
		_, err := service.DeleteWorkflow(ctx, serverapi.WorkflowDeleteRequest{
			WorkflowID:           workflowID,
			Confirmed:            true,
			ExpectedVersion:      preview.Impact.Version,
			ExpectedProjectCount: preview.Impact.ProjectCount,
			ExpectedLinkCount:    preview.Impact.LinkCount,
			ExpectedTaskCount:    preview.Impact.TaskCount,
		})
		return err
	}, nil)
}

func TestServiceWorkflowTaskDeleteWaitsForSameTaskMutation(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)

	waitForTaskMutation(t, service, workflow.TaskID(taskID), func() error {
		return service.DeleteWorkflowTask(ctx, serverapi.WorkflowTaskDeleteRequest{TaskID: taskID})
	}, func() {
		if _, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: taskID}); err != nil {
			t.Fatalf("GetWorkflowTask while delete waits: %v", err)
		}
	})

	if _, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: taskID}); err == nil {
		t.Fatal("deleted workflow task remains readable after Task ownership release")
	}
}

func TestServiceWorkflowTaskReadDoesNotWaitForRuntimeLifecycleOwnership(t *testing.T) {
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep executable unavailable: %v", err)
	}
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	taskID := workflow.TaskID(task.Task.ID)
	started, err := service.store.StartTask(ctx, taskID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller, err := workflowexecution.NewCurrentNodeController(
		service.store,
		initialBranchControllerRunner{},
		authority,
		service.taskMutations,
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: initialBranchControllerSteerer{},
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	projector := workflowview.NewTaskProjector()
	projection, err := workflowview.NewTaskStatusProjection(metadataStore, service.store, projector, controller)
	if err != nil {
		t.Fatalf("NewTaskStatusProjection: %v", err)
	}
	dependencyCounter, err := workflowview.NewTaskDependencyCounter(metadataStore)
	if err != nil {
		t.Fatalf("NewTaskDependencyCounter: %v", err)
	}
	dependencies, err := workflowview.NewTaskDependencies(metadataStore, projection, dependencyCounter)
	if err != nil {
		t.Fatalf("NewTaskDependencies: %v", err)
	}
	service.readModels.TaskDetail, err = workflowview.NewTaskDetail(metadataStore, projection, dependencies)
	if err != nil {
		t.Fatalf("NewTaskDetail: %v", err)
	}
	ref := sessionruntime.WorkflowExecutionRef{
		ProjectID:   binding.ProjectID,
		WorkflowID:  workflowID,
		CurrentNode: started.Mutation.Created[0].Reference,
	}
	lease, err := authority.NewWorkflowExecutionLease(ref)
	if err != nil {
		t.Fatalf("NewWorkflowExecutionLease: %v", err)
	}
	handle, err := authority.StartScriptExecution(ctx, sessionruntime.ScriptExecutionRequest{
		Workflow: &lease,
		Command:  sessionruntime.ScriptCommand{Path: sleepPath, Args: []string{"30"}},
	})
	if err != nil {
		t.Fatalf("StartScriptExecution: %v", err)
	}
	t.Cleanup(func() {
		lease.Cancel()
		_ = handle.Stop(context.Background())
	})
	if _, err := controller.ObserveWorkflowTaskExecutions([]workflow.TaskID{taskID}); err != nil {
		t.Fatalf("prime Task read snapshot: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseSelection := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseSelection()
	selectionDone := make(chan error, 1)
	go func() {
		selectionDone <- authority.WithWorkflowManualMoveSelection(taskID, func(sessionruntime.WorkflowInterruptSelection) error {
			close(entered)
			<-release
			return errors.New("release lifecycle selection without applying it")
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("Runtime lifecycle selection did not acquire ownership")
	}

	readDone := make(chan error, 1)
	go func() {
		_, readErr := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: task.Task.ID})
		readDone <- readErr
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("GetWorkflowTask while Runtime lifecycle ownership held: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GetWorkflowTask waited for Runtime lifecycle ownership")
	}
	releaseSelection()
	if err := <-selectionDone; err == nil {
		t.Fatal("Runtime lifecycle selection unexpectedly committed")
	}
}

func TestServiceTaskStartAppliesExplicitNoneSelectionAndLocksTarget(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           taskID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowTaskActionOutcomeApplied || response.Applied == nil {
		t.Fatalf("start response = %+v, want applied", response)
	}
	if len(response.Applied.CurrentNodes) != 1 || strings.TrimSpace(response.Applied.CurrentNodes[0].NodeID) == "" {
		t.Fatalf("start response = %+v, want one Current Node", response)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil ||
		targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone ||
		targetContext.Task.ManagedWorktreeID != nil ||
		targetContext.Task.PendingInitialManagedBranchName != nil {
		t.Fatalf("locked target = %+v, managed worktree = %v, want none", targetContext.Task.ExecutionTarget, targetContext.Task.ManagedWorktreeID)
	}
}

func TestServiceAffectedStartNodeWithProvisionalWorktreeStartsAndLocksTarget(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	worktreeRoot := filepath.Join(t.TempDir(), "task-worktree")
	worktreeID := "worktree-" + task.Task.ID
	requestedRef := "HEAD"
	resolvedRef := "refs/heads/main"
	commitOID := strings.Repeat("a", 40)
	bindWorkflowServiceManagedWorktree(t, ctx, metadataStore, binding.WorkspaceID, workflow.TaskID(task.Task.ID), worktreeID, worktreeRoot, true)
	infrastructure := &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			ResolvedRef:  &resolvedRef,
			CommitOID:    &commitOID,
			Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
		},
		materialize: func(taskID workflow.TaskID) (ExecutionTargetMaterialization, error) {
			root := workflowstore.ManagedExecutionRoot{WorktreeID: worktreeID, Root: worktreeRoot}
			return ExecutionTargetMaterialization{RetainedRoot: &root}, nil
		},
	}
	service.executionTargets = infrastructure

	detail, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: task.Task.ID})
	if err != nil || detail.Task.ExecutionTarget != nil || detail.Task.WorktreePath != nil || !detail.Task.Actions.CanStart {
		t.Fatalf("provisional Task detail = %+v, %v; want hidden target facts and Start action", detail.Task, err)
	}
	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("StartWorkflowTask = %+v, %v; want applied", response, err)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil ||
		targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeHead ||
		targetContext.Task.ExecutionTarget.CommitOID == nil ||
		*targetContext.Task.ExecutionTarget.CommitOID != commitOID ||
		targetContext.Task.ManagedWorktreeID == nil ||
		*targetContext.Task.ManagedWorktreeID != worktreeID {
		t.Fatalf("locked target = %+v, managed worktree = %v", targetContext.Task.ExecutionTarget, targetContext.Task.ManagedWorktreeID)
	}
}

func TestServiceTaskResumeDoesNotRepeatCompletedSetupAfterTargetLockFailure(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	preparations := make(chan workflowexecution.TaskStartPreparation, 1)
	finalizers := make(chan workflowexecution.TaskPreparationFinalizer, 1)
	service.currentNodeExecution = &currentNodeCompletionExecutionStub{
		store:             service.store,
		startPreparations: preparations,
		startFinalizers:   finalizers,
	}
	requestedRef := "HEAD"
	commitOID := strings.Repeat("b", 40)
	worktreeID := "worktree-" + task.Task.ID
	worktreeRoot := filepath.Join(t.TempDir(), "task-worktree")
	firstMaterialization := true
	infrastructure := &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			CommitOID:    &commitOID,
			Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
		},
		materialize: func(taskID workflow.TaskID) (ExecutionTargetMaterialization, error) {
			if firstMaterialization {
				bindWorkflowServiceManagedWorktree(t, ctx, metadataStore, binding.WorkspaceID, taskID, worktreeID, worktreeRoot, false)
				firstMaterialization = false
			}
			root := workflowstore.ManagedExecutionRoot{WorktreeID: worktreeID, Root: worktreeRoot}
			return ExecutionTargetMaterialization{
				RetainedRoot: &root,
				SetupResult:  &worktree.WorktreeSetupResult{Completed: &serverapi.WorktreeSetupCompleted{}},
				RetainedWorktree: &serverapi.WorktreeTopologyEntry{
					Variant: serverapi.WorktreeTopologyVariantRegistered,
					Registered: &serverapi.WorktreeRegisteredFacts{
						Git: serverapi.WorktreeGitFacts{CanonicalRoot: worktreeRoot, HeadObject: commitOID},
						Kent: serverapi.WorktreeKentFacts{
							WorktreeID: worktreeID, CanonicalRoot: worktreeRoot, DisplayName: task.Task.ShortID,
						},
					},
				},
			}, nil
		},
	}
	service.executionTargets = infrastructure
	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeHead,
		},
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("StartWorkflowTask = %+v, %v; want applied placement", response, err)
	}
	preparation := <-preparations
	if err := preparation.Prepare(ctx); err != nil {
		t.Fatalf("setup preparation: %v", err)
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	preparationErr := preparation.Commit(canceledCtx)
	if !errors.Is(preparationErr, context.Canceled) {
		t.Fatalf("target lock error = %v, want context canceled", preparationErr)
	}
	var typedPreparationErr *workflowexecution.TaskStartPreparationError
	if !errors.As(preparationErr, &typedPreparationErr) {
		t.Fatalf("preparation error = %T %v, want typed interruption", preparationErr, preparationErr)
	}
	reference, err := workflow.NewCurrentNodeReference(
		workflow.TaskID(task.Task.ID),
		workflow.NodeID(response.Applied.CurrentNodes[0].NodeID),
		nil,
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference: %v", err)
	}
	if err := service.store.InterruptCurrentNode(
		ctx,
		reference,
		workflow.CurrentNodeInterruptionReason("workflow_task_setup_failed"),
		typedPreparationErr.InterruptionDetail(),
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	(<-finalizers)(workflowexecution.TaskPreparationFinalization{
		Kind:  workflowexecution.TaskPreparationFailed,
		Cause: preparationErr,
	})
	attention, err := service.ListWorkflowTaskAttention(ctx, serverapi.WorkflowTaskAttentionListRequest{TaskID: task.Task.ID})
	if err != nil || len(attention.Items) != 1 || attention.Items[0].DetailJSON == nil {
		t.Fatalf("setup recovery attention = %+v, %v; want one typed interruption", attention, err)
	}

	service.currentNodeExecution = &currentNodeCompletionExecutionStub{store: service.store}
	resumed, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeHead,
		},
	})
	if err != nil || resumed.Applied == nil {
		t.Fatalf("ResumeWorkflowTask = %+v, %v; want applied recovery", resumed, err)
	}
	if want := []worktreecontract.SetupRequirement{
		worktreecontract.SetupRequirementRequired,
		worktreecontract.SetupRequirementAlreadyCompleted,
	}; !reflect.DeepEqual(infrastructure.setupRequirements, want) {
		t.Fatalf("setup requirements = %v, want %v", infrastructure.setupRequirements, want)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext after Resume: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil ||
		targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeHead ||
		targetContext.Task.ManagedWorktreeID == nil ||
		*targetContext.Task.ManagedWorktreeID != worktreeID {
		t.Fatalf("recovered target = %+v, worktree = %v; want locked retained target", targetContext.Task.ExecutionTarget, targetContext.Task.ManagedWorktreeID)
	}
}

func TestTaskSetupObservationPublishesRetryReadyFailureOnlyAfterFinalization(t *testing.T) {
	setupOperationID := serverapi.NewWorktreeSetupOperationID()
	recorder := &workflowTaskSetupEventRecorder{}
	observation, err := newTaskSetupObservation(
		setupOperationID,
		workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeNone},
		recorder,
	)
	if err != nil {
		t.Fatalf("newTaskSetupObservation: %v", err)
	}
	preparationErr := taskPreparationError(
		setupOperationID,
		initiatingActionTargetPreflight{
			selection: workflow.ExecutionTargetSelection{Mode: workflow.ExecutionTargetModeNone},
			explicit:  true,
		},
		nil,
		nil,
		nil,
		errors.New("target preparation failed"),
	)
	observation.record(preparedInitiatingActionTarget{}, preparationErr)
	if events := recorder.recordedEvents(); len(events) != 0 {
		t.Fatalf("setup events before preparation finalization = %+v, want none", events)
	}

	observation.finalize(workflowexecution.TaskPreparationFinalization{
		Kind:  workflowexecution.TaskPreparationFailed,
		Cause: preparationErr,
	})
	events := recorder.recordedEvents()
	if len(events) != 1 {
		t.Fatalf("setup events after preparation finalization = %+v, want one", events)
	}
	event := events[0]
	if err := event.Validate(); err != nil {
		t.Fatalf("setup event validation: %v", err)
	}
	if event.Phase != serverapi.WorktreeSetupPhaseFailed ||
		event.Failed == nil ||
		event.Failed.RetryReadiness != serverapi.WorktreeSetupRetryReady ||
		event.Failed.Cause.Kind != serverapi.WorktreeSetupFailureTargetPreparation ||
		event.Failed.ExecutionTarget == nil ||
		event.Failed.ExecutionTarget.Mode != serverapi.WorkflowExecutionTargetModeNone {
		t.Fatalf("setup event = %+v, want retry-ready target preparation failure", event)
	}
}

func TestServiceTaskStartDefersConfiguredTargetResolutionFailure(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	preparations := make(chan workflowexecution.TaskStartPreparation, 1)
	service.currentNodeExecution = &currentNodeCompletionExecutionStub{
		store: service.store, startPreparations: preparations,
	}
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolveErr: &worktree.GitRevisionResolutionError{
			Kind:         worktree.GitRevisionResolutionErrorInvalidRevision,
			RequestedRef: "HEAD",
		},
	}

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("StartWorkflowTask = %+v, %v; want applied placement", response, err)
	}
	var resolutionErr *worktree.GitRevisionResolutionError
	preparationErr := (<-preparations).Prepare(ctx)
	if !errors.As(preparationErr, &resolutionErr) {
		t.Fatalf("asynchronous preparation error = %v, want revision failure", preparationErr)
	}
	var interrupted *workflowexecution.TaskStartPreparationError
	if !errors.As(preparationErr, &interrupted) ||
		interrupted.InterruptionDetail().Code != configuredTargetPreparationFailureCode {
		t.Fatalf("asynchronous preparation error = %T %v, want configured-target interruption", preparationErr, preparationErr)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil {
		t.Fatalf("execution target = %+v, want unlocked", targetContext.Task.ExecutionTarget)
	}
}

func TestServiceTaskResumeReselectsUnavailableUnlockedTarget(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started, err := service.store.StartTask(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	reference := started.Mutation.Created[0].Reference
	if err := service.store.InterruptCurrentNode(
		ctx,
		reference,
		workflow.CurrentNodeInterruptionReason("workflow_runtime_start_failed"),
		workflow.CurrentNodeInterruptionDetail{
			Code: configuredTargetPreparationFailureCode,
			Fields: map[string]string{
				"mode":  string(serverapi.WorkflowExecutionTargetModeHead),
				"cause": string(serverapi.WorkflowExecutionTargetUnavailableCauseInvalidRevision),
			},
			ConfiguredExecutionTargetUnavailable: &workflow.ConfiguredExecutionTargetUnavailable{
				Mode:  workflow.ExecutionTargetModeHead,
				Cause: workflow.ExecutionTargetUnavailableCauseInvalidRevision,
			},
		},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeAskOnFirstExecution,
	})
	execution := &currentNodeCompletionExecutionStub{store: service.store}
	service.currentNodeExecution = execution
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolveErr: &worktree.GitRevisionResolutionError{
			Kind:         worktree.GitRevisionResolutionErrorInvalidRevision,
			RequestedRef: "HEAD",
		},
	}
	branchName := "feature/reselect-unavailable"

	response, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		BranchName:       &branchName,
	})
	if err != nil {
		t.Fatalf("ResumeWorkflowTask unavailable target: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired ||
		response.SelectionRequired == nil ||
		response.SelectionRequired.Reason != serverapi.WorkflowExecutionTargetSelectionReasonConfiguredTargetUnavailable {
		t.Fatalf("resume response = %+v, want configured target selection", response)
	}
	if execution.resumeEligibilityCalls != 1 {
		t.Fatalf("Resume preflight calls = %d, want one before configured-target selection", execution.resumeEligibilityCalls)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext before selection: %v", err)
	}
	if targetContext.Task.PendingInitialManagedBranchName == nil ||
		*targetContext.Task.PendingInitialManagedBranchName != task.Task.ShortID {
		t.Fatalf("pending branch before selection = %v, want unchanged %q", targetContext.Task.PendingInitialManagedBranchName, task.Task.ShortID)
	}

	service.executionTargets = &recordingExecutionTargetInfrastructure{}
	response, err = service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil {
		t.Fatalf("ResumeWorkflowTask selected target: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied || response.Applied == nil {
		t.Fatalf("resume response = %+v, want applied", response)
	}
	if execution.resumeEligibilityCalls != 2 {
		t.Fatalf("Resume preflight calls = %d, want one per request", execution.resumeEligibilityCalls)
	}
	targetContext, err = service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil ||
		targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone {
		t.Fatalf("execution target = %+v, want locked none target", targetContext.Task.ExecutionTarget)
	}
}

func TestServiceTaskResumePreservesConfiguredSelectionAfterMaterializationFailure(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started, err := service.store.StartTask(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if err := service.store.InterruptCurrentNode(
		ctx,
		started.Mutation.Created[0].Reference,
		workflow.CurrentNodeInterruptionReason("workflow_runtime_start_failed"),
		workflow.CurrentNodeInterruptionDetail{Code: "workflow_runtime_start_failed"},
	); err != nil {
		t.Fatalf("InterruptCurrentNode: %v", err)
	}
	requestedRef := "HEAD"
	commitOID := strings.Repeat("c", 40)
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			CommitOID:    &commitOID,
			Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
		},
		materializeErr: &worktree.GitRevisionResolutionError{
			Kind:         worktree.GitRevisionResolutionErrorGitFailure,
			RequestedRef: requestedRef,
		},
	}

	_, err = service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	var preparationErr *workflowexecution.TaskStartPreparationError
	if !errors.As(err, &preparationErr) ||
		preparationErr.InterruptionDetail().Code != configuredTargetPreparationFailureCode ||
		preparationErr.InterruptionDetail().ConfiguredExecutionTargetUnavailable == nil ||
		preparationErr.InterruptionDetail().ConfiguredExecutionTargetUnavailable.Cause !=
			workflow.ExecutionTargetUnavailableCauseGitFailure {
		t.Fatalf("ResumeWorkflowTask error = %T %v, want configured-target preparation failure", err, err)
	}
}

func TestServiceTaskResumeNoOpsWhenTaskAlreadyResumed(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	if _, err := service.store.StartTask(ctx, workflow.TaskID(task.Task.ID)); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	service.currentNodeExecution = &currentNodeCompletionExecutionStub{store: service.store}

	response, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil {
		t.Fatalf("ResumeWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeNoOp ||
		response.NoOp == nil ||
		len(response.NoOp.CurrentNodes) != 1 {
		t.Fatalf("ResumeWorkflowTask = %+v, want no-op with current ready node", response)
	}
}

func TestServiceConcurrentTaskResumeReturnsAppliedThenNoOp(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	if _, err := service.store.StartTask(ctx, workflow.TaskID(task.Task.ID)); err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	currentNodes, err := service.store.ListCurrentNodes(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListCurrentNodes: %v", err)
	}
	for _, currentNode := range currentNodes {
		if err := service.store.InterruptCurrentNode(
			ctx,
			currentNode.Reference,
			workflow.CurrentNodeInterruptionReasonUserInterrupt,
			workflow.NewCurrentNodeInterruptionDetail(
				string(workflow.CurrentNodeInterruptionReasonUserInterrupt),
				nil,
			),
		); err != nil {
			t.Fatalf("InterruptCurrentNode: %v", err)
		}
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	controller, err := workflowexecution.NewCurrentNodeController(
		service.store,
		initialBranchControllerRunner{},
		authority,
		service.taskMutations,
		workflowexecution.CurrentNodeControllerConfig{
			AgentConcurrency:  1,
			AssignmentSteerer: initialBranchControllerSteerer{},
		},
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeController: %v", err)
	}
	t.Cleanup(func() {
		_ = controller.Close()
		_ = authority.Close(context.Background())
	})
	service.currentNodeExecution = controller
	request := serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	}

	responses := make(chan serverapi.WorkflowTaskResumeResponse, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			response, resumeErr := service.ResumeWorkflowTask(ctx, request)
			responses <- response
			errs <- resumeErr
		}()
	}
	outcomes := map[serverapi.WorkflowExecutionTargetActionOutcome]int{}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("ResumeWorkflowTask: %v", err)
		}
		outcomes[(<-responses).Outcome]++
	}
	if outcomes[serverapi.WorkflowExecutionTargetActionOutcomeApplied] != 1 ||
		outcomes[serverapi.WorkflowExecutionTargetActionOutcomeNoOp] != 1 {
		t.Fatalf("concurrent Resume outcomes = %+v, want one applied and one no_op", outcomes)
	}
}

func TestServiceTaskResumePromotesConcurrencyQueuedCurrentNodes(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	node := workflow.CurrentNode{
		Reference: workflow.CurrentNodeReference{
			TaskID: workflow.TaskID(task.Task.ID),
			NodeID: workflow.NodeID("node-queued"),
		},
	}
	execution := &currentNodeCompletionExecutionStub{
		store:            service.store,
		promoted:         []workflow.CurrentNode{node},
		promotionHandled: true,
	}
	service.currentNodeExecution = execution

	response, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{
		TaskID:           task.Task.ID,
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
	})
	if err != nil {
		t.Fatalf("ResumeWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied ||
		response.Applied == nil ||
		len(response.Applied.CurrentNodes) != 1 ||
		response.Applied.CurrentNodes[0].NodeID != string(node.Reference.NodeID) {
		t.Fatalf("ResumeWorkflowTask response = %+v, want promoted Current Node", response)
	}
	if execution.resumeEligibilityCalls != 0 {
		t.Fatalf(
			"Resume eligibility calls = %d, want queued promotion before interrupted Resume",
			execution.resumeEligibilityCalls,
		)
	}
}

func TestServiceTaskStartReturnsTypedErrorForInvalidExplicitCustomRef(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	preparations := make(chan workflowexecution.TaskStartPreparation, 1)
	service.currentNodeExecution = &currentNodeCompletionExecutionStub{
		store: service.store, startPreparations: preparations,
	}
	customRef := "missing-ref"
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolveErr: &worktree.GitRevisionResolutionError{
			Kind:         worktree.GitRevisionResolutionErrorInvalidRevision,
			RequestedRef: customRef,
		},
	}

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           taskID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode:      serverapi.WorkflowExecutionTargetModeCustomRef,
			CustomRef: &customRef,
		},
	})
	if err != nil || response.Applied == nil {
		t.Fatalf("StartWorkflowTask = %+v, %v; want applied placement", response, err)
	}
	var resolutionErr *serverapi.WorkflowExecutionTargetResolutionError
	if err := (<-preparations).Prepare(ctx); !errors.As(err, &resolutionErr) ||
		resolutionErr.Code != serverapi.WorkflowExecutionTargetResolutionErrorInvalidRevision ||
		resolutionErr.RequestedRef != customRef {
		t.Fatalf("StartWorkflowTask error = %v, want typed invalid custom ref", err)
	}
	targetContext, contextErr := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(taskID))
	if contextErr != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", contextErr)
	}
	if targetContext.Task.ExecutionTarget != nil {
		t.Fatalf("execution target = %+v, want unlocked", targetContext.Task.ExecutionTarget)
	}
}

func TestServiceAllowsInvalidDefaultBacklogButRejectsUnlinkedWorkflow(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	unlinked, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Unlinked"})
	if err != nil {
		t.Fatalf("CreateWorkflow unlinked: %v", err)
	}
	unlinkedWorkflowID := unlinked.Workflow.ID
	if _, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{ProjectID: binding.ProjectID, WorkflowID: &unlinkedWorkflowID, Title: "Task", Body: "Body"}); err == nil {
		t.Fatalf("expected unlinked workflow task create to fail")
	}
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, unlinked.Workflow.ID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID}); !errors.Is(err, workflowstore.ErrWorkflowValidationFailed) {
		t.Fatalf("expected invalid default workflow start error, got %v", err)
	}
}

func TestServiceTaskCreateMapsNoLinkedWorkflowsSelectionError(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)

	_, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID: binding.ProjectID,
		Title:     "No workflow",
	})
	var selectionErr *serverapi.WorkflowTaskCreateSelectionError
	if !errors.As(err, &selectionErr) {
		t.Fatalf("CreateWorkflowTask error = %v, want WorkflowTaskCreateSelectionError", err)
	}
	if selectionErr.Reason != serverapi.WorkflowTaskCreateSelectionReasonNoLinkedWorkflows ||
		selectionErr.ProjectID != binding.ProjectID ||
		selectionErr.WorkflowID != nil {
		t.Fatalf("selection error = %+v", selectionErr)
	}
}

func TestServiceTaskCreateMapsExplicitWorkflowNotLinkedSelectionError(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)

	_, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID:  binding.ProjectID,
		WorkflowID: &workflowID,
		Title:      "Unlinked workflow",
	})
	var selectionErr *serverapi.WorkflowTaskCreateSelectionError
	if !errors.As(err, &selectionErr) {
		t.Fatalf("CreateWorkflowTask error = %v, want WorkflowTaskCreateSelectionError", err)
	}
	if selectionErr.Reason != serverapi.WorkflowTaskCreateSelectionReasonWorkflowNotLinked ||
		selectionErr.ProjectID != binding.ProjectID ||
		selectionErr.WorkflowID == nil ||
		*selectionErr.WorkflowID != workflowID {
		t.Fatalf("selection error = %+v", selectionErr)
	}
}

func TestServiceTaskCreateMapsAmbiguousWorkflowSelectionError(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	firstWorkflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	secondWorkflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkWorkflowServiceProject(t, ctx, service, serverapi.WorkflowLinkProjectRequest{
		ProjectID:     binding.ProjectID,
		WorkflowID:    firstWorkflowID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultNever,
	})
	linkWorkflowServiceProject(t, ctx, service, serverapi.WorkflowLinkProjectRequest{
		ProjectID:     binding.ProjectID,
		WorkflowID:    secondWorkflowID,
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultNever,
	})

	_, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID: binding.ProjectID,
		Title:     "Ambiguous workflow",
	})
	var selectionErr *serverapi.WorkflowTaskCreateSelectionError
	if !errors.As(err, &selectionErr) {
		t.Fatalf("CreateWorkflowTask error = %v, want WorkflowTaskCreateSelectionError", err)
	}
	if selectionErr.Reason != serverapi.WorkflowTaskCreateSelectionReasonAmbiguousWithoutDefault ||
		selectionErr.ProjectID != binding.ProjectID ||
		selectionErr.WorkflowID != nil {
		t.Fatalf("selection error = %+v", selectionErr)
	}
}

func TestServiceTaskCreateMapsRetryableStoreConflict(t *testing.T) {
	err := workflowTaskCreateError(workflowstore.TaskCreateConflictError{
		Reason: workflowstore.TaskCreateConflictSerialization,
		Cause:  errors.New("database locked"),
	}, "project-1")
	var conflictErr *serverapi.WorkflowTaskCreateConflictError
	if !errors.As(err, &conflictErr) || conflictErr.Reason != serverapi.WorkflowTaskCreateConflictReasonSerialization {
		t.Fatalf("workflowTaskCreateError = %T %v, want typed serialization conflict", err, err)
	}
}

func TestServiceCreatesAndListsProjectLabels(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()

	created, err := service.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "  Priority  ",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}
	if created.Label.ID == "" || created.Label.Name != "Priority" {
		t.Fatalf("created label = %+v", created.Label)
	}

	listed, err := service.ListWorkflowProjectLabels(ctx, serverapi.WorkflowProjectLabelCatalogRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("ListWorkflowProjectLabels: %v", err)
	}
	if listed.Catalog.ProjectID != binding.ProjectID || !reflect.DeepEqual(listed.Catalog.Labels, []serverapi.WorkflowProjectLabel{created.Label}) {
		t.Fatalf("catalog = %+v, want created label", listed.Catalog)
	}

	event := nextWorkflowProjectEvent(t, sub)
	if !stringPointerEquals(event.ProjectID, binding.ProjectID) ||
		event.WorkflowID != nil ||
		event.Resource != serverapi.WorkflowProjectEventResourceLabel ||
		event.Action != serverapi.WorkflowProjectEventActionCreated ||
		event.PrimaryEntityID != created.Label.ID ||
		len(event.RelatedIDs) != 0 {
		t.Fatalf("event = %+v, want project-scoped label created event", event)
	}
}

func TestServiceRenamesAndDeletesProjectLabels(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	created, err := service.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Priority",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()

	renamed, err := service.RenameWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelRenameRequest{
		ProjectID: binding.ProjectID,
		LabelID:   created.Label.ID,
		Name:      "Urgent",
	})
	if err != nil {
		t.Fatalf("RenameWorkflowProjectLabel: %v", err)
	}
	if renamed.Label.ID != created.Label.ID || renamed.Label.Name != "Urgent" {
		t.Fatalf("renamed label = %+v", renamed.Label)
	}
	renameEvent := nextWorkflowProjectEvent(t, sub)
	if renameEvent.Action != serverapi.WorkflowProjectEventActionRenamed ||
		renameEvent.PrimaryEntityID != created.Label.ID ||
		!stringPointerEquals(renameEvent.ProjectID, binding.ProjectID) ||
		renameEvent.WorkflowID != nil {
		t.Fatalf("rename event = %+v", renameEvent)
	}

	deleted, err := service.DeleteWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelDeleteRequest{
		ProjectID: binding.ProjectID,
		LabelID:   created.Label.ID,
	})
	if err != nil {
		t.Fatalf("DeleteWorkflowProjectLabel: %v", err)
	}
	if deleted.LabelID != created.Label.ID {
		t.Fatalf("deleted response = %+v", deleted)
	}
	deleteEvent := nextWorkflowProjectEvent(t, sub)
	if deleteEvent.Action != serverapi.WorkflowProjectEventActionDeleted ||
		deleteEvent.PrimaryEntityID != created.Label.ID ||
		!stringPointerEquals(deleteEvent.ProjectID, binding.ProjectID) ||
		deleteEvent.WorkflowID != nil {
		t.Fatalf("delete event = %+v", deleteEvent)
	}
}

func TestServiceReordersProjectLabelsAndOnlyPublishesChangedOrder(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	first, err := service.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "First",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel First: %v", err)
	}
	second, err := service.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Second",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel Second: %v", err)
	}
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()

	ordered := []string{first.Label.ID, second.Label.ID}
	applied, err := service.ReorderWorkflowProjectLabels(ctx, serverapi.WorkflowProjectLabelReorderRequest{
		ProjectID: binding.ProjectID,
		LabelIDs:  ordered,
	})
	if err != nil {
		t.Fatalf("ReorderWorkflowProjectLabels applied: %v", err)
	}
	if got := []string{applied.Catalog.Labels[0].ID, applied.Catalog.Labels[1].ID}; !reflect.DeepEqual(got, ordered) {
		t.Fatalf("applied order = %+v, want %+v", got, ordered)
	}
	event := nextWorkflowProjectEvent(t, sub)
	if event.Resource != serverapi.WorkflowProjectEventResourceLabel ||
		event.Action != serverapi.WorkflowProjectEventActionReordered ||
		event.PrimaryEntityID != binding.ProjectID ||
		!stringPointerEquals(event.ProjectID, binding.ProjectID) {
		t.Fatalf("reorder event = %+v", event)
	}

	if _, err := service.ReorderWorkflowProjectLabels(ctx, serverapi.WorkflowProjectLabelReorderRequest{
		ProjectID: binding.ProjectID,
		LabelIDs:  ordered,
	}); err != nil {
		t.Fatalf("ReorderWorkflowProjectLabels unchanged: %v", err)
	}
	noEventCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := sub.Next(noEventCtx); err == nil {
		t.Fatal("unchanged reorder published an event")
	}
}

func TestServiceGetsAndUpdatesTaskLabels(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	zulu, err := service.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{ProjectID: binding.ProjectID, Name: "Zulu"})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel Zulu: %v", err)
	}
	alpha, err := service.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{ProjectID: binding.ProjectID, Name: "alpha"})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel alpha: %v", err)
	}
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: binding.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()

	empty, err := service.GetWorkflowTaskLabels(ctx, serverapi.WorkflowTaskLabelsGetRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("GetWorkflowTaskLabels empty: %v", err)
	}
	if empty.Assignment.TaskID != task.Task.ID || len(empty.Assignment.LabelIDs) != 0 {
		t.Fatalf("empty assignment = %+v", empty.Assignment)
	}

	updated, err := service.UpdateWorkflowTaskLabels(ctx, serverapi.WorkflowTaskLabelsUpdateRequest{
		TaskID:      task.Task.ID,
		AddLabelIDs: []string{zulu.Label.ID, alpha.Label.ID},
	})
	if err != nil {
		t.Fatalf("UpdateWorkflowTaskLabels: %v", err)
	}
	if !reflect.DeepEqual(updated.Assignment.LabelIDs, []string{alpha.Label.ID, zulu.Label.ID}) {
		t.Fatalf("updated assignment = %+v, want project-order IDs", updated.Assignment)
	}
	event := nextWorkflowProjectEvent(t, sub)
	if !stringPointerEquals(event.ProjectID, binding.ProjectID) ||
		!workflowIDPointerEquals(event.WorkflowID, workflowID) ||
		event.Resource != serverapi.WorkflowProjectEventResourceTask ||
		event.Action != serverapi.WorkflowProjectEventActionLabelsChanged ||
		event.PrimaryEntityID != task.Task.ID ||
		len(event.RelatedIDs) != 0 {
		t.Fatalf("event = %+v, want task labels-changed event", event)
	}

	reloaded, err := service.GetWorkflowTaskLabels(ctx, serverapi.WorkflowTaskLabelsGetRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("GetWorkflowTaskLabels reloaded: %v", err)
	}
	if reloaded.Assignment.TaskID != updated.Assignment.TaskID ||
		!reflect.DeepEqual(reloaded.Assignment.LabelIDs, updated.Assignment.LabelIDs) {
		t.Fatalf("reloaded assignment = %+v, want %+v", reloaded, updated)
	}
}

func TestServiceCreatesWorkflowTaskWithAtomicLabels(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	projectLabel, err := service.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Priority",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}

	created, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID: binding.ProjectID,
		Title:     "Labeled task",
		LabelIDs:  []string{projectLabel.Label.ID},
	})
	if err != nil {
		t.Fatalf("CreateWorkflowTask: %v", err)
	}
	assignment, err := service.GetWorkflowTaskLabels(ctx, serverapi.WorkflowTaskLabelsGetRequest{TaskID: created.Task.ID})
	if err != nil {
		t.Fatalf("GetWorkflowTaskLabels: %v", err)
	}
	if !reflect.DeepEqual(assignment.Assignment.LabelIDs, []string{projectLabel.Label.ID}) {
		t.Fatalf("assignment = %+v, want created label", assignment.Assignment)
	}
}

func TestServiceMapsWorkflowLabelFailures(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	created, err := service.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Priority",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel: %v", err)
	}
	if _, err := service.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "priority",
	}); !workflowLabelErrorHasReason(err, serverapi.WorkflowLabelErrorReasonNameConflict) {
		t.Fatalf("duplicate create error = %T %v", err, err)
	}
	if _, err := service.RenameWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelRenameRequest{
		ProjectID: binding.ProjectID,
		LabelID:   created.Label.ID,
		Name:      "invalid\tname",
	}); !workflowLabelErrorHasReason(err, serverapi.WorkflowLabelErrorReasonInvalidName) {
		t.Fatalf("invalid rename error = %T %v", err, err)
	}
	if _, err := service.ListWorkflowProjectLabels(ctx, serverapi.WorkflowProjectLabelCatalogRequest{
		ProjectID: "project-missing",
	}); !workflowLabelErrorHasReason(err, serverapi.WorkflowLabelErrorReasonProjectNotFound) {
		t.Fatalf("missing project error = %T %v", err, err)
	}
	if _, err := service.DeleteWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelDeleteRequest{
		ProjectID: binding.ProjectID,
		LabelID:   "11111111-1111-4111-8111-111111111111",
	}); !workflowLabelErrorHasReason(err, serverapi.WorkflowLabelErrorReasonLabelNotFound) {
		t.Fatalf("missing label error = %T %v", err, err)
	}
	if _, err := service.GetWorkflowTaskLabels(ctx, serverapi.WorkflowTaskLabelsGetRequest{
		TaskID: "task-missing",
	}); !workflowLabelErrorHasReason(err, serverapi.WorkflowLabelErrorReasonTaskNotFound) {
		t.Fatalf("missing task error = %T %v", err, err)
	}
	for index := 1; index < serverapi.WorkflowLabelMaxIDs; index++ {
		if _, err := service.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{
			ProjectID: binding.ProjectID,
			Name:      fmt.Sprintf("Label %03d", index),
		}); err != nil {
			t.Fatalf("CreateWorkflowProjectLabel %d: %v", index, err)
		}
	}
	if _, err := service.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: binding.ProjectID,
		Name:      "Overflow",
	}); !workflowLabelErrorHasReason(err, serverapi.WorkflowLabelErrorReasonCatalogLimit) {
		t.Fatalf("catalog limit error = %T %v", err, err)
	}
}

func TestServiceMapsWorkflowTaskLabelScopeFailures(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	other, err := metadataStore.CreateProjectForWorkspace(ctx, t.TempDir(), "Other")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace other: %v", err)
	}
	foreign, err := service.CreateWorkflowProjectLabel(ctx, serverapi.WorkflowProjectLabelCreateRequest{
		ProjectID: other.ProjectID,
		Name:      "Foreign",
	})
	if err != nil {
		t.Fatalf("CreateWorkflowProjectLabel foreign: %v", err)
	}

	if _, err := service.UpdateWorkflowTaskLabels(ctx, serverapi.WorkflowTaskLabelsUpdateRequest{
		TaskID:      task.Task.ID,
		AddLabelIDs: []string{"11111111-1111-4111-8111-111111111111"},
	}); !workflowLabelErrorHasReason(err, serverapi.WorkflowLabelErrorReasonLabelNotFound) {
		t.Fatalf("missing label error = %T %v", err, err)
	}
	if _, err := service.UpdateWorkflowTaskLabels(ctx, serverapi.WorkflowTaskLabelsUpdateRequest{
		TaskID:      task.Task.ID,
		AddLabelIDs: []string{foreign.Label.ID},
	}); !workflowLabelErrorHasReason(err, serverapi.WorkflowLabelErrorReasonWrongProject) {
		t.Fatalf("wrong project error = %T %v", err, err)
	}
	if _, err := service.CreateWorkflowTask(ctx, serverapi.WorkflowTaskCreateRequest{
		ProjectID: binding.ProjectID,
		Title:     "Foreign label",
		LabelIDs:  []string{foreign.Label.ID},
	}); !workflowLabelErrorHasReason(err, serverapi.WorkflowLabelErrorReasonWrongProject) {
		t.Fatalf("labeled task create wrong project error = %T %v", err, err)
	}
	raw101 := make([]string, serverapi.WorkflowLabelMaxIDs+1)
	for index := range raw101 {
		raw101[index] = "not-a-uuid"
	}
	_, err = service.UpdateWorkflowTaskLabels(ctx, serverapi.WorkflowTaskLabelsUpdateRequest{
		TaskID:      task.Task.ID,
		AddLabelIDs: raw101,
	})
	var mutationErr *serverapi.WorkflowLabelError
	if !errors.As(err, &mutationErr) ||
		mutationErr.Reason != serverapi.WorkflowLabelErrorReasonInvalidMutation ||
		mutationErr.Field == nil ||
		*mutationErr.Field != "add_label_ids" {
		t.Fatalf("invalid mutation error = %T %+v", err, err)
	}
}

type recordingExecutionTargetInfrastructure struct {
	resolution                workflowstore.ExecutionTargetSnapshot
	resolveSelection          workflow.ExecutionTargetSelection
	initialBranchInspection   InitialTaskBranchInspectionRequest
	initialBranchInspections  int
	initialBranchErr          error
	initialBranchAssertion    InitialTaskBranchAssertionRequest
	initialBranchAssertions   int
	initialBranchAssertionErr error
	materializeTaskID         workflow.TaskID
	materializeRequest        ExecutionTargetMaterializeRequest
	restoreTaskID             workflow.TaskID
	restoreRequest            ExecutionTargetRestoreRequest
	restoreRequests           chan<- ExecutionTargetRestoreRequest
	setupOperationID          *serverapi.WorktreeSetupOperationID
	setupRequirements         []worktreecontract.SetupRequirement
	materialize               func(workflow.TaskID) (ExecutionTargetMaterialization, error)
	resolveErr                error
	materializeErr            error
	restoreErr                error
}

type manualMoveExecutionStub struct {
	currentNodeCompletionExecutionStub
	started          []workflow.CurrentNodeReference
	quiescentErr     error
	quiescentErrors  []error
	quiescentTaskIDs []workflow.TaskID
	disposition      workflowexecution.ManualMoveDisposition
	dispositionErr   error
	dispositionCalls int
	interruptTaskIDs []workflow.TaskID
	interruptErr     error
	interruptHook    func()
}

type workflowAttentionRecorder struct {
	resolutions []workflowstore.TaskAttentionResolution
	pending     []workflow.ApprovalID
}

type workflowTaskSetupEventRecorder struct {
	mu     sync.Mutex
	events []serverapi.WorktreeSetupEvent
}

func (r *workflowTaskSetupEventRecorder) PublishWorkflowTaskSetupEvent(event serverapi.WorktreeSetupEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *workflowTaskSetupEventRecorder) recordedEvents() []serverapi.WorktreeSetupEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]serverapi.WorktreeSetupEvent(nil), r.events...)
}

func (r *workflowAttentionRecorder) FinalizeTaskResolution(resolution workflowstore.TaskAttentionResolution) {
	r.resolutions = append(r.resolutions, resolution)
}

func (r *workflowAttentionRecorder) PublishPendingApproval(_ context.Context, approvalID workflow.ApprovalID) {
	r.pending = append(r.pending, approvalID)
}

func (r *workflowAttentionRecorder) resolvedApprovalIDs() []workflow.ApprovalID {
	var approvalIDs []workflow.ApprovalID
	for _, resolution := range r.resolutions {
		for _, approval := range resolution.Approvals {
			approvalIDs = append(approvalIDs, approval.ApprovalID)
		}
	}
	return approvalIDs
}

func newManualMoveExecutionStub(service *Service) *manualMoveExecutionStub {
	return &manualMoveExecutionStub{
		currentNodeCompletionExecutionStub: currentNodeCompletionExecutionStub{
			store:                 service.store,
			manualMoveAssignments: workflowServiceManualMoveAssignments(service),
		},
	}
}

func (s *manualMoveExecutionStub) StartTask(
	ctx context.Context,
	taskID workflow.TaskID,
	preparation workflowexecution.TaskStartPreparation,
	finalizer workflowexecution.TaskPreparationFinalizer,
) (workflowstore.StartTaskResult, error) {
	started, err := s.currentNodeCompletionExecutionStub.StartTask(ctx, taskID, preparation, finalizer)
	if err == nil {
		s.recordStarted(started.Mutation.Created)
	}
	return started, err
}

func (s *manualMoveExecutionStub) ApplyPendingApproval(ctx context.Context, approvalID workflow.ApprovalID) (workflowstore.PendingApprovalApplyResult, error) {
	applied, err := s.currentNodeCompletionExecutionStub.ApplyPendingApproval(ctx, approvalID)
	if err == nil {
		s.recordStarted(applied.Mutation.Created)
	}
	return applied, err
}

func (s *manualMoveExecutionStub) ApplyManualMove(
	ctx context.Context,
	prepared workflowstore.ManualMovePreparation,
	candidate *workflowstore.ExecutionTargetCandidate,
) (workflowstore.ManualMoveResult, error) {
	if err := s.EnsureTaskQuiescent(prepared.TaskID()); err != nil {
		return workflowstore.ManualMoveResult{}, err
	}
	moved, err := s.currentNodeCompletionExecutionStub.ApplyManualMove(ctx, prepared, candidate)
	if err == nil && moved.Outcome == workflowstore.ManualMoveResultOutcomeApplied {
		s.recordStarted(moved.Mutation.Created)
	}
	return moved, err
}

func (s *manualMoveExecutionStub) recordStarted(nodes []workflow.CurrentNode) {
	for _, currentNode := range nodes {
		if currentNode.Scheduling != nil {
			s.started = append(s.started, currentNode.Reference)
		}
	}
}

func (s *manualMoveExecutionStub) EnsureTaskQuiescent(taskID workflow.TaskID) error {
	index := len(s.quiescentTaskIDs)
	s.quiescentTaskIDs = append(s.quiescentTaskIDs, taskID)
	if index < len(s.quiescentErrors) {
		return s.quiescentErrors[index]
	}
	return s.quiescentErr
}

func (s *manualMoveExecutionStub) ManualMoveDisposition(workflow.TaskID) (workflowexecution.ManualMoveDisposition, error) {
	s.dispositionCalls++
	if s.dispositionErr != nil {
		return "", s.dispositionErr
	}
	if s.disposition == "" {
		return workflowexecution.ManualMoveDispositionQuiescent, nil
	}
	return s.disposition, nil
}

func (s *manualMoveExecutionStub) InterruptForManualMove(_ context.Context, taskID workflow.TaskID, beforeSelection func() error) error {
	if s.interruptHook != nil {
		s.interruptHook()
	}
	if beforeSelection != nil {
		if err := beforeSelection(); err != nil {
			return err
		}
	}
	s.interruptTaskIDs = append(s.interruptTaskIDs, taskID)
	return s.interruptErr
}

func waitForTaskMutation(t *testing.T, service *Service, taskID workflow.TaskID, operation func() error, whileBlocked func()) {
	t.Helper()
	entered := make(chan struct{})
	release := make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- service.taskMutations.Run(context.Background(), taskID, func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	finished := make(chan error, 1)
	go func() {
		finished <- operation()
	}()
	select {
	case err := <-finished:
		t.Fatalf("Task mutation escaped Task ownership: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if whileBlocked != nil {
		whileBlocked()
	}
	close(release)
	if err := <-held; err != nil {
		t.Fatalf("hold Task mutation: %v", err)
	}
	if err := <-finished; err != nil {
		t.Fatalf("Task mutation after ownership release: %v", err)
	}
}

func (i *recordingExecutionTargetInfrastructure) RestoreExecutionTarget(_ context.Context, req ExecutionTargetRestoreRequest) error {
	i.restoreTaskID = req.TaskID
	i.restoreRequest = req
	i.setupOperationID = req.SetupOperationID
	if i.restoreRequests != nil {
		i.restoreRequests <- req
	}
	return i.restoreErr
}

func (i *recordingExecutionTargetInfrastructure) ResolveExecutionTarget(_ context.Context, req ExecutionTargetResolveRequest) (workflowstore.ExecutionTargetSnapshot, error) {
	i.resolveSelection = req.Selection
	if i.resolveErr != nil {
		return workflowstore.ExecutionTargetSnapshot{}, i.resolveErr
	}
	return i.resolution, nil
}

func (i *recordingExecutionTargetInfrastructure) InspectProspectiveInitialTaskBranch(_ context.Context, req InitialTaskBranchInspectionRequest) error {
	i.initialBranchInspection = req
	i.initialBranchInspections++
	return i.initialBranchErr
}

func (i *recordingExecutionTargetInfrastructure) AssertInitialTaskBranch(_ context.Context, req InitialTaskBranchAssertionRequest) error {
	i.initialBranchAssertion = req
	i.initialBranchAssertions++
	return i.initialBranchAssertionErr
}

func (i *recordingExecutionTargetInfrastructure) MaterializeExecutionTarget(_ context.Context, req ExecutionTargetMaterializeRequest) (ExecutionTargetMaterialization, error) {
	i.materializeTaskID = req.TaskID
	i.materializeRequest = req
	i.setupOperationID = req.SetupOperationID
	i.setupRequirements = append(i.setupRequirements, req.SetupRequirement)
	var materialization ExecutionTargetMaterialization
	if i.materialize != nil {
		var err error
		materialization, err = i.materialize(req.TaskID)
		if err != nil {
			return materialization, err
		}
	}
	return materialization, i.materializeErr
}

func TestServiceWorkflowListPaginatesAndCreateLinkIsAtomic(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	for _, name := range []string{"Gamma", "Alpha", "Beta"} {
		if _, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: name}); err != nil {
			t.Fatalf("CreateWorkflow %q: %v", name, err)
		}
	}
	defaultPage, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{})
	if err != nil {
		t.Fatalf("ListWorkflows defaults: %v", err)
	}
	if len(defaultPage.Workflows) != 3 || defaultPage.NextOffset != nil {
		t.Fatalf("default page = %+v", defaultPage)
	}
	zeroOffset := 0
	limit := 2
	page1, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{Offset: &zeroOffset, Limit: &limit})
	if err != nil {
		t.Fatalf("ListWorkflows page1: %v", err)
	}
	if len(page1.Workflows) != 2 || page1.NextOffset == nil || *page1.NextOffset != 2 {
		t.Fatalf("page1 = %+v", page1)
	}
	page2, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{Offset: page1.NextOffset, Limit: &limit})
	if err != nil {
		t.Fatalf("ListWorkflows page2: %v", err)
	}
	if len(page2.Workflows) != 1 || page2.NextOffset != nil {
		t.Fatalf("page2 = %+v", page2)
	}
	beyondEnd := 3
	beyondEndPage, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{Offset: &beyondEnd, Limit: &limit})
	if err != nil {
		t.Fatalf("ListWorkflows beyond end: %v", err)
	}
	if len(beyondEndPage.Workflows) != 0 || beyondEndPage.NextOffset != nil {
		t.Fatalf("beyond end page = %+v", beyondEndPage)
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
	if created.Workflow.ID.IsZero() || created.Link.WorkflowID != created.Workflow.ID || !created.Link.Default {
		t.Fatalf("created = %+v, want first default link", created)
	}
	projectID := binding.ProjectID
	projectLimit := 10
	projectPage, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{ProjectID: &projectID, Limit: &projectLimit})
	if err != nil {
		t.Fatalf("project ListWorkflows: %v", err)
	}
	if projectPage.ProjectID == nil || *projectPage.ProjectID != projectID || len(projectPage.Workflows) != 1 {
		t.Fatalf("project page = %+v, want one project-scoped workflow", projectPage)
	}
	if projectPage.Workflows[0].ProjectLink == nil || !projectPage.Workflows[0].ProjectLink.Default {
		t.Fatalf("project workflow = %+v, want default project metadata", projectPage.Workflows[0])
	}
	exactWorkflowID := created.Workflow.ID
	exactPage, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{WorkflowID: &exactWorkflowID, Limit: &projectLimit})
	if err != nil {
		t.Fatalf("exact ListWorkflows: %v", err)
	}
	if len(exactPage.Workflows) != 1 || exactPage.Workflows[0].ID != exactWorkflowID {
		t.Fatalf("exact page = %+v, want selected workflow", exactPage)
	}
	if _, err := service.CreateAndLinkWorkflowToProject(ctx, serverapi.WorkflowCreateAndLinkProjectRequest{
		Name:          "Broken",
		ProjectID:     "missing-project",
		DefaultPolicy: serverapi.WorkflowProjectLinkDefaultIfProjectHasNone,
	}); err == nil {
		t.Fatalf("expected invalid project create-and-link to fail")
	}
	filtered, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{Limit: &projectLimit, Query: "Broken"})
	if err != nil {
		t.Fatalf("ListWorkflows filtered: %v", err)
	}
	if len(filtered.Workflows) != 0 {
		t.Fatalf("failed create-and-link left workflows: %+v", filtered.Workflows)
	}
}

func TestServiceWorkflowDeletePreviewsBlocksAndPublishesDeletion(t *testing.T) {
	ctx, service, projectID, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	preview, err := service.PreviewWorkflowDelete(ctx, serverapi.WorkflowDeletePreviewRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}
	if preview.Impact.WorkflowID != workflowID || preview.Impact.ProjectCount != 1 || preview.Impact.LinkCount != 1 || preview.Impact.TaskCount != 1 {
		t.Fatalf("delete preview = %+v, want one project/link/task", preview)
	}
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: projectID})
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
	if !stringPointerEquals(event.ProjectID, projectID) || !workflowIDPointerEquals(event.WorkflowID, workflowID) || event.Resource != "workflow" || event.Action != "deleted" || event.PrimaryEntityID != workflowID.String() || len(event.RelatedIDs) != 0 {
		t.Fatalf("event = %+v, want workflow deleted event", event)
	}
	eventCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	workflowEvent, err := workflowSub.Next(eventCtx)
	if err != nil {
		t.Fatalf("workflow subscription delete next: %v", err)
	}
	if workflowEvent.ProjectID != nil || !workflowIDPointerEquals(workflowEvent.WorkflowID, workflowID) || workflowEvent.Resource != "workflow" || workflowEvent.Action != "deleted" || workflowEvent.PrimaryEntityID != workflowID.String() || len(workflowEvent.RelatedIDs) != 0 {
		t.Fatalf("workflow-scoped delete event = %+v, want projectless workflow delete event", workflowEvent)
	}
	if _, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: taskID}); err == nil {
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

func TestServiceWorkflowGraphValidatePreviewAndSave(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	source, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow source: %v", err)
	}
	graph := serverapi.WorkflowGraphDraftFromDefinition(source.Definition)
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

	agentID := workflowServiceGraphEntityID("node-agent-" + workflowID.String())
	startEdgeID := workflowServiceGraphEntityID("edge-start-" + workflowID.String())
	startGroupID := workflowServiceGraphEntityID("group-start-" + workflowID.String())
	renamedGraph := renameWorkflowGraphDraftNode(graph, agentID, "Preview Agent")
	renamedGraph = setWorkflowGraphDraftNodeCompletionMode(renamedGraph, agentID, "tool")
	renamedGraph = setWorkflowGraphDraftEdgePrompt(renamedGraph, startEdgeID, "Saved edge prompt.")
	renamedGraph = setWorkflowGraphDraftTransitionDescription(renamedGraph, startGroupID, "Start implementation from the backlog.")
	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: source.Definition.Workflow.Version,
		Metadata:        &serverapi.WorkflowGraphMetadata{Name: "Preview Workflow", Description: "Preview only"},
		Graph:           renamedGraph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	if !preview.Changed || !preview.CanSave || preview.ConfirmationRequired || len(preview.Blockers) != 0 {
		t.Fatalf("preview graph save = %+v, want savable preview without blockers", preview)
	}
	afterPreview, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow after preview: %v", err)
	}
	if afterPreview.Definition.Workflow.Version != source.Definition.Workflow.Version || afterPreview.Definition.Workflow.Name == "Preview Workflow" || workflowServiceNodeByID(t, afterPreview.Definition, agentID).DisplayName == "Preview Agent" {
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
	if _, err := service.SubscribeWorkflow(ctx, serverapi.WorkflowSubscribeRequest{WorkflowID: runtimeids.NewWorkflowID()}); err == nil {
		t.Fatal("SubscribeWorkflow accepted missing workflow")
	}
	customRef := "refs/tags/v1"
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: source.Definition.Workflow.Version,
		Metadata: &serverapi.WorkflowGraphMetadata{
			Name:        "Saved Workflow",
			Description: "Saved metadata",
			ExecutionTargetPolicy: &serverapi.WorkflowExecutionTargetConfiguration{
				Mode:      serverapi.WorkflowExecutionTargetModeCustomRef,
				CustomRef: &customRef,
			},
		},
		Graph: renamedGraph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph: %v", err)
	}
	if !saved.Saved || !saved.Changed || saved.Definition == nil || saved.CurrentVersion != source.Definition.Workflow.Version+1 {
		t.Fatalf("saved graph = %+v, want saved canonical definition with incremented version", saved)
	}
	if saved.Definition.Workflow.Name != "Saved Workflow" || saved.Definition.Workflow.Description != "Saved metadata" {
		t.Fatalf("saved workflow metadata = %+v, want combined metadata persisted", saved.Definition.Workflow)
	}
	if saved.Definition.Workflow.ExecutionTargetPolicy.Mode != serverapi.WorkflowExecutionTargetModeCustomRef ||
		saved.Definition.Workflow.ExecutionTargetPolicy.CustomRef == nil ||
		*saved.Definition.Workflow.ExecutionTargetPolicy.CustomRef != customRef {
		t.Fatalf("saved workflow target policy = %+v, want custom ref", saved.Definition.Workflow.ExecutionTargetPolicy)
	}
	if workflowServiceEdgeByID(t, *saved.Definition, startEdgeID).PromptTemplate != "Saved edge prompt." {
		t.Fatalf("saved response edge prompt = %q, want edited edge prompt", workflowServiceEdgeByID(t, *saved.Definition, startEdgeID).PromptTemplate)
	}
	if workflowServiceNodeByID(t, *saved.Definition, agentID).CompletionMode != "tool" {
		t.Fatalf("saved response node completion mode = %q, want tool", workflowServiceNodeByID(t, *saved.Definition, agentID).CompletionMode)
	}
	if workflowServiceTransitionGroupByID(t, *saved.Definition, startGroupID).Description != "Start implementation from the backlog." {
		t.Fatalf("saved response transition description = %q, want edited transition description", workflowServiceTransitionGroupByID(t, *saved.Definition, startGroupID).Description)
	}
	for _, event := range waitWorkflowProjectActions(t, sub, "workflow", "graph_saved") {
		if !stringPointerEquals(event.ProjectID, binding.ProjectID) || !workflowIDPointerEquals(event.WorkflowID, workflowID) {
			t.Fatalf("event = %+v, want linked workflow event", event)
		}
	}
	eventCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	workflowEvent, err := workflowSub.Next(eventCtx)
	if err != nil {
		t.Fatalf("workflow subscription next: %v", err)
	}
	if workflowEvent.ProjectID != nil || !workflowIDPointerEquals(workflowEvent.WorkflowID, workflowID) || workflowEvent.Resource != "workflow" || workflowEvent.Action != "graph_saved" {
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

func TestServiceWorkflowGraphSaveAllowsEmptyPromptButTaskStartRejects(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	source, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow source: %v", err)
	}
	graph := serverapi.WorkflowGraphDraftFromDefinition(source.Definition)
	graph = setWorkflowGraphDraftEdgePrompt(graph, workflowServiceGraphEntityID("edge-start-"+workflowID.String()), "")

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

func TestServiceWorkflowGraphValidationParityForUnavailableAssigneeAndInvalidScript(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	source, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow source: %v", err)
	}
	graph := serverapi.WorkflowGraphDraftFromDefinition(source.Definition)
	scriptID := workflowServiceGraphEntityID("node-script-" + workflowID.String())
	scriptTransitionID := workflowServiceGraphEntityID("group-agent-script-" + workflowID.String())
	scriptDoneTransitionID := workflowServiceGraphEntityID("group-script-done-" + workflowID.String())
	doneID := workflowServiceNodeIDByKind(t, source.Definition, "terminal")
	agentID := workflowServiceNodeIDByKey(t, source.Definition, "agent")
	graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
		ID:          scriptID,
		Key:         "script",
		Kind:        "script",
		DisplayName: "Script",
	})
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: scriptTransitionID, SourceNodeID: agentID, TransitionID: "script", DisplayName: "Script"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: scriptDoneTransitionID, SourceNodeID: scriptID, TransitionID: "script_done", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		serverapi.WorkflowGraphDraftEdge{ID: workflowServiceGraphEntityID("edge-agent-script-" + workflowID.String()), TransitionGroupID: scriptTransitionID, Key: "script", TargetNodeID: scriptID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session"},
		serverapi.WorkflowGraphDraftEdge{ID: workflowServiceGraphEntityID("edge-script-done-" + workflowID.String()), TransitionGroupID: scriptDoneTransitionID, Key: "done", TargetNodeID: doneID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session"},
	)
	savedScript, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: source.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph script fixture: %v", err)
	}
	if !savedScript.Saved || savedScript.Definition == nil {
		t.Fatalf("script fixture save = %+v, want saved definition", savedScript)
	}
	invalidGraph := serverapi.WorkflowGraphDraftFromDefinition(*savedScript.Definition)
	for index := range invalidGraph.Nodes {
		if invalidGraph.Nodes[index].ID == agentID {
			invalidGraph.Nodes[index].SubagentRole = "unavailable-assignee"
		}
	}
	savedInvalid, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: savedScript.Definition.Workflow.Version,
		Graph:           invalidGraph,
	})
	if err != nil || !savedInvalid.Saved || savedInvalid.Definition == nil {
		t.Fatalf("SaveWorkflowGraph invalid Draft = %+v, err = %v", savedInvalid, err)
	}
	current := serverapi.WorkflowGetResponse{Definition: *savedInvalid.Definition}

	savedDraft, err := service.ValidateWorkflow(ctx, serverapi.WorkflowValidateRequest{WorkflowID: workflowID, Mode: serverapi.WorkflowValidationModeDraft})
	if err != nil {
		t.Fatalf("ValidateWorkflow saved draft: %v", err)
	}
	savedExecution, err := service.ValidateWorkflow(ctx, serverapi.WorkflowValidateRequest{WorkflowID: workflowID, Mode: serverapi.WorkflowValidationModeExecution})
	if err != nil {
		t.Fatalf("ValidateWorkflow saved execution: %v", err)
	}
	draft, err := service.ValidateWorkflowGraphDraft(ctx, serverapi.WorkflowGraphValidateDraftRequest{
		WorkflowID: workflowID,
		Graph:      invalidGraph,
		Modes:      []serverapi.WorkflowValidationMode{serverapi.WorkflowValidationModeDraft, serverapi.WorkflowValidationModeExecution},
	})
	if err != nil {
		t.Fatalf("ValidateWorkflowGraphDraft: %v", err)
	}
	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Definition.Workflow.Version,
		Graph:           invalidGraph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave: %v", err)
	}
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Definition.Workflow.Version,
		Graph:           invalidGraph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph no-op invalid definition: %v", err)
	}
	if !saved.Saved || saved.CurrentVersion != current.Definition.Workflow.Version {
		t.Fatalf("invalid-definition no-op save = %+v, want stable saved response", saved)
	}

	for mode, expected := range map[serverapi.WorkflowValidationMode]serverapi.WorkflowValidateResponse{
		serverapi.WorkflowValidationModeDraft:     savedDraft,
		serverapi.WorkflowValidationModeExecution: savedExecution,
	} {
		if !reflect.DeepEqual(draft.Results[mode], expected) ||
			!reflect.DeepEqual(preview.ValidationResults[mode], expected) ||
			!reflect.DeepEqual(saved.ValidationResults[mode], expected) {
			t.Fatalf("%s validation parity: draft=%+v preview=%+v save=%+v saved=%+v", mode, draft.Results[mode], preview.ValidationResults[mode], saved.ValidationResults[mode], expected)
		}
	}
	if !workflowValidationHasCode(savedDraft.Errors, string(workflow.CodeAgentRoleMissing)) ||
		workflowValidationHasCode(savedDraft.Errors, workflowscript.CodeMissingPath) ||
		!workflowValidationHasCode(savedExecution.Errors, string(workflow.CodeAgentRoleMissing)) ||
		!workflowValidationHasCode(savedExecution.Errors, workflowscript.CodeMissingPath) {
		t.Fatalf("saved validation = draft=%+v execution=%+v, want unavailable assignee in both and missing script path only in execution", savedDraft, savedExecution)
	}
}

func TestServiceWorkflowGraphSaveProjectsRemovedTransitionBranchImpactDeterministically(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow current: %v", err)
	}
	graph := serverapi.WorkflowGraphDraftFromDefinition(current.Definition)
	spareNodeID := workflowServiceGraphEntityID("node-spare-done-" + workflowID.String())
	removedEdgeID := workflowServiceGraphEntityID("edge-spare-done-" + workflowID.String())
	graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
		ID:          spareNodeID,
		Key:         "spare_done",
		Kind:        "terminal",
		DisplayName: "Spare Done",
	})
	graph.Edges = append(graph.Edges, serverapi.WorkflowGraphDraftEdge{
		ID:                removedEdgeID,
		TransitionGroupID: workflowServiceGraphEntityID("group-done-" + workflowID.String()),
		Key:               "spare_done",
		TargetNodeID:      spareNodeID,
		AssigneeSelection: "configured",
		ThinkingSelection: "configured",
		ContextMode:       "new_session",
		ContextSource:     serverapi.WorkflowContextSource{Kind: "immediate_source"},
	})
	added, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph add Transition Branch: %v", err)
	}
	if !added.Saved {
		t.Fatalf("add Transition Branch response = %+v, want saved", added)
	}
	current, err = service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow with removable Transition Branch: %v", err)
	}
	graph = serverapi.WorkflowGraphDraftFromDefinition(current.Definition)
	graph.Edges = slices.DeleteFunc(graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) bool {
		return edge.ID == removedEdgeID
	})
	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave remove Transition Branch: %v", err)
	}
	wantRemoved := []serverapi.WorkflowGraphEntityReference{
		{
			EntityType: serverapi.WorkflowGraphEntityTypeEdge,
			EntityID:   removedEdgeID,
		},
	}
	if !preview.Changed ||
		preview.Impact.RemovedEdgeCount != 1 ||
		!slices.Equal(preview.Impact.RemovedEntities, wantRemoved) ||
		preview.Impact.NodeTaskReferenceCount != 0 ||
		preview.Impact.EdgeTaskReferenceCount != 0 {
		t.Fatalf("removed Transition Branch preview = %+v, want changed exact impact %+v with unchanged Task-reference counts", preview, wantRemoved)
	}
	wantConfirmationEntities := []serverapi.WorkflowGraphEntityReference{{
		EntityType: serverapi.WorkflowGraphEntityTypeEdge,
		EntityID:   removedEdgeID,
	}}
	if got := workflowServiceGraphSaveBlockerEntities(preview.Blockers, "confirmation_required"); !slices.Equal(got, wantConfirmationEntities) {
		t.Fatalf("confirmation_required affected entities = %+v, want %+v", got, wantConfirmationEntities)
	}
	if err := preview.Validate(); err != nil {
		t.Fatalf("preview response validation: %v", err)
	}
	first, err := json.Marshal(preview)
	if err != nil {
		t.Fatalf("marshal first preview: %v", err)
	}
	var roundTrip serverapi.WorkflowGraphSavePreviewResponse
	if err := json.Unmarshal(first, &roundTrip); err != nil {
		t.Fatalf("unmarshal preview: %v", err)
	}
	if !slices.Equal(roundTrip.Impact.RemovedEntities, wantRemoved) {
		t.Fatalf("round-trip removed entities = %+v, want canonical order %+v", roundTrip.Impact.RemovedEntities, wantRemoved)
	}
	second, err := json.Marshal(preview)
	if err != nil {
		t.Fatalf("marshal second preview: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("preview JSON is not deterministic:\nfirst:  %s\nsecond: %s", first, second)
	}
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Definition.Workflow.Version,
		Graph:           graph,
		Confirmation: &serverapi.WorkflowGraphSaveConfirmation{
			ExpectedRemovedNodeGroupCount:       preview.Impact.RemovedNodeGroupCount,
			ExpectedRemovedNodeCount:            preview.Impact.RemovedNodeCount,
			ExpectedRemovedTransitionGroupCount: preview.Impact.RemovedTransitionGroupCount,
			ExpectedRemovedEdgeCount:            preview.Impact.RemovedEdgeCount,
			ExpectedNodeTaskReferenceCount:      preview.Impact.NodeTaskReferenceCount,
			ExpectedEdgeTaskReferenceCount:      preview.Impact.EdgeTaskReferenceCount,
		},
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph confirmed removal: %v", err)
	}
	if !saved.Saved || !saved.Changed {
		t.Fatalf("confirmed removal = %+v, want changed save", saved)
	}
}

func TestServiceWorkflowGraphSaveRejectsStaleNoop(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	original, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow original: %v", err)
	}
	if _, err := service.UpdateWorkflow(ctx, serverapi.WorkflowUpdateRequest{
		WorkflowID:  workflowID,
		Name:        "Remote rename",
		Description: "Remote description",
	}); err != nil {
		t.Fatalf("UpdateWorkflow: %v", err)
	}
	current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow current: %v", err)
	}
	req := serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: original.Definition.Workflow.Version,
		Graph:           serverapi.WorkflowGraphDraftFromDefinition(original.Definition),
	}
	preview, err := service.PreviewWorkflowGraphSave(ctx, req)
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave stale no-op: %v", err)
	}
	if preview.Changed || preview.CanSave {
		t.Fatalf("stale no-op preview = %+v, want unchanged blocked response", preview)
	}
	if got := workflowServiceGraphSaveBlockerEntities(preview.Blockers, "version_changed"); got == nil || len(got) != 0 {
		t.Fatalf("version_changed affected entities = %+v, want present empty collection", got)
	}

	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      req.WorkflowID,
		ExpectedVersion: req.ExpectedVersion,
		Graph:           req.Graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph stale no-op: %v", err)
	}
	if saved.Saved || saved.CanSave {
		t.Fatalf("stale no-op save = %+v, want blocked response", saved)
	}
	if got := workflowServiceGraphSaveBlockerEntities(saved.Blockers, "version_changed"); got == nil || len(got) != 0 {
		t.Fatalf("save version_changed affected entities = %+v, want present empty collection", got)
	}
	after, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow after stale no-op: %v", err)
	}
	if after.Definition.Workflow.Version != current.Definition.Workflow.Version ||
		after.Definition.Workflow.Name != current.Definition.Workflow.Name {
		t.Fatalf("workflow after stale no-op = %+v, want unchanged current workflow %+v", after.Definition.Workflow, current.Definition.Workflow)
	}
}

func TestServiceWorkflowGraphSaveAllowsRemovingNodeGroupWithoutConfirmation(t *testing.T) {
	ctx, service, workflowID := newWorkflowGraphAtomicFanOutFixture(t)
	current, removedGroupID := addWorkflowGraphAtomicParallelNodeGroup(t, ctx, service, workflowID)
	graph := serverapi.WorkflowGraphDraftFromDefinition(current)
	graph.NodeGroups = slices.DeleteFunc(graph.NodeGroups, func(group serverapi.WorkflowGraphDraftNodeGroup) bool {
		return group.ID == removedGroupID
	})
	for index := range graph.Nodes {
		if graph.Nodes[index].GroupID != nil && *graph.Nodes[index].GroupID == removedGroupID {
			graph.Nodes[index].GroupID = nil
			graph.Nodes[index].GroupKey = ""
		}
	}

	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave remove Node Group: %v", err)
	}
	wantRemoved := []serverapi.WorkflowGraphEntityReference{{
		EntityType: serverapi.WorkflowGraphEntityTypeNodeGroup,
		EntityID:   removedGroupID,
	}}
	if !preview.Changed ||
		preview.Impact.RemovedNodeGroupCount != 1 ||
		!slices.Equal(preview.Impact.RemovedEntities, wantRemoved) {
		t.Fatalf("removed Node Group preview = %+v, want changed exact impact %+v", preview, wantRemoved)
	}
	if !preview.CanSave || preview.ConfirmationRequired {
		t.Fatalf("removed Node Group preview = %+v, want saveable without confirmation", preview)
	}

	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph remove Node Group: %v", err)
	}
	if !saved.Saved || !saved.Changed || saved.ConfirmationRequired {
		t.Fatalf("removed Node Group save = %+v, want changed save without confirmation", saved)
	}
}

func addWorkflowGraphAtomicParallelNodeGroup(
	t *testing.T,
	ctx context.Context,
	service *Service,
	workflowID runtimeids.WorkflowID,
) (serverapi.WorkflowDefinition, string) {
	t.Helper()
	current := getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)
	graph := serverapi.WorkflowGraphDraftFromDefinition(current)
	graph.TransitionGroups = slices.DeleteFunc(graph.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) bool {
		return group.ID == workflowServiceGraphEntityID("group-alternate-"+workflowID.String())
	})
	graph.Edges = slices.DeleteFunc(graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) bool {
		return edge.ID == workflowServiceGraphEntityID("edge-alternate-"+workflowID.String())
	})
	assertWorkflowGraphAtomicChangedSave(t, ctx, service, current, graph)
	current = getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID)

	groupID := workflowServiceGraphEntityID("group-parallel-" + workflowID.String())
	graph = serverapi.WorkflowGraphDraftFromDefinition(current)
	graph.NodeGroups = append(graph.NodeGroups, serverapi.WorkflowGraphDraftNodeGroup{
		ID: groupID, Key: "parallel", DisplayName: "Parallel",
	})
	for index := range graph.Nodes {
		switch graph.Nodes[index].ID {
		case workflowServiceGraphEntityID("node-a-" + workflowID.String()),
			workflowServiceGraphEntityID("node-b-" + workflowID.String()),
			workflowServiceGraphEntityID("node-join-" + workflowID.String()):
			graph.Nodes[index].GroupID = &groupID
			graph.Nodes[index].GroupKey = "parallel"
		}
	}
	assertWorkflowGraphAtomicChangedSave(t, ctx, service, current, graph)
	return getWorkflowGraphAtomicDefinition(t, ctx, service, workflowID), groupID
}

func TestServiceWorkflowGraphSaveChecksVersionBeforeDraftIdentityFields(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	original, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow original: %v", err)
	}
	current, err := service.UpdateWorkflow(ctx, serverapi.WorkflowUpdateRequest{
		WorkflowID:  workflowID,
		Name:        "Updated Workflow",
		Description: "",
	})
	if err != nil {
		t.Fatalf("UpdateWorkflow: %v", err)
	}

	blankGroupID := ""
	graph := serverapi.WorkflowGraphDraft{Nodes: []serverapi.WorkflowGraphDraftNode{{
		ID:             "node-prefixed",
		Kind:           string(serverapi.WorkflowNodeKindAgent),
		GroupID:        &blankGroupID,
		CompletionMode: "tool",
	}}}
	preview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: original.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave stale malformed Draft: %v", err)
	}
	if preview.CurrentVersion != current.Definition.Workflow.Version ||
		!workflowServiceHasGraphSaveBlocker(preview.Blockers, "version_changed") {
		t.Fatalf("preview = %+v, want current-version blocker", preview)
	}

	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: original.Definition.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph stale malformed Draft: %v", err)
	}
	if saved.Saved ||
		saved.CurrentVersion != current.Definition.Workflow.Version ||
		!workflowServiceHasGraphSaveBlocker(saved.Blockers, "version_changed") {
		t.Fatalf("save = %+v, want current-version blocker", saved)
	}

	_, err = service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Definition.Workflow.Version,
		Graph:           graph,
	})
	if !isWorkflowServiceRequestFieldError(err, "graph.nodes.group_id") {
		t.Fatalf("current-version preview error = %v, want group_id validation", err)
	}
	_, err = service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Definition.Workflow.Version,
		Graph:           graph,
	})
	if !isWorkflowServiceRequestFieldError(err, "graph.nodes.group_id") {
		t.Fatalf("current-version save error = %v, want group_id validation", err)
	}
}

func TestServiceWorkflowGraphSaveRejectsAdditionIdentityOwnedByCurrentEntityOfAnotherType(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow current: %v", err)
	}
	graph := serverapi.WorkflowGraphDraftFromDefinition(current.Definition)
	currentNodeID := current.Definition.Nodes[0].ID
	conflictingGroupIndex := len(graph.NodeGroups)
	graph.NodeGroups = append(graph.NodeGroups, serverapi.WorkflowGraphDraftNodeGroup{
		ID:          currentNodeID,
		Key:         "conflicting_identity",
		DisplayName: "Conflicting Identity",
	})

	for _, operation := range []struct {
		name string
		call func() error
	}{
		{
			name: "preview",
			call: func() error {
				_, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
					WorkflowID:      workflowID,
					ExpectedVersion: current.Definition.Workflow.Version,
					Graph:           graph,
				})
				return err
			},
		},
		{
			name: "save",
			call: func() error {
				_, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
					WorkflowID:      workflowID,
					ExpectedVersion: current.Definition.Workflow.Version,
					Graph:           graph,
				})
				return err
			},
		},
	} {
		t.Run(operation.name, func(t *testing.T) {
			wantField := fmt.Sprintf("graph.node_groups[%d].id", conflictingGroupIndex)
			if err := operation.call(); !isWorkflowServiceRequestFieldError(err, wantField) {
				t.Fatalf("%s error = %#v, want typed cross-type identity rejection", operation.name, err)
			}
		})
	}

	reloaded, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow reloaded: %v", err)
	}
	if reloaded.Definition.Workflow.Version != current.Definition.Workflow.Version {
		t.Fatalf("Workflow Version = %d, want %d", reloaded.Definition.Workflow.Version, current.Definition.Workflow.Version)
	}
}

func TestServiceWorkflowGraphSaveUsesStoreVersionOrdering(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow current: %v", err)
	}

	if err := service.store.UpdateWorkflowInfo(ctx, workflowID, "Concurrent update", ""); err != nil {
		t.Fatalf("UpdateWorkflowInfo: %v", err)
	}
	outcome, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Definition.Workflow.Version,
		Graph: serverapi.WorkflowGraphDraft{Nodes: []serverapi.WorkflowGraphDraftNode{{
			ID:             "node-prefixed",
			Kind:           string(serverapi.WorkflowNodeKindAgent),
			CompletionMode: "tool",
		}}},
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph stale malformed Draft: %v", err)
	}
	if outcome.Saved ||
		outcome.CurrentVersion != current.Definition.Workflow.Version+1 ||
		!workflowServiceHasGraphSaveBlocker(outcome.Blockers, "version_changed") {
		t.Fatalf("save = %+v, want current-version blocker", outcome)
	}
}

func TestServiceWorkflowGraphSaveDoesNotWaitForTaskMutation(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow current: %v", err)
	}

	mutationStarted := make(chan struct{})
	releaseMutation := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- service.taskMutations.Run(ctx, workflow.TaskID("task-unrelated"), func(context.Context) error {
			close(mutationStarted)
			<-releaseMutation
			return nil
		})
	}()
	<-mutationStarted

	saveDone := make(chan struct {
		response serverapi.WorkflowGraphSaveResponse
		err      error
	}, 1)
	go func() {
		response, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
			WorkflowID:      workflowID,
			ExpectedVersion: current.Definition.Workflow.Version,
			Graph:           serverapi.WorkflowGraphDraftFromDefinition(current.Definition),
		})
		saveDone <- struct {
			response serverapi.WorkflowGraphSaveResponse
			err      error
		}{response: response, err: err}
	}()

	select {
	case outcome := <-saveDone:
		if outcome.err != nil {
			t.Fatalf("SaveWorkflowGraph: %v", outcome.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Workflow graph save waited for unrelated Task mutation")
	}

	close(releaseMutation)
	if err := <-mutationDone; err != nil {
		t.Fatalf("release Task mutation: %v", err)
	}
}

func TestServiceWorkflowGraphPreviewOrdersVersionGateWithSameWorkflowMetadataMutation(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	otherWorkflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow current: %v", err)
	}
	otherCurrent, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: otherWorkflowID})
	if err != nil {
		t.Fatalf("GetWorkflow other current: %v", err)
	}

	laneStarted := make(chan struct{})
	releaseLane := make(chan struct{})
	laneDone := make(chan error, 1)
	go func() {
		_, err := service.store.RunWorkflowGraphSaveOperation(ctx, workflowID, func(ctx context.Context) (workflowstore.WorkflowGraphSaveResult, error) {
			close(laneStarted)
			<-releaseLane
			return workflowstore.WorkflowGraphSaveResult{}, service.store.UpdateWorkflowInfo(ctx, workflowID, "Concurrent update", "")
		})
		laneDone <- err
	}()
	<-laneStarted

	previewDone := make(chan struct {
		response serverapi.WorkflowGraphSavePreviewResponse
		err      error
	}, 1)
	go func() {
		response, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
			WorkflowID:      workflowID,
			ExpectedVersion: current.Definition.Workflow.Version,
			Graph: serverapi.WorkflowGraphDraft{Nodes: []serverapi.WorkflowGraphDraftNode{{
				ID:             "node-prefixed",
				Kind:           string(serverapi.WorkflowNodeKindAgent),
				CompletionMode: "tool",
			}}},
		})
		previewDone <- struct {
			response serverapi.WorkflowGraphSavePreviewResponse
			err      error
		}{response: response, err: err}
	}()

	otherPreview, err := service.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      otherWorkflowID,
		ExpectedVersion: otherCurrent.Definition.Workflow.Version,
		Graph:           serverapi.WorkflowGraphDraftFromDefinition(otherCurrent.Definition),
	})
	if err != nil {
		t.Fatalf("PreviewWorkflowGraphSave other Workflow: %v", err)
	}
	if otherPreview.Changed {
		t.Fatalf("other Workflow preview = %+v, want unchanged", otherPreview)
	}
	select {
	case outcome := <-previewDone:
		t.Fatalf("same-Workflow Preview bypassed metadata ordering: response=%+v error=%v", outcome.response, outcome.err)
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseLane)
	if err := <-laneDone; err != nil {
		t.Fatalf("same-Workflow metadata mutation: %v", err)
	}
	outcome := <-previewDone
	if outcome.err != nil {
		t.Fatalf("PreviewWorkflowGraphSave stale malformed Draft: %v", outcome.err)
	}
	if outcome.response.CurrentVersion != current.Definition.Workflow.Version+1 ||
		!workflowServiceHasGraphSaveBlocker(outcome.response.Blockers, "version_changed") {
		t.Fatalf("preview = %+v, want current-version blocker", outcome.response)
	}
}

func workflowServiceHasGraphSaveBlocker(blockers []serverapi.WorkflowGraphSaveBlocker, code string) bool {
	for _, blocker := range blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

func TestWorkflowGraphStoreSaveSerializesSameWorkflowWithoutBlockingDifferentWorkflow(t *testing.T) {
	resolver := &blockingWorkflowGraphRoleResolver{started: make(chan struct{}), release: make(chan struct{})}
	service, _, _ := newWorkflowServiceTestServiceWithRoleResolver(t, resolver)
	ctx := context.Background()
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	otherWorkflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow current: %v", err)
	}
	other, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: otherWorkflowID})
	if err != nil {
		t.Fatalf("GetWorkflow other: %v", err)
	}
	resolver.Arm()
	firstRequest := serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Definition.Workflow.Version,
		Graph:           renameWorkflowGraphDraftNode(serverapi.WorkflowGraphDraftFromDefinition(current.Definition), workflowServiceGraphEntityID("node-agent-"+workflowID.String()), "First"),
	}
	type saveResult struct {
		result workflowstore.WorkflowGraphSaveResult
		err    error
	}
	firstDone := make(chan saveResult, 1)
	go func() {
		storeRequest, err := workflowGraphStoreSaveRequest(
			firstRequest.WorkflowID,
			firstRequest.ExpectedVersion,
			firstRequest.Metadata,
			firstRequest.Graph,
			firstRequest.Confirmation,
		)
		if err != nil {
			firstDone <- saveResult{err: err}
			return
		}
		result, err := service.store.SaveWorkflowGraph(ctx, storeRequest)
		firstDone <- saveResult{result: result, err: err}
	}()
	<-resolver.started

	sameDone := make(chan saveResult, 1)
	go func() {
		request := serverapi.WorkflowGraphSaveRequest{
			WorkflowID:      workflowID,
			ExpectedVersion: current.Definition.Workflow.Version,
			Graph:           renameWorkflowGraphDraftNode(serverapi.WorkflowGraphDraftFromDefinition(current.Definition), workflowServiceGraphEntityID("node-agent-"+workflowID.String()), "Second"),
		}
		storeRequest, err := workflowGraphStoreSaveRequest(
			request.WorkflowID,
			request.ExpectedVersion,
			request.Metadata,
			request.Graph,
			request.Confirmation,
		)
		if err != nil {
			sameDone <- saveResult{err: err}
			return
		}
		result, err := service.store.SaveWorkflowGraph(ctx, storeRequest)
		sameDone <- saveResult{result: result, err: err}
	}()
	differentDone := make(chan saveResult, 1)
	go func() {
		request := serverapi.WorkflowGraphSaveRequest{
			WorkflowID:      otherWorkflowID,
			ExpectedVersion: other.Definition.Workflow.Version,
			Graph:           renameWorkflowGraphDraftNode(serverapi.WorkflowGraphDraftFromDefinition(other.Definition), workflowServiceGraphEntityID("node-agent-"+otherWorkflowID.String()), "Independent"),
		}
		storeRequest, err := workflowGraphStoreSaveRequest(
			request.WorkflowID,
			request.ExpectedVersion,
			request.Metadata,
			request.Graph,
			request.Confirmation,
		)
		if err != nil {
			differentDone <- saveResult{err: err}
			return
		}
		result, err := service.store.SaveWorkflowGraph(ctx, storeRequest)
		differentDone <- saveResult{result: result, err: err}
	}()
	different := <-differentDone
	if different.err != nil || !different.result.Saved {
		t.Fatalf("different workflow save = result=%+v error=%v, want independent completion", different.result, different.err)
	}
	select {
	case outcome := <-sameDone:
		t.Fatalf("same-workflow save completed while first was preparing: %+v", outcome)
	default:
	}
	close(resolver.release)
	first := <-firstDone
	if first.err != nil || !first.result.Saved {
		t.Fatalf("first workflow save = result=%+v error=%v", first.result, first.err)
	}
	second := <-sameDone
	versionChanged := false
	for _, blocker := range second.result.Blockers {
		versionChanged = versionChanged || blocker.Code == "version_changed"
	}
	if second.err != nil || second.result.Saved || !versionChanged {
		t.Fatalf("serialized same-workflow save = result=%+v error=%v, want stale version after first commit", second.result, second.err)
	}
}

func TestServiceWorkflowGraphSaveReturnsExactCommittedResponseWhenNextSaveWinsRace(t *testing.T) {
	ctx, service, _ := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow current: %v", err)
	}
	publisher := &blockingWorkflowGraphEventPublisher{started: make(chan struct{}), release: make(chan struct{})}
	service.store.SetWorkflowEventPublisher(publisher)
	agentID := workflowServiceNodeIDByKey(t, current.Definition, "agent")
	firstGraph := renameWorkflowGraphDraftNode(serverapi.WorkflowGraphDraftFromDefinition(current.Definition), agentID, "First response")
	type saveResult struct {
		response serverapi.WorkflowGraphSaveResponse
		err      error
	}
	firstDone := make(chan saveResult, 1)
	go func() {
		response, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
			WorkflowID:      workflowID,
			ExpectedVersion: current.Definition.Workflow.Version,
			Graph:           firstGraph,
		})
		firstDone <- saveResult{response: response, err: err}
	}()
	<-publisher.started

	secondGraph := renameWorkflowGraphDraftNode(firstGraph, agentID, "Second response")
	second, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Definition.Workflow.Version + 1,
		Graph:           secondGraph,
	})
	if err != nil {
		t.Fatalf("second SaveWorkflowGraph: %v", err)
	}
	if !second.Saved || second.Definition == nil || second.CurrentVersion != current.Definition.Workflow.Version+2 ||
		workflowServiceNodeByID(t, *second.Definition, agentID).DisplayName != "Second response" {
		t.Fatalf("second save = %+v, want exact second committed definition", second)
	}
	close(publisher.release)
	first := <-firstDone
	if first.err != nil || !first.response.Saved || first.response.Definition == nil ||
		first.response.CurrentVersion != current.Definition.Workflow.Version+1 ||
		workflowServiceNodeByID(t, *first.response.Definition, agentID).DisplayName != "First response" {
		t.Fatalf("first delayed response = %+v error=%v, want exact first committed definition", first.response, first.err)
	}
}

type blockingWorkflowGraphRoleResolver struct {
	started     chan struct{}
	release     chan struct{}
	mu          sync.Mutex
	armed       bool
	startedOnce bool
}

func (r *blockingWorkflowGraphRoleResolver) Arm() {
	r.mu.Lock()
	r.armed = true
	r.mu.Unlock()
}

func (r *blockingWorkflowGraphRoleResolver) ResolveConfiguredRole(role string) (workflow.TargetAgentRole, bool) {
	r.mu.Lock()
	shouldBlock := r.armed && !r.startedOnce
	if shouldBlock {
		r.startedOnce = true
	}
	r.mu.Unlock()
	if shouldBlock {
		close(r.started)
		<-r.release
	}
	return workflow.TargetAgentRole{Identity: role, QuestionsEnabled: true}, true
}

func (r *blockingWorkflowGraphRoleResolver) ExplicitCallableRoles() []workflow.TargetAgentRole {
	return nil
}

type blockingWorkflowGraphEventPublisher struct {
	started     chan struct{}
	release     chan struct{}
	mu          sync.Mutex
	startedOnce bool
}

func (p *blockingWorkflowGraphEventPublisher) PublishWorkflowEvent(context.Context, workflowstore.WorkflowEventRecord) error {
	p.mu.Lock()
	shouldBlock := !p.startedOnce
	if shouldBlock {
		p.startedOnce = true
	}
	p.mu.Unlock()
	if shouldBlock {
		close(p.started)
		<-p.release
	}
	return nil
}

func workflowValidationHasCode(errors []serverapi.WorkflowValidationError, code string) bool {
	for _, validationErr := range errors {
		if validationErr.Code == code {
			return true
		}
	}
	return false
}

func workflowGraphSaveResponseHasBlocker(response serverapi.WorkflowGraphSaveResponse, code string) bool {
	for _, blocker := range response.Blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
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

func newWorkflowServiceOrdinaryTaskFixture(t *testing.T) (context.Context, *Service, string, runtimeids.WorkflowID, string) {
	t.Helper()
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	return ctx, service, binding.ProjectID, workflowID, task.Task.ID
}

func newWorkflowServiceTestContextWithMetadata(t *testing.T) (context.Context, *Service, metadata.Binding, *metadata.Store) {
	t.Helper()
	service, binding, metadataStore := newWorkflowServiceTestServiceWithMetadata(t)
	return context.Background(), service, binding, metadataStore
}

func bindWorkflowServiceManagedWorktree(
	t *testing.T,
	ctx context.Context,
	store *metadata.Store,
	workspaceID string,
	taskID workflow.TaskID,
	worktreeID string,
	root string,
	createdBranch bool,
) {
	t.Helper()
	if err := store.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
		ID: worktreeID, WorkspaceID: workspaceID, CanonicalRoot: root, Managed: true, CreatedBranch: createdBranch,
	}); err != nil {
		t.Fatalf("UpsertWorktreeRecord: %v", err)
	}
	updated, err := store.Queries().BindInitialTaskManagedWorktree(ctx, sqlitegen.BindInitialTaskManagedWorktreeParams{
		TaskID: string(taskID), ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true}, UpdatedAtUnixMs: time.Now().UTC().UnixMilli(),
	})
	if err != nil || updated != 1 {
		t.Fatalf("UpdateTaskManagedWorktree = %d, %v; want one row", updated, err)
	}
}

func TestNewRejectsEveryMissingReadModelCapability(t *testing.T) {
	service, _, metadataStore := newWorkflowServiceTestServiceWithMetadata(t)
	complete := newWorkflowServiceReadModels(t, metadataStore, service.store, service.roleResolver, nil, nil)
	tests := []struct {
		name       string
		readModels ReadModels
	}{
		{name: "definitions", readModels: ReadModels{Board: complete.Board, TaskList: complete.TaskList, TaskSearch: complete.TaskSearch, TaskDetail: complete.TaskDetail, Activity: complete.Activity, Attention: complete.Attention}},
		{name: "board", readModels: ReadModels{Definitions: complete.Definitions, TaskList: complete.TaskList, TaskSearch: complete.TaskSearch, TaskDetail: complete.TaskDetail, Activity: complete.Activity, Attention: complete.Attention}},
		{name: "task list", readModels: ReadModels{Definitions: complete.Definitions, Board: complete.Board, TaskSearch: complete.TaskSearch, TaskDetail: complete.TaskDetail, Activity: complete.Activity, Attention: complete.Attention}},
		{name: "task search", readModels: ReadModels{Definitions: complete.Definitions, Board: complete.Board, TaskList: complete.TaskList, TaskDetail: complete.TaskDetail, Activity: complete.Activity, Attention: complete.Attention}},
		{name: "task detail", readModels: ReadModels{Definitions: complete.Definitions, Board: complete.Board, TaskList: complete.TaskList, TaskSearch: complete.TaskSearch, Activity: complete.Activity, Attention: complete.Attention}},
		{name: "activity", readModels: ReadModels{Definitions: complete.Definitions, Board: complete.Board, TaskList: complete.TaskList, TaskSearch: complete.TaskSearch, TaskDetail: complete.TaskDetail, Attention: complete.Attention}},
		{name: "attention", readModels: ReadModels{Definitions: complete.Definitions, Board: complete.Board, TaskList: complete.TaskList, TaskSearch: complete.TaskSearch, TaskDetail: complete.TaskDetail, Activity: complete.Activity}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(service.store, tt.readModels, service.roleResolver, workflowexecution.NewTaskMutationCoordinator()); err == nil {
				t.Fatal("New accepted a missing read-model capability")
			}
		})
	}
}

func newWorkflowServiceTestServiceWithMetadata(t *testing.T) (*Service, metadata.Binding, *metadata.Store) {
	return newWorkflowServiceTestServiceWithRoleResolver(t, testsetup.QuestionsEnabled("coder"))
}

func newWorkflowServiceTestServiceWithRoleResolver(t *testing.T, resolver workflow.RoleResolver) (*Service, metadata.Binding, *metadata.Store) {
	t.Helper()
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.PersistenceRootEnvName, filepath.Join(home, "kent-root"))
	cfg, err := config.Load(workspaceRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	metadataStore := testsetup.OpenStore(t, cfg.PersistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "WOR"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(resolver))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	readModels := newWorkflowServiceReadModels(t, metadataStore, store, resolver, nil, nil)
	baseExecution := &currentNodeCompletionExecutionStub{
		store:                 store,
		manualMoveAssignments: workflowServiceTestManualMoveAssignments(t, metadataStore),
	}
	service, err := New(store, readModels, resolver, workflowexecution.NewTaskMutationCoordinator(), WithCurrentNodeExecution(baseExecution))
	if err != nil {
		t.Fatalf("workflowsvc.New: %v", err)
	}
	service.executionTargets = &recordingExecutionTargetInfrastructure{}
	service.setupEvents = &workflowTaskSetupEventRecorder{}
	return service, binding, metadataStore
}

func newWorkflowServiceReadModels(
	t *testing.T,
	metadataStore *metadata.Store,
	store *workflowstore.Store,
	resolver workflow.RoleResolver,
	transcripts workflowview.SessionActiveTranscriptProvider,
	prompts workflowview.PendingPromptSource,
) ReadModels {
	t.Helper()
	if prompts == nil {
		prompts = emptyWorkflowPendingPromptSource{}
	}
	definitions, err := workflowview.NewDefinitionProjection(store)
	if err != nil {
		t.Fatalf("workflowview.NewDefinitionProjection: %v", err)
	}
	projector := workflowview.NewTaskProjector()
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	quiescence := workflowViewQuiescenceSource{}
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close workflow read-model authority: %v", err)
		}
	})
	projection, err := workflowview.NewTaskStatusProjection(
		metadataStore,
		store,
		projector,
		workflowViewStatusObservationSource{authority: authority, quiescence: quiescence},
	)
	if err != nil {
		t.Fatalf("workflowview.NewTaskStatusProjection: %v", err)
	}
	dependencyCounter, err := workflowview.NewTaskDependencyCounter(metadataStore)
	if err != nil {
		t.Fatalf("workflowview.NewTaskDependencyCounter: %v", err)
	}
	taskSearch, err := workflowview.NewTaskSearch(metadataStore, projection)
	if err != nil {
		t.Fatalf("workflowview.NewTaskSearch: %v", err)
	}
	taskList, err := workflowview.NewTaskList(metadataStore, definitions, projection)
	if err != nil {
		t.Fatalf("workflowview.NewTaskList: %v", err)
	}
	board, err := workflowview.NewBoard(metadataStore, definitions, resolver, projection)
	if err != nil {
		t.Fatalf("workflowview.NewBoard: %v", err)
	}
	dependencies, err := workflowview.NewTaskDependencies(metadataStore, projection, dependencyCounter)
	if err != nil {
		t.Fatalf("workflowview.NewTaskDependencies: %v", err)
	}
	taskDetail, err := workflowview.NewTaskDetail(metadataStore, projection, dependencies)
	if err != nil {
		t.Fatalf("workflowview.NewTaskDetail: %v", err)
	}
	activity, err := workflowview.NewActivity(metadataStore, projector)
	if err != nil {
		t.Fatalf("workflowview.NewActivity: %v", err)
	}
	attention, err := workflowview.NewAttention(metadataStore, definitions, authority, prompts)
	if err != nil {
		t.Fatalf("workflowview.NewAttention: %v", err)
	}
	return ReadModels{
		Definitions:      definitions,
		Board:            board,
		TaskList:         taskList,
		TaskSearch:       taskSearch,
		TaskDetail:       taskDetail,
		TaskDependencies: dependencies,
		TaskSessions:     emptyWorkflowTaskSessionReadModel{},
		Activity:         activity,
		Attention:        attention,
		Approvals:        emptyWorkflowApprovalView{},
	}
}

type emptyWorkflowTaskSessionReadModel struct{}

func (emptyWorkflowTaskSessionReadModel) List(_ context.Context, request serverapi.WorkflowTaskOffsetPageRequest) (serverapi.WorkflowTaskSessionListResponse, error) {
	return serverapi.WorkflowTaskSessionListResponse{
		TaskID: request.TaskID,
		WorkflowOffsetPage: serverapi.WorkflowOffsetPage[serverapi.WorkflowTaskSessionItem]{
			Items: []serverapi.WorkflowTaskSessionItem{},
		},
	}, nil
}

type workflowViewQuiescenceSource struct{}

type workflowViewStatusObservationSource struct {
	authority  *sessionruntime.Authority
	quiescence workflowViewTaskQuiescenceSource
}

type workflowViewTaskQuiescenceSource interface {
	CurrentTaskQuiescence([]workflow.TaskID) (map[workflow.TaskID]bool, error)
}

func (s workflowViewStatusObservationSource) ObserveWorkflowTaskExecutions(taskIDs []workflow.TaskID) (workflowexecution.WorkflowTaskExecutionObservation, error) {
	executions, err := s.authority.CurrentWorkflowTaskExecutionSnapshots()
	if err != nil {
		return workflowexecution.WorkflowTaskExecutionObservation{}, err
	}
	quiescence, err := s.quiescence.CurrentTaskQuiescence(taskIDs)
	if err != nil {
		return workflowexecution.WorkflowTaskExecutionObservation{}, err
	}
	return workflowexecution.WorkflowTaskExecutionObservation{
		Executions: executions,
		Quiescence: quiescence,
	}, nil
}

func (workflowViewQuiescenceSource) CurrentTaskQuiescence(taskIDs []workflow.TaskID) (map[workflow.TaskID]bool, error) {
	result := make(map[workflow.TaskID]bool, len(taskIDs))
	for _, taskID := range taskIDs {
		result[taskID] = true
	}
	return result, nil
}

type emptyWorkflowPendingPromptSource struct{}

type emptyWorkflowApprovalView struct{}

func (emptyWorkflowApprovalView) ListPendingApprovalsBySession(context.Context, serverapi.ApprovalListPendingBySessionRequest) (serverapi.ApprovalListPendingBySessionResponse, error) {
	return serverapi.ApprovalListPendingBySessionResponse{}, nil
}

func (emptyWorkflowPendingPromptSource) ListPendingPrompts(string) ([]workflowview.PendingPromptSnapshot, error) {
	return nil, nil
}

func stringPtr(value string) *string {
	return &value
}

func stringPointerEquals(value *string, expected string) bool {
	return value != nil && *value == expected
}

func workflowIDPointerEquals(value *runtimeids.WorkflowID, expected runtimeids.WorkflowID) bool {
	return value != nil && *value == expected
}

func workflowLabelErrorHasReason(err error, reason serverapi.WorkflowLabelErrorReason) bool {
	var labelErr *serverapi.WorkflowLabelError
	return errors.As(err, &labelErr) && labelErr.Reason == reason
}

func linkWorkflowServiceProject(t *testing.T, ctx context.Context, service *Service, req serverapi.WorkflowLinkProjectRequest) serverapi.WorkflowLinkProjectResponse {
	t.Helper()
	link, err := service.LinkWorkflowToProject(ctx, req)
	if err != nil {
		t.Fatalf("LinkWorkflowToProject: %v", err)
	}
	return link
}

func linkDefaultWorkflowServiceProject(t *testing.T, ctx context.Context, service *Service, projectID string, workflowID runtimeids.WorkflowID) {
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

func setWorkflowServiceExecutionTargetPolicy(t *testing.T, ctx context.Context, service *Service, workflowID runtimeids.WorkflowID, policy serverapi.WorkflowExecutionTargetConfiguration) {
	t.Helper()
	current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow before policy update: %v", err)
	}
	_, err = service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Definition.Workflow.Version,
		Metadata: &serverapi.WorkflowGraphMetadata{
			Name:                  current.Definition.Workflow.Name,
			Description:           current.Definition.Workflow.Description,
			ExecutionTargetPolicy: &policy,
		},
		Graph: serverapi.WorkflowGraphDraftFromDefinition(current.Definition),
	})
	if err != nil {
		t.Fatalf("SaveWorkflowGraph execution target policy: %v", err)
	}
}

func createDefaultWorkflowServiceTask(t *testing.T, ctx context.Context, service *Service, projectID string) serverapi.WorkflowTaskCreateResponse {
	t.Helper()
	return createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{ProjectID: projectID, Title: "Task", Body: "Body"})
}

func startWorkflowServiceTask(t *testing.T, ctx context.Context, service *Service, taskID string) serverapi.WorkflowTaskStartApplied {
	t.Helper()
	started, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           taskID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if err := started.Validate(); err != nil || started.Applied == nil {
		t.Fatalf("StartWorkflowTask response = %+v, validation error = %v", started, err)
	}
	return *started.Applied
}

func createWorkflowServiceValidWorkflow(t *testing.T, ctx context.Context, service *Service) runtimeids.WorkflowID {
	t.Helper()
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	startID := workflowServiceNodeIDByKind(t, def.Definition, "start")
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	agentID := workflowServiceGraphEntityID("node-agent-" + created.Workflow.ID.String())
	startGroupID := workflowServiceGraphEntityID("group-start-" + created.Workflow.ID.String())
	doneGroupID := workflowServiceGraphEntityID("group-done-" + created.Workflow.ID.String())
	startEdgeID := workflowServiceGraphEntityID("edge-start-" + created.Workflow.ID.String())
	doneEdgeID := workflowServiceGraphEntityID("edge-done-" + created.Workflow.ID.String())
	graph := serverapi.WorkflowGraphDraftFromDefinition(def.Definition)
	graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
		ID: agentID, Key: "agent", Kind: "agent", DisplayName: "Agent", SubagentRole: "coder",
	})
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: startGroupID, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: doneGroupID, SourceNodeID: agentID, TransitionID: "done", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		serverapi.WorkflowGraphDraftEdge{ID: startEdgeID, TransitionGroupID: startGroupID, Key: "start", TargetNodeID: agentID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session", PromptTemplate: "Do work."},
		serverapi.WorkflowGraphDraftEdge{ID: doneEdgeID, TransitionGroupID: doneGroupID, Key: "done", TargetNodeID: doneID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session"},
	)
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: created.Workflow.ID, ExpectedVersion: def.Definition.Workflow.Version, Graph: graph,
	})
	if err != nil || !saved.Saved {
		t.Fatalf("SaveWorkflowGraph valid fixture = %+v, err = %v", saved, err)
	}
	return created.Workflow.ID
}

func createWorkflowServiceWorkflowWithScriptNode(t *testing.T, ctx context.Context, service *Service, nodeID string, scriptPath string) runtimeids.WorkflowID {
	t.Helper()
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Script Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	graph := serverapi.WorkflowGraphDraftFromDefinition(def.Definition)
	graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
		ID: nodeID, Key: "script", Kind: "script", DisplayName: "Script", ScriptPath: stringPtr(scriptPath),
	})
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: created.Workflow.ID, ExpectedVersion: def.Definition.Workflow.Version, Graph: graph,
	})
	if err != nil || !saved.Saved {
		t.Fatalf("SaveWorkflowGraph script fixture = %+v, err = %v", saved, err)
	}
	return created.Workflow.ID
}

func createWorkflowServiceChainedWorkflow(t *testing.T, ctx context.Context, service *Service) runtimeids.WorkflowID {
	t.Helper()
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Chained Workflow"})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: created.Workflow.ID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	startID := workflowServiceNodeIDByKind(t, def.Definition, "start")
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	planID := workflowServiceGraphEntityID("node-plan-" + created.Workflow.ID.String())
	implementID := workflowServiceGraphEntityID("node-implement-" + created.Workflow.ID.String())
	startGroup := workflowServiceGraphEntityID("group-start-" + created.Workflow.ID.String())
	nextGroup := workflowServiceGraphEntityID("group-next-" + created.Workflow.ID.String())
	doneGroup := workflowServiceGraphEntityID("group-done-" + created.Workflow.ID.String())
	graph := serverapi.WorkflowGraphDraftFromDefinition(def.Definition)
	graph.Nodes = append(graph.Nodes,
		serverapi.WorkflowGraphDraftNode{ID: planID, Key: "plan", Kind: "agent", DisplayName: "Plan", SubagentRole: "coder"},
		serverapi.WorkflowGraphDraftNode{ID: implementID, Key: "implement", Kind: "agent", DisplayName: "Implement", SubagentRole: "coder"},
	)
	graph.TransitionGroups = append(graph.TransitionGroups,
		serverapi.WorkflowGraphDraftTransitionGroup{ID: startGroup, SourceNodeID: startID, TransitionID: "start", DisplayName: "Start"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: nextGroup, SourceNodeID: planID, TransitionID: "next", DisplayName: "Next"},
		serverapi.WorkflowGraphDraftTransitionGroup{ID: doneGroup, SourceNodeID: implementID, TransitionID: "done", DisplayName: "Done"},
	)
	graph.Edges = append(graph.Edges,
		serverapi.WorkflowGraphDraftEdge{ID: workflowServiceGraphEntityID("edge-start-" + created.Workflow.ID.String()), TransitionGroupID: startGroup, Key: "start", TargetNodeID: planID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session", PromptTemplate: "Plan work."},
		serverapi.WorkflowGraphDraftEdge{ID: workflowServiceGraphEntityID("edge-next-" + created.Workflow.ID.String()), TransitionGroupID: nextGroup, Key: "next", TargetNodeID: implementID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session", PromptTemplate: "Implement {{.Params.prior_summary}}.", Parameters: []serverapi.WorkflowParameter{{Key: "prior_summary", Description: "Prior summary.", Purpose: "ordinary"}}},
		serverapi.WorkflowGraphDraftEdge{ID: workflowServiceGraphEntityID("edge-done-" + created.Workflow.ID.String()), TransitionGroupID: doneGroup, Key: "done", TargetNodeID: doneID, AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session"},
	)
	saved, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID: created.Workflow.ID, ExpectedVersion: def.Definition.Workflow.Version, Graph: graph,
	})
	if err != nil || !saved.Saved {
		t.Fatalf("SaveWorkflowGraph chained fixture = %+v, err = %v", saved, err)
	}
	return created.Workflow.ID
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

func workflowServiceGraphSaveBlockerEntities(blockers []serverapi.WorkflowGraphSaveBlocker, code string) []serverapi.WorkflowGraphEntityReference {
	for _, blocker := range blockers {
		if blocker.Code == code {
			return blocker.AffectedEntities
		}
	}
	return nil
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

func requireWorkflowServiceEdgeApproval(t *testing.T, ctx context.Context, service *Service, workflowID runtimeids.WorkflowID, edgeKey string) {
	t.Helper()
	current, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow before Approval update: %v", err)
	}
	graph := serverapi.WorkflowGraphDraftFromDefinition(current.Definition)
	found := false
	for index := range graph.Edges {
		if graph.Edges[index].Key != edgeKey {
			continue
		}
		graph.Edges[index].RequiresApproval = true
		found = true
	}
	if !found {
		t.Fatalf("workflow edge key %q not found", edgeKey)
	}
	if _, err := service.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      workflowID,
		ExpectedVersion: current.Definition.Workflow.Version,
		Graph:           graph,
	}); err != nil {
		t.Fatalf("SaveWorkflowGraph Approval update: %v", err)
	}
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
