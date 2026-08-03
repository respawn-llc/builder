package registry

import (
	"context"
	"log/slog"
	"strings"

	"core/server/sessionruntime"
	"core/shared/clientui"
	"core/shared/serverapi"
)

func (r *RuntimeRegistry) WithWorkflowEventPublisher(publisher func(context.Context, serverapi.WorkflowProjectEvent) error) *RuntimeRegistry {
	if r == nil {
		return r
	}
	r.workflowEventPublisher = publisher
	return r
}

func (r *RuntimeRegistry) publishTaskQuestionWaiting(sessionID string, snapshot PendingPromptSnapshot) {
	if r == nil || r.workflowEventPublisher == nil {
		return
	}
	target := snapshot.Request.AttentionTarget
	if target == nil || target.Kind != clientui.AttentionNotificationTargetWorkflowTask {
		return
	}
	projectID := strings.TrimSpace(target.ProjectID)
	taskID := strings.TrimSpace(target.TaskID)
	trimmedSessionID := strings.TrimSpace(sessionID)
	askID := strings.TrimSpace(snapshot.Request.ID)
	if projectID == "" || taskID == "" || trimmedSessionID == "" || askID == "" {
		slog.Warn(
			"skip workflow question waiting event with invalid identifiers",
			"project_id", projectID,
			"task_id", taskID,
			"session_id", trimmedSessionID,
			"ask_id", askID,
		)
		return
	}
	event := serverapi.WorkflowProjectEvent{
		ProjectID:        &projectID,
		WorkflowID:       target.WorkflowID,
		Resource:         serverapi.WorkflowProjectEventResourceTask,
		Action:           serverapi.WorkflowProjectEventActionQuestionWaiting,
		PrimaryEntityID:  taskID,
		RelatedIDs:       []string{trimmedSessionID, askID},
		OccurredAtUnixMs: snapshot.CreatedAt.UTC().UnixMilli(),
	}
	if err := r.workflowEventPublisher(context.Background(), event); err != nil {
		slog.Warn(
			"publish workflow question waiting event failed",
			"project_id", projectID,
			"workflow_id", target.WorkflowID,
			"task_id", target.TaskID,
			"session_id", sessionID,
			"ask_id", snapshot.Request.ID,
			"error", err,
		)
	}
}

func (r *RuntimeRegistry) publishTaskQuestionWaitingForScope(scope sessionruntime.ExecutionScope, snapshot PendingPromptSnapshot) {
	if r == nil || r.workflowEventPublisher == nil {
		return
	}
	ref, ok := scope.Workflow()
	if !ok {
		return
	}
	resource, ok := scope.Resource()
	if !ok {
		return
	}
	projectID := strings.TrimSpace(ref.ProjectID)
	taskID := strings.TrimSpace(string(ref.CurrentNode.TaskID))
	sessionID := resource.SessionID().String()
	askID := strings.TrimSpace(snapshot.Request.ID)
	if projectID == "" || taskID == "" || askID == "" {
		return
	}
	workflowID := ref.WorkflowID
	event := serverapi.WorkflowProjectEvent{
		ProjectID:        &projectID,
		WorkflowID:       &workflowID,
		Resource:         serverapi.WorkflowProjectEventResourceTask,
		Action:           serverapi.WorkflowProjectEventActionQuestionWaiting,
		PrimaryEntityID:  taskID,
		RelatedIDs:       []string{sessionID, askID},
		OccurredAtUnixMs: snapshot.CreatedAt.UTC().UnixMilli(),
	}
	if err := r.workflowEventPublisher(context.Background(), event); err != nil {
		slog.Warn("publish workflow question waiting event failed", "project_id", projectID, "task_id", taskID, "error", err)
	}
}
