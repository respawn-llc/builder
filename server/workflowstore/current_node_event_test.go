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

func TestCompleteCurrentNodeReturnsCommittedMutationWithWakeError(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	store.SetWorkflowEventPublisher(&recordingCurrentNodeEventPublisher{err: errors.New("wake unavailable")})

	result, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "completed"},
	})
	if err == nil {
		t.Fatal("completion succeeded without surfacing wake failure")
	}
	if len(result.Mutation.Removed) != 1 || len(result.Mutation.Created) != 1 {
		t.Fatalf("committed completion mutation = %+v", result.Mutation)
	}
	current, listErr := store.ListCurrentNodes(ctx, task.ID)
	if listErr != nil || len(current) != 1 || !current[0].Reference.Equal(result.Mutation.Created[0].Reference) {
		t.Fatalf("current nodes after committed completion = %+v, err=%v", current, listErr)
	}
}

func TestInterruptCurrentNodesPublishesOneTaskWakeAfterAggregateCommit(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createFanoutJoinWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	source := startTask(t, ctx, store, task.ID).Mutation.Created[0]
	publisher := &recordingCurrentNodeEventPublisher{}
	store.SetWorkflowEventPublisher(publisher)
	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		OutputValues: map[string]string{"summary": "fanout complete"},
	}); err != nil {
		t.Fatalf("fanout completion: %v", err)
	}
	publisher.events = nil
	nodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("list fanout nodes: %v", err)
	}
	references := make([]workflow.CurrentNodeReference, 0, len(nodes))
	for _, node := range nodes {
		references = append(references, node.Reference)
	}
	_, err = store.InterruptCurrentNodes(
		ctx,
		references,
		workflow.CurrentNodeInterruptionReasonUserInterrupt,
		workflow.NewCurrentNodeInterruptionDetail("user_interrupt", nil),
	)
	if err != nil {
		t.Fatalf("interrupt current nodes: %v", err)
	}
	if len(publisher.events) != 1 || publisher.events[0].PrimaryEntityID != string(task.ID) {
		t.Fatalf("aggregate wake events = %+v, want exactly one Task event", publisher.events)
	}
}
