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
	workflowGraphJSONString
	workflowGraphJSONInteger
	workflowGraphJSONBoolean
)

type workflowGraphJSONSchema struct {
	fields    map[string]workflowGraphJSONContext
	required  []string
	element   workflowGraphJSONContext
	forbidden string
}

var workflowGraphJSONSchemas = map[workflowGraphJSONContext]workflowGraphJSONSchema{
	workflowGraphJSONDocument: {
		fields: map[string]workflowGraphJSONContext{
			"workflow_id": workflowGraphJSONString, "expected_version": workflowGraphJSONInteger, "graph": workflowGraphJSONGraph,
		},
		required: []string{"workflow_id", "expected_version", "graph"},
	},
	workflowGraphJSONGraph: {
		fields: map[string]workflowGraphJSONContext{
			"node_groups": workflowGraphJSONNodeGroups, "nodes": workflowGraphJSONNodes,
			"transition_groups": workflowGraphJSONTransitionGroups, "edges": workflowGraphJSONEdges,
		},
		required: []string{"node_groups", "nodes", "transition_groups", "edges"},
	},
	workflowGraphJSONNodeGroup: {
		fields: map[string]workflowGraphJSONContext{
			"id": workflowGraphJSONString, "key": workflowGraphJSONString, "display_name": workflowGraphJSONString,
		},
		required: []string{"id", "key", "display_name"},
	},
	workflowGraphJSONNode: {
		fields: map[string]workflowGraphJSONContext{
			"id": workflowGraphJSONString, "key": workflowGraphJSONString, "kind": workflowGraphJSONString,
			"display_name": workflowGraphJSONString, "join_input_providers": workflowGraphJSONJoinInputProviders,
		},
		required:  []string{"id", "key", "kind", "display_name"},
		forbidden: "group_key",
	},
	workflowGraphJSONJoinInputProvider: {
		fields: map[string]workflowGraphJSONContext{
			"input_name": workflowGraphJSONString, "provider_edge_id": workflowGraphJSONString,
		},
		required: []string{"input_name", "provider_edge_id"},
	},
	workflowGraphJSONTransitionGroup: {
		fields: map[string]workflowGraphJSONContext{
			"id": workflowGraphJSONString, "source_node_id": workflowGraphJSONString,
			"transition_id": workflowGraphJSONString, "display_name": workflowGraphJSONString,
		},
		required: []string{"id", "source_node_id", "transition_id", "display_name"},
	},
	workflowGraphJSONEdge: {
		fields: map[string]workflowGraphJSONContext{
			"id": workflowGraphJSONString, "transition_group_id": workflowGraphJSONString,
			"key": workflowGraphJSONString, "target_node_id": workflowGraphJSONString,
			"assignee_selection": workflowGraphJSONString, "thinking_selection": workflowGraphJSONString,
			"requires_approval": workflowGraphJSONBoolean, "context_mode": workflowGraphJSONString,
			"context_source": workflowGraphJSONContextSource, "parameters": workflowGraphJSONParameters,
		},
		required: []string{
			"id", "transition_group_id", "key", "target_node_id", "assignee_selection",
			"thinking_selection", "requires_approval", "context_mode", "context_source",
		},
	},
	workflowGraphJSONContextSource: {
		fields: map[string]workflowGraphJSONContext{"kind": workflowGraphJSONString}, required: []string{"kind"},
	},
	workflowGraphJSONParameter: {
		fields: map[string]workflowGraphJSONContext{
			"key": workflowGraphJSONString, "description": workflowGraphJSONString, "purpose": workflowGraphJSONString,
		},
		required: []string{"key", "description", "purpose"},
	},
	workflowGraphJSONNodeGroups:         {element: workflowGraphJSONNodeGroup},
	workflowGraphJSONNodes:              {element: workflowGraphJSONNode},
	workflowGraphJSONJoinInputProviders: {element: workflowGraphJSONJoinInputProvider},
	workflowGraphJSONTransitionGroups:   {element: workflowGraphJSONTransitionGroup},
	workflowGraphJSONEdges:              {element: workflowGraphJSONEdge},
	workflowGraphJSONParameters:         {element: workflowGraphJSONParameter},
}

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
	if err := document.Validate(); err != nil {
		return workflowGraphDocument{}, err
	}
	return document, nil
}

func decodeWorkflowGraphDocument(data []byte) (workflowGraphDocument, error) {
	if err := validateWorkflowGraphDocumentJSON(data); err != nil {
		return workflowGraphDocument{}, err
	}
	var decoded workflowGraphDocumentDecode
	if err := json.Unmarshal(data, &decoded); err != nil {
		return workflowGraphDocument{}, err
	}
	if decoded.WorkflowID == nil || decoded.ExpectedVersion == nil || decoded.Graph == nil ||
		decoded.Graph.NodeGroups == nil || decoded.Graph.Nodes == nil ||
		decoded.Graph.TransitionGroups == nil || decoded.Graph.Edges == nil {
		return workflowGraphDocument{}, errors.New("Workflow graph document requires workflow_id, expected_version, graph, and all graph collections")
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
	decoder.UseNumber()
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
	if err := validateWorkflowGraphJSONToken(context, token); err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		schema := workflowGraphJSONSchemas[context]
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
			if schema.forbidden != "" && key == schema.forbidden {
				return fmt.Errorf("%s is not allowed", key)
			}
			seen[key] = true
			if err := scanWorkflowGraphJSONValue(decoder, schema.fields[key]); err != nil {
				return err
			}
		}
		if _, err := decoder.Token(); err != nil {
			return err
		}
		for _, field := range schema.required {
			if !seen[field] {
				return fmt.Errorf("%s is required", field)
			}
		}
		return nil
	case '[':
		elementContext := workflowGraphJSONSchemas[context].element
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

func validateWorkflowGraphJSONToken(context workflowGraphJSONContext, token json.Token) error {
	switch context {
	case workflowGraphJSONDocument,
		workflowGraphJSONGraph,
		workflowGraphJSONNodeGroup,
		workflowGraphJSONNode,
		workflowGraphJSONJoinInputProvider,
		workflowGraphJSONTransitionGroup,
		workflowGraphJSONEdge,
		workflowGraphJSONContextSource,
		workflowGraphJSONParameter:
		if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
			return errors.New("required JSON object has invalid value")
		}
	case workflowGraphJSONNodeGroups,
		workflowGraphJSONNodes,
		workflowGraphJSONJoinInputProviders,
		workflowGraphJSONTransitionGroups,
		workflowGraphJSONEdges,
		workflowGraphJSONParameters:
		if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
			return errors.New("required JSON array has invalid value")
		}
	case workflowGraphJSONString:
		if _, ok := token.(string); !ok {
			return errors.New("required JSON string has invalid value")
		}
	case workflowGraphJSONInteger:
		number, ok := token.(json.Number)
		if !ok {
			return errors.New("required JSON integer has invalid value")
		}
		if _, err := number.Int64(); err != nil {
			return errors.New("required JSON integer has invalid value")
		}
	case workflowGraphJSONBoolean:
		if _, ok := token.(bool); !ok {
			return errors.New("required JSON boolean has invalid value")
		}
	}
	return nil
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
	if err := (serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      d.WorkflowID,
		ExpectedVersion: d.ExpectedVersion,
		Graph:           graph,
	}).Validate(); err != nil {
		return serverapi.WorkflowGraphDraft{}, err
	}
	return graph, nil
}
