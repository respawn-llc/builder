package workflowstore

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"core/server/workflow"
	"core/shared/serverapi"
)

type recordingCurrentNodeEventPublisher struct {
	events []WorkflowEventRecord
	err    error
}

type countingCurrentNodeDiagnosticHandler struct {
	count atomic.Int64
}

func (*countingCurrentNodeDiagnosticHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *countingCurrentNodeDiagnosticHandler) Handle(context.Context, slog.Record) error {
	h.count.Add(1)
	return nil
}

func (h *countingCurrentNodeDiagnosticHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *countingCurrentNodeDiagnosticHandler) WithGroup(string) slog.Handler {
	return h
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

func TestCompleteCurrentNodePreservesCommittedResultWhenCompletionEventDeliveryFails(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	deliveryErr := errors.New("completion event delivery unavailable")
	publisher := &recordingCurrentNodeEventPublisher{err: deliveryErr}
	store.SetWorkflowEventPublisher(publisher)
	diagnostics := &countingCurrentNodeDiagnosticHandler{}
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(diagnostics))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	result, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "completed"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode returned post-commit delivery error: %v", err)
	}
	if len(result.Mutation.Removed) != 1 ||
		!result.Mutation.Removed[0].Equal(source.Reference) ||
		len(result.Mutation.Created) != 1 ||
		len(result.AutomaticIntents) != 1 ||
		!result.AutomaticIntents[0].CurrentNode.Equal(result.Mutation.Created[0].Reference) {
		t.Fatalf("committed completion result = %+v, want exact replacement and Automatic Intent", result)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("completion event attempts = %d, want one", len(publisher.events))
	}
	if diagnostics.count.Load() != 1 {
		t.Fatalf("recorded completion diagnostics = %d, want one", diagnostics.count.Load())
	}
}
