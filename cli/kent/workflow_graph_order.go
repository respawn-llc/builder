package main

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"core/shared/serverapi"
)

type workflowGraphPreview struct {
	Graph    serverapi.WorkflowGraphDraft
	Response serverapi.WorkflowGraphSavePreviewResponse
}

func canonicalWorkflowGraphDraftFromDefinition(definition serverapi.WorkflowDefinition) (serverapi.WorkflowGraphDraft, error) {
	return normalizeWorkflowGraphDraftOrder(serverapi.WorkflowDefinition{}, workflowGraphDraftFromDefinition(definition))
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
	graph, err := normalizeWorkflowGraphDraftOrder(current, submitted)
	if err != nil {
		return workflowGraphPreview{}, err
	}
	response, err := remote.PreviewWorkflowGraphSave(ctx, serverapi.WorkflowGraphSavePreviewRequest{
		WorkflowID:      current.Workflow.ID,
		ExpectedVersion: current.Workflow.Version,
		Graph:           graph,
	})
	if err != nil {
		return workflowGraphPreview{}, err
	}
	return workflowGraphPreview{Graph: graph, Response: response}, nil
}

func normalizeWorkflowGraphDraftOrder(
	current serverapi.WorkflowDefinition,
	submitted serverapi.WorkflowGraphDraft,
) (serverapi.WorkflowGraphDraft, error) {
	nodeGroups, err := normalizeWorkflowGraphEntities(
		current.NodeGroups,
		submitted.NodeGroups,
		func(group serverapi.WorkflowNodeGroup) string { return group.GroupID },
		func(group serverapi.WorkflowGraphDraftNodeGroup) string { return group.ID },
		func(left, right serverapi.WorkflowGraphDraftNodeGroup) bool {
			return left.Key < right.Key || left.Key == right.Key && left.ID < right.ID
		},
	)
	if err != nil {
		return serverapi.WorkflowGraphDraft{}, fmt.Errorf("normalize Node Groups: %w", err)
	}
	nodes, err := normalizeWorkflowGraphEntities(
		current.Nodes,
		submitted.Nodes,
		func(node serverapi.WorkflowNode) string { return node.ID },
		func(node serverapi.WorkflowGraphDraftNode) string { return node.ID },
		func(left, right serverapi.WorkflowGraphDraftNode) bool {
			return left.Key < right.Key || left.Key == right.Key && left.ID < right.ID
		},
	)
	if err != nil {
		return serverapi.WorkflowGraphDraft{}, fmt.Errorf("normalize Nodes: %w", err)
	}
	transitionGroups, err := normalizeWorkflowGraphEntities(
		current.TransitionGroups,
		submitted.TransitionGroups,
		func(group serverapi.WorkflowTransitionGroup) string { return group.ID },
		func(group serverapi.WorkflowGraphDraftTransitionGroup) string { return group.ID },
		func(left, right serverapi.WorkflowGraphDraftTransitionGroup) bool {
			if left.SourceNodeID != right.SourceNodeID {
				return left.SourceNodeID < right.SourceNodeID
			}
			if left.TransitionID != right.TransitionID {
				return left.TransitionID < right.TransitionID
			}
			return left.ID < right.ID
		},
	)
	if err != nil {
		return serverapi.WorkflowGraphDraft{}, fmt.Errorf("normalize Transition Groups: %w", err)
	}
	edges, err := normalizeWorkflowGraphEntities(
		current.Edges,
		submitted.Edges,
		func(edge serverapi.WorkflowEdge) string { return edge.ID },
		func(edge serverapi.WorkflowGraphDraftEdge) string { return edge.ID },
		func(left, right serverapi.WorkflowGraphDraftEdge) bool {
			if left.TransitionGroupID != right.TransitionGroupID {
				return left.TransitionGroupID < right.TransitionGroupID
			}
			if left.Key != right.Key {
				return left.Key < right.Key
			}
			return left.ID < right.ID
		},
	)
	if err != nil {
		return serverapi.WorkflowGraphDraft{}, fmt.Errorf("normalize Transition Branches: %w", err)
	}
	return serverapi.WorkflowGraphDraft{
		NodeGroups:       nodeGroups,
		Nodes:            nodes,
		TransitionGroups: transitionGroups,
		Edges:            edges,
	}, nil
}

func normalizeWorkflowGraphEntities[Current any, Submitted any](
	current []Current,
	submitted []Submitted,
	currentID func(Current) string,
	submittedID func(Submitted) string,
	additionLess func(Submitted, Submitted) bool,
) ([]Submitted, error) {
	submittedByID := make(map[string]Submitted, len(submitted))
	for _, entity := range submitted {
		id := submittedID(entity)
		if _, exists := submittedByID[id]; exists {
			return nil, fmt.Errorf("duplicate entity id %q", id)
		}
		submittedByID[id] = entity
	}

	ordered := make([]Submitted, 0, len(submitted))
	for _, entity := range current {
		id := currentID(entity)
		if submittedEntity, exists := submittedByID[id]; exists {
			ordered = append(ordered, submittedEntity)
			delete(submittedByID, id)
		}
	}
	additions := make([]Submitted, 0, len(submittedByID))
	for _, entity := range submittedByID {
		additions = append(additions, entity)
	}
	sort.Slice(additions, func(left, right int) bool {
		return additionLess(additions[left], additions[right])
	})
	return append(ordered, additions...), nil
}
