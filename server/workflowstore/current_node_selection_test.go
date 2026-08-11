package workflowstore

import (
	"testing"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

func TestCurrentNodeSelectionRoundTripsForAgentAndRejectsNonAgentAssignment(t *testing.T) {
	ctx, store, binding := newTestStoreContext(t)
	workflowID := createMaterializedCurrentNodeWorkflow(t, ctx, store)
	linkWorkflow(t, ctx, store, binding.ProjectID, workflowID, true)
	task := createDefaultTask(t, ctx, store, binding.ProjectID)
	definition, _, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	agent := nodeByKind(t, definition, workflow.NodeKindAgent)
	terminal := nodeByKind(t, definition, workflow.NodeKindTerminal)

	agentReference, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeIDOf(agent), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference agent: %v", err)
	}
	selection, err := workflow.NewAgentExecutionSelection("reviewer", nil, workflow.AssigneeOriginTransitionSelected)
	if err != nil {
		t.Fatalf("NewAgentExecutionSelection: %v", err)
	}
	agentCurrentNode, err := workflow.NewCurrentNodeWithExecutionSelection(
		agentReference,
		nil,
		workflow.MaterializedPriorValues{},
		nil,
		&workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady},
		&selection,
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeWithExecutionSelection: %v", err)
	}
	if _, err := store.queries.DeleteSerialTaskCurrentNode(ctx, sqlitegen.DeleteSerialTaskCurrentNodeParams{
		TaskID: string(task.ID),
		NodeID: string(workflow.NodeIDOf(nodeByKind(t, definition, workflow.NodeKindStart))),
	}); err != nil {
		t.Fatalf("delete initial current node: %v", err)
	}
	withoutSelection, err := workflow.NewCurrentNode(agentReference, nil, &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady})
	if err != nil {
		t.Fatalf("NewCurrentNode without selection: %v", err)
	}
	if err := insertTaskCurrentNode(ctx, store.queries, withoutSelection, time.Now().UTC()); err == nil {
		t.Fatal("insert Agent current node without selection succeeded")
	}
	if err := insertTaskCurrentNode(ctx, store.queries, agentCurrentNode, time.Now().UTC()); err != nil {
		t.Fatalf("insert agent current node: %v", err)
	}
	reloaded, err := currentNodeForReference(ctx, store.queries, agentReference)
	if err != nil {
		t.Fatalf("currentNodeForReference agent: %v", err)
	}
	if reloaded.AgentExecutionSelection == nil || reloaded.AgentExecutionSelection.Assignee != "reviewer" {
		t.Fatalf("reloaded agent selection = %#v, want reviewer", reloaded.AgentExecutionSelection)
	}
	if _, err := store.queries.DeleteSerialTaskCurrentNode(ctx, sqlitegen.DeleteSerialTaskCurrentNodeParams{
		TaskID: string(task.ID),
		NodeID: string(workflow.NodeIDOf(agent)),
	}); err != nil {
		t.Fatalf("delete agent current node: %v", err)
	}

	terminalReference, err := workflow.NewCurrentNodeReference(task.ID, workflow.NodeIDOf(terminal), nil)
	if err != nil {
		t.Fatalf("NewCurrentNodeReference terminal: %v", err)
	}
	terminalCurrentNode, err := workflow.NewCurrentNodeWithExecutionSelection(
		terminalReference,
		nil,
		workflow.MaterializedPriorValues{},
		nil,
		nil,
		&selection,
	)
	if err != nil {
		t.Fatalf("NewCurrentNodeWithExecutionSelection terminal: %v", err)
	}
	if err := insertTaskCurrentNode(ctx, store.queries, terminalCurrentNode, time.Now().UTC()); err == nil {
		t.Fatal("insert terminal current node with assignment succeeded")
	}
}
