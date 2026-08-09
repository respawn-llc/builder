package main

import (
	"encoding/json"
	"slices"
	"testing"

	"core/shared/serverapi"
)

func TestWorkflowGraphApplyPreviewsInCurrentDefinitionOrder(t *testing.T) {
	current := serverapi.WorkflowDefinition{
		Workflow:   serverapi.WorkflowRecord{ID: workflowGraphApplyID(t), Version: 1},
		NodeGroups: []serverapi.WorkflowNodeGroup{{GroupID: "group-z", GroupKey: "zeta", DisplayName: "Zeta"}, {GroupID: "group-a", GroupKey: "alpha", DisplayName: "Alpha"}},
		Nodes:      []serverapi.WorkflowNode{{ID: "node-z", Key: "zeta", Kind: "terminal", DisplayName: "Zeta"}, {ID: "node-a", Key: "alpha", Kind: "start", DisplayName: "Alpha"}},
		TransitionGroups: []serverapi.WorkflowTransitionGroup{{ID: "transition-z", SourceNodeID: "node-z", TransitionID: "next", DisplayName: "Next"},
			{ID: "transition-a", SourceNodeID: "node-a", TransitionID: "next", DisplayName: "Next"}},
		Edges: []serverapi.WorkflowEdge{
			{ID: "edge-z", TransitionGroupID: "transition-z", Key: "next", TargetNodeID: "node-a", AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session", ContextSource: serverapi.WorkflowContextSource{Kind: "immediate_source"}},
			{ID: "edge-a", TransitionGroupID: "transition-a", Key: "next", TargetNodeID: "node-z", AssigneeSelection: "configured", ThinkingSelection: "configured", ContextMode: "new_session", ContextSource: serverapi.WorkflowContextSource{Kind: "immediate_source"}},
		},
	}
	document, err := workflowGraphDocumentFromDefinition(current)
	if err != nil {
		t.Fatalf("document: %v", err)
	}
	slices.Reverse(document.Graph.NodeGroups)
	slices.Reverse(document.Graph.Nodes)
	slices.Reverse(document.Graph.TransitionGroups)
	slices.Reverse(document.Graph.Edges)
	input, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	remote := &workflowGraphApplyRemote{
		definition:      current,
		previewResponse: graphApplyPreview(1, false, true, false, nil),
	}
	installWorkflowCommandRemote(t, remote)
	exit, outcome, stderr := runWorkflowGraphApplyCommand(t, []string{"graph", "apply", "-", "--json"}, string(input))
	if exit != 0 || outcome.Outcome != workflowGraphApplyUnchanged {
		t.Fatalf("exit = %d, outcome = %+v, stderr = %q", exit, outcome, stderr)
	}
	assertWorkflowGraphEntityIDs(t, remote.previewRequest.Graph.NodeGroups, func(group serverapi.WorkflowGraphDraftNodeGroup) string { return group.ID }, []string{"group-z", "group-a"})
	assertWorkflowGraphEntityIDs(t, remote.previewRequest.Graph.Nodes, func(node serverapi.WorkflowGraphDraftNode) string { return node.ID }, []string{"node-z", "node-a"})
	assertWorkflowGraphEntityIDs(t, remote.previewRequest.Graph.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) string { return group.ID }, []string{"transition-z", "transition-a"})
	assertWorkflowGraphEntityIDs(t, remote.previewRequest.Graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) string { return edge.ID }, []string{"edge-z", "edge-a"})
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
