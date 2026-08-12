package main

import (
	"encoding/json"
	"fmt"

	"core/shared/jsoncontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"

	invjsonschema "github.com/invopop/jsonschema"
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
}

type workflowGraphDocumentSchema struct {
	WorkflowID      string                           `json:"workflow_id"`
	ExpectedVersion int64                            `json:"expected_version" jsonschema:"minimum=0"`
	Graph           workflowGraphDocumentGraphSchema `json:"graph"`
}

type workflowGraphDocumentGraphSchema struct {
	NodeGroups       []workflowGraphNodeGroupSchema       `json:"node_groups"`
	Nodes            []workflowGraphNodeSchema            `json:"nodes"`
	TransitionGroups []workflowGraphTransitionGroupSchema `json:"transition_groups"`
	Edges            []workflowGraphEdgeSchema            `json:"edges"`
}

type workflowGraphNodeGroupSchema struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
}

type workflowGraphNodeSchema struct {
	ID                 string                                 `json:"id"`
	Key                string                                 `json:"key"`
	Kind               string                                 `json:"kind"`
	DisplayName        string                                 `json:"display_name"`
	GroupID            *string                                `json:"group_id" jsonschema:"nullable"`
	SubagentRole       string                                 `json:"subagent_role,omitempty"`
	CompletionMode     string                                 `json:"completion_mode,omitempty"`
	ScriptPath         *string                                `json:"script_path,omitempty" jsonschema:"nullable"`
	JoinInputProviders []workflowGraphJoinInputProviderSchema `json:"join_input_providers,omitempty"`
}

type workflowGraphJoinInputProviderSchema struct {
	InputName      string `json:"input_name"`
	ProviderEdgeID string `json:"provider_edge_id"`
}

type workflowGraphTransitionGroupSchema struct {
	ID           string `json:"id"`
	SourceNodeID string `json:"source_node_id"`
	TransitionID string `json:"transition_id"`
	DisplayName  string `json:"display_name"`
	Description  string `json:"description,omitempty"`
}

type workflowGraphEdgeSchema struct {
	ID                string                           `json:"id"`
	TransitionGroupID string                           `json:"transition_group_id"`
	Key               string                           `json:"key"`
	TargetNodeID      string                           `json:"target_node_id"`
	AssigneeSelection string                           `json:"assignee_selection"`
	ThinkingSelection string                           `json:"thinking_selection"`
	RequiresApproval  bool                             `json:"requires_approval"`
	ContextMode       string                           `json:"context_mode"`
	ContextSource     workflowGraphContextSourceSchema `json:"context_source"`
	PromptTemplate    string                           `json:"prompt_template,omitempty"`
	Parameters        []workflowGraphParameterSchema   `json:"parameters,omitempty"`
}

type workflowGraphContextSourceSchema struct {
	Kind    string `json:"kind"`
	NodeKey string `json:"node_key,omitempty"`
}

type workflowGraphParameterSchema struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Purpose     string `json:"purpose"`
}

func allowWorkflowGraphUnknownFields(schema *invjsonschema.Schema) {
	schema.AdditionalProperties = invjsonschema.TrueSchema
}

func (workflowGraphDocumentSchema) JSONSchemaExtend(schema *invjsonschema.Schema) {
	allowWorkflowGraphUnknownFields(schema)
}

func (workflowGraphDocumentGraphSchema) JSONSchemaExtend(schema *invjsonschema.Schema) {
	allowWorkflowGraphUnknownFields(schema)
}

func (workflowGraphNodeGroupSchema) JSONSchemaExtend(schema *invjsonschema.Schema) {
	allowWorkflowGraphUnknownFields(schema)
}

func (workflowGraphNodeSchema) JSONSchemaExtend(schema *invjsonschema.Schema) {
	allowWorkflowGraphUnknownFields(schema)
	schema.PropertyNames = &invjsonschema.Schema{
		Not: &invjsonschema.Schema{Const: "group_key"},
	}
}

func (workflowGraphJoinInputProviderSchema) JSONSchemaExtend(schema *invjsonschema.Schema) {
	allowWorkflowGraphUnknownFields(schema)
}

func (workflowGraphTransitionGroupSchema) JSONSchemaExtend(schema *invjsonschema.Schema) {
	allowWorkflowGraphUnknownFields(schema)
}

func (workflowGraphEdgeSchema) JSONSchemaExtend(schema *invjsonschema.Schema) {
	allowWorkflowGraphUnknownFields(schema)
}

func (workflowGraphContextSourceSchema) JSONSchemaExtend(schema *invjsonschema.Schema) {
	allowWorkflowGraphUnknownFields(schema)
}

func (workflowGraphParameterSchema) JSONSchemaExtend(schema *invjsonschema.Schema) {
	allowWorkflowGraphUnknownFields(schema)
}

type workflowGraphDocumentContract struct {
	schema jsoncontract.Internal
}

func prepareWorkflowGraphDocumentContract() (workflowGraphDocumentContract, error) {
	schema, err := jsoncontract.NewPreparer(false).Internal(
		"Workflow graph CLI document",
		workflowGraphDocumentSchema{},
	)
	if err != nil {
		return workflowGraphDocumentContract{}, err
	}
	return workflowGraphDocumentContract{schema: schema}, nil
}

func workflowGraphDocumentFromDefinition(definition serverapi.WorkflowDefinition) (workflowGraphDocument, error) {
	return workflowGraphDocumentFromDraft(
		definition.Workflow.ID,
		definition.Workflow.Version,
		serverapi.WorkflowGraphDraftFromDefinition(definition),
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

func (c workflowGraphDocumentContract) Decode(data []byte) (workflowGraphDocument, error) {
	if err := c.schema.Validate(data); err != nil {
		return workflowGraphDocument{}, err
	}
	var document workflowGraphDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return workflowGraphDocument{}, fmt.Errorf("decode Workflow graph document: %w", err)
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
