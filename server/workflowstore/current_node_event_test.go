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

func TestCompleteCurrentNodePublishesCompletionEventAfterCommittedMutation(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	publisher := &recordingCurrentNodeEventPublisher{}
	store.SetWorkflowEventPublisher(publisher)

	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "completed"},
	}); err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if len(publisher.events) != 1 ||
		publisher.events[0].PrimaryEntityID != string(task.ID) ||
		publisher.events[0].Action != serverapi.WorkflowProjectEventActionCompleted {
		t.Fatalf("completion events = %+v, want one completed task event", publisher.events)
	}
}

func TestCompleteCurrentNodeReturnsCommittedResultWithPublicationDiagnostic(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	publicationErr := errors.New("wake unavailable")
	store.SetWorkflowEventPublisher(&recordingCurrentNodeEventPublisher{err: publicationErr})

	outcome, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "completed"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if !outcome.CommitReceipt.Committed {
		t.Fatal("completion outcome did not report its committed receipt")
	}
	if !errors.Is(outcome.PostCommitDiagnostic, publicationErr) {
		t.Fatalf("post-commit diagnostic = %v, want %v", outcome.PostCommitDiagnostic, publicationErr)
	}
	if len(outcome.Mutation.Removed) != 1 || !outcome.Mutation.Removed[0].Equal(source.Reference) {
		t.Fatalf("committed completion result = %+v, want removed source", outcome.CurrentNodeCompletionResult)
	}
}
