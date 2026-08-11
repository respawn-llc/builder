package registry

import (
	"context"
	"fmt"
	"strings"
	"time"

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

func (r *RuntimeRegistry) publishTaskQuestionWaitingForScope(scope sessionruntime.ExecutionScope, snapshot PendingPromptSnapshot) error {
	if r == nil || r.workflowEventPublisher == nil {
		return nil
	}
	ref, ok := scope.Workflow()
	if !ok {
		return nil
	}
	resource, ok := scope.Resource()
	if !ok {
		return nil
	}
	projectID := strings.TrimSpace(ref.ProjectID)
	taskID := strings.TrimSpace(string(ref.CurrentNode.TaskID))
	sessionID := resource.SessionID().String()
	askID := strings.TrimSpace(snapshot.Request.ID)
	switch {
	case projectID == "":
		return fmt.Errorf("workflow prompt scope %s has no project id", scope.ID())
	case taskID == "":
		return fmt.Errorf("workflow prompt scope %s has no task id", scope.ID())
	case askID == "":
		return fmt.Errorf("workflow prompt scope %s has a prompt without an id", scope.ID())
	}
	workflowID := ref.WorkflowID
	if err := r.workflowEventPublisher(context.Background(), serverapi.WorkflowProjectEvent{
		ProjectID: &projectID, WorkflowID: &workflowID,
		Resource:        serverapi.WorkflowProjectEventResourceTask,
		Action:          serverapi.WorkflowProjectEventActionQuestionWaiting,
		PrimaryEntityID: taskID, RelatedIDs: []string{sessionID, askID},
		OccurredAtUnixMs: snapshot.CreatedAt.UTC().UnixMilli(),
	}); err != nil {
		return fmt.Errorf("publish workflow question waiting event: %w", err)
	}
	return nil
}

func (r *RuntimeRegistry) publishTaskQuestionCleared(sessionID string, snapshot PendingPromptSnapshot) error {
	if r == nil || r.workflowEventPublisher == nil {
		return nil
	}
	target := snapshot.Request.AttentionTarget
	if target == nil || target.Kind != clientui.AttentionNotificationTargetWorkflowTask {
		return nil
	}
	projectID := strings.TrimSpace(target.ProjectID)
	taskID := strings.TrimSpace(target.TaskID)
	promptID := strings.TrimSpace(snapshot.Request.ID)
	targetSessionID := strings.TrimSpace(target.SessionID)
	switch {
	case projectID == "":
		return fmt.Errorf("workflow prompt %q attention target has no project id", promptID)
	case target.WorkflowID == nil || target.WorkflowID.IsZero():
		return fmt.Errorf("workflow prompt %q attention target has no workflow id", promptID)
	case taskID == "":
		return fmt.Errorf("workflow prompt %q attention target has no task id", promptID)
	case promptID == "":
		return fmt.Errorf("workflow prompt attention target has no prompt id")
	case targetSessionID == "":
		return fmt.Errorf("workflow prompt %q attention target has no session id", promptID)
	case targetSessionID != strings.TrimSpace(sessionID):
		return fmt.Errorf("workflow prompt %q attention target session %q does not match resolved session %q", promptID, targetSessionID, sessionID)
	}
	workflowID := *target.WorkflowID
	if err := r.workflowEventPublisher(context.Background(), serverapi.WorkflowProjectEvent{
		ProjectID: &projectID, WorkflowID: &workflowID,
		Resource:         serverapi.WorkflowProjectEventResourceTask,
		Action:           serverapi.WorkflowProjectEventActionQuestionCleared,
		PrimaryEntityID:  taskID,
		RelatedIDs:       []string{targetSessionID, promptID},
		OccurredAtUnixMs: time.Now().UTC().UnixMilli(),
	}); err != nil {
		return fmt.Errorf("publish workflow question cleared event: %w", err)
	}
	return nil
}
