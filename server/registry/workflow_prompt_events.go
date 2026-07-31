package registry

import (
	"context"
	"log/slog"
	"strings"

	"core/shared/clientui"
	"core/shared/serverapi"
)

type WorkflowEventPublisher interface {
	PublishWorkflowEvent(context.Context, serverapi.WorkflowProjectEvent) error
}

func (r *RuntimeRegistry) WithWorkflowEventPublisher(publisher WorkflowEventPublisher) *RuntimeRegistry {
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
	workflowID := strings.TrimSpace(target.WorkflowID)
	event := serverapi.WorkflowProjectEvent{
		ProjectID:        &projectID,
		WorkflowID:       &workflowID,
		Resource:         serverapi.WorkflowProjectEventResourceTask,
		Action:           serverapi.WorkflowProjectEventActionQuestionWaiting,
		PrimaryEntityID:  strings.TrimSpace(target.TaskID),
		RelatedIDs:       []string{strings.TrimSpace(sessionID), strings.TrimSpace(snapshot.Request.ID)},
		OccurredAtUnixMs: snapshot.CreatedAt.UTC().UnixMilli(),
	}
	if err := r.workflowEventPublisher.PublishWorkflowEvent(context.Background(), event); err != nil {
		slog.Warn(
			"publish workflow question waiting event failed",
			"project_id", projectID,
			"workflow_id", workflowID,
			"task_id", target.TaskID,
			"session_id", sessionID,
			"ask_id", snapshot.Request.ID,
			"error", err,
		)
	}
}
