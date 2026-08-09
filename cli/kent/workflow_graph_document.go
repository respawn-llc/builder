package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

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
	GroupID            string                                `json:"group_id,omitempty"`
	SubagentRole       string                                `json:"subagent_role,omitempty"`
	CompletionMode     string                                `json:"completion_mode,omitempty"`
	ScriptPath         *string                               `json:"script_path,omitempty"`
	JoinInputProviders []serverapi.WorkflowJoinInputProvider `json:"join_input_providers,omitempty"`
	groupIDPresent     bool
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

type workflowGraphJSONContext uint8

const (
	workflowGraphJSONUnknown workflowGraphJSONContext = iota
	workflowGraphJSONDocument
	workflowGraphJSONGraph
	workflowGraphJSONNodeGroups
	workflowGraphJSONNodeGroup
	workflowGraphJSONNodes
	workflowGraphJSONNode
	workflowGraphJSONJoinInputProviders
	workflowGraphJSONJoinInputProvider
	workflowGraphJSONTransitionGroups
	workflowGraphJSONTransitionGroup
	workflowGraphJSONEdges
	workflowGraphJSONEdge
	workflowGraphJSONContextSource
	workflowGraphJSONParameters
	workflowGraphJSONParameter
)

func (n *workflowGraphDocumentNode) UnmarshalJSON(data []byte) error {
	type documentNode workflowGraphDocumentNode
	var decoded documentNode
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*n = workflowGraphDocumentNode(decoded)
	_, n.groupIDPresent = fields["group_id"]
	return nil
}

func workflowGraphDocumentFromDefinition(definition serverapi.WorkflowDefinition) (workflowGraphDocument, error) {
	graph, err := canonicalWorkflowGraphDraftFromDefinition(definition)
	if err != nil {
		return workflowGraphDocument{}, err
	}
	return workflowGraphDocumentFromDraft(definition.Workflow.ID, definition.Workflow.Version, graph)
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
			NodeGroups:       cloneWorkflowGraphDocumentEntities(graph.NodeGroups),
			Nodes:            make([]workflowGraphDocumentNode, 0, len(graph.Nodes)),
			TransitionGroups: cloneWorkflowGraphDocumentEntities(graph.TransitionGroups),
			Edges:            make([]serverapi.WorkflowGraphDraftEdge, 0, len(graph.Edges)),
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
			JoinInputProviders: append([]serverapi.WorkflowJoinInputProvider(nil), node.JoinInputProviders...),
		})
	}
	for _, edge := range graph.Edges {
		edge.Parameters = append([]serverapi.WorkflowParameter(nil), edge.Parameters...)
		document.Graph.Edges = append(document.Graph.Edges, edge)
	}
	if err := document.Validate(); err != nil {
		return workflowGraphDocument{}, err
	}
	return document, nil
}

func cloneWorkflowGraphDocumentEntities[T any](entities []T) []T {
	cloned := make([]T, len(entities))
	copy(cloned, entities)
	return cloned
}

func decodeWorkflowGraphDocument(data []byte) (workflowGraphDocument, error) {
	if err := validateWorkflowGraphDocumentJSON(data); err != nil {
		return workflowGraphDocument{}, err
	}
	var decoded workflowGraphDocumentDecode
	if err := json.Unmarshal(data, &decoded); err != nil {
		return workflowGraphDocument{}, err
	}
	if decoded.WorkflowID == nil {
		return workflowGraphDocument{}, errors.New("workflow_id is required")
	}
	if decoded.ExpectedVersion == nil {
		return workflowGraphDocument{}, errors.New("expected_version is required")
	}
	if decoded.Graph == nil {
		return workflowGraphDocument{}, errors.New("graph is required")
	}
	if decoded.Graph.NodeGroups == nil {
		return workflowGraphDocument{}, errors.New("graph.node_groups is required")
	}
	if decoded.Graph.Nodes == nil {
		return workflowGraphDocument{}, errors.New("graph.nodes is required")
	}
	if decoded.Graph.TransitionGroups == nil {
		return workflowGraphDocument{}, errors.New("graph.transition_groups is required")
	}
	if decoded.Graph.Edges == nil {
		return workflowGraphDocument{}, errors.New("graph.edges is required")
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
	if err := document.Validate(); err != nil {
		return workflowGraphDocument{}, err
	}
	return document, nil
}

func validateWorkflowGraphDocumentJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanWorkflowGraphJSONValue(decoder, workflowGraphJSONDocument); err != nil {
		return err
	}
	if _, err := decoder.Token(); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("unexpected trailing JSON value")
}

func scanWorkflowGraphJSONValue(decoder *json.Decoder, context workflowGraphJSONContext) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object field name must be a string")
			}
			if seen[key] {
				return fmt.Errorf("duplicate JSON field %q", key)
			}
			if context == workflowGraphJSONNode && key == "group_key" {
				return errors.New("graph.nodes[].group_key is not allowed")
			}
			seen[key] = true
			if err := scanWorkflowGraphJSONValue(decoder, workflowGraphJSONChildContext(context, key)); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return err
		}
		for _, field := range workflowGraphJSONRequiredFields(context) {
			if !seen[field] {
				return fmt.Errorf("%s is required", field)
			}
		}
		return nil
	case '[':
		elementContext := workflowGraphJSONArrayElementContext(context)
		for decoder.More() {
			if err := scanWorkflowGraphJSONValue(decoder, elementContext); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func workflowGraphJSONChildContext(context workflowGraphJSONContext, key string) workflowGraphJSONContext {
	switch context {
	case workflowGraphJSONDocument:
		if key == "graph" {
			return workflowGraphJSONGraph
		}
	case workflowGraphJSONGraph:
		switch key {
		case "node_groups":
			return workflowGraphJSONNodeGroups
		case "nodes":
			return workflowGraphJSONNodes
		case "transition_groups":
			return workflowGraphJSONTransitionGroups
		case "edges":
			return workflowGraphJSONEdges
		}
	case workflowGraphJSONNode:
		if key == "join_input_providers" {
			return workflowGraphJSONJoinInputProviders
		}
	case workflowGraphJSONEdge:
		switch key {
		case "context_source":
			return workflowGraphJSONContextSource
		case "parameters":
			return workflowGraphJSONParameters
		}
	}
	return workflowGraphJSONUnknown
}

func workflowGraphJSONArrayElementContext(context workflowGraphJSONContext) workflowGraphJSONContext {
	switch context {
	case workflowGraphJSONNodeGroups:
		return workflowGraphJSONNodeGroup
	case workflowGraphJSONNodes:
		return workflowGraphJSONNode
	case workflowGraphJSONJoinInputProviders:
		return workflowGraphJSONJoinInputProvider
	case workflowGraphJSONTransitionGroups:
		return workflowGraphJSONTransitionGroup
	case workflowGraphJSONEdges:
		return workflowGraphJSONEdge
	case workflowGraphJSONParameters:
		return workflowGraphJSONParameter
	default:
		return workflowGraphJSONUnknown
	}
}

func workflowGraphJSONRequiredFields(context workflowGraphJSONContext) []string {
	switch context {
	case workflowGraphJSONDocument:
		return []string{"workflow_id", "expected_version", "graph"}
	case workflowGraphJSONGraph:
		return []string{"node_groups", "nodes", "transition_groups", "edges"}
	case workflowGraphJSONNodeGroup:
		return []string{"id", "key", "display_name"}
	case workflowGraphJSONNode:
		return []string{"id", "key", "kind", "display_name"}
	case workflowGraphJSONJoinInputProvider:
		return []string{"input_name", "provider_edge_id"}
	case workflowGraphJSONTransitionGroup:
		return []string{"id", "source_node_id", "transition_id", "display_name"}
	case workflowGraphJSONEdge:
		return []string{
			"id",
			"transition_group_id",
			"key",
			"target_node_id",
			"assignee_selection",
			"thinking_selection",
			"requires_approval",
			"context_mode",
			"context_source",
		}
	case workflowGraphJSONContextSource:
		return []string{"kind"}
	case workflowGraphJSONParameter:
		return []string{"key", "description", "purpose"}
	default:
		return nil
	}
}

func (d workflowGraphDocument) Validate() error {
	if d.WorkflowID.IsZero() {
		return errors.New("workflow_id is required")
	}
	if d.ExpectedVersion < 0 {
		return errors.New("expected_version must be non-negative")
	}
	if d.Graph.NodeGroups == nil || d.Graph.Nodes == nil || d.Graph.TransitionGroups == nil || d.Graph.Edges == nil {
		return errors.New("Workflow graph document collections are required")
	}
	groupIDs := make(map[string]bool, len(d.Graph.NodeGroups))
	for index, group := range d.Graph.NodeGroups {
		if strings.TrimSpace(group.ID) == "" {
			return fmt.Errorf("graph.node_groups[%d].id is required", index)
		}
		if groupIDs[group.ID] {
			return fmt.Errorf("graph.node_groups[%d].id %q is duplicated", index, group.ID)
		}
		groupIDs[group.ID] = true
	}
	for index, node := range d.Graph.Nodes {
		if strings.TrimSpace(node.ID) == "" {
			return fmt.Errorf("graph.nodes[%d].id is required", index)
		}
		if node.groupIDPresent && strings.TrimSpace(node.GroupID) == "" {
			return fmt.Errorf("graph.nodes[%d].group_id is required when present", index)
		}
		if node.GroupID != "" && !groupIDs[node.GroupID] {
			return fmt.Errorf("graph.nodes[%d].group_id %q is not in graph.node_groups", index, node.GroupID)
		}
	}
	_, err := d.WorkflowGraphDraft()
	return err
}

func (d workflowGraphDocument) WorkflowGraphDraft() (serverapi.WorkflowGraphDraft, error) {
	graph := serverapi.WorkflowGraphDraft{
		NodeGroups:       append([]serverapi.WorkflowGraphDraftNodeGroup(nil), d.Graph.NodeGroups...),
		Nodes:            make([]serverapi.WorkflowGraphDraftNode, 0, len(d.Graph.Nodes)),
		TransitionGroups: append([]serverapi.WorkflowGraphDraftTransitionGroup(nil), d.Graph.TransitionGroups...),
		Edges:            make([]serverapi.WorkflowGraphDraftEdge, 0, len(d.Graph.Edges)),
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
			JoinInputProviders: append([]serverapi.WorkflowJoinInputProvider(nil), node.JoinInputProviders...),
		})
	}
	for _, edge := range d.Graph.Edges {
		edge.Parameters = append([]serverapi.WorkflowParameter(nil), edge.Parameters...)
		graph.Edges = append(graph.Edges, edge)
	}
	if err := (serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      d.WorkflowID,
		ExpectedVersion: d.ExpectedVersion,
		Graph:           graph,
	}).Validate(); err != nil {
		return serverapi.WorkflowGraphDraft{}, err
	}
	return graph, nil
}
