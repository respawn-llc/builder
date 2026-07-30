package workflow_test

import (
	"testing"

	"core/internal/testharness/testsetup"
	"core/server/workflow"
)

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

func joinParameterWorkflow(t *testing.T) workflow.Definition {
	return workflow.Definition{
		ID:          testsetup.WorkflowID(t, "workflow_join_parameters"),
		DisplayName: "Join Parameter Workflow",
		Nodes: []workflow.Node{
			testStartNode(testsetup.WorkflowID(t, "workflow_join_parameters"), "node_start", "backlog", "Backlog"),
			testAgentNode(testsetup.WorkflowID(t, "workflow_join_parameters"), "node_split", "split", "Split", workflow.NodeFields{SubagentRole: "coder"}),
			testAgentNode(testsetup.WorkflowID(t, "workflow_join_parameters"), "node_branch_a", "branch_a", "Branch A", workflow.NodeFields{SubagentRole: "coder"}),
			testAgentNode(testsetup.WorkflowID(t, "workflow_join_parameters"), "node_branch_b", "branch_b", "Branch B", workflow.NodeFields{SubagentRole: "coder"}),
			testJoinNode(testsetup.WorkflowID(t, "workflow_join_parameters"), "node_join", "join", "Join"),
			testAgentNode(testsetup.WorkflowID(t, "workflow_join_parameters"), "node_consume", "consume", "Consume", workflow.NodeFields{SubagentRole: "coder"}),
			testTerminalNode(testsetup.WorkflowID(t, "workflow_join_parameters"), "node_done", "done", "Done"),
		},
		TransitionGroups: []workflow.TransitionGroup{
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "group_start", SourceNodeID: "node_start", TransitionID: "start", DisplayName: "Start"},
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "group_split", SourceNodeID: "node_split", TransitionID: "split", DisplayName: "Split"},
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "group_branch_a_join", SourceNodeID: "node_branch_a", TransitionID: "join_a", DisplayName: "Join A"},
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "group_branch_b_join", SourceNodeID: "node_branch_b", TransitionID: "join_b", DisplayName: "Join B"},
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "group_join_consume", SourceNodeID: "node_join", TransitionID: "consume", DisplayName: "Consume"},
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "group_consume_done", SourceNodeID: "node_consume", TransitionID: "done", DisplayName: "Done"},
		},
		Edges: []workflow.Edge{
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "edge_start_split", Key: "start", TransitionGroupID: "group_start", TargetNodeID: "node_split", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Split."},
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "edge_split_a", Key: "branch_a", TransitionGroupID: "group_split", TargetNodeID: "node_branch_a", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "A."},
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "edge_split_b", Key: "branch_b", TransitionGroupID: "group_split", TargetNodeID: "node_branch_b", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "B."},
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "edge_branch_a_join", Key: "join_a", TransitionGroupID: "group_branch_a_join", TargetNodeID: "node_join", ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "plan", Description: "Implementation plan."}}},
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "edge_branch_b_join", Key: "join_b", TransitionGroupID: "group_branch_b_join", TargetNodeID: "node_join", ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "risk", Description: "Known implementation risk."}}},
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "edge_join_consume", Key: "consume", TransitionGroupID: "group_join_consume", TargetNodeID: "node_consume", ContextMode: workflow.ContextModeNewSession, PromptTemplate: "Use {{.Params.plan}} and {{.Params.risk}}."},
			{WorkflowID: testsetup.WorkflowID(t, "workflow_join_parameters"), ID: "edge_consume_done", Key: "done", TransitionGroupID: "group_consume_done", TargetNodeID: "node_done", ContextMode: workflow.ContextModeNewSession, Parameters: []workflow.Parameter{{Key: "summary", Description: "Summary."}}},
		},
	}
}
