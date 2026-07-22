package workflowstore

import (
	"context"
	"testing"

	"core/server/workflow"
)

func workflowGraphSaveRequestFromDefinition(workflowID workflow.WorkflowID, revision int64, confirmed bool, definition workflow.Definition) WorkflowGraphSaveRequest {
	request := WorkflowGraphSaveRequest{WorkflowID: workflowID, ExpectedVersion: revision, Confirmed: confirmed}
	groupKeyByID := make(map[string]string, len(definition.NodeGroups))
	for index, group := range definition.NodeGroups {
		request.NodeGroups = append(request.NodeGroups, NodeGroupRecord{
			ID:          group.ID,
			WorkflowID:  workflowID,
			Key:         group.Key,
			DisplayName: group.DisplayName,
			SortOrder:   int64(index * 100),
		})
		groupKeyByID[group.ID] = string(group.Key)
	}
	for _, node := range definition.Nodes {
		groupID := workflow.NodeGroupID(node)
		request.Nodes = append(request.Nodes, NodeRecord{
			ID:                 workflow.NodeIDOf(node),
			WorkflowID:         workflowID,
			Key:                workflow.NodeKey(node),
			Kind:               node.Kind(),
			DisplayName:        workflow.NodeDisplayName(node),
			GroupID:            groupID,
			GroupKey:           groupKeyByID[groupID],
			SubagentRole:       workflow.NodeSubagentRole(node),
			PromptTemplate:     workflow.NodePromptTemplate(node),
			CompletionMode:     workflow.NodeCompletionMode(node),
			ScriptPath:         workflow.NodeScriptPath(node).String(),
			InputFields:        workflow.NodeInputFields(node),
			JoinInputProviders: workflow.NodeJoinInputProviders(node),
			OutputFields:       workflow.NodeOutputFields(node),
		})
	}
	for _, group := range definition.TransitionGroups {
		request.TransitionGroups = append(request.TransitionGroups, TransitionGroupRecord{
			ID:           group.ID,
			WorkflowID:   workflowID,
			SourceNodeID: group.SourceNodeID,
			TransitionID: group.TransitionID,
			DisplayName:  group.DisplayName,
			Description:  group.Description,
		})
	}
	for _, edge := range definition.Edges {
		request.Edges = append(request.Edges, EdgeRecord{
			ID:                 edge.ID,
			WorkflowID:         workflowID,
			TransitionGroupID:  edge.TransitionGroupID,
			Key:                edge.Key,
			TargetNodeID:       edge.TargetNodeID,
			ContextMode:        edge.ContextMode,
			ContextSource:      edge.ContextSource,
			RequiresApproval:   edge.RequiresApproval,
			PromptTemplate:     edge.PromptTemplate,
			Parameters:         edge.Parameters,
			InputBindings:      edge.InputBindings,
			OutputRequirements: edge.OutputRequirements,
		})
	}
	return request
}

func saveWorkflowGraphFixture(t *testing.T, ctx context.Context, store *Store, workflowID workflow.WorkflowID, edit func(workflow.Definition, *WorkflowGraphSaveRequest)) WorkflowGraphSaveResult {
	t.Helper()
	definition, record, err := store.GetDefinition(ctx, workflowID)
	if err != nil {
		t.Fatalf("GetDefinition workflow fixture: %v", err)
	}
	request := workflowGraphSaveRequestFromDefinition(workflowID, record.Version, false, definition)
	edit(definition, &request)
	result, err := store.SaveWorkflowGraph(ctx, request)
	if err != nil {
		t.Fatalf("SaveWorkflowGraph workflow fixture: %v", err)
	}
	if !result.Saved {
		t.Fatalf("SaveWorkflowGraph workflow fixture rejected: blockers=%+v validation=%+v", result.Blockers, result.ValidationErrors)
	}
	return result
}

func workflowGraphSaveEdgeRecord(t *testing.T, edges []EdgeRecord, edgeID workflow.EdgeID) *EdgeRecord {
	t.Helper()
	for index := range edges {
		if edges[index].ID == edgeID {
			return &edges[index]
		}
	}
	t.Fatalf("edge record %q missing from %+v", edgeID, edges)
	return nil
}
