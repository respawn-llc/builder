package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const emptyWorkflowGraphDocumentJSON = `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[]}}`

func TestWorkflowGraphInspectDocumentProjectionRoundTripsPublicShape(t *testing.T) {
	ctx, remote, workflowID := newWorkflowGraphServiceFixture(t)
	current := getWorkflowGraphDefinition(t, ctx, remote, workflowID)

	document, err := workflowGraphDocumentFromDefinition(current)
	if err != nil {
		t.Fatalf("workflowGraphDocumentFromDefinition: %v", err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal Workflow graph document: %v", err)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &root); err != nil {
		t.Fatalf("decode Workflow graph document shape: %v", err)
	}
	requireWorkflowGraphDocumentKeys(t, root, "expected_version", "graph", "workflow_id")
	var graph map[string]json.RawMessage
	if err := json.Unmarshal(root["graph"], &graph); err != nil {
		t.Fatalf("decode Workflow graph container shape: %v", err)
	}
	requireWorkflowGraphDocumentKeys(t, graph, "edges", "node_groups", "nodes", "transition_groups")
	var nodes []map[string]json.RawMessage
	if err := json.Unmarshal(graph["nodes"], &nodes); err != nil {
		t.Fatalf("decode Workflow graph document Nodes: %v", err)
	}
	foundGroupedNode := false
	for _, node := range nodes {
		if _, grouped := node["group_id"]; grouped {
			foundGroupedNode = true
		}
		if _, leaked := node["group_key"]; leaked {
			t.Fatalf("Workflow graph document Node leaked group_key: %s", node["group_key"])
		}
	}
	if !foundGroupedNode {
		t.Fatal("Workflow graph document has no group_id membership")
	}
	canonical, err := canonicalWorkflowGraphDraftFromDefinition(current)
	if err != nil {
		t.Fatalf("canonicalWorkflowGraphDraftFromDefinition: %v", err)
	}
	requireWorkflowGraphIDs(t, document.Graph.NodeGroups, func(group serverapi.WorkflowGraphDraftNodeGroup) string { return group.ID }, workflowGraphDraftNodeGroupIDs(canonical))
	requireWorkflowGraphIDs(t, document.Graph.Nodes, func(node workflowGraphDocumentNode) string { return node.ID }, workflowGraphDraftNodeIDs(canonical))
	requireWorkflowGraphIDs(t, document.Graph.TransitionGroups, func(group serverapi.WorkflowGraphDraftTransitionGroup) string { return group.ID }, workflowGraphDraftTransitionGroupIDs(canonical))
	requireWorkflowGraphIDs(t, document.Graph.Edges, func(edge serverapi.WorkflowGraphDraftEdge) string { return edge.ID }, workflowGraphDraftEdgeIDs(canonical))

	decoded, err := decodeWorkflowGraphDocument(encoded)
	if err != nil {
		t.Fatalf("decodeWorkflowGraphDocument: %v", err)
	}
	reencoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-marshal Workflow graph document: %v", err)
	}
	if !bytes.Equal(reencoded, encoded) {
		t.Fatalf("Workflow graph document round trip changed\n got: %s\nwant: %s", reencoded, encoded)
	}
}

func TestWorkflowGraphInspectDocumentProjectionEmitsExplicitEmptyArrays(t *testing.T) {
	workflowID, err := runtimeids.ParseWorkflowID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseWorkflowID: %v", err)
	}
	document, err := workflowGraphDocumentFromDraft(workflowID, 1, serverapi.WorkflowGraphDraft{
		NodeGroups:       []serverapi.WorkflowGraphDraftNodeGroup{},
		Nodes:            []serverapi.WorkflowGraphDraftNode{},
		TransitionGroups: []serverapi.WorkflowGraphDraftTransitionGroup{},
		Edges:            []serverapi.WorkflowGraphDraftEdge{},
	})
	if err != nil {
		t.Fatalf("workflowGraphDocumentFromDraft: %v", err)
	}
	encoded, err := json.Marshal(document.Graph)
	if err != nil {
		t.Fatalf("marshal Workflow graph container: %v", err)
	}
	var graph map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &graph); err != nil {
		t.Fatalf("decode Workflow graph container: %v", err)
	}
	requireWorkflowGraphDocumentKeys(t, graph, "edges", "node_groups", "nodes", "transition_groups")
	for name, raw := range graph {
		var entities []json.RawMessage
		if err := json.Unmarshal(raw, &entities); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if entities == nil || len(entities) != 0 {
			t.Fatalf("%s = %v, want explicit empty array", name, entities)
		}
	}
}

func requireWorkflowGraphDocumentKeys(t *testing.T, value map[string]json.RawMessage, expected ...string) {
	t.Helper()
	actual := make([]string, 0, len(value))
	for key := range value {
		actual = append(actual, key)
	}
	slices.Sort(actual)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		t.Fatalf("JSON object keys = %v, want %v", actual, expected)
	}
}

func TestWorkflowGraphApplyDocumentRejectsMalformedInputBeforeRemoteOpening(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "duplicate top-level field", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"expected_version":2,"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[]}}`},
		{name: "duplicate nested field", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[],"nodes":[],"transition_groups":[],"edges":[]}}`},
		{name: "trailing JSON", data: emptyWorkflowGraphDocumentJSON + `{}`},
		{name: "recognized group_key", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[{"id":"node-1","key":"node","kind":"agent","display_name":"Node","group_key":"legacy"}],"transition_groups":[],"edges":[]}}`},
		{name: "missing workflow_id", data: `{"expected_version":1,"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[]}}`},
		{name: "missing expected_version", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[]}}`},
		{name: "missing graph", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1}`},
		{name: "missing node_groups", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"nodes":[],"transition_groups":[],"edges":[]}}`},
		{name: "missing nodes", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"transition_groups":[],"edges":[]}}`},
		{name: "missing transition_groups", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[],"edges":[]}}`},
		{name: "missing edges", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[],"transition_groups":[]}}`},
		{name: "graph has wrong shape", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":[]}`},
		{name: "nodes have wrong shape", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":{},"transition_groups":[],"edges":[]}}`},
		{name: "expected_version has wrong shape", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":"1","graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[]}}`},
		{name: "null collection", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":null,"nodes":[],"transition_groups":[],"edges":[]}}`},
		{name: "null required string", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[{"id":"node-1","key":"node","kind":"agent","display_name":null}],"transition_groups":[],"edges":[]}}`},
		{name: "null requires approval", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[{"id":"edge-1","transition_group_id":"group-1","key":"edge","target_node_id":"node-1","assignee_selection":"configured","thinking_selection":"configured","requires_approval":null,"context_mode":"new_session","context_source":{"kind":"immediate_source"}}]}}`},
		{name: "null context source", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[{"id":"edge-1","transition_group_id":"group-1","key":"edge","target_node_id":"node-1","assignee_selection":"configured","thinking_selection":"configured","requires_approval":false,"context_mode":"new_session","context_source":null}]}}`},
		{name: "scalar context source", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[{"id":"edge-1","transition_group_id":"group-1","key":"edge","target_node_id":"node-1","assignee_selection":"configured","thinking_selection":"configured","requires_approval":false,"context_mode":"new_session","context_source":"immediate_source"}]}}`},
		{name: "missing Node field", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[{"id":"node-1","key":"node","kind":"agent"}],"transition_groups":[],"edges":[]}}`},
		{name: "missing Transition Branch field", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[{"id":"edge-1","transition_group_id":"group-1","key":"edge","target_node_id":"node-1","assignee_selection":"configured","thinking_selection":"configured","context_mode":"new_session","context_source":{"kind":"immediate_source"}}]}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			opened := 0
			err := runWorkflowGraphDocumentCodecBoundary([]byte(test.data), func() {
				opened++
			})
			if err == nil {
				t.Fatal("malformed Workflow graph document was accepted")
			}
			if opened != 0 {
				t.Fatalf("remote opened %d times for malformed Workflow graph document", opened)
			}
		})
	}
}

func TestWorkflowGraphApplyDocumentValidatesGroupIDsAndClearsSharedGroupKey(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "missing Node Group ID", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[{"id":"","key":"group","display_name":"Group"}],"nodes":[],"transition_groups":[],"edges":[]}}`},
		{name: "empty group_id", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[{"id":"group-1","key":"group","display_name":"Group"}],"nodes":[{"id":"node-1","key":"node","kind":"agent","display_name":"Node","group_id":""}],"transition_groups":[],"edges":[]}}`},
		{name: "unknown group_id", data: `{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[{"id":"group-1","key":"group","display_name":"Group"}],"nodes":[{"id":"node-1","key":"node","kind":"agent","display_name":"Node","group_id":"group-missing"}],"transition_groups":[],"edges":[]}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeWorkflowGraphDocument([]byte(test.data)); err == nil {
				t.Fatal("invalid Group identity was accepted")
			}
		})
	}

	document, err := decodeWorkflowGraphDocument([]byte(`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"graph":{"node_groups":[{"id":"group-1","key":"group","display_name":"Group"}],"nodes":[{"id":"node-1","key":"node","kind":"agent","display_name":"Node","group_id":"group-1"}],"transition_groups":[],"edges":[]}}`))
	if err != nil {
		t.Fatalf("decode valid grouped Node: %v", err)
	}
	graph, err := document.WorkflowGraphDraft()
	if err != nil {
		t.Fatalf("WorkflowGraphDraft: %v", err)
	}
	if len(graph.Nodes) != 1 || graph.Nodes[0].GroupID != "group-1" || graph.Nodes[0].GroupKey != "" {
		t.Fatalf("converted Node = %+v, want group_id with shared GroupKey absent", graph.Nodes)
	}
}

func TestWorkflowGraphInspectApplyDocumentPreservesParameterAndJoinInputProviderOrder(t *testing.T) {
	ctx, remote, workflowID := newWorkflowGraphServiceFixture(t)
	current := getWorkflowGraphDefinition(t, ctx, remote, workflowID)
	wantProviders := []serverapi.WorkflowJoinInputProvider{
		{InputName: "second", ProviderEdgeID: "edge-branch-b"},
		{InputName: "first", ProviderEdgeID: "edge-branch-a"},
	}
	for index := range current.Nodes {
		if current.Nodes[index].ID == "node-join-zeta" {
			current.Nodes[index].JoinInputProviders = wantProviders
		}
	}
	wantParameters := []serverapi.WorkflowParameter{
		{Key: "second", Description: "Second", Purpose: "ordinary"},
		{Key: "first", Description: "First", Purpose: "ordinary"},
	}
	for index := range current.Edges {
		if current.Edges[index].ID == "edge-plan-zeta-a" {
			current.Edges[index].Parameters = wantParameters
		}
	}

	document, err := workflowGraphDocumentFromDefinition(current)
	if err != nil {
		t.Fatalf("workflowGraphDocumentFromDefinition: %v", err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal Workflow graph document: %v", err)
	}
	decoded, err := decodeWorkflowGraphDocument(encoded)
	if err != nil {
		t.Fatalf("decodeWorkflowGraphDocument: %v", err)
	}
	graph, err := decoded.WorkflowGraphDraft()
	if err != nil {
		t.Fatalf("WorkflowGraphDraft: %v", err)
	}
	if got := workflowGraphDraftNodeByID(t, graph, "node-join-zeta").JoinInputProviders; !slices.Equal(got, wantProviders) {
		t.Fatalf("Join Input Provider order = %+v, want authored order", got)
	}
	if got := workflowGraphDraftEdgeByID(t, graph, "edge-plan-zeta-a").Parameters; !slices.Equal(got, wantParameters) {
		t.Fatalf("Parameter order = %+v, want authored order", got)
	}
}

func TestWorkflowGraphApplyDocumentToleratesUnknownFields(t *testing.T) {
	data := []byte(`{"workflow_id":"11111111-1111-4111-8111-111111111111","expected_version":1,"unknown_top":{"future":true},"graph":{"node_groups":[],"nodes":[],"transition_groups":[],"edges":[],"unknown_graph":[1,2,3]}}`)
	opened := 0
	if err := runWorkflowGraphDocumentCodecBoundary(data, func() { opened++ }); err != nil {
		t.Fatalf("unknown fields rejected: %v", err)
	}
	if opened != 1 {
		t.Fatalf("remote opened %d times, want once after valid decoding", opened)
	}
}

func workflowGraphDraftNodeGroupIDs(graph serverapi.WorkflowGraphDraft) []string {
	ids := make([]string, 0, len(graph.NodeGroups))
	for _, group := range graph.NodeGroups {
		ids = append(ids, group.ID)
	}
	return ids
}

func workflowGraphDraftNodeIDs(graph serverapi.WorkflowGraphDraft) []string {
	ids := make([]string, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func workflowGraphDraftTransitionGroupIDs(graph serverapi.WorkflowGraphDraft) []string {
	ids := make([]string, 0, len(graph.TransitionGroups))
	for _, group := range graph.TransitionGroups {
		ids = append(ids, group.ID)
	}
	return ids
}

func workflowGraphDraftEdgeIDs(graph serverapi.WorkflowGraphDraft) []string {
	ids := make([]string, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		ids = append(ids, edge.ID)
	}
	return ids
}

func workflowGraphDraftNodeByID(t *testing.T, graph serverapi.WorkflowGraphDraft, id string) serverapi.WorkflowGraphDraftNode {
	t.Helper()
	for _, node := range graph.Nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("missing draft Node %q", id)
	return serverapi.WorkflowGraphDraftNode{}
}

func workflowGraphDraftEdgeByID(t *testing.T, graph serverapi.WorkflowGraphDraft, id string) serverapi.WorkflowGraphDraftEdge {
	t.Helper()
	for _, edge := range graph.Edges {
		if edge.ID == id {
			return edge
		}
	}
	t.Fatalf("missing draft Transition Branch %q", id)
	return serverapi.WorkflowGraphDraftEdge{}
}

func runWorkflowGraphDocumentCodecBoundary(data []byte, openRemote func()) error {
	if openRemote == nil {
		return errors.New("remote opener is required")
	}
	if _, err := decodeWorkflowGraphDocument(data); err != nil {
		return err
	}
	openRemote()
	return nil
}
