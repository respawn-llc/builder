package workflowview

import (
	"context"
	"testing"

	"core/server/workflow"
	"core/server/workflowstore"
)

func TestDefinitionProjectionLoadsCanonicalDefinitionSnapshot(t *testing.T) {
	ctx, _, store, _ := newWorkflowViewTestContextStore(t)
	created, err := store.CreateWorkflow(ctx, workflowstore.CreateWorkflowRequest{
		Name:        "Canonical workflow",
		Description: "Projected from the workflow store.",
	})
	if err != nil {
		t.Fatalf("CreateWorkflow: %v", err)
	}
	definition, _, err := store.GetDefinition(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	start := workflowViewNodeByKind(t, definition, workflow.NodeKindStart)
	done := workflowViewNodeByKind(t, definition, workflow.NodeKindTerminal)
	group, _, err := store.AddNodeGroup(ctx, workflowstore.NodeGroupRecord{
		ID:          "group-implementation",
		WorkflowID:  created.ID,
		Key:         "implementation",
		DisplayName: "Implementation",
		SortOrder:   240,
	})
	if err != nil {
		t.Fatalf("AddNodeGroup: %v", err)
	}
	agentID := workflow.NodeID("node-agent")
	if _, err := store.AddNode(ctx, workflowstore.NodeRecord{
		ID:             agentID,
		WorkflowID:     created.ID,
		Key:            "agent",
		Kind:           workflow.NodeKindAgent,
		DisplayName:    "Agent",
		GroupKey:       string(group.Key),
		SubagentRole:   "coder",
		PromptTemplate: "Do the work.",
		InputFields:    []workflow.InputField{{Name: "brief", Description: "The work brief."}},
		OutputFields:   []workflow.OutputField{{Name: "summary", Description: "The result summary."}},
	}); err != nil {
		t.Fatalf("AddNode: %v", err)
	}
	startGroupID := workflow.TransitionGroupID("transition-start")
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{
		ID:           startGroupID,
		WorkflowID:   created.ID,
		SourceNodeID: workflow.NodeIDOf(start),
		TransitionID: "start",
		DisplayName:  "Start",
		Description:  "Begin implementation.",
	}); err != nil {
		t.Fatalf("AddTransitionGroup start: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{
		ID:                "edge-start",
		WorkflowID:        created.ID,
		TransitionGroupID: startGroupID,
		Key:               "start",
		TargetNodeID:      agentID,
		ContextMode:       workflow.ContextModeNewSession,
		PromptTemplate:    "Implement {{ .Inputs.brief }}.",
	}); err != nil {
		t.Fatalf("AddEdge start: %v", err)
	}
	doneGroupID := workflow.TransitionGroupID("transition-done")
	if _, err := store.AddTransitionGroup(ctx, workflowstore.TransitionGroupRecord{
		ID:           doneGroupID,
		WorkflowID:   created.ID,
		SourceNodeID: agentID,
		TransitionID: "done",
		DisplayName:  "Done",
	}); err != nil {
		t.Fatalf("AddTransitionGroup done: %v", err)
	}
	if _, err := store.AddEdge(ctx, workflowstore.EdgeRecord{
		ID:                "edge-done",
		WorkflowID:        created.ID,
		TransitionGroupID: doneGroupID,
		Key:               "done",
		TargetNodeID:      workflow.NodeIDOf(done),
		ContextMode:       workflow.ContextModeNewSession,
	}); err != nil {
		t.Fatalf("AddEdge done: %v", err)
	}

	projection, err := NewDefinitionProjection(store)
	if err != nil {
		t.Fatalf("NewDefinitionProjection: %v", err)
	}
	apiDefinition, nodeKinds, err := projection.GetDefinition(context.Background(), string(created.ID))
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}

	if apiDefinition.Workflow.ID != string(created.ID) ||
		apiDefinition.Workflow.Name != "Canonical workflow" ||
		apiDefinition.Workflow.Description != "Projected from the workflow store." ||
		apiDefinition.Workflow.Version != 7 {
		t.Fatalf("workflow metadata = %+v", apiDefinition.Workflow)
	}
	if len(apiDefinition.NodeGroups) != 1 ||
		apiDefinition.NodeGroups[0].GroupID != group.ID ||
		apiDefinition.NodeGroups[0].SortOrder != 240 {
		t.Fatalf("node groups = %+v", apiDefinition.NodeGroups)
	}
	if len(apiDefinition.Nodes) != 3 || len(apiDefinition.TransitionGroups) != 2 || len(apiDefinition.Edges) != 2 {
		t.Fatalf("graph = nodes:%+v transition_groups:%+v edges:%+v", apiDefinition.Nodes, apiDefinition.TransitionGroups, apiDefinition.Edges)
	}
	if nodeKinds[string(agentID)] != workflow.NodeKindAgent ||
		nodeKinds[string(workflow.NodeIDOf(start))] != workflow.NodeKindStart ||
		nodeKinds[string(workflow.NodeIDOf(done))] != workflow.NodeKindTerminal {
		t.Fatalf("node kinds = %+v", nodeKinds)
	}
	if len(apiDefinition.DerivedWiring.Nodes) != len(apiDefinition.Nodes) ||
		len(apiDefinition.DerivedWiring.TransitionGroups) != len(apiDefinition.TransitionGroups) ||
		len(apiDefinition.DerivedWiring.Edges) != len(apiDefinition.Edges) {
		t.Fatalf("derived wiring = %+v", apiDefinition.DerivedWiring)
	}
}
