package main

import (
	"slices"
	"testing"

	"core/shared/serverapi"
)

func TestWorkflowGraphOrderCanonicalizesInspectAndNormalizesApply(t *testing.T) {
	current := serverapi.WorkflowDefinition{
		NodeGroups: []serverapi.WorkflowNodeGroup{{GroupID: "group-z", GroupKey: "zeta"}, {GroupID: "group-a", GroupKey: "alpha"}},
		Nodes:      []serverapi.WorkflowNode{{ID: "node-z", Key: "zeta"}, {ID: "node-a", Key: "alpha"}},
		TransitionGroups: []serverapi.WorkflowTransitionGroup{{ID: "transition-z", SourceNodeID: "node-z", TransitionID: "next"},
			{ID: "transition-a", SourceNodeID: "node-a", TransitionID: "next"}},
		Edges: []serverapi.WorkflowEdge{{ID: "edge-z", TransitionGroupID: "transition-z", Key: "next"},
			{ID: "edge-a", TransitionGroupID: "transition-a", Key: "next"}},
	}
	canonical, err := canonicalWorkflowGraphDraftFromDefinition(current)
	if err != nil {
		t.Fatalf("canonical graph: %v", err)
	}
	assertWorkflowGraphEntityIDs(t, canonical.NodeGroups, func(group serverapi.WorkflowGraphDraftNodeGroup) string { return group.ID }, []string{"group-a", "group-z"})
	assertWorkflowGraphEntityIDs(t, canonical.Nodes, func(node serverapi.WorkflowGraphDraftNode) string { return node.ID }, []string{"node-a", "node-z"})
	assertWorkflowGraphEntityIDs(t, canonical.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) string { return group.ID }, []string{"transition-a", "transition-z"})
	assertWorkflowGraphEntityIDs(t, canonical.Edges, func(edge serverapi.WorkflowGraphDraftEdge) string { return edge.ID }, []string{"edge-a", "edge-z"})

	submitted := workflowGraphDraftFromDefinition(current)
	slices.Reverse(submitted.NodeGroups)
	slices.Reverse(submitted.Nodes)
	slices.Reverse(submitted.TransitionGroups)
	slices.Reverse(submitted.Edges)
	normalized, err := normalizeWorkflowGraphDraftOrder(current, submitted)
	if err != nil {
		t.Fatalf("normalize graph: %v", err)
	}
	assertWorkflowGraphEntityIDs(t, normalized.NodeGroups, func(group serverapi.WorkflowGraphDraftNodeGroup) string { return group.ID }, []string{"group-z", "group-a"})
	assertWorkflowGraphEntityIDs(t, normalized.Nodes, func(node serverapi.WorkflowGraphDraftNode) string { return node.ID }, []string{"node-z", "node-a"})
	assertWorkflowGraphEntityIDs(t, normalized.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) string { return group.ID }, []string{"transition-z", "transition-a"})
	assertWorkflowGraphEntityIDs(t, normalized.Edges, func(edge serverapi.WorkflowGraphDraftEdge) string { return edge.ID }, []string{"edge-z", "edge-a"})
}

func assertWorkflowGraphEntityIDs[T any](t *testing.T, entities []T, id func(T) string, want []string) {
	t.Helper()
	got := make([]string, 0, len(entities))
	for _, entity := range entities {
		got = append(got, id(entity))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("entity IDs = %v, want %v", got, want)
	}
}
