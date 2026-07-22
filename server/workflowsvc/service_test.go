package workflowsvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"core/internal/testharness/testsetup"
	"core/prompts"
	"core/server/attentionnotify"
	"core/server/llm"
	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/requestmemo"
	"core/server/session"
	"core/server/sessionruntime"
	askquestion "core/server/tools"
	"core/server/workflow"
	"core/server/workflowattention"
	"core/server/workflowexecution"
	"core/server/workflowrunner"
	"core/server/workflowscript"
	"core/server/workflowstore"
	"core/server/workflowview"
	"core/server/worktree"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/toolspec"

	"github.com/google/uuid"
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

func installWorkflowServiceScheduler(t *testing.T, service *Service, notifier *recordingSchedulerNotifier) {
	t.Helper()
	service.schedulerWake = notifier
	automaticStarts, err := workflowexecution.NewAutomaticStartRegistration(notifier, workflowexecution.NewFatalSignal())
	if err != nil {
		t.Fatalf("NewAutomaticStartRegistration: %v", err)
	}
	service.automaticStarts = automaticStarts
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

func TestGetWorkflowTaskCountsAttentionWithoutReadingTranscripts(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	transcripts := &recordingWorkflowTaskTranscriptProvider{}
	prompts := &recordingWorkflowTaskPromptSource{}
	service.readModels = newWorkflowServiceReadModels(t, metadataStore, service.store, service.roleResolver, transcripts, prompts)
	task, runID, _ := createWorkflowServiceWaitingAsk(t, ctx, service, metadataStore, binding, "Attention count", "session-attention-count", "ask-attention-count")
	if _, err := metadataStore.DB().ExecContext(ctx, `
INSERT INTO task_transitions (
    id,
    task_id,
    source_run_id,
    source_placement_id,
    source_node_key,
    source_node_display_name,
    transition_id,
    transition_display_name,
    workflow_revision_seen,
    actor,
    state,
    commentary,
    output_values_json,
    created_at_unix_ms,
    applied_at_unix_ms
) VALUES (?, ?, ?, NULL, 'agent', 'Agent', 'approval', 'Approval', 1, 'agent', 'pending_approval', '', '{}', 2, NULL)`,
		"transition-attention-count",
		task.Task.ID,
		runID,
	); err != nil {
		t.Fatalf("insert pending approval attention: %v", err)
	}

	response, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: task.Task.ID})
	if err != nil {
		t.Fatalf("GetWorkflowTask: %v", err)
	}
	if response.Task.AttentionCount != 2 {
		t.Fatalf("attention count = %d, want 2", response.Task.AttentionCount)
	}
	if transcripts.calls != 0 {
		t.Fatalf("transcript reads = %d, want 0", transcripts.calls)
	}
	if prompts.calls != 0 {
		t.Fatalf("pending prompt reads = %d, want 0", prompts.calls)
	}
}

type recordingWorkflowTaskTranscriptProvider struct {
	calls int
}

func (p *recordingWorkflowTaskTranscriptProvider) SessionNewestActiveSegmentQuestions(context.Context, string) ([]workflowview.PendingQuestionTranscriptEntry, error) {
	p.calls++
	return nil, errors.New("task get must not read transcripts")
}

type recordingWorkflowTaskPromptSource struct {
	calls int
}

func (p *recordingWorkflowTaskPromptSource) ListPendingPrompts(string) []workflowview.PendingPromptSnapshot {
	p.calls++
	return nil
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
	if got.Code != workflowscript.CodeMissingPath || got.WorkflowID == nil || *got.WorkflowID != workflowID || got.NodeID != "node-script" || !got.BlocksContext {
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
	activity, err := service.ListWorkflowTaskActivity(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("ListWorkflowTaskActivity: %v", err)
	}
	if len(activity.Items) == 0 || activity.Items[0].Type != "comment" || activity.Items[0].Comment == nil || activity.Items[0].Comment.Body != "updated" {
		t.Fatalf("activity after replace = %+v", activity.Items)
	}
	if err := service.DeleteWorkflowTaskComment(ctx, serverapi.WorkflowTaskCommentDeleteRequest{CommentID: added.Comment.ID}); err != nil {
		t.Fatalf("DeleteWorkflowTaskComment: %v", err)
	}
	activity, err = service.ListWorkflowTaskActivity(ctx, serverapi.WorkflowTaskActivityListRequest{TaskID: taskID})
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
	task, runID, sessionID := createWorkflowServiceWaitingAsk(t, ctx, service, metadataStore, binding, "Question", "session-task-question", "ask-task-question")
	responder := &recordingPromptResponder{}
	service.prompts = responder
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: task.Task.ProjectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	service.readModels.TaskDetail = unavailableWorkflowTaskDetailReadModel{}

	req := serverapi.WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-question", TaskID: task.Task.ID, AskID: "ask-task-question", FreeformAnswer: "ship it"}
	if err := service.AnswerWorkflowTaskQuestion(ctx, req); err != nil {
		t.Fatalf("AnswerWorkflowTaskQuestion: %v", err)
	}
	if responder.sessionID != sessionID || responder.response.RequestID != "ask-task-question" || responder.response.FreeformAnswer != "ship it" {
		t.Fatalf("prompt response = session:%q response:%+v", responder.sessionID, responder.response)
	}
	if responder.response.SelectedOptionNumber != nil {
		t.Fatalf("selected option = %v, want nil", *responder.response.SelectedOptionNumber)
	}
	event := nextWorkflowProjectEvent(t, sub)
	if event.ProjectID == nil ||
		*event.ProjectID != task.Task.ProjectID ||
		event.WorkflowID == nil ||
		*event.WorkflowID != task.Task.WorkflowID ||
		event.Resource != serverapi.WorkflowProjectEventResourceTask ||
		event.Action != serverapi.WorkflowProjectEventActionQuestionAnswered ||
		event.PrimaryEntityID != task.Task.ID ||
		!sameStringSet(event.RelatedIDs, []string{runID, responder.response.RequestID}) {
		t.Fatalf("question answered event = %+v", event)
	}
	if err := service.AnswerWorkflowTaskQuestion(ctx, req); err != nil {
		t.Fatalf("AnswerWorkflowTaskQuestion replay: %v", err)
	}
	selectedOption := 1
	req.SelectedOptionNumber = &selectedOption
	if err := service.AnswerWorkflowTaskQuestion(ctx, req); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("AnswerWorkflowTaskQuestion present selection replay error = %v, want payload mismatch", err)
	}
	req.SelectedOptionNumber = nil
	req.FreeformAnswer = "different"
	if err := service.AnswerWorkflowTaskQuestion(ctx, req); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("AnswerWorkflowTaskQuestion mismatch error = %v", err)
	}
	if err := service.AnswerWorkflowTaskQuestion(ctx, serverapi.WorkflowTaskQuestionAnswerRequest{ClientRequestID: "req-bad", TaskID: task.Task.ID, AskID: "missing", FreeformAnswer: "nope"}); !errors.Is(err, workflowstore.ErrTaskAskNotPending) {
		t.Fatalf("AnswerWorkflowTaskQuestion missing ask error = %v", err)
	}
}

func TestServiceAnswersTaskQuestionWhileWorkflowMutationIsBusy(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	task, _, _ := createWorkflowServiceWaitingAsk(t, ctx, service, metadataStore, binding, "Question", "session-task-question", "ask-task-question")
	responder := &recordingPromptResponder{}
	service.prompts = responder

	permitHeld := make(chan struct{})
	releasePermit := make(chan struct{})
	permitDone := make(chan error, 1)
	go func() {
		permitDone <- service.mutationPermit.Run(context.Background(), func(context.Context) error {
			close(permitHeld)
			<-releasePermit
			return nil
		})
	}()
	<-permitHeld

	answerDone := make(chan error, 1)
	go func() {
		answerDone <- service.AnswerWorkflowTaskQuestion(ctx, serverapi.WorkflowTaskQuestionAnswerRequest{
			ClientRequestID: "req-question-while-mutation-busy",
			TaskID:          task.Task.ID,
			AskID:           "ask-task-question",
			FreeformAnswer:  "ship it",
		})
	}()

	select {
	case err := <-answerDone:
		if err != nil {
			t.Fatalf("AnswerWorkflowTaskQuestion: %v", err)
		}
	case <-time.After(time.Second):
		close(releasePermit)
		<-permitDone
		t.Fatal("AnswerWorkflowTaskQuestion blocked behind an unrelated workflow mutation")
	}
	close(releasePermit)
	if err := <-permitDone; err != nil {
		t.Fatalf("mutation permit holder: %v", err)
	}
	if responder.response.FreeformAnswer != "ship it" {
		t.Fatalf("prompt response = %+v", responder.response)
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
	ctx, service, _, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	doneID := workflowServiceNodeIDByKind(t, def.Definition, "terminal")
	if _, err := service.AddWorkflowTransitionGroup(ctx, serverapi.WorkflowTransitionGroupAddRequest{WorkflowID: workflowID, GroupID: "group-invalid", SourceNodeID: doneID, TransitionID: "invalid", DisplayName: "Invalid"}); err != nil {
		t.Fatalf("AddWorkflowTransitionGroup invalid: %v", err)
	}
	if _, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: taskID}); err == nil {
		t.Fatalf("expected current graph validation error, got %v", err)
	} else {
		var validationErr workflowstore.WorkflowValidationError
		if !errors.As(err, &validationErr) || !validationErr.HasCode(workflow.CodeTerminalHasOutgoingEdge) {
			t.Fatalf("expected current graph validation error, got %v", err)
		}
	}
}

func TestServiceTaskStartRequiresSelectionWithoutApplyingAction(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	notifier := &recordingSchedulerNotifier{}
	installWorkflowServiceScheduler(t, service, notifier)

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           taskID,
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired ||
		response.Applied != nil ||
		response.SelectionRequired == nil ||
		response.SelectionRequired.Reason != serverapi.WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection {
		t.Fatalf("start response = %+v, want policy selection requirement", response)
	}
	if notifier.count != 0 {
		t.Fatalf("scheduler notifications = %d, want none", notifier.count)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want none", runs)
	}
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 0 {
		t.Fatalf("transitions = %+v, want none", transitions)
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
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied || response.Applied == nil {
		t.Fatalf("start response = %+v, want applied", response)
	}
	if response.Applied.RunID == "" {
		t.Fatalf("start response = %+v, want run", response)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil ||
		targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeNone ||
		targetContext.Task.ManagedWorktreeID != "" {
		t.Fatalf("locked target = %+v, managed worktree = %q, want none", targetContext.Task.ExecutionTarget, targetContext.Task.ManagedWorktreeID)
	}
}

func TestServiceTaskStartMaterializesConfiguredHeadBeforeLockingTarget(t *testing.T) {
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
	infrastructure := &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			ResolvedRef:  &resolvedRef,
			CommitOID:    &commitOID,
			Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
		},
		materialize: func(taskID workflow.TaskID) (workflowstore.ManagedExecutionRoot, error) {
			if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{
				ID:            worktreeID,
				WorkspaceID:   binding.WorkspaceID,
				CanonicalRoot: worktreeRoot,
				Managed:       true,
				CreatedBranch: true,
			}); err != nil {
				t.Fatalf("UpsertWorktreeRecord: %v", err)
			}
			updated, err := metadataStore.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
				ID:                string(taskID),
				ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
				UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
			})
			if err != nil {
				t.Fatalf("UpdateTaskManagedWorktree: %v", err)
			}
			if updated != 1 {
				t.Fatalf("UpdateTaskManagedWorktree updated %d rows, want 1", updated)
			}
			return workflowstore.ManagedExecutionRoot{WorktreeID: worktreeID, Root: worktreeRoot}, nil
		},
	}
	service.executionTargets = infrastructure
	setupID := serverapi.NewWorktreeSetupOperationID()

	response, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: setupID,
		TaskID:           task.Task.ID,
	})
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeApplied || response.Applied == nil {
		t.Fatalf("start response = %+v, want applied", response)
	}
	if infrastructure.resolveSelection.Mode != workflow.ExecutionTargetModeHead ||
		infrastructure.materializeTaskID != workflow.TaskID(task.Task.ID) ||
		infrastructure.setupOperationID != setupID {
		t.Fatalf("execution target infrastructure = %+v, want resolved and materialized HEAD for task", infrastructure)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget == nil ||
		targetContext.Task.ExecutionTarget.Mode != workflow.ExecutionTargetModeHead ||
		targetContext.Task.ExecutionTarget.CommitOID == nil ||
		*targetContext.Task.ExecutionTarget.CommitOID != commitOID ||
		targetContext.Task.ManagedWorktreeID != worktreeID {
		t.Fatalf("locked target = %+v, managed worktree = %q", targetContext.Task.ExecutionTarget, targetContext.Task.ManagedWorktreeID)
	}
}

func TestServiceTaskStartLeavesActionUnlockedWhenMaterializationFails(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	requestedRef := "HEAD"
	commitOID := strings.Repeat("b", 40)
	setupErr := errors.New("setup failed")
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			CommitOID:    &commitOID,
			Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
		},
		materializeErr: setupErr,
	}
	notifier := &recordingSchedulerNotifier{}
	installWorkflowServiceScheduler(t, service, notifier)

	_, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           taskID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeHead,
		},
	})
	if !errors.Is(err, setupErr) {
		t.Fatalf("StartWorkflowTask error = %v, want setup failure", err)
	}
	if notifier.count != 0 {
		t.Fatalf("scheduler notifications = %d, want none", notifier.count)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil {
		t.Fatalf("execution target = %+v, want unlocked", targetContext.Task.ExecutionTarget)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs = %+v, want none", runs)
	}
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 0 {
		t.Fatalf("transitions = %+v, want none", transitions)
	}
}

func TestServiceTaskStartReturnsSelectionWhenConfiguredTargetIsUnavailable(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeHead,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
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
	if err != nil {
		t.Fatalf("StartWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired ||
		response.Applied != nil ||
		response.SelectionRequired == nil ||
		response.SelectionRequired.Reason != serverapi.WorkflowExecutionTargetSelectionReasonConfiguredTargetUnavailable ||
		response.SelectionRequired.ConfiguredTarget == nil ||
		response.SelectionRequired.ConfiguredTarget.Mode != serverapi.WorkflowExecutionTargetModeHead ||
		response.SelectionRequired.UnavailableCause != serverapi.WorkflowExecutionTargetUnavailableCauseInvalidRevision {
		t.Fatalf("start response = %+v, want unavailable configured HEAD selection requirement", response)
	}
	targetContext, err := service.store.GetTaskExecutionTargetContext(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("GetTaskExecutionTargetContext: %v", err)
	}
	if targetContext.Task.ExecutionTarget != nil {
		t.Fatalf("execution target = %+v, want unlocked", targetContext.Task.ExecutionTarget)
	}
}

func TestServiceTaskStartReturnsTypedErrorForInvalidExplicitCustomRef(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	customRef := "missing-ref"
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolveErr: &worktree.GitRevisionResolutionError{
			Kind:         worktree.GitRevisionResolutionErrorInvalidRevision,
			RequestedRef: customRef,
		},
	}

	_, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           taskID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode:      serverapi.WorkflowExecutionTargetModeCustomRef,
			CustomRef: &customRef,
		},
	})
	var resolutionErr *serverapi.WorkflowExecutionTargetResolutionError
	if !errors.As(err, &resolutionErr) ||
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
		t.Fatalf("updated assignment = %+v, want alphabetical IDs", updated.Assignment)
	}
	event := nextWorkflowProjectEvent(t, sub)
	if !stringPointerEquals(event.ProjectID, binding.ProjectID) ||
		!stringPointerEquals(event.WorkflowID, workflowID) ||
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

func TestServiceStartTaskAutomationValidatesAndRecordsRunnableRun(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	setWorkflowServiceExecutionTargetPolicy(t, ctx, service, workflowID, serverapi.WorkflowExecutionTargetConfiguration{
		Mode: serverapi.WorkflowExecutionTargetModeNone,
	})
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	notifier := &recordingSchedulerNotifier{}
	installWorkflowServiceScheduler(t, service, notifier)

	started, err := service.StartTaskAutomation(ctx, task.Task.ID)
	if err != nil {
		t.Fatalf("StartTaskAutomation: %v", err)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns after automation: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != workflow.RunID(started.RunID) || runs[0].AutomationRequestedAt != nil {
		t.Fatalf("runs after automation = %+v", runs)
	}
	if len(notifier.automatic) != 1 || len(notifier.automatic[0]) != 1 || notifier.automatic[0][0] != workflow.RunID(started.RunID) {
		t.Fatalf("automatic intents = %+v, want %s", notifier.automatic, started.RunID)
	}
	if _, err := service.StartTaskAutomation(ctx, task.Task.ID); err == nil {
		t.Fatalf("expected second start to fail")
	}
	if notifier.count != 1 {
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

func TestServiceRejectsUnsafeTaskStartBeforeExecutionTargetOrScheduler(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	resolver, ok := service.roleResolver.(testsetup.RoleResolver)
	if !ok {
		t.Fatalf("role resolver type = %T, want test resolver", service.roleResolver)
	}
	resolver["coder"][toolspec.ToolAskQuestion] = false
	infrastructure := &recordingExecutionTargetInfrastructure{}
	notifier := &recordingSchedulerNotifier{}
	service.executionTargets = infrastructure
	installWorkflowServiceScheduler(t, service, notifier)

	_, err := service.StartTaskAutomation(ctx, taskID)
	var validationErr workflowstore.WorkflowValidationError
	if !errors.As(err, &validationErr) || !validationErr.HasCode(workflow.CodeAgentRoleRequiredToolDisabled) {
		t.Fatalf("StartTaskAutomation error = %v, want required-tool validation", err)
	}
	if infrastructure.resolveSelection.Mode != "" || infrastructure.materializeTaskID != "" || infrastructure.restoreTaskID != "" {
		t.Fatalf("execution-target infrastructure called: %+v", infrastructure)
	}
	if notifier.count != 0 {
		t.Fatalf("scheduler notifications = %d, want none", notifier.count)
	}
}

func TestServiceCompleteWorkflowTaskFromAgentSessionRequestsAutomaticSuccessorStart(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
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
	installWorkflowServiceScheduler(t, service, notifier)
	service.attentionFinalizer = finalizer

	completed, err := service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
		AgentSessionID: sessionID,
		Commentary:     "finished",
		OutputValues:   map[string]string{"prior_summary": "plan"},
	})
	if err != nil {
		t.Fatalf("CompleteWorkflowTask: %v", err)
	}
	if completed.TaskID != task.Task.ID || completed.RunID != started.RunID || completed.State != "applied" {
		t.Fatalf("complete response = %+v", completed)
	}
	if completed.Handoff.SourceNodeDisplayName != "Plan" || completed.Handoff.DestinationDisplayName != "Implement" {
		t.Fatalf("completion handoff = %+v, want Plan -> Implement", completed.Handoff)
	}
	if len(completed.RunIDs) != 1 ||
		len(notifier.automatic) != 1 ||
		len(notifier.automatic[0]) != 1 ||
		string(notifier.automatic[0][0]) != completed.RunIDs[0] {
		t.Fatalf("agent completion automatic starts = %+v, response runs = %+v", notifier.automatic, completed.RunIDs)
	}
	if len(notifier.explicit) != 0 {
		t.Fatalf("agent completion explicit starts = %+v, want none", notifier.explicit)
	}
	if len(finalizer.results) != 1 || finalizer.results[0].TransitionID != workflow.TransitionID(completed.TransitionID) || finalizer.results[0].State != "applied" {
		t.Fatalf("attention finalizer results = %+v", finalizer.results)
	}
	event := nextWorkflowProjectEvent(t, sub)
	if !stringPointerEquals(event.ProjectID, binding.ProjectID) || !stringPointerEquals(event.WorkflowID, workflowID) || event.Resource != "task" || event.Action != "completed" {
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
	if len(runs) != 2 || runs[0].CompletedAt == nil || runs[1].StartedAt != nil {
		t.Fatalf("runs after completion = %+v, want completed source and queued successor", runs)
	}
}

func TestServiceCompleteWorkflowTaskSignalsFatalFailureWhenCommittedSuccessorsCannotBeRegistered(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceChainedWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
	sessionID := "session-agent-registration-failure"
	claimAndAttachWorkflowServiceRun(t, ctx, service, metadataStore, binding, started.RunID, sessionID)
	registrationErr := errors.New("automatic successor registration failed")
	fatalSignal := workflowexecution.NewFatalSignal()
	automaticStarts, err := workflowexecution.NewAutomaticStartRegistration(&recordingSchedulerNotifier{registrationErr: registrationErr}, fatalSignal)
	if err != nil {
		t.Fatalf("NewAutomaticStartRegistration: %v", err)
	}
	service.automaticStarts = automaticStarts

	_, err = service.CompleteWorkflowTask(ctx, serverapi.WorkflowTaskCompleteRequest{
		ActorKind:      serverapi.WorkflowTaskCompleteActorAgent,
		AgentSessionID: sessionID,
		OutputValues:   map[string]string{"prior_summary": "plan"},
	})
	var fatalErr workflowexecution.WorkflowExecutionFatalError
	if !errors.As(err, &fatalErr) {
		t.Fatalf("CompleteWorkflowTask error = %T %v, want workflow execution fatal error", err, err)
	}
	if signaled := <-fatalSignal.Failures(); !errors.As(signaled, &fatalErr) {
		t.Fatalf("signaled failure = %T %v, want workflow execution fatal error", signaled, signaled)
	}
	runs, err := service.store.ListRuns(ctx, workflow.TaskID(task.Task.ID))
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 || runs[0].CompletedAt == nil || runs[1].StartedAt != nil || runs[1].InterruptedAt != nil {
		t.Fatalf("runs after registration panic = %+v, want committed source and unstarted successor for restart reconciliation", runs)
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
	installWorkflowServiceScheduler(t, service, notifier)
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
	installWorkflowServiceScheduler(t, service, notifier)
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
		t.Fatalf("force completion scheduler starts = %d, want none for a terminal target", notifier.count)
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
	installWorkflowServiceScheduler(t, service, notifier)
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
	if notifier.count != 0 {
		t.Fatalf("force completion scheduler starts = %d, want none for a terminal target", notifier.count)
	}
}

func TestServiceMoveTaskAutoApprovedReplacementResolvesOldPendingApproval(t *testing.T) {
	fixture := newWorkflowServicePendingApprovalFixture(t)

	replacementResponse, err := fixture.service.MoveWorkflowTask(fixture.ctx, serverapi.WorkflowTaskMoveRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           fixture.taskID,
		TargetNodeID:     fixture.planID,
		AllowMissingEdge: true,
		AutoApprove:      true,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil {
		t.Fatalf("replacement MoveWorkflowTask: %v", err)
	}
	replacement := workflowServiceMoveApplied(t, replacementResponse)
	if replacement.State != "applied" {
		t.Fatalf("replacement move = %+v, want applied", replacement)
	}
	if len(fixture.finalizer.results) != 2 {
		t.Fatalf("attention finalizer results = %+v, want initial pending and approved replacement", fixture.finalizer.results)
	}
	resolved := fixture.finalizer.results[1].ResolvedApprovalProjections
	if len(resolved) != 1 || resolved[0].TransitionID != workflow.TransitionID(fixture.pending.TransitionID) {
		t.Fatalf("replacement resolved approvals = %+v, want old transition %s", resolved, fixture.pending.TransitionID)
	}
}

func TestServiceApproveCompletionResolvesCapturedApprovalWithFreshFinalizer(t *testing.T) {
	ctx, service, projectID, workflowID, taskID, transitionID := newWorkflowServicePendingCompletionApproval(t)
	publisher := &recordingWorkflowAttentionPublisher{}
	service.attentionFinalizer = workflowattention.NewFinalizer(failingWorkflowPendingProjectionProvider{t: t}, publisher)

	_, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskTransitionID: transitionID,
	})
	if err != nil {
		t.Fatalf("ApproveWorkflowTask: %v", err)
	}
	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved notifications = %+v, want one", publisher.resolved)
	}
	resolved := publisher.resolved[0]
	if resolved.scope.ProjectID != projectID || resolved.scope.WorkflowID != workflowID || resolved.scope.TaskID != taskID {
		t.Fatalf("resolved scope = %+v", resolved.scope)
	}
}

func TestServiceManualMoveResolvesCapturedApprovalWithFreshFinalizer(t *testing.T) {
	fixture := newWorkflowServicePendingApprovalFixture(t)
	publisher := &recordingWorkflowAttentionPublisher{}
	fixture.service.attentionFinalizer = workflowattention.NewFinalizer(failingWorkflowPendingProjectionProvider{t: t}, publisher)

	_, err := fixture.service.MoveWorkflowTask(fixture.ctx, serverapi.WorkflowTaskMoveRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           fixture.taskID,
		TargetNodeID:     fixture.planID,
		AllowMissingEdge: true,
		AutoApprove:      true,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeNone,
		},
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved notifications = %+v, want one", publisher.resolved)
	}
	resolved := publisher.resolved[0]
	if resolved.scope.ProjectID == "" || resolved.scope.WorkflowID != fixture.workflowID || resolved.scope.TaskID != fixture.taskID {
		t.Fatalf("resolved scope = %+v", resolved.scope)
	}
}

func TestServiceManualMoveResolvesCapturedInterruptionWithFreshFinalizer(t *testing.T) {
	ctx, service, projectID, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	started := startWorkflowServiceTask(t, ctx, service, taskID)
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := service.store.InterruptRunGeneration(ctx, workflow.RunID(started.RunID), claimed.Generation, "manual_move", `{"error":"move detail"}`); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	backlogID := workflowServiceNodeIDByKey(t, def.Definition, "backlog")
	publisher := &recordingWorkflowAttentionPublisher{}
	service.attentionFinalizer = workflowattention.NewFinalizer(failingWorkflowPendingProjectionProvider{t: t}, publisher)

	if _, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           taskID,
		TargetNodeID:     backlogID,
		AllowMissingEdge: true,
	}); err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved notifications = %+v, want one", publisher.resolved)
	}
	resolved := publisher.resolved[0]
	if resolved.id.UUID != started.RunID ||
		resolved.scope.ProjectID != projectID ||
		resolved.scope.WorkflowID != workflowID ||
		resolved.scope.TaskID != taskID {
		t.Fatalf("resolved interruption = %+v", resolved)
	}
}

type workflowServiceMoveRuntimeClient struct{}

func (workflowServiceMoveRuntimeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func TestServiceMovePreflightPreservesRealWaitingQuestion(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	cfg, err := config.Load(binding.CanonicalRoot, config.LoadOptions{})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	projectSessionsDir := filepath.Join(cfg.PersistenceRoot, "projects", binding.ProjectID, "sessions")
	sessionStore, err := session.Create(
		projectSessionsDir,
		"move-preflight",
		cfg.WorkspaceRoot,
		sessioncontract.SessionCategoryMain,
		metadataStore.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(sessionStore.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("NewOpenSessionDescriptor: %v", err)
	}
	cfg.Settings.Model = "gpt-5"
	cfg.Settings.ModelContextWindow = 200000
	cfg.Settings.Reviewer.Frequency = "off"
	runtimePlan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: cfg.Settings,
		Workdir:  cfg.WorkspaceRoot,
		Client:   workflowServiceMoveRuntimeClient{},
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: cfg.PersistenceRoot,
		StoreOptions:    metadataStore.AuthoritativeSessionStoreOptions(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	automaticStarts, err := workflowexecution.NewAutomaticStartRegistration(workflowexecution.NewAutomaticIntents(), workflowexecution.NewFatalSignal())
	if err != nil {
		t.Fatalf("NewAutomaticStartRegistration: %v", err)
	}
	starter, err := workflowrunner.NewStarter(
		cfg,
		metadataStore,
		service.store,
		nil,
		nil,
		workflowrunner.StarterOptions{
			RuntimeAuthority: authority,
			AutomaticStarts:  automaticStarts,
			MutationPermit:   service.mutationPermit,
		},
	)
	if err != nil {
		t.Fatalf("NewStarter: %v", err)
	}
	service.moveAuthority = starter
	workflowRef := sessionruntime.WorkflowExecutionRef{
		TaskID:     workflow.TaskID(task.Task.ID),
		RunID:      workflow.RunID("run-move-preflight-waiting"),
		Generation: 1,
	}
	handle, err := authority.StartAgentExecution(ctx, sessionruntime.AgentExecutionRequest{
		Descriptor: descriptor,
		Runtime:    &runtimePlan,
		Workflow:   &workflowRef,
		Resource:   sessionruntime.OpenAgentResource{},
		Runner: func(ctx context.Context, scope sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
			_, waitErr := authority.AwaitPromptResponse(ctx, scope.ID(), askquestion.AskQuestionRequest{
				ID:       uuid.NewString(),
				StepID:   uuid.NewString(),
				Question: "Proceed?",
			})
			return waitErr
		},
	})
	if err != nil {
		t.Fatalf("StartAgentExecution: %v", err)
	}
	t.Cleanup(func() {
		_ = handle.Stop(context.Background())
	})
	if !testsetup.Until(time.Now().Add(5*time.Second), 10*time.Millisecond, func() bool {
		snapshot, snapshotErr := authority.CurrentTaskExecutionSnapshot(workflowRef.TaskID)
		return snapshotErr == nil && len(snapshot.Executions) == 1 && snapshot.Executions[0].WaitingQuestion
	}) {
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		_, waitErr := handle.Wait(waitCtx)
		snapshot, snapshotErr := authority.CurrentTaskExecutionSnapshot(workflowRef.TaskID)
		t.Fatalf("agent execution did not enter waiting-question state: wait=%v snapshot=%+v snapshotErr=%v", waitErr, snapshot, snapshotErr)
	}
	assertWaiting := func(label string) {
		t.Helper()
		snapshot, snapshotErr := authority.CurrentTaskExecutionSnapshot(workflowRef.TaskID)
		if snapshotErr != nil {
			t.Fatalf("%s CurrentTaskExecutionSnapshot: %v", label, snapshotErr)
		}
		if len(snapshot.Executions) != 1 || !snapshot.Executions[0].WaitingQuestion {
			t.Fatalf("%s waiting-question snapshot = %+v, want unchanged live waiter", label, snapshot)
		}
	}

	if _, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
		TargetNodeID:     "missing-node",
		AllowMissingEdge: true,
	}); err == nil {
		t.Fatal("invalid MoveWorkflowTask succeeded")
	}
	assertWaiting("invalid move")

	definition, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	agentNodeID := workflowServiceNodeIDByKind(t, definition.Definition, "agent")
	response, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
		TargetNodeID:     agentNodeID,
		AllowMissingEdge: true,
		AutoApprove:      true,
	})
	if err != nil {
		t.Fatalf("selection-required MoveWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired || response.SelectionRequired == nil || response.Applied != nil {
		t.Fatalf("selection-required response = %+v", response)
	}
	assertWaiting("selection-required move")
}

func TestServiceApproveTerminalTransitionDoesNotResolveExecutionTarget(t *testing.T) {
	ctx, service, _, _, _, transitionID := newWorkflowServicePendingCompletionApproval(t)
	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolveErr: errors.New("terminal approval must not resolve an execution target"),
	}

	approvedResponse, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskTransitionID: transitionID})
	if err != nil {
		t.Fatalf("ApproveWorkflowTask: %v", err)
	}
	approved := workflowServiceApproveApplied(t, approvedResponse)
	if approved.State != "approved" || len(approved.RunIDs) != 0 {
		t.Fatalf("terminal approval = %+v, want approved without run", approved)
	}
}

func TestServiceApproveExecutableTransitionRequiresSelectionWithoutApplyingApproval(t *testing.T) {
	ctx, service, _, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
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
	if _, err := service.store.UpdateEdge(ctx, workflowstore.EdgeRecord{
		ID:                workflow.EdgeID(startEdge.ID),
		WorkflowID:        workflow.WorkflowID(workflowID),
		TransitionGroupID: workflow.TransitionGroupID(startEdge.TransitionGroupID),
		Key:               workflow.ModelKey(startEdge.Key),
		TargetNodeID:      workflow.NodeID(startEdge.TargetNodeID),
		RequiresApproval:  true,
		ContextMode:       workflow.ContextMode(startEdge.ContextMode),
		ContextSource:     workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(startEdge.ContextSource.Kind), NodeKey: workflow.ModelKey(startEdge.ContextSource.NodeKey)}),
		PromptTemplate:    startEdge.PromptTemplate,
		Parameters:        domainParameters(startEdge.Parameters),
	}); err != nil {
		t.Fatalf("enable start edge approval: %v", err)
	}
	movedResponse, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           taskID,
		TargetNodeID:     startEdge.TargetNodeID,
		AllowMissingEdge: true,
		AutoApprove:      true,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	moved := workflowServiceMoveApplied(t, movedResponse)
	if moved.State != "pending_approval" {
		t.Fatalf("move response = %+v, want pending approval", movedResponse)
	}
	notifier := &recordingSchedulerNotifier{}
	finalizer := &recordingWorkflowAttentionFinalizer{}
	installWorkflowServiceScheduler(t, service, notifier)
	service.attentionFinalizer = finalizer

	response, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskTransitionID: moved.TransitionID,
	})
	if err != nil {
		t.Fatalf("ApproveWorkflowTask: %v", err)
	}
	if response.Outcome != serverapi.WorkflowExecutionTargetActionOutcomeSelectionRequired ||
		response.Applied != nil ||
		response.SelectionRequired == nil ||
		response.SelectionRequired.Reason != serverapi.WorkflowExecutionTargetSelectionReasonPolicyRequiresSelection {
		t.Fatalf("approve response = %+v, want policy selection requirement", response)
	}
	if notifier.count != 0 {
		t.Fatalf("scheduler notifications = %d, want none", notifier.count)
	}
	if len(finalizer.results) != 0 {
		t.Fatalf("attention finalizer results = %+v, want none", finalizer.results)
	}
	transitions, err := service.store.ListTransitions(ctx, workflow.TaskID(taskID))
	if err != nil {
		t.Fatalf("ListTransitions: %v", err)
	}
	if len(transitions) != 1 || transitions[0].State != "pending_approval" {
		t.Fatalf("transitions = %+v, want unchanged pending approval", transitions)
	}
}

func TestServiceApproveExecutableTransitionMaterializesSelectionBeforeApplying(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
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
	movedResponse, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskID:           task.Task.ID,
		TargetNodeID:     startEdge.TargetNodeID,
		AllowMissingEdge: true,
		AutoApprove:      true,
	})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	moved := workflowServiceMoveApplied(t, movedResponse)
	if moved.State != "pending_approval" {
		t.Fatalf("move response = %+v, want pending approval", movedResponse)
	}
	worktreeID := "worktree-" + task.Task.ID
	worktreeRoot := filepath.Join(t.TempDir(), "approval-worktree")
	requestedRef := "HEAD"
	commitOID := strings.Repeat("e", 40)
	infrastructure := &recordingExecutionTargetInfrastructure{
		resolution: workflowstore.ExecutionTargetSnapshot{
			Mode:         workflow.ExecutionTargetModeHead,
			RequestedRef: &requestedRef,
			CommitOID:    &commitOID,
			Provenance:   workflowstore.ExecutionTargetProvenanceResolved,
		},
		materialize: func(taskID workflow.TaskID) (workflowstore.ManagedExecutionRoot, error) {
			if err := metadataStore.UpsertWorktreeRecord(ctx, metadata.WorktreeRecord{ID: worktreeID, WorkspaceID: binding.WorkspaceID, CanonicalRoot: worktreeRoot, Managed: true, CreatedBranch: true}); err != nil {
				return workflowstore.ManagedExecutionRoot{}, err
			}
			updated, err := metadataStore.Queries().UpdateTaskManagedWorktree(ctx, sqlitegen.UpdateTaskManagedWorktreeParams{
				ID:                string(taskID),
				ManagedWorktreeID: sql.NullString{String: worktreeID, Valid: true},
				UpdatedAtUnixMs:   time.Now().UTC().UnixMilli(),
			})
			if err != nil {
				return workflowstore.ManagedExecutionRoot{}, err
			}
			if updated != 1 {
				return workflowstore.ManagedExecutionRoot{}, sql.ErrNoRows
			}
			return workflowstore.ManagedExecutionRoot{WorktreeID: worktreeID, Root: worktreeRoot}, nil
		},
	}
	service.executionTargets = infrastructure
	setupID := serverapi.NewWorktreeSetupOperationID()
	approvedResponse, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		SetupOperationID: setupID,
		TaskTransitionID: moved.TransitionID,
		ExecutionTarget: &serverapi.WorkflowExecutionTargetSelection{
			Mode: serverapi.WorkflowExecutionTargetModeHead,
		},
	})
	if err != nil {
		t.Fatalf("ApproveWorkflowTask: %v", err)
	}
	approved := workflowServiceApproveApplied(t, approvedResponse)
	if approved.State != "approved" || len(approved.RunIDs) != 1 {
		t.Fatalf("approval = %+v, want approved executable run", approved)
	}
	if infrastructure.materializeTaskID != workflow.TaskID(task.Task.ID) || infrastructure.setupOperationID != setupID {
		t.Fatalf("execution target infrastructure = %+v, want task %s setup %s", infrastructure, task.Task.ID, setupID.String())
	}

	service.executionTargets = &recordingExecutionTargetInfrastructure{
		resolveErr: errors.New("locked target must not be resolved again"),
	}
	replayedResponse, err := service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskTransitionID: moved.TransitionID,
	})
	if err != nil {
		t.Fatalf("ApproveWorkflowTask replay: %v", err)
	}
	replayed := workflowServiceApproveApplied(t, replayedResponse)
	if replayed.State != "approved" || len(replayed.RunIDs) != 1 || replayed.RunIDs[0] != approved.RunIDs[0] {
		t.Fatalf("approval replay = %+v, want healthy locked target reuse", replayed)
	}
	if service.executionTargets.(*recordingExecutionTargetInfrastructure).restoreTaskID != workflow.TaskID(task.Task.ID) {
		t.Fatalf("locked target restore task = %q, want %q", service.executionTargets.(*recordingExecutionTargetInfrastructure).restoreTaskID, task.Task.ID)
	}

	service.executionTargets = &recordingExecutionTargetInfrastructure{
		restoreErr: &worktree.LockedTaskWorktreeError{Cause: worktree.LockedTaskWorktreeCauseMissingBranch},
	}
	_, err = service.ApproveWorkflowTask(ctx, serverapi.WorkflowTaskApproveRequest{
		SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
		TaskTransitionID: moved.TransitionID,
	})
	var lockedErr *serverapi.WorkflowLockedExecutionTargetError
	if !errors.As(err, &lockedErr) || lockedErr.Cause != serverapi.WorkflowLockedExecutionTargetCauseMissingBranch {
		t.Fatalf("ApproveWorkflowTask locked repair error = %v, want typed missing branch", err)
	}
}

func TestServiceInterruptTaskTargetsRunAndCancelsRuntime(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	started := startWorkflowServiceTask(t, ctx, service, taskID)
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	prepared := prepareWorkflowServiceInterrupt(service, taskID, started.RunID, claimed.Generation)

	interrupted, err := service.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("InterruptWorkflowTask: %v", err)
	}
	if len(interrupted.Runs) != 1 {
		t.Fatalf("interrupt response=%+v, want one run", interrupted)
	}
	if prepared.requestStopCalls != 1 || prepared.waitCalls != 1 {
		t.Fatalf("prepared interrupt request/wait calls = %d/%d, want 1/1", prepared.requestStopCalls, prepared.waitCalls)
	}
}

func TestServiceInterruptTaskRejectsDurableStartedRunWithoutExactLiveExecution(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	started := startWorkflowServiceTask(t, ctx, service, taskID)
	if _, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0); err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}

	_, err := service.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{TaskID: taskID})
	if !errors.Is(err, workflowexecution.ErrNoInterruptibleExecution) {
		t.Fatalf("InterruptWorkflowTask error = %v, want no exact live execution", err)
	}
	run, err := service.store.GetRun(ctx, workflow.RunID(started.RunID))
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.InterruptedAt != nil {
		t.Fatalf("durable-only run was interrupted: %+v", run)
	}
}

func TestWorkflowMutationPermitSerializesConcurrentTaskStarts(t *testing.T) {
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	const taskCount = 12
	taskIDs := make([]string, 0, taskCount)
	for range taskCount {
		task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
		taskIDs = append(taskIDs, task.Task.ID)
	}

	start := make(chan struct{})
	errs := make(chan error, taskCount)
	var wg sync.WaitGroup
	for _, taskID := range taskIDs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := service.StartWorkflowTask(ctx, serverapi.WorkflowTaskStartRequest{
				SetupOperationID: serverapi.NewWorktreeSetupOperationID(),
				TaskID:           taskID,
				ExecutionTarget:  &serverapi.WorkflowExecutionTargetSelection{Mode: serverapi.WorkflowExecutionTargetModeNone},
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent StartWorkflowTask: %v", err)
		}
	}
}

func TestInterruptRejectsClaimedRunUntilExactExecutionScopeRegisters(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	started := startWorkflowServiceTask(t, ctx, service, taskID)
	runtime := newClaimScopeRaceRuntime()
	service.interruptAuthority = runtime
	scheduler, err := workflowexecution.NewSchedulerService(
		service.store,
		runtime,
		service.mutationPermit,
		workflowexecution.SchedulerConfig{Concurrency: 1},
	)
	if err != nil {
		t.Fatalf("NewSchedulerService: %v", err)
	}
	t.Cleanup(func() { _ = scheduler.Close() })

	startDone := make(chan error, 1)
	go func() {
		startDone <- scheduler.StartExplicitRuns(ctx, []workflow.RunID{workflow.RunID(started.RunID)})
	}()
	request := <-runtime.entered

	if _, err := service.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{TaskID: taskID}); !errors.Is(err, workflowexecution.ErrNoInterruptibleExecution) {
		t.Fatalf("InterruptWorkflowTask during preparation error = %v, want no interruptible execution", err)
	}
	run, err := service.store.GetRun(ctx, request.RunID)
	if err != nil {
		t.Fatalf("GetRun during preparation: %v", err)
	}
	if run.InterruptedAt != nil {
		t.Fatalf("preparing run was durably interrupted without an exact scope: %+v", run)
	}

	close(runtime.release)
	if err := <-startDone; err != nil {
		t.Fatalf("StartExplicitRuns: %v", err)
	}
	if _, err := service.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{TaskID: taskID}); err != nil {
		t.Fatalf("InterruptWorkflowTask: %v", err)
	}
	run, err = service.store.GetRun(ctx, request.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.InterruptedAt == nil {
		t.Fatalf("run was not interrupted after exact scope registration: %+v", run)
	}
}

func TestServiceInterruptTaskWithCustomReasonDoesNotSurfaceInterruptedRunAttention(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	started := startWorkflowServiceTask(t, ctx, service, taskID)
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	prepareWorkflowServiceInterrupt(service, taskID, started.RunID, claimed.Generation)

	if _, err := service.InterruptWorkflowTask(ctx, serverapi.WorkflowTaskInterruptRequest{
		TaskID: taskID,
		Reason: "operator paused this run",
	}); err != nil {
		t.Fatalf("InterruptWorkflowTask: %v", err)
	}

	attention, err := service.readModels.Attention.List(ctx, serverapi.WorkflowAttentionListRequest{})
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
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	startWorkflowServiceTask(t, ctx, service, taskID)
	canceler := &recordingTaskRuntimeCanceler{}
	service.runtimeCancel = canceler

	if err := service.CancelWorkflowTask(ctx, serverapi.WorkflowTaskCancelRequest{TaskID: taskID, Reason: "stop"}); err != nil {
		t.Fatalf("CancelWorkflowTask: %v", err)
	}
	if len(canceler.taskIDs) != 1 || canceler.taskIDs[0] != workflow.TaskID(taskID) {
		t.Fatalf("canceled tasks = %+v", canceler.taskIDs)
	}
}

func TestServiceCancelTaskResolvesPendingApprovalAttention(t *testing.T) {
	fixture := newWorkflowServicePendingApprovalFixture(t)

	if err := fixture.service.CancelWorkflowTask(fixture.ctx, serverapi.WorkflowTaskCancelRequest{TaskID: fixture.taskID, Reason: "stop"}); err != nil {
		t.Fatalf("CancelWorkflowTask: %v", err)
	}
	if len(fixture.finalizer.results) != 2 || len(fixture.finalizer.results[1].ResolvedApprovalProjections) != 1 || fixture.finalizer.results[1].ResolvedApprovalProjections[0].TransitionID != workflow.TransitionID(fixture.pending.TransitionID) {
		t.Fatalf("attention finalizer results = %+v", fixture.finalizer.results)
	}
}

func TestServiceCancelTaskResolvesCapturedApprovalWithFreshFinalizer(t *testing.T) {
	fixture := newWorkflowServicePendingApprovalFixture(t)
	publisher := &recordingWorkflowAttentionPublisher{}
	fixture.service.attentionFinalizer = workflowattention.NewFinalizer(failingWorkflowPendingProjectionProvider{t: t}, publisher)

	if err := fixture.service.CancelWorkflowTask(fixture.ctx, serverapi.WorkflowTaskCancelRequest{TaskID: fixture.taskID, Reason: "stop"}); err != nil {
		t.Fatalf("CancelWorkflowTask: %v", err)
	}
	if len(publisher.resolved) != 1 || publisher.resolved[0].scope.WorkflowID != fixture.workflowID || publisher.resolved[0].scope.TaskID != fixture.taskID {
		t.Fatalf("resolved notifications = %+v", publisher.resolved)
	}
}

func TestServiceCancelTaskResolvesCapturedInterruptionWithFreshFinalizer(t *testing.T) {
	ctx, service, _, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	started := startWorkflowServiceTask(t, ctx, service, taskID)
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := service.store.InterruptRunGeneration(ctx, workflow.RunID(started.RunID), claimed.Generation, "cancel_interruption", `{"error":"cancel detail"}`); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	publisher := &recordingWorkflowAttentionPublisher{}
	service.attentionFinalizer = workflowattention.NewFinalizer(failingWorkflowPendingProjectionProvider{t: t}, publisher)

	if err := service.CancelWorkflowTask(ctx, serverapi.WorkflowTaskCancelRequest{TaskID: taskID, Reason: "stop"}); err != nil {
		t.Fatalf("CancelWorkflowTask: %v", err)
	}
	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved notifications = %+v, want one", publisher.resolved)
	}
	resolved := publisher.resolved[0]
	if resolved.id.UUID != started.RunID || resolved.scope.WorkflowID != workflowID || resolved.scope.TaskID != taskID {
		t.Fatalf("resolved interruption = %+v", resolved)
	}
}

func TestServiceDeleteTaskCancelsRuntimeAndPublishesEvent(t *testing.T) {
	ctx, service, projectID, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	startWorkflowServiceTask(t, ctx, service, taskID)
	sub, err := service.SubscribeWorkflowProject(ctx, serverapi.WorkflowProjectSubscribeRequest{ProjectID: projectID})
	if err != nil {
		t.Fatalf("SubscribeWorkflowProject: %v", err)
	}
	defer func() { _ = sub.Close() }()
	canceler := &recordingTaskRuntimeCanceler{}
	service.runtimeCancel = canceler
	worktreeCleanup := &recordingTaskWorktreeDeleter{}
	service.taskWorktreeCleanup = worktreeCleanup

	if err := service.DeleteWorkflowTask(ctx, serverapi.WorkflowTaskDeleteRequest{TaskID: taskID}); err != nil {
		t.Fatalf("DeleteWorkflowTask: %v", err)
	}
	if len(canceler.taskIDs) != 1 || canceler.taskIDs[0] != workflow.TaskID(taskID) {
		t.Fatalf("canceled tasks = %+v", canceler.taskIDs)
	}
	if len(worktreeCleanup.taskIDs) != 1 || worktreeCleanup.taskIDs[0] != taskID {
		t.Fatalf("worktree cleanup tasks = %+v", worktreeCleanup.taskIDs)
	}
	event := nextWorkflowProjectEvent(t, sub)
	if !stringPointerEquals(event.ProjectID, projectID) || !stringPointerEquals(event.WorkflowID, workflowID) || event.Resource != "task" || event.Action != "deleted" || event.PrimaryEntityID != taskID || len(event.RelatedIDs) != 0 {
		t.Fatalf("delete event = %+v, want task deleted event", event)
	}
	if _, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: taskID}); err == nil {
		t.Fatalf("deleted workflow task should not remain readable")
	}
}

func TestServiceDeleteTaskResolvesPendingApprovalAttention(t *testing.T) {
	fixture := newWorkflowServicePendingApprovalFixture(t)

	if err := fixture.service.DeleteWorkflowTask(fixture.ctx, serverapi.WorkflowTaskDeleteRequest{TaskID: fixture.taskID}); err != nil {
		t.Fatalf("DeleteWorkflowTask: %v", err)
	}
	if len(fixture.finalizer.results) != 2 || len(fixture.finalizer.results[1].ResolvedApprovalProjections) != 1 || fixture.finalizer.results[1].ResolvedApprovalProjections[0].TransitionID != workflow.TransitionID(fixture.pending.TransitionID) {
		t.Fatalf("attention finalizer results = %+v", fixture.finalizer.results)
	}
}

func TestServiceDeleteTaskResolvesCapturedApprovalWithFreshFinalizer(t *testing.T) {
	fixture := newWorkflowServicePendingApprovalFixture(t)
	publisher := &recordingWorkflowAttentionPublisher{}
	fixture.service.attentionFinalizer = workflowattention.NewFinalizer(failingWorkflowPendingProjectionProvider{t: t}, publisher)

	if err := fixture.service.DeleteWorkflowTask(fixture.ctx, serverapi.WorkflowTaskDeleteRequest{TaskID: fixture.taskID}); err != nil {
		t.Fatalf("DeleteWorkflowTask: %v", err)
	}
	if len(publisher.resolved) != 1 || publisher.resolved[0].scope.WorkflowID != fixture.workflowID || publisher.resolved[0].scope.TaskID != fixture.taskID {
		t.Fatalf("resolved notifications = %+v", publisher.resolved)
	}
}

func TestServiceDeleteTaskResolvesCapturedInterruptionWithFreshFinalizer(t *testing.T) {
	ctx, service, _, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	started := startWorkflowServiceTask(t, ctx, service, taskID)
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := service.store.InterruptRunGeneration(ctx, workflow.RunID(started.RunID), claimed.Generation, "delete_interruption", `{"error":"delete detail"}`); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	publisher := &recordingWorkflowAttentionPublisher{}
	service.attentionFinalizer = workflowattention.NewFinalizer(failingWorkflowPendingProjectionProvider{t: t}, publisher)

	if err := service.DeleteWorkflowTask(ctx, serverapi.WorkflowTaskDeleteRequest{TaskID: taskID}); err != nil {
		t.Fatalf("DeleteWorkflowTask: %v", err)
	}
	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved notifications = %+v, want one", publisher.resolved)
	}
	resolved := publisher.resolved[0]
	if resolved.id.UUID != started.RunID || resolved.scope.WorkflowID != workflowID || resolved.scope.TaskID != taskID {
		t.Fatalf("resolved interruption = %+v", resolved)
	}
}

func TestServiceDeleteWorkflowResolvesPendingApprovalAttention(t *testing.T) {
	fixture := newWorkflowServicePendingApprovalFixture(t)
	preview, err := fixture.service.PreviewWorkflowDelete(fixture.ctx, serverapi.WorkflowDeletePreviewRequest{WorkflowID: fixture.workflowID})
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}

	deleted, err := fixture.service.DeleteWorkflow(fixture.ctx, serverapi.WorkflowDeleteRequest{
		WorkflowID:           fixture.workflowID,
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
	if len(fixture.finalizer.results) != 2 {
		t.Fatalf("attention finalizer results = %+v, want pending setup and delete resolution", fixture.finalizer.results)
	}
	resolved := fixture.finalizer.results[1].ResolvedApprovalProjections
	if len(resolved) != 1 || resolved[0].TransitionID != workflow.TransitionID(fixture.pending.TransitionID) {
		t.Fatalf("delete resolved approvals = %+v, want transition %s", resolved, fixture.pending.TransitionID)
	}
}

func TestServiceDeleteWorkflowResolvesInterruptedRunAttention(t *testing.T) {
	ctx, service, _, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	started := startWorkflowServiceTask(t, ctx, service, taskID)
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := service.store.InterruptRunGeneration(ctx, workflow.RunID(started.RunID), claimed.Generation, "workflow_runtime_failed", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	publisher := &recordingWorkflowAttentionPublisher{}
	service.attentionFinalizer = workflowattention.NewFinalizer(failingWorkflowPendingProjectionProvider{t: t}, publisher)
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
	if len(publisher.resolved) != 1 || publisher.resolved[0].id.UUID != started.RunID {
		t.Fatalf("resolved interrupted runs = %+v, want %s", publisher.resolved, started.RunID)
	}
}

func TestServiceDeleteWorkflowResolvesInterruptedRunAttentionAcrossProjects(t *testing.T) {
	ctx, service, firstProject, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	secondProject, err := metadataStore.CreateProjectForWorkspace(ctx, t.TempDir(), "Second workflow attention project")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace: %v", err)
	}
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, firstProject.ProjectID, workflowID)
	linkDefaultWorkflowServiceProject(t, ctx, service, secondProject.ProjectID, workflowID)
	requireWorkflowServiceEdgeApproval(t, ctx, service, workflowID, "done")

	interrupt := func(projectID string) (workflow.RunID, string) {
		t.Helper()
		task := createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{ProjectID: projectID, Title: "Interrupted task", Body: "Body"})
		started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
		claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
		if err != nil {
			t.Fatalf("ClaimRun: %v", err)
		}
		if err := service.store.InterruptRunGeneration(ctx, workflow.RunID(started.RunID), claimed.Generation, "workflow_runtime_failed", "{}"); err != nil {
			t.Fatalf("InterruptRunGeneration: %v", err)
		}
		return workflow.RunID(started.RunID), task.Task.ID
	}
	pendingApproval := func(projectID string) (workflow.TransitionID, string) {
		t.Helper()
		task := createWorkflowServiceTask(t, ctx, service, serverapi.WorkflowTaskCreateRequest{ProjectID: projectID, Title: "Approval task", Body: "Body"})
		started := startWorkflowServiceTask(t, ctx, service, task.Task.ID)
		completed, err := service.store.CompleteRun(ctx, workflowstore.CompleteRunRequest{
			RunID:        workflow.RunID(started.RunID),
			TransitionID: "done",
			Actor:        "agent",
		})
		if err != nil {
			t.Fatalf("CompleteRun: %v", err)
		}
		if completed.Result.State != "pending_approval" {
			t.Fatalf("completion = %+v, want pending approval", completed)
		}
		return completed.Result.TransitionID, task.Task.ID
	}

	firstRunID, firstInterruptedTaskID := interrupt(firstProject.ProjectID)
	secondRunID, secondInterruptedTaskID := interrupt(secondProject.ProjectID)
	firstTransitionID, firstApprovalTaskID := pendingApproval(firstProject.ProjectID)
	secondTransitionID, secondApprovalTaskID := pendingApproval(secondProject.ProjectID)
	unrelatedWorkflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, secondProject.ProjectID, unrelatedWorkflowID)
	requireWorkflowServiceEdgeApproval(t, ctx, service, unrelatedWorkflowID, "done")
	unrelatedRunID, _ := interrupt(secondProject.ProjectID)
	unrelatedTransitionID, _ := pendingApproval(secondProject.ProjectID)

	publisher := &recordingWorkflowAttentionPublisher{}
	service.attentionFinalizer = workflowattention.NewFinalizer(failingWorkflowPendingProjectionProvider{t: t}, publisher)
	preview, err := service.PreviewWorkflowDelete(ctx, serverapi.WorkflowDeletePreviewRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}
	if _, err := service.DeleteWorkflow(ctx, serverapi.WorkflowDeleteRequest{
		WorkflowID:           workflowID,
		Confirmed:            true,
		ExpectedVersion:      preview.Impact.Version,
		ExpectedProjectCount: preview.Impact.ProjectCount,
		ExpectedLinkCount:    preview.Impact.LinkCount,
		ExpectedTaskCount:    preview.Impact.TaskCount,
	}); err != nil {
		t.Fatalf("DeleteWorkflow: %v", err)
	}

	resolved := make(map[clientui.AttentionNotificationID]attentionnotify.RoutingScope, len(publisher.resolved))
	for _, publication := range publisher.resolved {
		resolved[publication.id] = publication.scope
	}
	if len(resolved) != 4 {
		t.Fatalf("resolved workflow attention = %+v, want four", publisher.resolved)
	}
	assertResolution := func(kind clientui.AttentionNotificationKind, id string, projectID string, taskID string) {
		t.Helper()
		scope, ok := resolved[clientui.AttentionNotificationID{Kind: kind, UUID: id}]
		if !ok || scope.ProjectID != projectID || scope.WorkflowID != workflowID || scope.TaskID != taskID {
			t.Fatalf("resolved %s %s = %+v, want project %s workflow %s task %s", kind, id, scope, projectID, workflowID, taskID)
		}
	}
	assertResolution(clientui.AttentionNotificationKindInterruptedRun, string(firstRunID), firstProject.ProjectID, firstInterruptedTaskID)
	assertResolution(clientui.AttentionNotificationKindInterruptedRun, string(secondRunID), secondProject.ProjectID, secondInterruptedTaskID)
	assertResolution(clientui.AttentionNotificationKindApproval, string(firstTransitionID), firstProject.ProjectID, firstApprovalTaskID)
	assertResolution(clientui.AttentionNotificationKindApproval, string(secondTransitionID), secondProject.ProjectID, secondApprovalTaskID)
	if _, ok := resolved[clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindInterruptedRun, UUID: string(unrelatedRunID)}]; ok {
		t.Fatalf("resolved workflow attention = %+v, must exclude unrelated run %s", publisher.resolved, unrelatedRunID)
	}
	if _, ok := resolved[clientui.AttentionNotificationID{Kind: clientui.AttentionNotificationKindApproval, UUID: string(unrelatedTransitionID)}]; ok {
		t.Fatalf("resolved workflow attention = %+v, must exclude unrelated transition %s", publisher.resolved, unrelatedTransitionID)
	}
}

func TestServiceDeleteWorkflowRollbackPublishesNoAttentionResolution(t *testing.T) {
	ctx, service, binding, metadataStore := newWorkflowServiceTestContextWithMetadata(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	taskID := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID).Task.ID
	started := startWorkflowServiceTask(t, ctx, service, taskID)
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := service.store.InterruptRunGeneration(ctx, workflow.RunID(started.RunID), claimed.Generation, "workflow_runtime_failed", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	if _, err := metadataStore.DB().ExecContext(ctx, `
CREATE TRIGGER fail_workflow_task_delete
BEFORE DELETE ON tasks
BEGIN
    SELECT RAISE(ABORT, 'forced workflow deletion failure');
END;`); err != nil {
		t.Fatalf("create workflow deletion failure trigger: %v", err)
	}
	publisher := &recordingWorkflowAttentionPublisher{}
	service.attentionFinalizer = workflowattention.NewFinalizer(failingWorkflowPendingProjectionProvider{t: t}, publisher)
	preview, err := service.PreviewWorkflowDelete(ctx, serverapi.WorkflowDeletePreviewRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("PreviewWorkflowDelete: %v", err)
	}

	_, err = service.DeleteWorkflow(ctx, serverapi.WorkflowDeleteRequest{
		WorkflowID:           workflowID,
		Confirmed:            true,
		ExpectedVersion:      preview.Impact.Version,
		ExpectedProjectCount: preview.Impact.ProjectCount,
		ExpectedLinkCount:    preview.Impact.LinkCount,
		ExpectedTaskCount:    preview.Impact.TaskCount,
	})
	if err == nil {
		t.Fatal("DeleteWorkflow succeeded, want forced rollback")
	}
	if len(publisher.resolved) != 0 || len(publisher.pending) != 0 {
		t.Fatalf("attention publications after rollback = pending %+v resolved %+v, want none", publisher.pending, publisher.resolved)
	}
	if _, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: taskID}); err != nil {
		t.Fatalf("GetWorkflowTask after rollback: %v", err)
	}
}

func TestServiceDeleteTaskPreflightBlockedDoesNotCancelRuns(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	startWorkflowServiceTask(t, ctx, service, taskID)
	canceler := &recordingTaskRuntimeCanceler{}
	service.runtimeCancel = canceler
	worktreeCleanup := &recordingTaskWorktreeDeleter{preflightErr: serverapi.ErrWorktreeBlocked}
	service.taskWorktreeCleanup = worktreeCleanup

	err := service.DeleteWorkflowTask(ctx, serverapi.WorkflowTaskDeleteRequest{TaskID: taskID})
	if !errors.Is(err, serverapi.ErrWorktreeBlocked) {
		t.Fatalf("DeleteWorkflowTask error = %v, want ErrWorktreeBlocked", err)
	}
	if len(worktreeCleanup.preflightTaskIDs) != 1 || worktreeCleanup.preflightTaskIDs[0] != taskID {
		t.Fatalf("preflight tasks = %+v, want one preflight for %s", worktreeCleanup.preflightTaskIDs, taskID)
	}
	if len(canceler.taskIDs) != 0 {
		t.Fatalf("canceled tasks = %+v, want none when preflight blocks", canceler.taskIDs)
	}
	if len(worktreeCleanup.taskIDs) != 0 {
		t.Fatalf("worktree delete tasks = %+v, want none when preflight blocks", worktreeCleanup.taskIDs)
	}
	if _, err := service.GetWorkflowTask(ctx, serverapi.WorkflowTaskGetRequest{TaskID: taskID}); err != nil {
		t.Fatalf("blocked task should remain readable: %v", err)
	}
}

func TestServiceResumeTaskRequeuesRunAndNotifiesScheduler(t *testing.T) {
	ctx, service, _, _, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	started := startWorkflowServiceTask(t, ctx, service, taskID)
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := service.store.InterruptRunGeneration(ctx, workflow.RunID(started.RunID), claimed.Generation, "manual", "{}"); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	notifier := &recordingSchedulerNotifier{}
	finalizer := &recordingWorkflowAttentionFinalizer{}
	installWorkflowServiceScheduler(t, service, notifier)
	service.attentionFinalizer = finalizer

	resumed, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{TaskID: taskID})
	if err != nil {
		t.Fatalf("ResumeWorkflowTask: %v", err)
	}
	if len(resumed.Runs) != 1 || resumed.Runs[0].Generation <= claimed.Generation || resumed.Runs[0].PlacementID == "" || resumed.Runs[0].NodeID == "" {
		t.Fatalf("resume response = %+v, want same run requeued", resumed)
	}
	if notifier.count != 1 {
		t.Fatalf("scheduler notifications = %d, want 1", notifier.count)
	}
	if len(finalizer.results) != 1 || len(finalizer.results[0].ResolvedInterruptedRunProjections) != 1 || finalizer.results[0].ResolvedInterruptedRunProjections[0].RunID != workflow.RunID(started.RunID) {
		t.Fatalf("resolved interrupted runs = %+v, want %s", finalizer.results, started.RunID)
	}
}

func TestServiceResumeTaskResolvesCapturedInterruptionWithFreshFinalizer(t *testing.T) {
	ctx, service, _, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	started := startWorkflowServiceTask(t, ctx, service, taskID)
	claimed, err := service.store.ClaimRun(ctx, workflow.RunID(started.RunID), 0)
	if err != nil {
		t.Fatalf("ClaimRun: %v", err)
	}
	if err := service.store.InterruptRunGeneration(ctx, workflow.RunID(started.RunID), claimed.Generation, "manual_resume", `{"error":"resume detail"}`); err != nil {
		t.Fatalf("InterruptRunGeneration: %v", err)
	}
	publisher := &recordingWorkflowAttentionPublisher{}
	service.attentionFinalizer = workflowattention.NewFinalizer(failingWorkflowPendingProjectionProvider{t: t}, publisher)

	if _, err := service.ResumeWorkflowTask(ctx, serverapi.WorkflowTaskResumeRequest{TaskID: taskID}); err != nil {
		t.Fatalf("ResumeWorkflowTask: %v", err)
	}
	if len(publisher.resolved) != 1 {
		t.Fatalf("resolved notifications = %+v, want one", publisher.resolved)
	}
	resolved := publisher.resolved[0]
	if resolved.id.UUID != started.RunID || resolved.scope.WorkflowID != workflowID || resolved.scope.TaskID != taskID {
		t.Fatalf("resolved interruption = %+v", resolved)
	}
}

type recordingSchedulerNotifier struct {
	count           int
	automatic       [][]workflow.RunID
	explicit        [][]workflow.RunID
	registrationErr error
}

func (n *recordingSchedulerNotifier) RegisterAutomaticStarts(runIDs []workflow.RunID) error {
	n.count++
	n.automatic = append(n.automatic, append([]workflow.RunID(nil), runIDs...))
	return n.registrationErr
}

func (n *recordingSchedulerNotifier) StartExplicitRuns(_ context.Context, runIDs []workflow.RunID) error {
	n.count++
	n.explicit = append(n.explicit, append([]workflow.RunID(nil), runIDs...))
	return nil
}

func (n *recordingSchedulerNotifier) EnsureTaskQuiescent(context.Context, workflow.TaskID) error {
	return nil
}

type recordingWorkflowAttentionFinalizer struct {
	results []workflowattention.TransitionResult
}

type failingWorkflowPendingProjectionProvider struct {
	t *testing.T
}

func (p failingWorkflowPendingProjectionProvider) PendingApprovalProjection(context.Context, workflow.TransitionID) (workflowattention.ApprovalProjection, bool, error) {
	p.t.Fatal("pending approval projection read during captured resolution")
	return workflowattention.ApprovalProjection{}, false, nil
}

func (p failingWorkflowPendingProjectionProvider) PendingInterruptedRunProjection(context.Context, workflow.RunID) (workflowattention.InterruptedRunProjection, bool, error) {
	p.t.Fatal("pending interrupted-run projection read during captured resolution")
	return workflowattention.InterruptedRunProjection{}, false, nil
}

type workflowResolvedPublication struct {
	scope attentionnotify.RoutingScope
	id    clientui.AttentionNotificationID
	kind  clientui.AttentionNotificationKind
}

type recordingWorkflowAttentionPublisher struct {
	pending  []clientui.AttentionNotification
	resolved []workflowResolvedPublication
}

func (p *recordingWorkflowAttentionPublisher) PublishPending(_ attentionnotify.RoutingScope, notification clientui.AttentionNotification) error {
	p.pending = append(p.pending, notification)
	return nil
}

func (p *recordingWorkflowAttentionPublisher) PublishResolved(scope attentionnotify.RoutingScope, id clientui.AttentionNotificationID, kind clientui.AttentionNotificationKind, _ time.Time) error {
	p.resolved = append(p.resolved, workflowResolvedPublication{scope: scope, id: id, kind: kind})
	return nil
}

func (f *recordingWorkflowAttentionFinalizer) FinalizeTransition(_ context.Context, result workflowattention.TransitionResult) {
	f.results = append(f.results, result)
}

func (f *recordingWorkflowAttentionFinalizer) PublishPendingInterruptedRun(context.Context, workflow.RunID) {
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

type recordingPreparedInterrupt struct {
	executions       []workflowexecution.ExactExecutionScope
	requestStopCalls int
	waitCalls        int
	waitErr          error
	commitErr        error
}

func prepareWorkflowServiceInterrupt(service *Service, taskID string, runID string, generation int64) *recordingPreparedInterrupt {
	prepared := &recordingPreparedInterrupt{executions: []workflowexecution.ExactExecutionScope{{
		ScopeID:    runtimeids.NewExecutionScopeID(),
		TaskID:     workflow.TaskID(taskID),
		RunID:      workflow.RunID(runID),
		Generation: generation,
	}}}
	service.interruptAuthority = &recordingInterruptAuthority{prepared: prepared}
	return prepared
}

func (p *recordingPreparedInterrupt) Commit(operation func([]workflowexecution.ExactExecutionScope) error) error {
	if p.commitErr != nil {
		return p.commitErr
	}
	if err := operation(append([]workflowexecution.ExactExecutionScope(nil), p.executions...)); err != nil {
		return err
	}
	p.requestStopCalls++
	return nil
}

func (p *recordingPreparedInterrupt) Wait(context.Context) error {
	p.waitCalls++
	return p.waitErr
}

type recordingInterruptAuthority struct {
	prepared workflowexecution.PreparedInterrupt
	err      error
}

func (a *recordingInterruptAuthority) PrepareWorkflowInterrupt(workflowexecution.InterruptSelector) (workflowexecution.PreparedInterrupt, error) {
	return a.prepared, a.err
}

type claimScopeRaceRuntime struct {
	entered chan workflowexecution.SchedulerStartRunRequest
	release chan struct{}

	mu       sync.Mutex
	prepared workflowexecution.PreparedInterrupt
}

func newClaimScopeRaceRuntime() *claimScopeRaceRuntime {
	return &claimScopeRaceRuntime{
		entered: make(chan workflowexecution.SchedulerStartRunRequest, 1),
		release: make(chan struct{}),
	}
}

func (r *claimScopeRaceRuntime) StartWorkflowRun(ctx context.Context, req workflowexecution.SchedulerStartRunRequest) error {
	r.entered <- req
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-r.release:
	}
	r.mu.Lock()
	r.prepared = &recordingPreparedInterrupt{executions: []workflowexecution.ExactExecutionScope{{
		ScopeID:    runtimeids.NewExecutionScopeID(),
		TaskID:     req.TaskID,
		RunID:      req.RunID,
		Generation: req.Generation,
	}}}
	r.mu.Unlock()
	return nil
}

func (r *claimScopeRaceRuntime) PrepareWorkflowInterrupt(selector workflowexecution.InterruptSelector) (workflowexecution.PreparedInterrupt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.prepared == nil {
		return nil, workflowexecution.ErrNoInterruptibleExecution
	}
	return r.prepared, nil
}

func (c *recordingTaskRuntimeRunCancelRequester) RequestCancelRun(runID workflow.RunID) bool {
	c.requestedRunIDs = append(c.requestedRunIDs, runID)
	return c.active
}

type recordingExecutionTargetInfrastructure struct {
	resolution        workflowstore.ExecutionTargetSnapshot
	resolveSelection  workflow.ExecutionTargetSelection
	materializeTaskID workflow.TaskID
	restoreTaskID     workflow.TaskID
	setupOperationID  serverapi.WorktreeSetupOperationID
	materialize       func(workflow.TaskID) (workflowstore.ManagedExecutionRoot, error)
	resolveErr        error
	materializeErr    error
	restoreErr        error
}

func (i *recordingExecutionTargetInfrastructure) RestoreExecutionTarget(_ context.Context, req ExecutionTargetRestoreRequest) error {
	i.restoreTaskID = req.TaskID
	i.setupOperationID = req.SetupOperationID
	return i.restoreErr
}

func (i *recordingExecutionTargetInfrastructure) ResolveExecutionTarget(_ context.Context, req ExecutionTargetResolveRequest) (workflowstore.ExecutionTargetSnapshot, error) {
	i.resolveSelection = req.Selection
	if i.resolveErr != nil {
		return workflowstore.ExecutionTargetSnapshot{}, i.resolveErr
	}
	return i.resolution, nil
}

func (i *recordingExecutionTargetInfrastructure) MaterializeExecutionTarget(_ context.Context, req ExecutionTargetMaterializeRequest) (workflowstore.ManagedExecutionRoot, error) {
	i.materializeTaskID = req.TaskID
	i.setupOperationID = req.SetupOperationID
	if i.materializeErr != nil {
		return workflowstore.ManagedExecutionRoot{}, i.materializeErr
	}
	if i.materialize != nil {
		return i.materialize(req.TaskID)
	}
	return workflowstore.ManagedExecutionRoot{}, nil
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

type unavailableWorkflowTaskDetailReadModel struct{}

func (unavailableWorkflowTaskDetailReadModel) GetTask(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("task detail unavailable")
}

func (unavailableWorkflowTaskDetailReadModel) GetTaskByProjectShortID(context.Context, string, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("task detail unavailable")
}

func (unavailableWorkflowTaskDetailReadModel) GetTaskByShortID(context.Context, string) (serverapi.WorkflowTaskDetail, error) {
	return serverapi.WorkflowTaskDetail{}, errors.New("task detail unavailable")
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
	projectID := binding.ProjectID
	projectPage, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{ProjectID: &projectID, PageSize: 10})
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
	exactPage, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{WorkflowID: &exactWorkflowID, PageSize: 10})
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
	filtered, err := service.ListWorkflows(ctx, serverapi.WorkflowListRequest{PageSize: 10, Query: "Broken"})
	if err != nil {
		t.Fatalf("ListWorkflows filtered: %v", err)
	}
	if len(filtered.Workflows) != 0 {
		t.Fatalf("failed create-and-link left workflows: %+v", filtered.Workflows)
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
	if !stringPointerEquals(unlinkEvent.ProjectID, binding.ProjectID) || !stringPointerEquals(unlinkEvent.WorkflowID, unusedWorkflowID) {
		t.Fatalf("unlink event = %+v, want project/workflow identity", unlinkEvent)
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
	if !stringPointerEquals(event.ProjectID, projectID) || !stringPointerEquals(event.WorkflowID, workflowID) || event.Resource != "workflow" || event.Action != "deleted" || event.PrimaryEntityID != workflowID || len(event.RelatedIDs) != 0 {
		t.Fatalf("event = %+v, want workflow deleted event", event)
	}
	eventCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	workflowEvent, err := workflowSub.Next(eventCtx)
	if err != nil {
		t.Fatalf("workflow subscription delete next: %v", err)
	}
	if workflowEvent.ProjectID != nil || !stringPointerEquals(workflowEvent.WorkflowID, workflowID) || workflowEvent.Resource != "workflow" || workflowEvent.Action != "deleted" || workflowEvent.PrimaryEntityID != workflowID || len(workflowEvent.RelatedIDs) != 0 {
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
	if !saved.Saved || saved.Definition == nil || saved.CurrentVersion != source.Definition.Workflow.Version+1 {
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
		if !stringPointerEquals(event.ProjectID, binding.ProjectID) || !stringPointerEquals(event.WorkflowID, workflowID) {
			t.Fatalf("event = %+v, want linked workflow event", event)
		}
	}
	eventCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	workflowEvent, err := workflowSub.Next(eventCtx)
	if err != nil {
		t.Fatalf("workflow subscription next: %v", err)
	}
	if workflowEvent.ProjectID != nil || !stringPointerEquals(workflowEvent.WorkflowID, workflowID) || workflowEvent.Resource != "workflow" || workflowEvent.Action != "graph_saved" {
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

func newWorkflowServiceOrdinaryTaskFixture(t *testing.T) (context.Context, *Service, string, string, string) {
	t.Helper()
	ctx, service, binding := newWorkflowServiceTestContext(t)
	workflowID := createWorkflowServiceValidWorkflow(t, ctx, service)
	linkDefaultWorkflowServiceProject(t, ctx, service, binding.ProjectID, workflowID)
	task := createDefaultWorkflowServiceTask(t, ctx, service, binding.ProjectID)
	return ctx, service, binding.ProjectID, workflowID, task.Task.ID
}

func newWorkflowServicePendingCompletionApproval(t *testing.T) (context.Context, *Service, string, string, string, string) {
	t.Helper()
	ctx, service, projectID, workflowID, taskID := newWorkflowServiceOrdinaryTaskFixture(t)
	requireWorkflowServiceEdgeApproval(t, ctx, service, workflowID, "done")
	started := startWorkflowServiceTask(t, ctx, service, taskID)
	completed, err := service.store.CompleteRun(ctx, workflowstore.CompleteRunRequest{
		RunID:        workflow.RunID(started.RunID),
		TransitionID: "done",
		Actor:        "agent",
	})
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	if completed.Result.State != "pending_approval" {
		t.Fatalf("completion = %+v, want pending approval", completed)
	}
	return ctx, service, projectID, workflowID, taskID, string(completed.Result.TransitionID)
}

func requireWorkflowServiceEdgeApproval(t *testing.T, ctx context.Context, service *Service, workflowID, edgeKey string) {
	t.Helper()
	def, err := service.GetWorkflow(ctx, serverapi.WorkflowGetRequest{WorkflowID: workflowID})
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	var targetEdge serverapi.WorkflowEdge
	for _, edge := range def.Definition.Edges {
		if edge.Key == edgeKey {
			targetEdge = edge
			break
		}
	}
	if targetEdge.ID == "" {
		t.Fatalf("missing %s edge in %+v", edgeKey, def.Definition.Edges)
	}
	if _, err := service.store.UpdateEdge(ctx, workflowstore.EdgeRecord{ID: workflow.EdgeID(targetEdge.ID), WorkflowID: workflow.WorkflowID(workflowID), TransitionGroupID: workflow.TransitionGroupID(targetEdge.TransitionGroupID), Key: workflow.ModelKey(targetEdge.Key), TargetNodeID: workflow.NodeID(targetEdge.TargetNodeID), RequiresApproval: true, ContextMode: workflow.ContextMode(targetEdge.ContextMode), ContextSource: workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(targetEdge.ContextSource.Kind), NodeKey: workflow.ModelKey(targetEdge.ContextSource.NodeKey)}), PromptTemplate: targetEdge.PromptTemplate, Parameters: domainParameters(targetEdge.Parameters)}); err != nil {
		t.Fatalf("enable %s edge approval: %v", edgeKey, err)
	}
}

type workflowServicePendingApprovalFixture struct {
	ctx                        context.Context
	service                    *Service
	workflowID, taskID, planID string
	finalizer                  *recordingWorkflowAttentionFinalizer
	pending                    serverapi.WorkflowTaskMoveApplied
}

func newWorkflowServicePendingApprovalFixture(t *testing.T) workflowServicePendingApprovalFixture {
	t.Helper()
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
	response, err := service.MoveWorkflowTask(ctx, serverapi.WorkflowTaskMoveRequest{SetupOperationID: serverapi.NewWorktreeSetupOperationID(), TaskID: task.Task.ID, TargetNodeID: planID, AllowMissingEdge: true})
	if err != nil {
		t.Fatalf("MoveWorkflowTask: %v", err)
	}
	pending := workflowServiceMoveApplied(t, response)
	if pending.State != "pending_approval" {
		t.Fatalf("setup move = %+v, want pending approval", pending)
	}
	return workflowServicePendingApprovalFixture{ctx: ctx, service: service, workflowID: workflowID, taskID: task.Task.ID, planID: planID, finalizer: finalizer, pending: pending}
}

func newWorkflowServiceTestContextWithMetadata(t *testing.T) (context.Context, *Service, metadata.Binding, *metadata.Store) {
	t.Helper()
	service, binding, metadataStore := newWorkflowServiceTestServiceWithMetadata(t)
	return context.Background(), service, binding, metadataStore
}

func TestNewRejectsEveryMissingReadModelCapability(t *testing.T) {
	service, _, metadataStore := newWorkflowServiceTestServiceWithMetadata(t)
	complete := newWorkflowServiceReadModels(t, metadataStore, service.store, service.roleResolver, nil, nil)
	tests := []struct {
		name       string
		readModels ReadModels
	}{
		{name: "definitions", readModels: ReadModels{Board: complete.Board, TaskList: complete.TaskList, TaskDetail: complete.TaskDetail, Activity: complete.Activity, Attention: complete.Attention}},
		{name: "board", readModels: ReadModels{Definitions: complete.Definitions, TaskList: complete.TaskList, TaskDetail: complete.TaskDetail, Activity: complete.Activity, Attention: complete.Attention}},
		{name: "task list", readModels: ReadModels{Definitions: complete.Definitions, Board: complete.Board, TaskDetail: complete.TaskDetail, Activity: complete.Activity, Attention: complete.Attention}},
		{name: "task detail", readModels: ReadModels{Definitions: complete.Definitions, Board: complete.Board, TaskList: complete.TaskList, Activity: complete.Activity, Attention: complete.Attention}},
		{name: "activity", readModels: ReadModels{Definitions: complete.Definitions, Board: complete.Board, TaskList: complete.TaskList, TaskDetail: complete.TaskDetail, Attention: complete.Attention}},
		{name: "attention", readModels: ReadModels{Definitions: complete.Definitions, Board: complete.Board, TaskList: complete.TaskList, TaskDetail: complete.TaskDetail, Activity: complete.Activity}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(service.store, tt.readModels, service.roleResolver, workflowexecution.NewMutationPermit(), service.automaticStarts); err == nil {
				t.Fatal("New accepted a missing read-model capability")
			}
		})
	}
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
	metadataStore := testsetup.OpenStore(t, cfg.PersistenceRoot)
	binding, err := metadataStore.RegisterWorkspaceBinding(context.Background(), cfg.WorkspaceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding: %v", err)
	}
	if err := metadataStore.SetProjectKey(context.Background(), binding.ProjectID, "WOR"); err != nil {
		t.Fatalf("SetProjectKey: %v", err)
	}
	resolver := testsetup.QuestionsEnabled("coder")
	store, err := workflowstore.New(metadataStore, workflowstore.WithRoleResolver(resolver))
	if err != nil {
		t.Fatalf("workflowstore.New: %v", err)
	}
	readModels := newWorkflowServiceReadModels(t, metadataStore, store, resolver, nil, nil)
	automaticStarts, err := workflowexecution.NewAutomaticStartRegistration(workflowexecution.NewAutomaticIntents(), workflowexecution.NewFatalSignal())
	if err != nil {
		t.Fatalf("NewAutomaticStartRegistration: %v", err)
	}
	service, err := New(store, readModels, resolver, workflowexecution.NewMutationPermit(), automaticStarts)
	if err != nil {
		t.Fatalf("workflowsvc.New: %v", err)
	}
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
	definitions, err := workflowview.NewDefinitionProjection(store)
	if err != nil {
		t.Fatalf("workflowview.NewDefinitionProjection: %v", err)
	}
	projector := workflowview.NewTaskProjector()
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})
	board, err := workflowview.NewBoard(metadataStore, definitions, resolver, projector, authority)
	if err != nil {
		t.Fatalf("workflowview.NewBoard: %v", err)
	}
	taskList, err := workflowview.NewTaskList(metadataStore, definitions, projector)
	if err != nil {
		t.Fatalf("workflowview.NewTaskList: %v", err)
	}
	taskDetail, err := workflowview.NewTaskDetail(metadataStore, projector, authority)
	if err != nil {
		t.Fatalf("workflowview.NewTaskDetail: %v", err)
	}
	activity, err := workflowview.NewActivity(metadataStore, definitions, projector)
	if err != nil {
		t.Fatalf("workflowview.NewActivity: %v", err)
	}
	attention, err := workflowview.NewAttention(metadataStore.Queries(), projector, transcripts, prompts)
	if err != nil {
		t.Fatalf("workflowview.NewAttention: %v", err)
	}
	return ReadModels{
		Definitions: definitions,
		Board:       board,
		TaskList:    taskList,
		TaskDetail:  taskDetail,
		Activity:    activity,
		Attention:   attention,
	}
}

func stringPtr(value string) *string {
	return &value
}

func stringPointerEquals(value *string, expected string) bool {
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

func setWorkflowServiceExecutionTargetPolicy(t *testing.T, ctx context.Context, service *Service, workflowID string, policy serverapi.WorkflowExecutionTargetConfiguration) {
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
		Graph: workflowGraphDraftFromDefinition(current.Definition),
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

func workflowServiceMoveApplied(t *testing.T, response serverapi.WorkflowTaskMoveResponse) serverapi.WorkflowTaskMoveApplied {
	t.Helper()
	if err := response.Validate(); err != nil || response.Applied == nil {
		t.Fatalf("MoveWorkflowTask response = %+v, validation error = %v", response, err)
	}
	return *response.Applied
}

func workflowServiceApproveApplied(t *testing.T, response serverapi.WorkflowTaskApproveResponse) serverapi.WorkflowTaskApproveApplied {
	t.Helper()
	if err := response.Validate(); err != nil || response.Applied == nil {
		t.Fatalf("ApproveWorkflowTask response = %+v, validation error = %v", response, err)
	}
	return *response.Applied
}

func claimAndAttachWorkflowServiceRun(t *testing.T, ctx context.Context, service *Service, metadataStore *metadata.Store, binding metadata.Binding, runID string, sessionID string) workflowstore.RunnableRunRecord {
	t.Helper()
	if _, err := metadataStore.DB().ExecContext(ctx, `INSERT INTO sessions (id, project_id, workspace_id, artifact_relpath, name, first_prompt_preview, input_draft, previous_session_id, parent_agent_session_id, created_at_unix_ms, updated_at_unix_ms, last_sequence, model_request_count, launch_visible, cwd_relpath, continuation_json, locked_json, usage_state_json, metadata_json) VALUES (?, ?, ?, ?, '', '', '', NULL, NULL, 1, 1, 0, 0, 1, '.', '{}', '{}', '{}', '{}')`, sessionID, binding.ProjectID, binding.WorkspaceID, "sessions/"+sessionID); err != nil {
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

func createWorkflowServiceWorkflowWithScriptNode(t *testing.T, ctx context.Context, service *Service, nodeID string, scriptPath string) string {
	t.Helper()
	created, err := service.CreateWorkflow(ctx, serverapi.WorkflowCreateRequest{Name: "Script Workflow"})
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
