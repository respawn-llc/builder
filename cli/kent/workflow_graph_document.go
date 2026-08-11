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
	GroupID            *string                               `json:"group_id"`
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
	NodeGroups       *[]workflowGraphNodeGroupDecode       `json:"node_groups"`
	Nodes            *[]workflowGraphNodeDecode            `json:"nodes"`
	TransitionGroups *[]workflowGraphTransitionGroupDecode `json:"transition_groups"`
	Edges            *[]workflowGraphEdgeDecode            `json:"edges"`
}

type workflowGraphNodeGroupDecode struct {
	ID          *string `json:"id"`
	Key         *string `json:"key"`
	DisplayName *string `json:"display_name"`
}

type workflowGraphNodeDecode struct {
	ID                 *string                                `json:"id"`
	Key                *string                                `json:"key"`
	Kind               *string                                `json:"kind"`
	DisplayName        *string                                `json:"display_name"`
	GroupID            workflowGraphNullableStringDecode      `json:"group_id"`
	GroupKey           json.RawMessage                        `json:"group_key"`
	SubagentRole       string                                 `json:"subagent_role"`
	CompletionMode     string                                 `json:"completion_mode"`
	ScriptPath         *string                                `json:"script_path"`
	JoinInputProviders []workflowGraphJoinInputProviderDecode `json:"join_input_providers"`
}

type workflowGraphJoinInputProviderDecode struct {
	InputName      *string `json:"input_name"`
	ProviderEdgeID *string `json:"provider_edge_id"`
}

type workflowGraphTransitionGroupDecode struct {
	ID           *string `json:"id"`
	SourceNodeID *string `json:"source_node_id"`
	TransitionID *string `json:"transition_id"`
	DisplayName  *string `json:"display_name"`
	Description  string  `json:"description"`
}

type workflowGraphEdgeDecode struct {
	ID                *string                           `json:"id"`
	TransitionGroupID *string                           `json:"transition_group_id"`
	Key               *string                           `json:"key"`
	TargetNodeID      *string                           `json:"target_node_id"`
	AssigneeSelection *string                           `json:"assignee_selection"`
	ThinkingSelection *string                           `json:"thinking_selection"`
	RequiresApproval  *bool                             `json:"requires_approval"`
	ContextMode       *string                           `json:"context_mode"`
	ContextSource     *workflowGraphContextSourceDecode `json:"context_source"`
	PromptTemplate    string                            `json:"prompt_template"`
	Parameters        []workflowGraphParameterDecode    `json:"parameters"`
}

type workflowGraphContextSourceDecode struct {
	Kind    *string `json:"kind"`
	NodeKey string  `json:"node_key"`
}

type workflowGraphParameterDecode struct {
	Key         *string `json:"key"`
	Description *string `json:"description"`
	Purpose     *string `json:"purpose"`
}

type workflowGraphNullableStringDecode struct {
	Value   *string
	Present bool
}

func (d *workflowGraphNullableStringDecode) UnmarshalJSON(data []byte) error {
	d.Present = true
	return json.Unmarshal(data, &d.Value)
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
	if *decoded.ExpectedVersion < 0 {
		return workflowGraphDocument{}, errors.New("Workflow graph document expected_version must be non-negative")
	}
	document := workflowGraphDocument{
		WorkflowID:      *decoded.WorkflowID,
		ExpectedVersion: *decoded.ExpectedVersion,
		Graph: workflowGraphDocumentGraph{
			NodeGroups:       make([]serverapi.WorkflowGraphDraftNodeGroup, 0, len(*decoded.Graph.NodeGroups)),
			Nodes:            make([]workflowGraphDocumentNode, 0, len(*decoded.Graph.Nodes)),
			TransitionGroups: make([]serverapi.WorkflowGraphDraftTransitionGroup, 0, len(*decoded.Graph.TransitionGroups)),
			Edges:            make([]serverapi.WorkflowGraphDraftEdge, 0, len(*decoded.Graph.Edges)),
		},
	}
	for index, group := range *decoded.Graph.NodeGroups {
		if err := requireWorkflowGraphDocumentFields(index, "node_groups",
			workflowGraphRequiredField{"id", group.ID == nil},
			workflowGraphRequiredField{"key", group.Key == nil},
			workflowGraphRequiredField{"display_name", group.DisplayName == nil},
		); err != nil {
			return workflowGraphDocument{}, err
		}
		document.Graph.NodeGroups = append(document.Graph.NodeGroups, serverapi.WorkflowGraphDraftNodeGroup{
			ID: *group.ID, Key: *group.Key, DisplayName: *group.DisplayName,
		})
	}
	for index, node := range *decoded.Graph.Nodes {
		if err := requireWorkflowGraphDocumentFields(index, "nodes",
			workflowGraphRequiredField{"id", node.ID == nil},
			workflowGraphRequiredField{"key", node.Key == nil},
			workflowGraphRequiredField{"kind", node.Kind == nil},
			workflowGraphRequiredField{"display_name", node.DisplayName == nil},
			workflowGraphRequiredField{"group_id", !node.GroupID.Present},
		); err != nil {
			return workflowGraphDocument{}, err
		}
		if node.GroupKey != nil {
			return workflowGraphDocument{}, fmt.Errorf("graph.nodes[%d].group_key is not allowed", index)
		}
		providers := make([]serverapi.WorkflowJoinInputProvider, 0, len(node.JoinInputProviders))
		for providerIndex, provider := range node.JoinInputProviders {
			if provider.InputName == nil {
				return workflowGraphDocument{}, fmt.Errorf("graph.nodes[%d].join_input_providers[%d].input_name is required", index, providerIndex)
			}
			if provider.ProviderEdgeID == nil {
				return workflowGraphDocument{}, fmt.Errorf("graph.nodes[%d].join_input_providers[%d].provider_edge_id is required", index, providerIndex)
			}
			providers = append(providers, serverapi.WorkflowJoinInputProvider{
				InputName: *provider.InputName, ProviderEdgeID: *provider.ProviderEdgeID,
			})
		}
		document.Graph.Nodes = append(document.Graph.Nodes, workflowGraphDocumentNode{
			ID: *node.ID, Key: *node.Key, Kind: *node.Kind, DisplayName: *node.DisplayName,
			GroupID: node.GroupID.Value, SubagentRole: node.SubagentRole, CompletionMode: node.CompletionMode,
			ScriptPath: node.ScriptPath, JoinInputProviders: providers,
		})
	}
	for index, group := range *decoded.Graph.TransitionGroups {
		if err := requireWorkflowGraphDocumentFields(index, "transition_groups",
			workflowGraphRequiredField{"id", group.ID == nil},
			workflowGraphRequiredField{"source_node_id", group.SourceNodeID == nil},
			workflowGraphRequiredField{"transition_id", group.TransitionID == nil},
			workflowGraphRequiredField{"display_name", group.DisplayName == nil},
		); err != nil {
			return workflowGraphDocument{}, err
		}
		document.Graph.TransitionGroups = append(document.Graph.TransitionGroups, serverapi.WorkflowGraphDraftTransitionGroup{
			ID: *group.ID, SourceNodeID: *group.SourceNodeID, TransitionID: *group.TransitionID,
			DisplayName: *group.DisplayName, Description: group.Description,
		})
	}
	for index, edge := range *decoded.Graph.Edges {
		if err := requireWorkflowGraphDocumentFields(index, "edges",
			workflowGraphRequiredField{"id", edge.ID == nil},
			workflowGraphRequiredField{"transition_group_id", edge.TransitionGroupID == nil},
			workflowGraphRequiredField{"key", edge.Key == nil},
			workflowGraphRequiredField{"target_node_id", edge.TargetNodeID == nil},
			workflowGraphRequiredField{"assignee_selection", edge.AssigneeSelection == nil},
			workflowGraphRequiredField{"thinking_selection", edge.ThinkingSelection == nil},
			workflowGraphRequiredField{"requires_approval", edge.RequiresApproval == nil},
			workflowGraphRequiredField{"context_mode", edge.ContextMode == nil},
			workflowGraphRequiredField{"context_source", edge.ContextSource == nil},
		); err != nil {
			return workflowGraphDocument{}, err
		}
		if edge.ContextSource.Kind == nil {
			return workflowGraphDocument{}, fmt.Errorf("graph.edges[%d].context_source.kind is required", index)
		}
		parameters := make([]serverapi.WorkflowParameter, 0, len(edge.Parameters))
		for parameterIndex, parameter := range edge.Parameters {
			if parameter.Key == nil {
				return workflowGraphDocument{}, fmt.Errorf("graph.edges[%d].parameters[%d].key is required", index, parameterIndex)
			}
			if parameter.Description == nil {
				return workflowGraphDocument{}, fmt.Errorf("graph.edges[%d].parameters[%d].description is required", index, parameterIndex)
			}
			if parameter.Purpose == nil {
				return workflowGraphDocument{}, fmt.Errorf("graph.edges[%d].parameters[%d].purpose is required", index, parameterIndex)
			}
			parameters = append(parameters, serverapi.WorkflowParameter{
				Key: *parameter.Key, Description: *parameter.Description, Purpose: *parameter.Purpose,
			})
		}
		document.Graph.Edges = append(document.Graph.Edges, serverapi.WorkflowGraphDraftEdge{
			ID: *edge.ID, TransitionGroupID: *edge.TransitionGroupID, Key: *edge.Key,
			TargetNodeID: *edge.TargetNodeID, AssigneeSelection: *edge.AssigneeSelection,
			ThinkingSelection: *edge.ThinkingSelection, RequiresApproval: *edge.RequiresApproval,
			ContextMode:    *edge.ContextMode,
			ContextSource:  serverapi.WorkflowContextSource{Kind: *edge.ContextSource.Kind, NodeKey: edge.ContextSource.NodeKey},
			PromptTemplate: edge.PromptTemplate, Parameters: parameters,
		})
	}
	return document, nil
}

type workflowGraphRequiredField struct {
	name    string
	missing bool
}

func requireWorkflowGraphDocumentFields(index int, collection string, fields ...workflowGraphRequiredField) error {
	for _, field := range fields {
		if field.missing {
			return fmt.Errorf("graph.%s[%d].%s is required", collection, index, field.name)
		}
	}
	return nil
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
	if err := (serverapi.WorkflowGraphValidateDraftRequest{
		WorkflowID: d.WorkflowID,
		Graph:      graph,
		Modes:      []serverapi.WorkflowValidationMode{serverapi.WorkflowValidationModeDraft},
	}).Validate(); err != nil {
		return serverapi.WorkflowGraphDraft{}, err
	}
	return graph, nil
}
