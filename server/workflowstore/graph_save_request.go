package workflowstore

import (
	"slices"

	"core/server/workflow"
)

func NewWorkflowGraphSaveRequest(definition workflow.Definition, expectedVersion int64) WorkflowGraphSaveRequest {
	request := WorkflowGraphSaveRequest{
		WorkflowID:      definition.ID,
		ExpectedVersion: expectedVersion,
	}
	groupKeyByID := make(map[string]string, len(definition.NodeGroups))
	for index, group := range definition.NodeGroups {
		request.NodeGroups = append(request.NodeGroups, NodeGroupRecord{
			ID: group.ID, WorkflowID: definition.ID, Key: group.Key,
			DisplayName: group.DisplayName, SortOrder: int64(index * 100),
		})
		groupKeyByID[group.ID] = string(group.Key)
	}
	for _, node := range definition.Nodes {
		groupID, groupPresent := workflow.NodeGroupID(node)
		var groupIDPointer *string
		groupKey := ""
		if groupPresent {
			groupIDPointer = &groupID
			groupKey = groupKeyByID[groupID]
		}
		request.Nodes = append(request.Nodes, NodeRecord{
			ID: workflow.NodeIDOf(node), WorkflowID: definition.ID, Key: workflow.NodeKey(node),
			Kind: node.Kind(), DisplayName: workflow.NodeDisplayName(node),
			GroupID: groupIDPointer, GroupKey: groupKey,
			SubagentRole:       workflow.NodeSubagentRole(node),
			CompletionMode:     workflow.NodeCompletionMode(node),
			ScriptPath:         workflow.NodeScriptPath(node).String(),
			JoinInputProviders: workflow.NodeJoinInputProviders(node),
		})
	}
	for _, group := range definition.TransitionGroups {
		request.TransitionGroups = append(request.TransitionGroups, TransitionGroupRecord{
			ID: group.ID, WorkflowID: definition.ID, SourceNodeID: group.SourceNodeID,
			TransitionID: group.TransitionID, DisplayName: group.DisplayName,
			Description: group.Description,
		})
	}
	for _, edge := range definition.Edges {
		request.Edges = append(request.Edges, EdgeRecord{
			ID: edge.ID, WorkflowID: definition.ID, TransitionGroupID: edge.TransitionGroupID,
			Key: edge.Key, TargetNodeID: edge.TargetNodeID,
			AssigneeSelection: edge.AssigneeSelection, ThinkingSelection: edge.ThinkingSelection,
			RequiresApproval: edge.RequiresApproval, ContextMode: edge.ContextMode,
			ContextSource: edge.ContextSource, PromptTemplate: edge.PromptTemplate,
			Parameters:         slices.Clone(edge.Parameters),
			InputBindings:      slices.Clone(edge.InputBindings),
			OutputRequirements: slices.Clone(edge.OutputRequirements),
		})
	}
	return request
}
