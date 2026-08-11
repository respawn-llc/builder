package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/shared/apicontract"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type workflowGraphDraftMutation[T any] func(serverapi.WorkflowGraphDraft) (serverapi.WorkflowGraphDraft, T, error)

type workflowGraphMutationResult struct{ Version int64 }

func workflowGraphMutationBlocked(workflowID runtimeids.WorkflowID, blockers []serverapi.WorkflowGraphSaveBlocker) error {
	if len(blockers) == 0 {
		return fmt.Errorf(
			"Workflow %s graph mutation cannot be saved by this high-level command; use `kent workflow graph apply`",
			workflowID,
		)
	}
	codes := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		codes = append(codes, blocker.Code)
	}
	return fmt.Errorf(
		"Workflow %s graph mutation was blocked (%s); use `kent workflow graph apply`",
		workflowID,
		strings.Join(codes, ", "),
	)
}

func runWorkflowGraphMutation[T any](
	ctx context.Context,
	remote apicontract.WorkflowService,
	workflowID runtimeids.WorkflowID,
	mutate workflowGraphDraftMutation[T],
) (T, workflowGraphMutationResult, error) {
	var zero T
	current, err := resolveWorkflowDefinition(ctx, remote, workflowID)
	if err != nil {
		return zero, workflowGraphMutationResult{}, err
	}
	graph, value, err := mutate(serverapi.WorkflowGraphDraftFromDefinition(current))
	if err != nil {
		return zero, workflowGraphMutationResult{}, err
	}
	preview, err := previewWorkflowGraphDraft(ctx, remote, current, graph)
	if err != nil {
		return zero, workflowGraphMutationResult{}, err
	}
	if err := preview.Response.Validate(); err != nil {
		return zero, workflowGraphMutationResult{}, fmt.Errorf("validate Workflow graph save preview: %w", err)
	}
	if preview.Response.ConfirmationRequired || len(preview.Response.Blockers) != 0 || !preview.Response.CanSave {
		return zero, workflowGraphMutationResult{}, workflowGraphMutationBlocked(workflowID, preview.Response.Blockers)
	}
	response, err := remote.SaveWorkflowGraph(ctx, serverapi.WorkflowGraphSaveRequest{
		WorkflowID:      current.Workflow.ID,
		ExpectedVersion: current.Workflow.Version,
		Graph:           preview.Graph,
	})
	if err != nil {
		return zero, workflowGraphMutationResult{}, err
	}
	if err := response.Validate(); err != nil {
		return zero, workflowGraphMutationResult{}, fmt.Errorf("validate Workflow graph save response: %w", err)
	}
	if !response.Saved || response.ConfirmationRequired || len(response.Blockers) != 0 || !response.CanSave {
		return zero, workflowGraphMutationResult{}, workflowGraphMutationBlocked(workflowID, response.Blockers)
	}
	return value, workflowGraphMutationResult{Version: response.CurrentVersion}, nil
}

type workflowNodeUpdateDraftMutation struct {
	NodeKey        string
	Key            *string
	Kind           *string
	DisplayName    *string
	SubagentRole   workflowStringMutation
	CompletionMode workflowStringMutation
	ScriptPath     workflowOptionalStringMutation
}

type workflowStringMutation struct {
	Set   bool
	Value string
}

type workflowOptionalStringMutation struct {
	Set   bool
	Value *string
}

type workflowGraphMutationUsageError struct {
	err error
}

func (e workflowGraphMutationUsageError) Error() string {
	return e.err.Error()
}

func (e workflowGraphMutationUsageError) Unwrap() error {
	return e.err
}

func applyWorkflowGraphMutationValue[T any](target *T, value *T) {
	if value != nil {
		*target = *value
	}
}

type workflowEdgeDraftMutationResult struct {
	Edge          serverapi.WorkflowGraphDraftEdge
	Group         serverapi.WorkflowGraphDraftTransitionGroup
	TargetNodeKey string
}

type workflowEdgeAddDraftMutation struct {
	SourceNodeKey         string
	TransitionID          string
	TransitionDescription workflowStringMutation
	NewTransitionGroupID  string
	Edge                  serverapi.WorkflowGraphDraftEdge
	TargetNodeKey         string
}

type workflowEdgeUpdateDraftMutation struct {
	EdgeID                  string
	TransitionID            *string
	TransitionDisplayName   *string
	TransitionDescription   workflowStringMutation
	EdgeKey                 *string
	TargetNodeKey           *string
	ContextMode             *string
	ContextSource           *serverapi.WorkflowContextSource
	RequiresApproval        *bool
	PromptTemplate          workflowStringMutation
	AssigneeSelection       *string
	ThinkingSelection       *string
	TargetAssigneeParameter *serverapi.WorkflowParameter
	TargetThinkingParameter *serverapi.WorkflowParameter
	OrdinaryParameters      *[]serverapi.WorkflowParameter
}

func addWorkflowNodeDraftMutation(node serverapi.WorkflowGraphDraftNode) workflowGraphDraftMutation[serverapi.WorkflowGraphDraftNode] {
	return func(graph serverapi.WorkflowGraphDraft) (serverapi.WorkflowGraphDraft, serverapi.WorkflowGraphDraftNode, error) {
		graph.Nodes = append(graph.Nodes, node)
		return graph, node, nil
	}
}

func updateWorkflowNodeDraftMutation(update workflowNodeUpdateDraftMutation) workflowGraphDraftMutation[serverapi.WorkflowGraphDraftNode] {
	return func(graph serverapi.WorkflowGraphDraft) (serverapi.WorkflowGraphDraft, serverapi.WorkflowGraphDraftNode, error) {
		nodeKey := strings.TrimSpace(update.NodeKey)
		for index := range graph.Nodes {
			if graph.Nodes[index].Key != nodeKey {
				continue
			}
			applyWorkflowGraphMutationValue(&graph.Nodes[index].Key, update.Key)
			applyWorkflowGraphMutationValue(&graph.Nodes[index].Kind, update.Kind)
			applyWorkflowGraphMutationValue(&graph.Nodes[index].DisplayName, update.DisplayName)
			if update.SubagentRole.Set {
				graph.Nodes[index].SubagentRole = update.SubagentRole.Value
			}
			if update.CompletionMode.Set {
				graph.Nodes[index].CompletionMode = update.CompletionMode.Value
			}
			if update.ScriptPath.Set {
				graph.Nodes[index].ScriptPath = update.ScriptPath.Value
			}
			return graph, graph.Nodes[index], nil
		}
		return serverapi.WorkflowGraphDraft{}, serverapi.WorkflowGraphDraftNode{}, fmt.Errorf("workflow node key %q not found", nodeKey)
	}
}

func addWorkflowEdgeDraftMutation(add workflowEdgeAddDraftMutation) workflowGraphDraftMutation[workflowEdgeDraftMutationResult] {
	return func(graph serverapi.WorkflowGraphDraft) (serverapi.WorkflowGraphDraft, workflowEdgeDraftMutationResult, error) {
		source, err := findWorkflowGraphDraftNodeByKey(graph, add.SourceNodeKey)
		if err != nil {
			return serverapi.WorkflowGraphDraft{}, workflowEdgeDraftMutationResult{}, err
		}
		target, err := findWorkflowGraphDraftNodeByKey(graph, add.TargetNodeKey)
		if err != nil {
			return serverapi.WorkflowGraphDraft{}, workflowEdgeDraftMutationResult{}, err
		}
		transitionID := strings.TrimSpace(add.TransitionID)
		groupIndex := -1
		for index := range graph.TransitionGroups {
			group := graph.TransitionGroups[index]
			if group.SourceNodeID == source.ID && group.TransitionID == transitionID {
				groupIndex = index
				break
			}
		}
		if groupIndex < 0 {
			graph.TransitionGroups = append(graph.TransitionGroups, serverapi.WorkflowGraphDraftTransitionGroup{
				ID:           add.NewTransitionGroupID,
				SourceNodeID: source.ID,
				TransitionID: transitionID,
				DisplayName:  workflowDisplayNameFromKey(transitionID),
				Description:  add.TransitionDescription.Value,
			})
			groupIndex = len(graph.TransitionGroups) - 1
		} else if add.TransitionDescription.Set {
			graph.TransitionGroups[groupIndex].Description = add.TransitionDescription.Value
		}
		add.Edge.TransitionGroupID = graph.TransitionGroups[groupIndex].ID
		add.Edge.TargetNodeID = target.ID
		add.Edge.Parameters = append([]serverapi.WorkflowParameter(nil), add.Edge.Parameters...)
		graph.Edges = append(graph.Edges, add.Edge)
		return graph, workflowEdgeDraftMutationResult{
			Edge:          add.Edge,
			Group:         graph.TransitionGroups[groupIndex],
			TargetNodeKey: target.Key,
		}, nil
	}
}

func updateWorkflowEdgeDraftMutation(update workflowEdgeUpdateDraftMutation) workflowGraphDraftMutation[workflowEdgeDraftMutationResult] {
	return func(graph serverapi.WorkflowGraphDraft) (serverapi.WorkflowGraphDraft, workflowEdgeDraftMutationResult, error) {
		edgeIndex := -1
		edgeID := strings.TrimSpace(update.EdgeID)
		for index := range graph.Edges {
			if graph.Edges[index].ID == edgeID {
				edgeIndex = index
				break
			}
		}
		if edgeIndex < 0 {
			return serverapi.WorkflowGraphDraft{}, workflowEdgeDraftMutationResult{}, fmt.Errorf("workflow edge %q not found", edgeID)
		}
		groupIndex := -1
		for index := range graph.TransitionGroups {
			if graph.TransitionGroups[index].ID == graph.Edges[edgeIndex].TransitionGroupID {
				groupIndex = index
				break
			}
		}
		if groupIndex < 0 {
			return serverapi.WorkflowGraphDraft{}, workflowEdgeDraftMutationResult{}, fmt.Errorf(
				"workflow transition group %q not found",
				graph.Edges[edgeIndex].TransitionGroupID,
			)
		}

		group := &graph.TransitionGroups[groupIndex]
		if update.TransitionID != nil {
			group.TransitionID = *update.TransitionID
		}
		if update.TransitionDisplayName != nil {
			group.DisplayName = *update.TransitionDisplayName
		} else if update.TransitionID != nil {
			group.DisplayName = workflowDisplayNameFromKey(*update.TransitionID)
		}
		if update.TransitionDescription.Set {
			group.Description = update.TransitionDescription.Value
		}

		edge := &graph.Edges[edgeIndex]
		applyWorkflowGraphMutationValue(&edge.AssigneeSelection, update.AssigneeSelection)
		applyWorkflowGraphMutationValue(&edge.ThinkingSelection, update.ThinkingSelection)
		if update.TargetAssigneeParameter != nil && edge.AssigneeSelection != "previous_node" {
			return serverapi.WorkflowGraphDraft{}, workflowEdgeDraftMutationResult{}, workflowGraphMutationUsageError{
				err: errors.New("target-assignee-param requires assignee selection previous_node"),
			}
		}
		if update.TargetThinkingParameter != nil && edge.ThinkingSelection != "previous_node" {
			return serverapi.WorkflowGraphDraft{}, workflowEdgeDraftMutationResult{}, workflowGraphMutationUsageError{
				err: errors.New("target-thinking-param requires thinking selection previous_node"),
			}
		}
		parameters, err := workflowEdgeParametersForUpdate(
			edge.Parameters,
			edge.AssigneeSelection,
			edge.ThinkingSelection,
			update.TargetAssigneeParameter,
			update.TargetThinkingParameter,
			nil,
			false,
		)
		if err != nil {
			return serverapi.WorkflowGraphDraft{}, workflowEdgeDraftMutationResult{}, workflowGraphMutationUsageError{err: err}
		}
		edge.Parameters = parameters
		applyWorkflowGraphMutationValue(&edge.Key, update.EdgeKey)
		targetNodeKey := ""
		if update.TargetNodeKey != nil {
			target, err := findWorkflowGraphDraftNodeByKey(graph, *update.TargetNodeKey)
			if err != nil {
				return serverapi.WorkflowGraphDraft{}, workflowEdgeDraftMutationResult{}, err
			}
			edge.TargetNodeID = target.ID
			targetNodeKey = target.Key
		} else {
			target, err := findWorkflowGraphDraftNodeByID(graph, edge.TargetNodeID)
			if err != nil {
				return serverapi.WorkflowGraphDraft{}, workflowEdgeDraftMutationResult{}, err
			}
			targetNodeKey = target.Key
		}
		applyWorkflowGraphMutationValue(&edge.ContextMode, update.ContextMode)
		applyWorkflowGraphMutationValue(&edge.ContextSource, update.ContextSource)
		applyWorkflowGraphMutationValue(&edge.RequiresApproval, update.RequiresApproval)
		if update.PromptTemplate.Set {
			edge.PromptTemplate = update.PromptTemplate.Value
		}
		if update.OrdinaryParameters != nil {
			parameters, err := workflowEdgeParametersForUpdate(
				edge.Parameters,
				edge.AssigneeSelection,
				edge.ThinkingSelection,
				nil,
				nil,
				*update.OrdinaryParameters,
				len(*update.OrdinaryParameters) == 0,
			)
			if err != nil {
				return serverapi.WorkflowGraphDraft{}, workflowEdgeDraftMutationResult{}, workflowGraphMutationUsageError{err: err}
			}
			edge.Parameters = parameters
		}
		return graph, workflowEdgeDraftMutationResult{
			Edge:          *edge,
			Group:         *group,
			TargetNodeKey: targetNodeKey,
		}, nil
	}
}

func findWorkflowGraphDraftNodeByKey(graph serverapi.WorkflowGraphDraft, key string) (serverapi.WorkflowGraphDraftNode, error) {
	nodeKey := strings.TrimSpace(key)
	for _, node := range graph.Nodes {
		if node.Key == nodeKey {
			return node, nil
		}
	}
	return serverapi.WorkflowGraphDraftNode{}, fmt.Errorf("workflow node key %q not found", nodeKey)
}

func findWorkflowGraphDraftNodeByID(graph serverapi.WorkflowGraphDraft, id string) (serverapi.WorkflowGraphDraftNode, error) {
	for _, node := range graph.Nodes {
		if node.ID == id {
			return node, nil
		}
	}
	return serverapi.WorkflowGraphDraftNode{}, fmt.Errorf("workflow node %q not found", id)
}
