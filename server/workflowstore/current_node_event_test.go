package workflowstore

import (
	"context"
	"testing"
	"time"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type recordingWorkflowEventPublisher struct {
	event serverapi.WorkflowProjectEvent
}

func (p *recordingWorkflowEventPublisher) PublishWorkflowEvent(_ context.Context, event WorkflowEventRecord) error {
	p.event = event
	return nil
}

func TestCurrentNodeCompletionTaskEventUsesCommittedTaskIdentity(t *testing.T) {
	publisher := &recordingWorkflowEventPublisher{}
	store := &Store{now: time.Now}
	store.SetWorkflowEventPublisher(publisher)
	workflowID := runtimeids.NewWorkflowID()
	store.publishCurrentNodeTaskEvent(context.Background(), "project-1", workflowID, "task-1", serverapi.WorkflowProjectEventActionCompleted)
	if publisher.event.Resource != serverapi.WorkflowProjectEventResourceTask ||
		publisher.event.Action != serverapi.WorkflowProjectEventActionCompleted ||
		publisher.event.PrimaryEntityID != "task-1" ||
		publisher.event.ProjectID == nil || *publisher.event.ProjectID != "project-1" ||
		publisher.event.WorkflowID == nil || *publisher.event.WorkflowID != workflowID {
		t.Fatalf("event = %+v", publisher.event)
	}
}
