package workflow_test

import (
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
)

func TestNewNodeRejectsInvalidKind(t *testing.T) {
	_, err := workflow.NewNode(workflow.NodeIdentity{
		WorkflowID:  testsetup.WorkflowID(t, "workflow_test"),
		ID:          "node_test",
		Key:         "test",
		DisplayName: "Test",
	}, workflow.NodeKind("robot"), workflow.NodeFields{})
	if err == nil {
		t.Fatalf("expected invalid node kind to be rejected")
	}
}

func TestNodeWorkflowIDReturnsNilForAbsentNode(t *testing.T) {
	var node workflow.Node
	if workflow.NodeWorkflowID(node) != nil {
		t.Fatal("NodeWorkflowID(nil) must represent absence with nil")
	}
}

func TestNodeWorkflowIDPreservesInvalidZeroIdentity(t *testing.T) {
	node := workflow.StartNode{}
	workflowID := workflow.NodeWorkflowID(node)
	if workflowID == nil || !workflowID.IsZero() {
		t.Fatalf("NodeWorkflowID(zero identity) = %v, want a preserved invalid zero identity", workflowID)
	}
}

func TestNewNodeDropsUnsupportedFieldsForNonExecutableNodes(t *testing.T) {
	fields := workflow.NodeFields{
		SubagentRole:   "coder",
		CompletionMode: "manual",
	}

	for _, tc := range []struct {
		name string
		kind workflow.NodeKind
	}{
		{name: "start", kind: workflow.NodeKindStart},
		{name: "join", kind: workflow.NodeKindJoin},
		{name: "terminal", kind: workflow.NodeKindTerminal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node, err := workflow.NewNode(workflow.NodeIdentity{
				WorkflowID:  testsetup.WorkflowID(t, "workflow_test"),
				ID:          workflow.NodeID("node_" + tc.name),
				Key:         workflow.ModelKey(tc.name),
				DisplayName: tc.name,
			}, tc.kind, fields)
			if err != nil {
				t.Fatalf("expected %s node construction to pass: %v", tc.name, err)
			}
			if workflow.IsExecutableNode(node) {
				t.Fatalf("%s node should not be executable", tc.name)
			}
			if workflow.NodeSubagentRole(node) != "" || workflow.NodeCompletionMode(node) != "" {
				t.Fatalf("%s node retained executable fields", tc.name)
			}
		})
	}
}
