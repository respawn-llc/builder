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
	ExpectedVersion int64                      `json:"expected_version" jsonschema:"minimum=0"`
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
	GroupID            *string                               `json:"group_id" jsonschema:"nullable"`
	SubagentRole       string                                `json:"subagent_role,omitempty"`
	CompletionMode     string                                `json:"completion_mode,omitempty"`
	ScriptPath         *string                               `json:"script_path,omitempty" jsonschema:"nullable"`
	JoinInputProviders []serverapi.WorkflowJoinInputProvider `json:"join_input_providers,omitempty"`
}

func allowWorkflowGraphUnknownFields(schema *invjsonschema.Schema) {
	schema.AdditionalProperties = invjsonschema.TrueSchema
}

type workflowGraphDocumentContractSource struct{}

func (workflowGraphDocumentContractSource) JSONSchema() *invjsonschema.Schema {
	schema := (&invjsonschema.Reflector{
		Anonymous:      true,
		DoNotReference: true,
	}).Reflect(workflowGraphDocument{})
	allowWorkflowGraphUnknownFields(schema)
	schema.Properties.Set("workflow_id", &invjsonschema.Schema{Type: "string"})
	graph := workflowGraphSchemaProperty(schema, "graph")
	allowWorkflowGraphUnknownFields(graph)
	for _, collection := range []string{"node_groups", "nodes", "transition_groups", "edges"} {
		allowWorkflowGraphUnknownFields(workflowGraphSchemaArrayItems(graph, collection))
	}
	node := workflowGraphSchemaArrayItems(graph, "nodes")
	node.PropertyNames = &invjsonschema.Schema{
		Not: &invjsonschema.Schema{Const: "group_key"},
	}
	allowWorkflowGraphUnknownFields(workflowGraphSchemaArrayItems(node, "join_input_providers"))
	edge := workflowGraphSchemaArrayItems(graph, "edges")
	allowWorkflowGraphUnknownFields(workflowGraphSchemaProperty(edge, "context_source"))
	allowWorkflowGraphUnknownFields(workflowGraphSchemaArrayItems(edge, "parameters"))
	return schema
}

func workflowGraphSchemaProperty(schema *invjsonschema.Schema, name string) *invjsonschema.Schema {
	property, found := schema.Properties.Get(name)
	if !found {
		panic(fmt.Sprintf("workflow graph schema is missing property %q", name))
	}
	return property
}

func workflowGraphSchemaArrayItems(schema *invjsonschema.Schema, name string) *invjsonschema.Schema {
	property := workflowGraphSchemaProperty(schema, name)
	if property.Items == nil {
		panic(fmt.Sprintf("workflow graph schema property %q has no item schema", name))
	}
	return property.Items
}

type workflowGraphDocumentContract struct {
	schema jsoncontract.Internal
}

func prepareWorkflowGraphDocumentContract() (workflowGraphDocumentContract, error) {
	schema, err := jsoncontract.NewPreparer(false).Internal(
		"Workflow graph CLI document",
		workflowGraphDocumentContractSource{},
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
	if err := (serverapi.WorkflowGraphValidateDraftRequest{
		WorkflowID: d.WorkflowID,
		Graph:      graph,
		Modes:      []serverapi.WorkflowValidationMode{serverapi.WorkflowValidationModeDraft},
	}).Validate(); err != nil {
		return serverapi.WorkflowGraphDraft{}, err
	}
	return graph, nil
}
