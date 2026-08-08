package workflow_test

import (
	"testing"

	"core/server/workflow"
)

func TestSerialTransitionRequiresFanoutSiblings(t *testing.T) {
	def := fanoutWorkflow(t)
	def.Nodes = append(def.Nodes,
		testAgentNode(def.ID, "node_impl_a_detail", "impl_a_detail", "Implement A Detail", workflow.NodeFields{SubagentRole: "coder"}),
		testAgentNode(def.ID, "node_impl_b_detail", "impl_b_detail", "Implement B Detail", workflow.NodeFields{SubagentRole: "coder"}),
	)
	def.TransitionGroups = append(def.TransitionGroups,
		workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_impl_a_detail", SourceNodeID: "node_impl_a", TransitionID: "detail", DisplayName: "Detail"},
		workflow.TransitionGroup{WorkflowID: def.ID, ID: "group_impl_b_detail", SourceNodeID: "node_impl_b", TransitionID: "detail", DisplayName: "Detail"},
	)
	for index := range def.TransitionGroups {
		switch def.TransitionGroups[index].ID {
		case "group_impl_a_join":
			def.TransitionGroups[index].SourceNodeID = "node_impl_a_detail"
		case "group_impl_b_join":
			def.TransitionGroups[index].SourceNodeID = "node_impl_b_detail"
		}
	}
	def.Edges = append(def.Edges,
		workflow.Edge{WorkflowID: def.ID, ID: "edge_impl_a_detail", Key: "detail_a", TransitionGroupID: "group_impl_a_detail", TargetNodeID: "node_impl_a_detail", ContextMode: workflow.ContextModeNewSession},
		workflow.Edge{WorkflowID: def.ID, ID: "edge_impl_b_detail", Key: "detail_b", TransitionGroupID: "group_impl_b_detail", TargetNodeID: "node_impl_b_detail", ContextMode: workflow.ContextModeNewSession},
	)

	tests := []struct {
		name     string
		groupID  workflow.TransitionGroupID
		requires bool
	}{
		{name: "normal serial transition", groupID: "group_start", requires: false},
		{name: "fanout transition", groupID: "group_split", requires: false},
		{name: "serial transition in branch A", groupID: "group_impl_a_detail", requires: true},
		{name: "serial transition in branch B", groupID: "group_impl_b_detail", requires: true},
		{name: "transition after join", groupID: "group_join_done", requires: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workflow.SerialTransitionRequiresFanoutSiblings(def, tt.groupID); got != tt.requires {
				t.Fatalf("SerialTransitionRequiresFanoutSiblings(%q) = %t, want %t", tt.groupID, got, tt.requires)
			}
		})
	}
}
