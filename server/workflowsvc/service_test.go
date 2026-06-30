package workflowsvc

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"core/prompts"
	"core/server/metadata"
	"core/server/requestmemo"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowattention"
	"core/server/workflowstore"
	"core/server/workflowview"
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

func TestServiceListWorkflowTasksValidatesAndDelegates(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)

	if _, err := service.ListWorkflowTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: " "}); !isWorkflowServiceRequestFieldError(err, "project_id") {
		t.Fatalf("blank project error = %#v, want project_id validation", err)
	}
	resp, err := service.ListWorkflowTasks(ctx, serverapi.WorkflowTaskListRequest{ProjectID: binding.ProjectID})
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
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{ProjectID: binding.ProjectID, Title: "Question", Body: "Body"})
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	sessionID := "session-task-question"
	if _, err := metadataStore.DB().ExecContext(ctx, `INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, name, first_prompt_preview, input_draft, parent_session_id, created_at_unix_ms, updated_at_unix_ms, last_sequence, model_request_count, in_flight_step, launch_visible, cwd_relpath, continuation_json, locked_json, usage_state_json, metadata_json) VALUES (?, ?, ?, ?, '', '', '', '', 1, 1, 0, 0, 0, 1, '.', '{}', '{}', '{}', '{}')`, sessionID, binding.ProjectID, binding.WorkspaceID, "sessions/"+sessionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if err := service.store.AttachRunSession(ctx, workflow.RunID(started.RunID), claimed.Generation, sessionID); err != nil {
		t.Fatalf("AttachRunSession: %v", err)
	}
	if err := service.store.SetRunWaitingAsk(ctx, workflow.RunID(started.RunID), claimed.Generation, "ask-task-question"); err != nil {
		t.Fatalf("SetRunWaitingAsk: %v", err)
	}
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
	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{TaskID: task.Task.ID}); err == nil {
		t.Fatalf("expected current graph validation error, got %v", err)
	} else {
		var validationErr workflowstore.WorkflowValidationError
		if !errors.As(err, &validationErr) || !validationErr.HasCode(workflow.CodeTerminalHasOutgoingEdge) {
			t.Fatalf("expected current graph validation error, got %v", err)
		}
	}
}

func TestServiceTaskStartEnsuresTaskWorktreeBeforeRun(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	ensurer := &recordingTaskWorktreeEnsurer{hook: func(taskID string) {
		runs, err := service.store.ListRuns(ctx, workflow.TaskID(taskID))
		if err != nil {
			t.Fatalf("ListRuns during ensure: %v", err)
		}
		if len(runs) != 0 {
			t.Fatalf("task worktree ensure happened after run creation: %+v", runs)
		}
	}}
	service.taskWorktrees = ensurer
	startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	if ensurer.taskID != task.Task.ID {
		t.Fatalf("ensured task id = %q, want %q", ensurer.taskID, task.Task.ID)
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
	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{TaskID: task.Task.ID}); !errors.Is(err, workflowstore.ErrWorkflowValidationFailed) {
		t.Fatalf("expected invalid default workflow start error, got %v", err)
	}
}

func TestServiceStartTaskAutomationValidatesEnsuresWorktreeAndRecordsRunnableRun(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
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
	if len(runs) != 1 || runs[0].ID != workflow.RunID(started.RunID) || runs[0].AutomationRequestedAt == 0 {
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

	if _, err := service.StartTaskAutomation(ctx, task.Task.ID); err != nil {
		t.Fatalf("StartTaskAutomation: %v", err)
	}
	if notifier.count != 1 {
		t.Fatalf("scheduler notifications = %d, want 1", notifier.count)
	}
}

func TestServiceMoveTaskAutoApprovesMissingEdgeOverrideAndStartsAgent(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	implementID := workflowServiceNodeIDByKey(t, def.Definition, "implement")
	notifier := &recordingSchedulerNotifier{}
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.schedulerWake = notifier
	service.attentionFinalizer = finalizer

	moved, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{TaskID: task.Task.ID, TargetNodeID: implementID, AllowMissingEdge: true, AutoApprove: true, OutputValues: map[string]string{"prior_summary": "replacement"}})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if moved.State != "approved" || len(moved.PlacementIDs) != 1 || len(moved.RunIDs) != 1 {
		t.Fatalf("auto-approved override = %+v, want approved placement and run", moved)
	}
	if notifier.count != 1 {
		t.Fatalf("scheduler notifications = %d, want 1", notifier.count)
	}
	if len(finalizer.results) != 1 || finalizer.results[0].TransitionID != workflow.TransitionID(moved.TransitionID) || finalizer.results[0].State != "approved" {
		t.Fatalf("attention finalizer results = %+v", finalizer.results)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || string(runs[0].NodeID) != implementID || runs[0].AutomationRequestedAt == 0 {
		t.Fatalf("runs after auto-approved override = %+v, want requested implement automation", runs)
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
	if len(runs) != 1 || runs[0].CompletedAt == 0 {
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
	if len(runs) != 1 || runs[0].CompletedAt != 0 {
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
	implementID := workflowServiceNodeIDByKey(t, def.Definition, "implement")
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.attentionFinalizer = finalizer
	service.approve = func(context.Context, workflow.TransitionID) (workflowstore.CompleteRunResult, error) {
		return workflowstore.CompleteRunResult{}, errors.New("approval failed")
	}

	moved, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{TaskID: task.Task.ID, TargetNodeID: implementID, AllowMissingEdge: true, AutoApprove: true, OutputValues: map[string]string{"prior_summary": "replacement"}})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
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

func TestServiceMoveTaskAutoApprovedReplacementResolvesOldPendingApproval(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	implementID := workflowServiceNodeIDByKey(t, def.Definition, "implement")
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.attentionFinalizer = finalizer
	oldMove, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{TaskID: task.Task.ID, TargetNodeID: implementID, AllowMissingEdge: true, OutputValues: map[string]string{"prior_summary": "old"}})
	if err != nil {
		t.Fatalf("initial MoveWorkflowTask: %v", err)
	}
	if oldMove.State != "pending_approval" {
		t.Fatalf("initial move = %+v, want pending approval", oldMove)
	}

	replacement, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{TaskID: task.Task.ID, TargetNodeID: implementID, AllowMissingEdge: true, AutoApprove: true, OutputValues: map[string]string{"prior_summary": "replacement"}})
	if err != nil {
		t.Fatalf("replacement MoveWorkflowTask: %v", err)
	}
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

	moved, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{TaskID: task.Task.ID, TargetNodeID: agentID, AllowMissingEdge: true, AutoApprove: true})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if moved.State != "pending_approval" || len(moved.PlacementIDs) != 0 || len(moved.RunIDs) != 0 || moved.ApprovalError != "" {
		t.Fatalf("approval-gated move = %+v, want pending approval without automation", moved)
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

func TestServiceCancelTaskCancelsActiveRuntime(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	if _, err := service.StartTaskAutomation(ctx, task.Task.ID); err != nil {
		t.Fatalf("StartTaskAutomation: %v", err)
	}
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
	implementID := workflowServiceNodeIDByKey(t, def.Definition, "implement")
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.attentionFinalizer = finalizer
	moved, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{TaskID: task.Task.ID, TargetNodeID: implementID, AllowMissingEdge: true})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
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
	if _, err := service.StartTaskAutomation(ctx, task.Task.ID); err != nil {
		t.Fatalf("StartTaskAutomation: %v", err)
	}
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
	implementID := workflowServiceNodeIDByKey(t, def.Definition, "implement")
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.attentionFinalizer = finalizer
	moved, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{TaskID: task.Task.ID, TargetNodeID: implementID, AllowMissingEdge: true})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
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
	implementID := workflowServiceNodeIDByKey(t, def.Definition, "implement")
	finalizer := &recordingWorkflowAttentionFinalizer{}
	service.attentionFinalizer = finalizer
	moved, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{TaskID: task.Task.ID, TargetNodeID: implementID, AllowMissingEdge: true})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
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

func TestServiceDeleteTaskPreflightBlockedDoesNotCancelRuns(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	if _, err := service.StartTaskAutomation(ctx, task.Task.ID); err != nil {
		t.Fatalf("StartTaskAutomation: %v", err)
	}
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
	service.schedulerWake = notifier

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
}

type recordingSchedulerNotifier struct {
	count int
}

func (n *recordingSchedulerNotifier) Notify() {
	n.count++
}

type recordingWorkflowAttentionFinalizer struct {
	results []workflowattention.TransitionResult
}

func (f *recordingWorkflowAttentionFinalizer) FinalizeTransition(_ context.Context, result workflowattention.TransitionResult) {
	f.results = append(f.results, result)
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
	taskID string
	hook   func(string)
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

func (e *recordingTaskWorktreeEnsurer) EnsureTaskWorktree(ctx context.Context, taskID string) error {
	e.taskID = taskID
	if e.hook != nil {
		e.hook(taskID)
	}
	return nil
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
	link := linkWorkflowServiceProject(t, ctx, service, serverapi.WorkflowLinkProjectRequest{ProjectID: binding.ProjectID, WorkflowID: workflowID, Default: true})
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
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Workflow"})
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
	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{TaskID: task.Task.ID}); err == nil {
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
	linkWorkflowServiceProject(t, ctx, service, serverapi.WorkflowLinkProjectRequest{ProjectID: projectID, WorkflowID: workflowID, Default: true})
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

func startWorkflowServiceTask(t *testing.T, ctx context.Context, service *Service, taskID string) serverapi.WorkflowTaskStartResponse {
	t.Helper()
	started, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	return started
}

func claimAndAttachWorkflowServiceRun(t *testing.T, ctx context.Context, service *Service, metadataStore *metadata.Store, binding metadata.Binding, runID string, sessionID string) workflowstore.RunnableRunRecord {
	t.Helper()
	if _, err := metadataStore.DB().ExecContext(ctx, `INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, name, first_prompt_preview, input_draft, parent_session_id, created_at_unix_ms, updated_at_unix_ms, last_sequence, model_request_count, in_flight_step, launch_visible, cwd_relpath, continuation_json, locked_json, usage_state_json, metadata_json) VALUES (?, ?, ?, ?, '', '', '', '', 1, 1, 0, 0, 0, 1, '.', '{}', '{}', '{}', '{}')`, sessionID, binding.ProjectID, binding.WorkspaceID, "sessions/"+sessionID); err != nil {
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

func createWorkflowServiceValidWorkflow(t *testing.T, ctx context.Context, service *Service) string {
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

func createWorkflowServiceChainedWorkflow(t *testing.T, ctx context.Context, service *Service) string {
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
		graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{ID: node.ID, Key: node.Key, Kind: node.Kind, DisplayName: node.DisplayName, GroupID: node.GroupID, GroupKey: node.GroupKey, SubagentRole: node.SubagentRole, PromptTemplate: node.PromptTemplate, CompletionMode: node.CompletionMode, InputFields: node.InputFields, JoinInputProviders: node.JoinInputProviders})
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
