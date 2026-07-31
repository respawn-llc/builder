package registry

import (
	"context"
	"log/slog"
	"strings"

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
	event := serverapi.WorkflowProjectEvent{
		ProjectID:        &projectID,
		WorkflowID:       target.WorkflowID,
		Resource:         serverapi.WorkflowProjectEventResourceTask,
		Action:           serverapi.WorkflowProjectEventActionQuestionWaiting,
		PrimaryEntityID:  strings.TrimSpace(target.TaskID),
		RelatedIDs:       []string{strings.TrimSpace(sessionID), strings.TrimSpace(snapshot.Request.ID)},
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
