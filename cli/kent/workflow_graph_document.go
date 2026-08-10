package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type workflowGraphDocument struct {
	WorkflowID      runtimeids.WorkflowID      `json:"workflow_id"`
	ExpectedVersion int64                      `json:"expected_version"`
	Graph           workflowGraphDocumentGraph `json:"graph"`
}

type workflowGraphDocumentGraph struct {
	NodeGroups       []serverapi.WorkflowGraphDraftNodeGroup       `json:"node_groups"`
	Nodes            []workflowGraphDocumentNode                   `json:"nodes"`
	TransitionGroups []serverapi.WorkflowGraphDraftTransitionGroup `json:"transition_groups"`
	Edges            []serverapi.WorkflowGraphDraftEdge            `json:"edges"`
}

type workflowGraphDocumentNode struct {
	ID                 string                                `json:"id"`
	Key                string                                `json:"key"`
	Kind               string                                `json:"kind"`
	DisplayName        string                                `json:"display_name"`
	GroupID            *string                               `json:"group_id,omitempty"`
	SubagentRole       string                                `json:"subagent_role,omitempty"`
	CompletionMode     string                                `json:"completion_mode,omitempty"`
	ScriptPath         *string                               `json:"script_path,omitempty"`
	JoinInputProviders []serverapi.WorkflowJoinInputProvider `json:"join_input_providers,omitempty"`
	GroupKey           json.RawMessage                       `json:"group_key,omitempty"`
}

type workflowGraphDocumentDecode struct {
	WorkflowID      *runtimeids.WorkflowID            `json:"workflow_id"`
	ExpectedVersion *int64                            `json:"expected_version"`
	Graph           *workflowGraphDocumentGraphDecode `json:"graph"`
}

type workflowGraphDocumentGraphDecode struct {
	NodeGroups       *[]serverapi.WorkflowGraphDraftNodeGroup       `json:"node_groups"`
	Nodes            *[]workflowGraphDocumentNode                   `json:"nodes"`
	TransitionGroups *[]serverapi.WorkflowGraphDraftTransitionGroup `json:"transition_groups"`
	Edges            *[]serverapi.WorkflowGraphDraftEdge            `json:"edges"`
}

func workflowGraphDocumentFromDefinition(definition serverapi.WorkflowDefinition) (workflowGraphDocument, error) {
	return workflowGraphDocumentFromDraft(
		definition.Workflow.ID,
		definition.Workflow.Version,
		workflowGraphDraftFromDefinition(definition),
	)
}

func workflowGraphDocumentFromDraft(
	workflowID runtimeids.WorkflowID,
	expectedVersion int64,
	graph serverapi.WorkflowGraphDraft,
) (workflowGraphDocument, error) {
	document := workflowGraphDocument{
		WorkflowID:      workflowID,
		ExpectedVersion: expectedVersion,
		Graph: workflowGraphDocumentGraph{
			NodeGroups:       graph.NodeGroups,
			Nodes:            make([]workflowGraphDocumentNode, 0, len(graph.Nodes)),
			TransitionGroups: graph.TransitionGroups,
			Edges:            graph.Edges,
		},
	}
	for _, node := range graph.Nodes {
		document.Graph.Nodes = append(document.Graph.Nodes, workflowGraphDocumentNode{
			ID:                 node.ID,
			Key:                node.Key,
			Kind:               node.Kind,
			DisplayName:        node.DisplayName,
			GroupID:            node.GroupID,
			SubagentRole:       node.SubagentRole,
			CompletionMode:     node.CompletionMode,
			ScriptPath:         node.ScriptPath,
			JoinInputProviders: node.JoinInputProviders,
		})
	}
	return document, nil
}

func decodeWorkflowGraphDocument(data []byte) (workflowGraphDocument, error) {
	var decoded workflowGraphDocumentDecode
	if err := json.Unmarshal(data, &decoded); err != nil {
		return workflowGraphDocument{}, err
	}
	if decoded.WorkflowID == nil || decoded.ExpectedVersion == nil || decoded.Graph == nil ||
		decoded.Graph.NodeGroups == nil || decoded.Graph.Nodes == nil ||
		decoded.Graph.TransitionGroups == nil || decoded.Graph.Edges == nil {
		return workflowGraphDocument{}, errors.New("Workflow graph document requires workflow_id, expected_version, graph, and all graph collections")
	}
	for index, node := range *decoded.Graph.Nodes {
		if node.GroupKey != nil {
			return workflowGraphDocument{}, fmt.Errorf("graph.nodes[%d].group_key is not allowed", index)
		}
	}
	type workflowGraphEdgePresence struct {
		RequiresApproval *bool `json:"requires_approval"`
	}
	var edgePresence struct {
		Graph struct {
			Edges []workflowGraphEdgePresence `json:"edges"`
		} `json:"graph"`
	}
	if err := json.Unmarshal(data, &edgePresence); err != nil {
		return workflowGraphDocument{}, err
	}
	for index, edge := range edgePresence.Graph.Edges {
		if edge.RequiresApproval == nil {
			return workflowGraphDocument{}, fmt.Errorf("graph.edges[%d].requires_approval is required", index)
		}
	}
	document := workflowGraphDocument{
		WorkflowID:      *decoded.WorkflowID,
		ExpectedVersion: *decoded.ExpectedVersion,
		Graph: workflowGraphDocumentGraph{
			NodeGroups:       *decoded.Graph.NodeGroups,
			Nodes:            *decoded.Graph.Nodes,
			TransitionGroups: *decoded.Graph.TransitionGroups,
			Edges:            *decoded.Graph.Edges,
		},
	}
	return document, nil
}

func (d workflowGraphDocument) WorkflowGraphDraft() (serverapi.WorkflowGraphDraft, error) {
	graph := serverapi.WorkflowGraphDraft{
		NodeGroups:       d.Graph.NodeGroups,
		Nodes:            make([]serverapi.WorkflowGraphDraftNode, 0, len(d.Graph.Nodes)),
		TransitionGroups: d.Graph.TransitionGroups,
		Edges:            d.Graph.Edges,
	}
	for _, node := range d.Graph.Nodes {
		graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
			ID:                 node.ID,
			Key:                node.Key,
			Kind:               node.Kind,
			DisplayName:        node.DisplayName,
			GroupID:            node.GroupID,
			SubagentRole:       node.SubagentRole,
			CompletionMode:     node.CompletionMode,
			ScriptPath:         node.ScriptPath,
			JoinInputProviders: node.JoinInputProviders,
		})
	}
	if err := (serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      d.WorkflowID,
		ExpectedVersion: d.ExpectedVersion,
		Graph:           graph,
	}).Validate(); err != nil {
		return serverapi.WorkflowGraphDraft{}, err
	}
	return graph, nil
}
