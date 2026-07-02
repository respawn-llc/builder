package workflowstore

import (
	"context"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

func currentWorkflowGraphSavePrepared(ctx context.Context, q *sqlitegen.Queries, workflowID workflow.WorkflowID) (preparedWorkflowGraphSave, error) {
	nodeGroups, err := q.ListWorkflowNodeGroups(ctx, string(workflowID))
	if err != nil {
		return preparedWorkflowGraphSave{}, err
	}
	nodes, err := q.ListWorkflowNodes(ctx, string(workflowID))
	if err != nil {
		return preparedWorkflowGraphSave{}, err
	}
	transitionGroups, err := q.ListWorkflowTransitionGroups(ctx, string(workflowID))
	if err != nil {
		return preparedWorkflowGraphSave{}, err
	}
	edges, err := q.ListWorkflowEdges(ctx, string(workflowID))
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
		prepared.nodeGroups = append(prepared.nodeGroups, NodeGroupRecord{ID: group.ID, WorkflowID: workflow.WorkflowID(group.WorkflowID), Key: workflow.ModelKey(group.GroupKey), DisplayName: group.DisplayName, SortOrder: group.SortOrder})
		groupKeyByID[group.ID] = group.GroupKey
	}
	for _, node := range nodes {
		inputFields := []workflow.InputField{}
		if err := workflow.UnmarshalString(node.InputFieldsJson, &inputFields); err != nil {
			return preparedWorkflowGraphSave{}, err
		}
		joinProviders := []workflow.JoinInputProvider{}
		if err := workflow.UnmarshalString(node.JoinInputProvidersJson, &joinProviders); err != nil {
			return preparedWorkflowGraphSave{}, err
		}
		outputFields := []workflow.OutputField{}
		if err := workflow.UnmarshalString(node.OutputFieldsJson, &outputFields); err != nil {
			return preparedWorkflowGraphSave{}, err
		}
		groupID := ""
		if node.GroupID.Valid {
			groupID = node.GroupID.String
		}
		scriptPath := ""
		if node.ScriptPath.Valid {
			scriptPath = node.ScriptPath.String
		}
		prepared.nodes = append(prepared.nodes, NodeRecord{ID: workflow.NodeID(node.ID), WorkflowID: workflow.WorkflowID(node.WorkflowID), Key: workflow.ModelKey(node.NodeKey), Kind: workflow.NodeKind(node.Kind), DisplayName: node.DisplayName, GroupID: groupID, GroupKey: groupKeyByID[groupID], SubagentRole: node.SubagentRole, PromptTemplate: node.PromptTemplate, CompletionMode: node.CompletionMode, ScriptPath: scriptPath, InputFields: inputFields, JoinInputProviders: joinProviders, OutputFields: outputFields})
	}
	for _, group := range transitionGroups {
		prepared.transitionGroups = append(prepared.transitionGroups, TransitionGroupRecord{ID: workflow.TransitionGroupID(group.ID), WorkflowID: workflow.WorkflowID(group.WorkflowID), SourceNodeID: workflow.NodeID(group.SourceNodeID), TransitionID: workflow.TransitionID(group.TransitionID), DisplayName: group.DisplayName, Description: group.Description})
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
			WorkflowID:         workflow.WorkflowID(edge.WorkflowID),
			TransitionGroupID:  workflow.TransitionGroupID(edge.TransitionGroupID),
			Key:                workflow.ModelKey(edge.EdgeKey),
			TargetNodeID:       workflow.NodeID(edge.TargetNodeID),
			RequiresApproval:   edge.RequiresApproval != 0,
			ContextMode:        workflow.ContextMode(edge.ContextMode),
			ContextSource:      workflow.CanonicalContextSource(workflow.ContextSource{Kind: workflow.ContextSourceKind(edge.ContextSourceKind), NodeKey: workflow.ModelKey(edge.ContextSourceNodeKey)}),
			PromptTemplate:     edge.PromptTemplate,
			Parameters:         parameters,
			InputBindings:      inputs,
			OutputRequirements: requirements,
		})
	}
	return prepared, nil
}

func withWorkflowGraphNode(prepared preparedWorkflowGraphSave, node NodeRecord) preparedWorkflowGraphSave {
	out := clonePreparedWorkflowGraphSave(prepared)
	out.nodes = upsertWorkflowGraphRecord(out.nodes, node, func(node NodeRecord) workflow.NodeID { return node.ID })
	return out
}

func withoutWorkflowGraphNode(prepared preparedWorkflowGraphSave, nodeID workflow.NodeID) preparedWorkflowGraphSave {
	out := clonePreparedWorkflowGraphSave(prepared)
	out.nodes = removeWorkflowGraphRecord(out.nodes, nodeID, func(node NodeRecord) workflow.NodeID { return node.ID })
	return out
}

func withWorkflowGraphNodeGroup(prepared preparedWorkflowGraphSave, group NodeGroupRecord) preparedWorkflowGraphSave {
	out := clonePreparedWorkflowGraphSave(prepared)
	out.nodeGroups = upsertWorkflowGraphRecord(out.nodeGroups, group, func(group NodeGroupRecord) string { return group.ID })
	return out
}

func withoutWorkflowGraphNodeGroup(prepared preparedWorkflowGraphSave, groupID string) preparedWorkflowGraphSave {
	out := clonePreparedWorkflowGraphSave(prepared)
	out.nodeGroups = removeWorkflowGraphRecord(out.nodeGroups, groupID, func(group NodeGroupRecord) string { return group.ID })
	return out
}

func withWorkflowGraphTransitionGroup(prepared preparedWorkflowGraphSave, group TransitionGroupRecord) preparedWorkflowGraphSave {
	out := clonePreparedWorkflowGraphSave(prepared)
	out.transitionGroups = upsertWorkflowGraphRecord(out.transitionGroups, group, func(group TransitionGroupRecord) workflow.TransitionGroupID { return group.ID })
	return out
}

func withWorkflowGraphEdge(prepared preparedWorkflowGraphSave, edge EdgeRecord) preparedWorkflowGraphSave {
	out := clonePreparedWorkflowGraphSave(prepared)
	out.edges = upsertWorkflowGraphRecord(out.edges, edge, func(edge EdgeRecord) workflow.EdgeID { return edge.ID })
	return out
}

func withoutWorkflowGraphEdge(prepared preparedWorkflowGraphSave, edgeID workflow.EdgeID) preparedWorkflowGraphSave {
	out := clonePreparedWorkflowGraphSave(prepared)
	out.edges = removeWorkflowGraphRecord(out.edges, edgeID, func(edge EdgeRecord) workflow.EdgeID { return edge.ID })
	return out
}

func upsertWorkflowGraphRecord[T any, ID comparable](records []T, record T, id func(T) ID) []T {
	recordID := id(record)
	for i, current := range records {
		if id(current) == recordID {
			records[i] = record
			return records
		}
	}
	return append(records, record)
}

func removeWorkflowGraphRecord[T any, ID comparable](records []T, recordID ID, id func(T) ID) []T {
	filtered := make([]T, 0, len(records))
	for _, record := range records {
		if id(record) != recordID {
			filtered = append(filtered, record)
		}
	}
	return filtered
}

func clonePreparedWorkflowGraphSave(prepared preparedWorkflowGraphSave) preparedWorkflowGraphSave {
	return preparedWorkflowGraphSave{
		nodeGroups:       append([]NodeGroupRecord(nil), prepared.nodeGroups...),
		nodes:            append([]NodeRecord(nil), prepared.nodes...),
		transitionGroups: append([]TransitionGroupRecord(nil), prepared.transitionGroups...),
		edges:            append([]EdgeRecord(nil), prepared.edges...),
	}
}
