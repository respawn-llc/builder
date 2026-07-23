package workflowsvc

import (
	"context"
	"errors"
	"testing"

	"core/server/workflowstore"
	"core/shared/serverapi"
)

func TestWorkflowProjectEventBrokerRetainsBoundAndClosesOnGap(t *testing.T) {
	broker := newWorkflowProjectEventBroker()
	sub, err := broker.subscribe("project-1", "")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()

	for index := 0; index <= workflowProjectEventBufferSize; index++ {
		if err := broker.PublishWorkflowEvent(context.Background(), workflowstore.WorkflowEventRecord{
			ProjectID:        stringPtr("project-1"),
			WorkflowID:       stringPtr("workflow-1"),
			Resource:         serverapi.WorkflowProjectEventResourceTask,
			Action:           serverapi.WorkflowProjectEventActionUpdated,
			PrimaryEntityID:  "task-1",
			OccurredAtUnixMs: int64(index + 1),
		}); err != nil {
			t.Fatalf("publish %d: %v", index, err)
		}
	}

	for index := 0; index < workflowProjectEventBufferSize; index++ {
		if _, err := sub.Next(context.Background()); err != nil {
			t.Fatalf("Next buffered event %d: %v", index, err)
		}
	}
	if _, err := sub.Next(context.Background()); !errors.Is(err, serverapi.ErrStreamGap) {
		t.Fatalf("Next overflow error = %v, want stream gap", err)
	}
}

func TestWorkflowProjectEventBrokerCopiesRelatedIDs(t *testing.T) {
	broker := newWorkflowProjectEventBroker()
	sub, err := broker.subscribe("project-1", "")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = sub.Close() }()

	relatedIDs := []string{"run-1"}
	if err := broker.PublishWorkflowEvent(context.Background(), workflowstore.WorkflowEventRecord{
		ProjectID:        stringPtr("project-1"),
		WorkflowID:       stringPtr("workflow-1"),
		Resource:         serverapi.WorkflowProjectEventResourceTask,
		Action:           serverapi.WorkflowProjectEventActionStarted,
		PrimaryEntityID:  "task-1",
		RelatedIDs:       relatedIDs,
		OccurredAtUnixMs: 1,
	}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	relatedIDs[0] = "mutated"

	event, err := sub.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(event.RelatedIDs) != 1 || event.RelatedIDs[0] != "run-1" {
		t.Fatalf("related ids = %+v, want defensive copy", event.RelatedIDs)
	}
}
