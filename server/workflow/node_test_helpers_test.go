package workflow_test

import (
	"testing"

	"core/server/workflow"
)

func TestNewNodeRejectsInvalidKind(t *testing.T) {
	_, err := workflow.NewNode(workflow.NodeIdentity{
		WorkflowID:  testWorkflowID("workflow_test"),
		ID:          "node_test",
		Key:         "test",
		DisplayName: "Test",
	}, workflow.NodeKind("robot"), workflow.NodeFields{})
	if err == nil {
		t.Fatalf("expected invalid node kind to be rejected")
	}
}

func TestNewNodeDropsUnsupportedFieldsForNonExecutableNodes(t *testing.T) {
	fields := workflow.NodeFields{
		SubagentRole:   "coder",
		PromptTemplate: "Do work.",
		CompletionMode: "manual",
		InputFields:    []workflow.InputField{{Name: "input", Description: "Input."}},
		OutputFields:   []workflow.OutputField{{Name: "summary", Description: "Summary."}},
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
				WorkflowID:  testWorkflowID("workflow_test"),
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
			if workflow.NodeSubagentRole(node) != "" || workflow.NodePromptTemplate(node) != "" || workflow.NodeCompletionMode(node) != "" {
				t.Fatalf("%s node retained executable fields", tc.name)
			}
			if len(workflow.NodeInputFields(node)) != 0 || len(workflow.NodeOutputFields(node)) != 0 {
				t.Fatalf("%s node retained contract fields", tc.name)
			}
		})
	}
}
