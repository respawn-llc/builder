package workflowstore

import (
	"context"
	"database/sql"
	"fmt"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

func nullableGraphIdentity(value any) (*string, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		copy := typed
		return &copy, nil
	default:
		return nil, fmt.Errorf("graph identity has unexpected SQLite type %T", value)
	}
}

func nullableGraphIdentityArgument(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func workflowDefinitionFromPreparedGraph(
	prepared preparedWorkflowGraphSave,
	workflowID runtimeids.WorkflowID,
	displayName string,
	executionTargetPolicy workflow.ExecutionTargetPolicy,
) (workflow.Definition, error) {
	definition := workflow.Definition{
		ID:                    workflowID,
		DisplayName:           displayName,
		ExecutionTargetPolicy: executionTargetPolicy,
	}
	groupMemberIDs := map[string][]workflow.NodeID{}
	for _, group := range prepared.nodeGroups {
		definition.NodeGroups = append(definition.NodeGroups, workflow.NodeGroup{
			WorkflowID:    group.WorkflowID,
			ID:            group.ID,
			Key:           group.Key,
			DisplayName:   group.DisplayName,
			SortOrder:     group.SortOrder,
			MemberNodeIDs: groupMemberIDs[group.ID],
		})
	}
	for _, node := range prepared.nodes {
		if node.GroupID != nil {
			groupMemberIDs[*node.GroupID] = append(groupMemberIDs[*node.GroupID], node.ID)
		}
		workflowNode, err := workflowNodeFromRecord(node)
		if err != nil {
			return workflow.Definition{}, err
		}
		definition.Nodes = append(definition.Nodes, workflowNode)
	}
	for index := range definition.NodeGroups {
		definition.NodeGroups[index].MemberNodeIDs = groupMemberIDs[definition.NodeGroups[index].ID]
	}
	for _, group := range prepared.transitionGroups {
		definition.TransitionGroups = append(definition.TransitionGroups, workflow.TransitionGroup{
			WorkflowID:   group.WorkflowID,
			ID:           group.ID,
			SourceNodeID: group.SourceNodeID,
			TransitionID: group.TransitionID,
			DisplayName:  group.DisplayName,
			Description:  group.Description,
		})
	}
	for _, edge := range prepared.edges {
		definition.Edges = append(definition.Edges, workflowEdgeFromRecord(edge))
	}
	return definition, nil
}

func currentWorkflowGraphSavePrepared(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID) (preparedWorkflowGraphSave, error) {
	nodeGroups, err := q.ListWorkflowNodeGroups(ctx, workflowID)
	if err != nil {
		return preparedWorkflowGraphSave{}, err
	}
	nodes, err := q.ListWorkflowNodes(ctx, workflowID)
	if err != nil {
		return preparedWorkflowGraphSave{}, err
	}
	transitionGroups, err := q.ListWorkflowTransitionGroups(ctx, workflowID)
	if err != nil {
		return preparedWorkflowGraphSave{}, err
	}
	edges, err := q.ListWorkflowEdges(ctx, workflowID)
	if err != nil {
		return preparedWorkflowGraphSave{}, err
	}
	prepared := preparedWorkflowGraphSave{
		nodeGroups:       make([]NodeGroupRecord, 0, len(nodeGroups)),
		nodes:            make([]NodeRecord, 0, len(nodes)),
		transitionGroups: make([]TransitionGroupRecord, 0, len(transitionGroups)),
		edges:            make([]EdgeRecord, 0, len(edges)),
	}
	groupKeyByID := make(map[string]string, len(nodeGroups))
	for _, group := range nodeGroups {
		prepared.nodeGroups = append(prepared.nodeGroups, NodeGroupRecord{ID: group.ID, WorkflowID: group.WorkflowID, Key: workflow.ModelKey(group.GroupKey), DisplayName: group.DisplayName, SortOrder: group.SortOrder})
		groupKeyByID[group.ID] = group.GroupKey
	}
	for _, node := range nodes {
		joinProviders := []workflow.JoinInputProvider{}
		if err := workflow.UnmarshalString(node.JoinInputProvidersJson, &joinProviders); err != nil {
			return preparedWorkflowGraphSave{}, err
		}
		groupID, err := nullableGraphIdentity(node.GroupID)
		if err != nil {
			return preparedWorkflowGraphSave{}, err
		}
		scriptPath := ""
		if node.ScriptPath.Valid {
			scriptPath = node.ScriptPath.String
		}
		groupKey := ""
		if groupID != nil {
			groupKey = groupKeyByID[*groupID]
		}
		prepared.nodes = append(prepared.nodes, NodeRecord{ID: workflow.NodeID(node.ID), WorkflowID: node.WorkflowID, Key: workflow.ModelKey(node.NodeKey), Kind: workflow.NodeKind(node.Kind), DisplayName: node.DisplayName, GroupID: groupID, GroupKey: groupKey, SubagentRole: node.SubagentRole, CompletionMode: node.CompletionMode, ScriptPath: scriptPath, JoinInputProviders: joinProviders, SortOrder: node.SortOrder})
	}
	for _, group := range transitionGroups {
		prepared.transitionGroups = append(prepared.transitionGroups, TransitionGroupRecord{ID: workflow.TransitionGroupID(group.ID), WorkflowID: workflowID, SourceNodeID: workflow.NodeID(group.SourceNodeID), TransitionID: workflow.TransitionID(group.TransitionID), DisplayName: group.DisplayName, Description: group.Description, SortOrder: group.SortOrder})
	}
	for _, edge := range edges {
		parameters := []workflow.Parameter{}
		inputs := []workflow.InputBinding{}
		if err := workflow.UnmarshalString(edge.ParametersJson, &parameters); err != nil {
			return preparedWorkflowGraphSave{}, err
		}
		if err := workflow.UnmarshalString(edge.InputBindingsJson, &inputs); err != nil {
			return preparedWorkflowGraphSave{}, err
		}
		requirements := []workflow.OutputRequirement{}
		if err := workflow.UnmarshalString(edge.OutputRequirementsJson, &requirements); err != nil {
			return preparedWorkflowGraphSave{}, err
		}
		prepared.edges = append(prepared.edges, EdgeRecord{
			ID:                 workflow.EdgeID(edge.ID),
			WorkflowID:         edge.WorkflowID,
			TransitionGroupID:  workflow.TransitionGroupID(edge.TransitionGroupID),
			Key:                workflow.ModelKey(edge.EdgeKey),
			TargetNodeID:       workflow.NodeID(edge.TargetNodeID),
			AssigneeSelection:  workflow.AssigneeSelection(edge.AssigneeSelection),
			ThinkingSelection:  workflow.ThinkingSelection(edge.ThinkingSelection),
			RequiresApproval:   edge.RequiresApproval != 0,
			ContextMode:        workflow.ContextMode(edge.ContextMode),
			ContextSource:      workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(edge.ContextSourceKind), NodeKey: workflow.ModelKey(edge.ContextSourceNodeKey)}),
			PromptTemplate:     edge.PromptTemplate,
			Parameters:         parameters,
			InputBindings:      inputs,
			OutputRequirements: requirements,
			SortOrder:          edge.SortOrder,
		})
	}
	return prepared, nil
}

func workflowEdgeFromRecord(edge EdgeRecord) workflow.Edge {
	return workflow.Edge{
		WorkflowID:         edge.WorkflowID,
		ID:                 edge.ID,
		Key:                edge.Key,
		TransitionGroupID:  edge.TransitionGroupID,
		TargetNodeID:       edge.TargetNodeID,
		AssigneeSelection:  edge.AssigneeSelection,
		ThinkingSelection:  edge.ThinkingSelection,
		ContextMode:        edge.ContextMode,
		ContextSource:      workflow.CanonicalContextSource(edge.ContextSource),
		RequiresApproval:   edge.RequiresApproval,
		PromptTemplate:     edge.PromptTemplate,
		Parameters:         edge.Parameters,
		InputBindings:      edge.InputBindings,
		OutputRequirements: edge.OutputRequirements,
	}
}
