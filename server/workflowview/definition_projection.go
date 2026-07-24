package workflowview

import (
	"context"
	"errors"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/serverapi"
)

type DefinitionProjection struct {
	store *workflowstore.Store
}

type definitionSnapshot struct {
	domain    workflow.Definition
	api       serverapi.WorkflowDefinition
	nodeKinds map[string]workflow.NodeKind
}

func NewDefinitionProjection(store *workflowstore.Store) (*DefinitionProjection, error) {
	if store == nil {
		return nil, errors.New("workflow store is required")
	}
	return &DefinitionProjection{store: store}, nil
}

func workflowPickerItem(def serverapi.WorkflowDefinition, link sqlitegen.ProjectWorkflowLinkRecord, validation *workflow.ValidationResult) serverapi.WorkflowPickerItem {
	item := serverapi.WorkflowPickerItem{WorkflowID: def.Workflow.ID, DisplayName: def.Workflow.Name, Description: def.Workflow.Description, Version: def.Workflow.Version, IsProjectDefault: link.ID != "" && link.IsDefault != 0, ValidForTaskCreation: link.ID != ""}
	if validation != nil {
		item.ValidForTaskCreation = link.ID != "" && !validation.HasBlockingErrors()
		item.ValidationErrors = ValidationErrors(def.Workflow.ID, validation.Errors)
	}
	return item
}

func (p *DefinitionProjection) GetDefinition(ctx context.Context, workflowID string) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error) {
	snapshot, err := p.snapshot(ctx, workflowID)
	if err != nil {
		return serverapi.WorkflowDefinition{}, nil, err
	}
	return snapshot.api, snapshot.nodeKinds, nil
}

func (p *DefinitionProjection) CurrentNodesByTask(ctx context.Context, taskIDs []workflow.TaskID) (map[workflow.TaskID][]workflow.CurrentNode, error) {
	if p == nil || p.store == nil {
		return nil, errors.New("definition projection is required")
	}
	return p.store.ListCurrentNodesByTask(ctx, taskIDs)
}

func workflowNodesByID(def serverapi.WorkflowDefinition) map[string]serverapi.WorkflowNode {
	nodes := make(map[string]serverapi.WorkflowNode, len(def.Nodes))
	for _, node := range def.Nodes {
		nodes[node.ID] = node
	}
	return nodes
}

func (p *DefinitionProjection) snapshot(ctx context.Context, workflowID string) (definitionSnapshot, error) {
	if p == nil {
		return definitionSnapshot{}, errors.New("definition projection is required")
	}
	trimmedWorkflowID := strings.TrimSpace(workflowID)
	if trimmedWorkflowID == "" {
		return definitionSnapshot{}, errors.New("workflow_id is required")
	}
	domain, record, err := p.store.GetDefinition(ctx, workflow.WorkflowID(trimmedWorkflowID))
	if err != nil {
		return definitionSnapshot{}, err
	}
	api, nodeKinds := projectDefinition(domain, record)
	return definitionSnapshot{domain: domain, api: api, nodeKinds: nodeKinds}, nil
}

func projectDefinition(def workflow.Definition, record workflowstore.WorkflowRecord) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind) {
	api := serverapi.WorkflowDefinition{
		Workflow: serverapi.WorkflowRecord{
			ID:                    string(record.ID),
			Name:                  record.Name,
			Description:           record.Description,
			Version:               record.Version,
			ExecutionTargetPolicy: projectExecutionTargetPolicy(record.ExecutionTargetPolicy),
		},
	}
	if record.ProjectLink != nil {
		api.Workflow.ProjectLink = &serverapi.WorkflowListProjectLink{Default: record.ProjectLink.Default}
	}
	groupKeyByID := make(map[string]string, len(def.NodeGroups))
	for _, group := range def.NodeGroups {
		groupKeyByID[group.ID] = string(group.Key)
		api.NodeGroups = append(api.NodeGroups, serverapi.WorkflowNodeGroup{
			GroupID:     group.ID,
			WorkflowID:  string(group.WorkflowID),
			GroupKey:    string(group.Key),
			DisplayName: group.DisplayName,
			SortOrder:   int(group.SortOrder),
		})
	}
	nodeKinds := make(map[string]workflow.NodeKind, len(def.Nodes))
	for _, node := range def.Nodes {
		identity := node.Identity()
		var scriptPath *string
		if value, present := workflow.NodeScriptPath(node).Value(); present {
			scriptPath = &value
		}
		inputFields := workflow.NodeInputFields(node)
		projectedInputs := make([]serverapi.WorkflowInputField, 0, len(inputFields))
		for _, field := range inputFields {
			projectedInputs = append(projectedInputs, serverapi.WorkflowInputField{Name: field.Name, Description: field.Description})
		}
		joinProviders := workflow.NodeJoinInputProviders(node)
		projectedJoinProviders := make([]serverapi.WorkflowJoinInputProvider, 0, len(joinProviders))
		for _, provider := range joinProviders {
			projectedJoinProviders = append(projectedJoinProviders, serverapi.WorkflowJoinInputProvider{
				InputName:      provider.InputName,
				ProviderEdgeID: string(provider.ProviderEdgeID),
			})
		}
		nodeID := string(identity.ID)
		api.Nodes = append(api.Nodes, serverapi.WorkflowNode{
			ID:                 nodeID,
			WorkflowID:         string(identity.WorkflowID),
			Key:                string(identity.Key),
			Kind:               string(node.Kind()),
			DisplayName:        identity.DisplayName,
			GroupID:            identity.GroupID,
			GroupKey:           groupKeyByID[identity.GroupID],
			SubagentRole:       workflow.NodeSubagentRole(node),
			PromptTemplate:     workflow.NodePromptTemplate(node),
			CompletionMode:     workflow.NodeCompletionMode(node),
			ScriptPath:         scriptPath,
			InputFields:        projectedInputs,
			JoinInputProviders: projectedJoinProviders,
			OutputFields:       OutputFields(workflow.NodeOutputFields(node)),
		})
		nodeKinds[nodeID] = node.Kind()
	}
	for _, group := range def.TransitionGroups {
		api.TransitionGroups = append(api.TransitionGroups, serverapi.WorkflowTransitionGroup{
			ID:           string(group.ID),
			WorkflowID:   string(group.WorkflowID),
			SourceNodeID: string(group.SourceNodeID),
			TransitionID: string(group.TransitionID),
			DisplayName:  group.DisplayName,
			Description:  group.Description,
		})
	}
	for _, edge := range def.Edges {
		parameters := make([]serverapi.WorkflowParameter, 0, len(edge.Parameters))
		for _, parameter := range edge.Parameters {
			parameters = append(parameters, serverapi.WorkflowParameter{Key: parameter.Key, Description: parameter.Description})
		}
		requirements := make([]serverapi.WorkflowOutputRequirement, 0, len(edge.OutputRequirements))
		for _, requirement := range edge.OutputRequirements {
			requirements = append(requirements, serverapi.WorkflowOutputRequirement{FieldName: requirement.FieldName})
		}
		api.Edges = append(api.Edges, serverapi.WorkflowEdge{
			ID:                 string(edge.ID),
			WorkflowID:         string(edge.WorkflowID),
			TransitionGroupID:  string(edge.TransitionGroupID),
			Key:                string(edge.Key),
			TargetNodeID:       string(edge.TargetNodeID),
			RequiresApproval:   edge.RequiresApproval,
			ContextMode:        string(edge.ContextMode),
			ContextSource:      apiContextSource(edge.ContextSource),
			PromptTemplate:     edge.PromptTemplate,
			Parameters:         parameters,
			InputBindings:      InputBindings(edge.InputBindings),
			OutputRequirements: requirements,
		})
	}
	api.DerivedWiring = DerivedWiring(def)
	return api, nodeKinds
}

func projectExecutionTargetPolicy(policy workflow.ExecutionTargetPolicy) serverapi.WorkflowExecutionTargetConfiguration {
	canonical := policy.Canonical()
	var customRef *string
	if canonical.CustomRef != nil {
		value := *canonical.CustomRef
		customRef = &value
	}
	return serverapi.WorkflowExecutionTargetConfiguration{
		Mode:      serverapi.WorkflowExecutionTargetMode(canonical.Mode),
		CustomRef: customRef,
	}
}
