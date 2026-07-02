package workflow_test

import (
	"testing"

	"core/server/workflow"
)

func TestNewNodeRejectsInvalidKind(t *testing.T) {
	_, err := workflow.NewNode(workflow.NodeIdentity{
		WorkflowID:  "workflow_test",
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
				WorkflowID:  "workflow_test",
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

func testNode(workflowID workflow.WorkflowID, id workflow.NodeID, key workflow.ModelKey, displayName string, kind workflow.NodeKind, fields workflow.NodeFields) workflow.Node {
	node, err := workflow.NewNode(workflow.NodeIdentity{
		WorkflowID:  workflowID,
		ID:          id,
		Key:         key,
		DisplayName: displayName,
	}, kind, fields)
	if err != nil {
		panic(err)
	}
	return node
}

func testStartNode(workflowID workflow.WorkflowID, id workflow.NodeID, key workflow.ModelKey, displayName string) workflow.Node {
	return testNode(workflowID, id, key, displayName, workflow.NodeKindStart, workflow.NodeFields{})
}

func testAgentNode(workflowID workflow.WorkflowID, id workflow.NodeID, key workflow.ModelKey, displayName string, fields workflow.NodeFields) workflow.Node {
	return testNode(workflowID, id, key, displayName, workflow.NodeKindAgent, fields)
}

func testJoinNode(workflowID workflow.WorkflowID, id workflow.NodeID, key workflow.ModelKey, displayName string) workflow.Node {
	return testNode(workflowID, id, key, displayName, workflow.NodeKindJoin, workflow.NodeFields{})
}

func testTerminalNode(workflowID workflow.WorkflowID, id workflow.NodeID, key workflow.ModelKey, displayName string) workflow.Node {
	return testNode(workflowID, id, key, displayName, workflow.NodeKindTerminal, workflow.NodeFields{})
}

func testNodeFields(node workflow.Node) workflow.NodeFields {
	return workflow.NodeFields{
		SubagentRole:       workflow.NodeSubagentRole(node),
		PromptTemplate:     workflow.NodePromptTemplate(node),
		CompletionMode:     workflow.NodeCompletionMode(node),
		InputFields:        workflow.NodeInputFields(node),
		JoinInputProviders: workflow.NodeJoinInputProviders(node),
		OutputFields:       workflow.NodeOutputFields(node),
		ScriptPath:         workflow.NodeScriptPath(node),
	}
}

func updateNode(node workflow.Node, edit func(*workflow.NodeIdentity, *workflow.NodeKind, *workflow.NodeFields)) workflow.Node {
	identity := node.Identity()
	kind := node.Kind()
	fields := testNodeFields(node)
	edit(&identity, &kind, &fields)
	updated, err := workflow.NewNode(identity, kind, fields)
	if err != nil {
		panic(err)
	}
	return updated
}

func updateNodeAt(def *workflow.Definition, index int, edit func(*workflow.NodeIdentity, *workflow.NodeKind, *workflow.NodeFields)) {
	def.Nodes[index] = updateNode(def.Nodes[index], edit)
}
