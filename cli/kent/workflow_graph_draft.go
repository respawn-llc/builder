package main

import (
	"context"
	"errors"

	"core/shared/serverapi"
	"core/shared/textutil"
)

type workflowGraphPreview struct {
	Graph    serverapi.WorkflowGraphDraft
	Response serverapi.WorkflowGraphSavePreviewResponse
}

func workflowGraphDraftFromDefinition(def serverapi.WorkflowDefinition) serverapi.WorkflowGraphDraft {
	graph := serverapi.WorkflowGraphDraft{
		NodeGroups:       make([]serverapi.WorkflowGraphDraftNodeGroup, 0, len(def.NodeGroups)),
		Nodes:            make([]serverapi.WorkflowGraphDraftNode, 0, len(def.Nodes)),
		TransitionGroups: make([]serverapi.WorkflowGraphDraftTransitionGroup, 0, len(def.TransitionGroups)),
		Edges:            make([]serverapi.WorkflowGraphDraftEdge, 0, len(def.Edges)),
	}
	for _, group := range def.NodeGroups {
		graph.NodeGroups = append(graph.NodeGroups, serverapi.WorkflowGraphDraftNodeGroup{
			ID: group.GroupID, Key: group.GroupKey, DisplayName: group.DisplayName,
		})
	}
	for _, node := range def.Nodes {
		graph.Nodes = append(graph.Nodes, serverapi.WorkflowGraphDraftNode{
			ID: node.ID, Key: node.Key, Kind: node.Kind, DisplayName: node.DisplayName,
			GroupID: textutil.OptionalExactString(node.GroupID), GroupKey: node.GroupKey, SubagentRole: node.SubagentRole,
			CompletionMode: node.CompletionMode, ScriptPath: node.ScriptPath,
			JoinInputProviders: node.JoinInputProviders,
		})
	}
	for _, group := range def.TransitionGroups {
		graph.TransitionGroups = append(graph.TransitionGroups, serverapi.WorkflowGraphDraftTransitionGroup{
			ID: group.ID, SourceNodeID: group.SourceNodeID, TransitionID: group.TransitionID,
			DisplayName: group.DisplayName, Description: group.Description,
		})
	}
	for _, edge := range def.Edges {
		graph.Edges = append(graph.Edges, serverapi.WorkflowGraphDraftEdge{
			ID: edge.ID, TransitionGroupID: edge.TransitionGroupID, Key: edge.Key,
			TargetNodeID: edge.TargetNodeID, AssigneeSelection: edge.AssigneeSelection,
			ThinkingSelection: edge.ThinkingSelection, RequiresApproval: edge.RequiresApproval,
			ContextMode: edge.ContextMode, ContextSource: edge.ContextSource,
			PromptTemplate: edge.PromptTemplate, Parameters: cloneWorkflowParameters(edge.Parameters),
		})
	}
	return graph
}

func previewWorkflowGraphDraft(
	ctx context.Context,
	remote workflowCommandRemote,
	current serverapi.WorkflowDefinition,
	submitted serverapi.WorkflowGraphDraft,
) (workflowGraphPreview, error) {
	if remote == nil {
		return workflowGraphPreview{}, errors.New("workflow service is required")
	}
	response, err := remote.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      current.Workflow.ID,
		ExpectedVersion: current.Workflow.Version,
		Graph:           submitted,
	})
	if err != nil {
		return workflowGraphPreview{}, err
	}
	return workflowGraphPreview{Graph: submitted, Response: response}, nil
}
