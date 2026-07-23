package workflowstore

import (
	"database/sql"
	"errors"
	"testing"

	"core/server/workflow"
)

func TestCompleteCurrentNodeAtomicallyReplacesAgentAndReturnsSuccessorIntent(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	started := startTask(t, ctx, store, task.ID)
	if len(started.Mutation.Created) != 1 {
		t.Fatalf("StartTask mutation = %+v, want one source current node", started.Mutation)
	}
	source := started.Mutation.Created[0]
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	targetNode := nodeByKey(t, definition, "review")
	target, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeIDOf(targetNode), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference target: %v", err)
	}

	completed, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "plan complete"},
	})
	if err != nil {
		t.Fatalf("CompleteCurrentNode: %v", err)
	}
	if len(completed.Mutation.Removed) != 1 || !completed.Mutation.Removed[0].Equal(source.Reference) {
		t.Fatalf("completion removed = %+v, want source current node", completed.Mutation.Removed)
	}
	if len(completed.Mutation.Created) != 1 ||
		!completed.Mutation.Created[0].Reference.Equal(target) ||
		completed.Mutation.Created[0].Scheduling == nil ||
		completed.Mutation.Created[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("completion created = %+v, want ready review current node", completed.Mutation.Created)
	}
	if completed.Handoff != (CompletionHandoff{SourceNodeDisplayName: "Plan", DestinationDisplayName: "Review"}) {
		t.Fatalf("completion handoff = %+v, want Plan -> Review", completed.Handoff)
	}
	if len(completed.AutomaticIntents) != 1 || !completed.AutomaticIntents[0].Equal(target) {
		t.Fatalf("completion automatic intents = %+v, want review current node", completed.AutomaticIntents)
	}

	currentNodes, err := store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after completion: %v", err)
	}
	if len(currentNodes) != 1 ||
		!currentNodes[0].Reference.Equal(target) ||
		currentNodes[0].Scheduling == nil ||
		currentNodes[0].Scheduling.State != workflow.CurrentNodeSchedulingReady {
		t.Fatalf("current nodes after completion = %+v, want only ready review", currentNodes)
	}

	if _, err := store.CompleteCurrentNode(ctx, CurrentNodeCompletionRequest{
		Source:       source.Reference,
		TransitionID: "review",
		OutputValues: map[string]string{"summary": "stale"},
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale completion error = %v, want sql.ErrNoRows", err)
	}
	currentNodes, err = store.ListCurrentNodes(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListCurrentNodes after stale completion: %v", err)
	}
	if len(currentNodes) != 1 || !currentNodes[0].Reference.Equal(target) {
		t.Fatalf("current nodes after stale completion = %+v, want unchanged review", currentNodes)
	}
}
