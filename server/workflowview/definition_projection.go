package workflowview

import (
	"context"
	"errors"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflowstore"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/workflowcontract"
)

type DefinitionProjection struct {
	store   *workflowstore.Store
	catalog workflow.TargetAgentCatalog
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
	return &DefinitionProjection{store: store, catalog: store.TargetAgentCatalog()}, nil
}

func workflowPickerItem(def serverapi.WorkflowDefinition, link sqlitegen.ProjectWorkflowLinkRecord, validation *workflow.ValidationResult) serverapi.WorkflowPickerItem {
	item := serverapi.WorkflowPickerItem{WorkflowID: def.Workflow.ID, DisplayName: def.Workflow.Name, Description: def.Workflow.Description, Version: def.Workflow.Version, IsProjectDefault: link.ID != "" && link.IsDefault != 0, ValidForTaskCreation: link.ID != ""}
	if validation != nil {
		item.ValidForTaskCreation = link.ID != "" && !validation.HasBlockingErrors()
		item.ValidationErrors = ValidationErrors(workflow.WorkflowIDPointer(def.Workflow.ID), validation.Errors)
	}
	return item
}

func (p *DefinitionProjection) GetDefinition(ctx context.Context, workflowID runtimeids.WorkflowID) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind, error) {
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

func (p *DefinitionProjection) snapshot(ctx context.Context, workflowID runtimeids.WorkflowID) (definitionSnapshot, error) {
	if p == nil {
		return definitionSnapshot{}, errors.New("definition projection is required")
	}
	if workflowID.IsZero() {
		return definitionSnapshot{}, errors.New("workflow_id is required")
	}
	domain, record, err := p.store.GetDefinition(ctx, workflowID)
	if err != nil {
		return definitionSnapshot{}, err
	}
	api, nodeKinds := ProjectDefinition(domain, record, p.catalog)
	return definitionSnapshot{domain: domain, api: api, nodeKinds: nodeKinds}, nil
}

// ProjectDefinition is the canonical pure domain-to-API workflow projection.
func ProjectDefinition(def workflow.Definition, record workflowstore.WorkflowRecord, catalogs ...workflow.TargetAgentCatalog) (serverapi.WorkflowDefinition, map[string]workflow.NodeKind) {
	api := serverapi.WorkflowDefinition{
		Workflow: serverapi.WorkflowRecord{
			ID:                    record.ID,
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
			WorkflowID:  group.WorkflowID,
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
		joinProviders := workflow.NodeJoinInputProviders(node)
		projectedJoinProviders := make([]serverapi.WorkflowJoinInputProvider, 0, len(joinProviders))
		for _, provider := range joinProviders {
			projectedJoinProviders = append(projectedJoinProviders, serverapi.WorkflowJoinInputProvider{
				InputName:      provider.InputName,
				ProviderEdgeID: string(provider.ProviderEdgeID),
			})
		}
		nodeID := string(identity.ID)
		groupKey := ""
		if identity.GroupID != nil {
			groupKey = groupKeyByID[*identity.GroupID]
		}
		api.Nodes = append(api.Nodes, serverapi.WorkflowNode{
			ID:                 nodeID,
			WorkflowID:         identity.WorkflowID,
			Key:                string(identity.Key),
			Kind:               string(node.Kind()),
			DisplayName:        identity.DisplayName,
			GroupID:            textutil.Pointer(identity.GroupID),
			GroupKey:           groupKey,
			SubagentRole:       workflow.NodeSubagentRole(node),
			CompletionMode:     workflow.NodeCompletionMode(node),
			ScriptPath:         scriptPath,
			JoinInputProviders: projectedJoinProviders,
		})
		nodeKinds[nodeID] = node.Kind()
	}
	for _, group := range def.TransitionGroups {
		api.TransitionGroups = append(api.TransitionGroups, serverapi.WorkflowTransitionGroup{
			ID:           string(group.ID),
			WorkflowID:   group.WorkflowID,
			SourceNodeID: string(group.SourceNodeID),
			TransitionID: string(group.TransitionID),
			DisplayName:  group.DisplayName,
			Description:  group.Description,
		})
	}
	for _, edge := range def.Edges {
		parameters := make([]serverapi.WorkflowParameter, 0, len(edge.Parameters))
		for _, parameter := range edge.Parameters {
			parameters = append(parameters, serverapi.WorkflowParameter{Key: parameter.Key, Description: parameter.Description, Purpose: string(workflow.CanonicalParameterPurpose(parameter.Purpose))})
		}
		requirements := make([]serverapi.WorkflowOutputRequirement, 0, len(edge.OutputRequirements))
		for _, requirement := range edge.OutputRequirements {
			requirements = append(requirements, serverapi.WorkflowOutputRequirement{FieldName: requirement.FieldName})
		}
		api.Edges = append(api.Edges, serverapi.WorkflowEdge{
			ID:                 string(edge.ID),
			WorkflowID:         edge.WorkflowID,
			TransitionGroupID:  string(edge.TransitionGroupID),
			Key:                string(edge.Key),
			TargetNodeID:       string(edge.TargetNodeID),
			AssigneeSelection:  string(workflow.CanonicalAssigneeSelection(edge.AssigneeSelection)),
			ThinkingSelection:  string(workflow.CanonicalThinkingSelection(edge.ThinkingSelection)),
			RequiresApproval:   edge.RequiresApproval,
			ContextMode:        string(edge.ContextMode),
			ContextSource:      apiContextSource(edge.ContextSource),
			PromptTemplate:     edge.PromptTemplate,
			Parameters:         parameters,
			InputBindings:      InputBindings(edge.InputBindings),
			OutputRequirements: requirements,
		})
	}
	api.DerivedWiring = DerivedWiring(def, catalogs...)
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
		Mode:      workflowcontract.ExecutionTargetMode(canonical.Mode),
		CustomRef: customRef,
	}
}
