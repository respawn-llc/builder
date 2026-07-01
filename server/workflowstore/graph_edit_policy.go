package workflowstore

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

type WorkflowGraphEditPolicyImpact struct {
	ActiveNodePlacementCount             int64
	PendingApprovalCount                 int64
	ActiveRunCount                       int64
	RunnableRunCount                     int64
	StartNodeChangeCount                 int64
	LastTerminalChangeCount              int64
	TaskReferencedNodeKindChangeCount    int64
	TaskReferencedNodeKindChangeRefCount int64
	UnsafeTransitionChangeCount          int64
	UnsafeTransitionChangeRefCount       int64
	HistoryReinterpretingEdgeChangeCount int64
	HistoryReinterpretingEdgeRefCount    int64
}

type WorkflowGraphEditPolicyBlocker struct {
	Code    string
	Message string
	Count   int64
}

type WorkflowGraphEditPolicyResult struct {
	Impact   WorkflowGraphEditPolicyImpact
	Blockers []WorkflowGraphEditPolicyBlocker
}

type WorkflowGraphEditPolicyError struct {
	Blockers []WorkflowGraphEditPolicyBlocker
}

func (e WorkflowGraphEditPolicyError) Error() string {
	if len(e.Blockers) == 0 {
		return "workflow graph edit blocked"
	}
	messages := make([]string, 0, len(e.Blockers))
	for _, blocker := range e.Blockers {
		messages = append(messages, blocker.Message)
	}
	return strings.Join(messages, "; ")
}

func enforceWorkflowGraphEditPolicy(ctx context.Context, q *sqlitegen.Queries, workflowID workflow.WorkflowID, prepared preparedWorkflowGraphSave) error {
	result, err := workflowGraphEditPolicy(ctx, q, workflowID, prepared)
	if err != nil {
		return err
	}
	if len(result.Blockers) > 0 {
		return WorkflowGraphEditPolicyError{Blockers: result.Blockers}
	}
	return nil
}

func workflowGraphEditPolicy(ctx context.Context, q *sqlitegen.Queries, workflowID workflow.WorkflowID, prepared preparedWorkflowGraphSave) (WorkflowGraphEditPolicyResult, error) {
	activeImpact, err := workflowGraphActiveWorkPolicyImpact(ctx, q, workflowID)
	if err != nil {
		return WorkflowGraphEditPolicyResult{}, err
	}
	structuralImpact, err := workflowGraphStructuralPolicyImpact(ctx, q, workflowID, prepared)
	if err != nil {
		return WorkflowGraphEditPolicyResult{}, err
	}
	impact := WorkflowGraphEditPolicyImpact{
		ActiveNodePlacementCount:             activeImpact.ActiveNodePlacementCount,
		PendingApprovalCount:                 activeImpact.PendingApprovalCount,
		ActiveRunCount:                       activeImpact.ActiveRunCount,
		RunnableRunCount:                     activeImpact.RunnableRunCount,
		StartNodeChangeCount:                 structuralImpact.StartNodeChangeCount,
		LastTerminalChangeCount:              structuralImpact.LastTerminalChangeCount,
		TaskReferencedNodeKindChangeCount:    structuralImpact.TaskReferencedNodeKindChangeCount,
		TaskReferencedNodeKindChangeRefCount: structuralImpact.TaskReferencedNodeKindChangeRefCount,
		UnsafeTransitionChangeCount:          structuralImpact.UnsafeTransitionChangeCount,
		UnsafeTransitionChangeRefCount:       structuralImpact.UnsafeTransitionChangeRefCount,
		HistoryReinterpretingEdgeChangeCount: structuralImpact.HistoryReinterpretingEdgeChangeCount,
		HistoryReinterpretingEdgeRefCount:    structuralImpact.HistoryReinterpretingEdgeRefCount,
	}
	return WorkflowGraphEditPolicyResult{Impact: impact, Blockers: workflowGraphEditPolicyBlockers(impact)}, nil
}

func workflowGraphActiveWorkPolicyImpact(ctx context.Context, q *sqlitegen.Queries, workflowID workflow.WorkflowID) (WorkflowGraphEditPolicyImpact, error) {
	impact, err := q.GetWorkflowGraphActiveWorkPolicyImpact(ctx, string(workflowID))
	if err != nil {
		return WorkflowGraphEditPolicyImpact{}, err
	}
	return WorkflowGraphEditPolicyImpact{
		ActiveNodePlacementCount: impact.ActiveNodePlacementCount,
		PendingApprovalCount:     impact.PendingApprovalCount,
		ActiveRunCount:           impact.ActiveRunCount,
		RunnableRunCount:         impact.RunnableRunCount,
	}, nil
}

func workflowGraphStructuralPolicyImpact(ctx context.Context, q *sqlitegen.Queries, workflowID workflow.WorkflowID, prepared preparedWorkflowGraphSave) (WorkflowGraphEditPolicyImpact, error) {
	currentGraph, err := currentWorkflowGraphSavePrepared(ctx, q, workflowID)
	if err != nil {
		return WorkflowGraphEditPolicyImpact{}, err
	}
	nextNodes := map[workflow.NodeID]NodeRecord{}
	nextTerminalCount := int64(0)
	for _, node := range prepared.nodes {
		nextNodes[node.ID] = node
		if node.Kind == workflow.NodeKindTerminal {
			nextTerminalCount++
		}
	}
	impact := WorkflowGraphEditPolicyImpact{}
	currentTerminalCount := int64(0)
	for _, current := range currentGraph.nodes {
		nodeID := workflow.NodeID(current.ID)
		currentKind := workflow.NodeKind(current.Kind)
		next, exists := nextNodes[nodeID]
		if currentKind == workflow.NodeKindStart && (!exists || next.Kind != workflow.NodeKindStart) {
			impact.StartNodeChangeCount++
		}
		if currentKind == workflow.NodeKindTerminal {
			currentTerminalCount++
		}
		if exists && currentKind != next.Kind {
			refCount, err := q.CountTaskNodeReferences(ctx, nullableString(string(current.ID)))
			if err != nil {
				return WorkflowGraphEditPolicyImpact{}, err
			}
			if refCount > 0 {
				impact.TaskReferencedNodeKindChangeCount++
				impact.TaskReferencedNodeKindChangeRefCount += refCount
			}
		}
	}
	if currentTerminalCount > 0 && nextTerminalCount == 0 {
		impact.LastTerminalChangeCount = 1
	}
	transitionImpact, err := workflowGraphTransitionChangePolicyImpact(ctx, q, currentGraph, prepared)
	if err != nil {
		return WorkflowGraphEditPolicyImpact{}, err
	}
	impact.UnsafeTransitionChangeCount = transitionImpact.UnsafeTransitionChangeCount
	impact.UnsafeTransitionChangeRefCount = transitionImpact.UnsafeTransitionChangeRefCount
	impact.HistoryReinterpretingEdgeChangeCount = transitionImpact.HistoryReinterpretingEdgeChangeCount
	impact.HistoryReinterpretingEdgeRefCount = transitionImpact.HistoryReinterpretingEdgeRefCount
	return impact, nil
}

func workflowGraphTransitionChangePolicyImpact(ctx context.Context, q *sqlitegen.Queries, current preparedWorkflowGraphSave, next preparedWorkflowGraphSave) (WorkflowGraphEditPolicyImpact, error) {
	impact := WorkflowGraphEditPolicyImpact{}
	currentGroups := workflowGraphTransitionGroupsByID(current.transitionGroups)
	nextGroups := workflowGraphTransitionGroupsByID(next.transitionGroups)
	currentEdgesByGroupID := workflowGraphEdgesByTransitionGroupID(current.edges)
	for _, currentGroup := range current.transitionGroups {
		nextGroup, exists := nextGroups[currentGroup.ID]
		if exists && workflowTransitionGroupMetadataOnlyChange(currentGroup, nextGroup) {
			continue
		}
		refCount, err := countCurrentWorkflowEdgeReferences(ctx, q, currentEdgesByGroupID[currentGroup.ID])
		if err != nil {
			return WorkflowGraphEditPolicyImpact{}, err
		}
		runCount, err := countUnresolvedTaskRunsAtWorkflowNode(ctx, q, currentGroup.WorkflowID, currentGroup.SourceNodeID)
		if err != nil {
			return WorkflowGraphEditPolicyImpact{}, err
		}
		refCount += runCount
		if refCount > 0 {
			impact.UnsafeTransitionChangeCount++
			impact.UnsafeTransitionChangeRefCount += refCount
		}
	}
	nextEdges := workflowGraphEdgesByID(next.edges)
	for _, currentEdge := range current.edges {
		nextEdge, exists := nextEdges[currentEdge.ID]
		if !exists {
			if _, groupExists := nextGroups[currentEdge.TransitionGroupID]; !groupExists {
				continue
			}
			refCount, err := q.CountTaskEdgeReferences(ctx, sql.NullString{String: string(currentEdge.ID), Valid: true})
			if err != nil {
				return WorkflowGraphEditPolicyImpact{}, err
			}
			currentGroup, hasGroup := currentGroups[currentEdge.TransitionGroupID]
			if hasGroup {
				runCount, err := countUnresolvedTaskRunsAtWorkflowNode(ctx, q, currentEdge.WorkflowID, currentGroup.SourceNodeID)
				if err != nil {
					return WorkflowGraphEditPolicyImpact{}, err
				}
				refCount += runCount
			}
			if refCount > 0 {
				impact.UnsafeTransitionChangeCount++
				impact.UnsafeTransitionChangeRefCount += refCount
			}
			continue
		}
		if workflowEdgeHistoryReinterpretingChange(currentEdge, nextEdge) {
			refCount, err := q.CountAllTaskEdgeReferences(ctx, sql.NullString{String: string(currentEdge.ID), Valid: true})
			if err != nil {
				return WorkflowGraphEditPolicyImpact{}, err
			}
			if refCount > 0 {
				impact.HistoryReinterpretingEdgeChangeCount++
				impact.HistoryReinterpretingEdgeRefCount += refCount
			}
		}
		if workflowEdgeMetadataOnlyChange(currentEdge, nextEdge) {
			continue
		}
		refCount, err := q.CountTaskEdgeReferences(ctx, sql.NullString{String: string(currentEdge.ID), Valid: true})
		if err != nil {
			return WorkflowGraphEditPolicyImpact{}, err
		}
		currentGroup, hasGroup := currentGroups[currentEdge.TransitionGroupID]
		if hasGroup {
			runCount, err := countUnresolvedTaskRunsAtWorkflowNode(ctx, q, currentEdge.WorkflowID, currentGroup.SourceNodeID)
			if err != nil {
				return WorkflowGraphEditPolicyImpact{}, err
			}
			refCount += runCount
		}
		if refCount > 0 {
			impact.UnsafeTransitionChangeCount++
			impact.UnsafeTransitionChangeRefCount += refCount
		}
	}
	return impact, nil
}

func countCurrentWorkflowEdgeReferences(ctx context.Context, q *sqlitegen.Queries, edges []EdgeRecord) (int64, error) {
	total := int64(0)
	for _, edge := range edges {
		count, err := q.CountTaskEdgeReferences(ctx, sql.NullString{String: string(edge.ID), Valid: true})
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func countUnresolvedTaskRunsAtWorkflowNode(ctx context.Context, q *sqlitegen.Queries, workflowID workflow.WorkflowID, nodeID workflow.NodeID) (int64, error) {
	return q.CountUnresolvedTaskRunsAtWorkflowNode(ctx, sqlitegen.CountUnresolvedTaskRunsAtWorkflowNodeParams{
		WorkflowID: string(workflowID),
		NodeID:     sql.NullString{String: string(nodeID), Valid: true},
	})
}

func workflowTransitionGroupMetadataOnlyChange(current TransitionGroupRecord, next TransitionGroupRecord) bool {
	return current.ID == next.ID &&
		current.WorkflowID == next.WorkflowID &&
		current.SourceNodeID == next.SourceNodeID &&
		current.TransitionID == next.TransitionID
}

func workflowEdgeMetadataOnlyChange(current EdgeRecord, next EdgeRecord) bool {
	return current.ID == next.ID &&
		current.WorkflowID == next.WorkflowID &&
		current.TransitionGroupID == next.TransitionGroupID &&
		current.Key == next.Key &&
		current.TargetNodeID == next.TargetNodeID &&
		slices.Equal(current.Parameters, next.Parameters) &&
		slices.Equal(current.InputBindings, next.InputBindings) &&
		slices.Equal(current.OutputRequirements, next.OutputRequirements)
}

func workflowEdgeHistoryReinterpretingChange(current EdgeRecord, next EdgeRecord) bool {
	return current.TransitionGroupID != next.TransitionGroupID
}

func workflowGraphTransitionGroupsByID(groups []TransitionGroupRecord) map[workflow.TransitionGroupID]TransitionGroupRecord {
	out := make(map[workflow.TransitionGroupID]TransitionGroupRecord, len(groups))
	for _, group := range groups {
		out[group.ID] = group
	}
	return out
}

func workflowGraphEdgesByID(edges []EdgeRecord) map[workflow.EdgeID]EdgeRecord {
	out := make(map[workflow.EdgeID]EdgeRecord, len(edges))
	for _, edge := range edges {
		out[edge.ID] = edge
	}
	return out
}

func workflowGraphEdgesByTransitionGroupID(edges []EdgeRecord) map[workflow.TransitionGroupID][]EdgeRecord {
	out := map[workflow.TransitionGroupID][]EdgeRecord{}
	for _, edge := range edges {
		out[edge.TransitionGroupID] = append(out[edge.TransitionGroupID], edge)
	}
	return out
}

func workflowGraphEditPolicyBlockers(impact WorkflowGraphEditPolicyImpact) []WorkflowGraphEditPolicyBlocker {
	blockers := []WorkflowGraphEditPolicyBlocker{}
	if impact.StartNodeChangeCount > 0 {
		blockers = append(blockers, WorkflowGraphEditPolicyBlocker{Code: "start_node_changed", Message: "The workflow start node cannot be removed, replaced, or changed to another kind.", Count: impact.StartNodeChangeCount})
	}
	if impact.LastTerminalChangeCount > 0 {
		blockers = append(blockers, WorkflowGraphEditPolicyBlocker{Code: "last_terminal_changed", Message: "Workflow graph changes must leave at least one terminal node.", Count: impact.LastTerminalChangeCount})
	}
	if impact.TaskReferencedNodeKindChangeCount > 0 {
		blockers = append(blockers, WorkflowGraphEditPolicyBlocker{Code: "task_referenced_node_kind_changed", Message: "Workflow node kind changes are blocked for nodes referenced by existing tasks.", Count: impact.TaskReferencedNodeKindChangeRefCount})
	}
	if impact.UnsafeTransitionChangeCount > 0 {
		blockers = append(blockers, WorkflowGraphEditPolicyBlocker{Code: "active_transition_contract_changed", Message: "Transition routing and parameter contract changes are blocked while referenced transition work is unresolved.", Count: impact.UnsafeTransitionChangeRefCount})
	}
	if impact.HistoryReinterpretingEdgeChangeCount > 0 {
		blockers = append(blockers, WorkflowGraphEditPolicyBlocker{Code: "task_referenced_edge_group_changed", Message: "Transition branch group changes are blocked for branches referenced by existing tasks.", Count: impact.HistoryReinterpretingEdgeRefCount})
	}
	return blockers
}
