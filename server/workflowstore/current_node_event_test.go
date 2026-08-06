package workflowstore

import (
	"context"
	"errors"
	"testing"

	"core/server/workflow"
	"core/shared/serverapi"
)

type recordingCurrentNodeEventPublisher struct {
	events []WorkflowEventRecord
	err    error
}

func (p *recordingCurrentNodeEventPublisher) PublishWorkflowEvent(_ context.Context, event WorkflowEventRecord) error {
	p.events = append(p.events, event)
	return p.err
}

func TestCurrentNodeTaskEventPublicationUsesCommittedTaskIdentity(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	publisher := &recordingCurrentNodeEventPublisher{}
	store.SetWorkflowEventPublisher(publisher)

	if err := store.publishCurrentNodeTaskEvent(ctx, workflow.TaskID(task.ID), serverapi.WorkflowProjectEventActionCompleted); err != nil {
		t.Fatalf("publishCurrentNodeTaskEvent: %v", err)
	}
	if len(publisher.events) != 1 || publisher.events[0].PrimaryEntityID != string(task.ID) ||
		publisher.events[0].Resource != serverapi.WorkflowProjectEventResourceTask ||
		publisher.events[0].Action != serverapi.WorkflowProjectEventActionCompleted {
		t.Fatalf("published events = %+v", publisher.events)
	}
}

func TestCurrentNodeTaskEventPublicationErrorIsReturnedAfterCommit(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	publisher := &recordingCurrentNodeEventPublisher{err: errors.New("wake unavailable")}
	store.SetWorkflowEventPublisher(publisher)

	err := store.publishCurrentNodeTaskEvent(ctx, workflow.TaskID(task.ID), serverapi.WorkflowProjectEventActionCompleted)
	if err == nil {
		t.Fatal("publication error was swallowed")
	}
}
