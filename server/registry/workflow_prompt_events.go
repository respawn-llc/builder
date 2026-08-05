package registry

import (
	"context"
	"log/slog"
	"strings"

	"core/server/sessionruntime"
	"core/shared/serverapi"
)

func (r *RuntimeRegistry) WithWorkflowEventPublisher(publisher func(context.Context, serverapi.WorkflowProjectEvent) error) *RuntimeRegistry {
	if r == nil {
		return r
	}
	r.workflowEventPublisher = publisher
	return r
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
	if err := r.workflowEventPublisher(context.Background(), serverapi.WorkflowProjectEvent{
		ProjectID: &projectID, WorkflowID: &workflowID,
		Resource:        serverapi.WorkflowProjectEventResourceTask,
		Action:          serverapi.WorkflowProjectEventActionQuestionWaiting,
		PrimaryEntityID: taskID, RelatedIDs: []string{sessionID, askID},
		OccurredAtUnixMs: snapshot.CreatedAt.UTC().UnixMilli(),
	}); err != nil {
		slog.Warn("publish workflow question waiting event failed", "project_id", projectID, "task_id", taskID, "session_id", sessionID, "ask_id", askID, "error", err)
	}
}
