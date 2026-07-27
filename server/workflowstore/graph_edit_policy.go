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
	currentGraph, err := currentWorkflowGraphSavePrepared(ctx, q, workflowID)
	if err != nil {
		return WorkflowGraphEditPolicyResult{}, err
	}
	evaluation, err := evaluateWorkflowGraphSaveDynamicImpact(ctx, q, workflowID, describeWorkflowGraphSave(currentGraph, prepared))
	if err != nil {
		return WorkflowGraphEditPolicyResult{}, err
	}
	return evaluation.EditPolicy, nil
}

type workflowGraphEditPolicyStructuralDescriptor struct {
	StartNodeChangeCount    int64
	LastTerminalChangeCount int64
	NodeKindChanges         []workflow.NodeID
	TransitionChanges       []workflowGraphTransitionChangeDescriptor
	HistoryEdgeChanges      []workflow.EdgeID
}

type workflowGraphTransitionChangeDescriptor struct {
	WorkflowID   workflow.WorkflowID
	SourceNodeID workflow.NodeID
	EdgeIDs      []workflow.EdgeID
}

type workflowGraphSaveDynamicImpact struct {
	Impact     WorkflowGraphSaveImpact
	EditPolicy WorkflowGraphEditPolicyResult
}

func describeWorkflowGraphEditPolicy(current preparedWorkflowGraphSave, next preparedWorkflowGraphSave) workflowGraphEditPolicyStructuralDescriptor {
	nextNodes := map[workflow.NodeID]NodeRecord{}
	nextTerminalCount := int64(0)
	for _, node := range next.nodes {
		nextNodes[node.ID] = node
		if node.Kind == workflow.NodeKindTerminal {
			nextTerminalCount++
		}
	}
	descriptor := workflowGraphEditPolicyStructuralDescriptor{}
	currentTerminalCount := int64(0)
	for _, currentNode := range current.nodes {
		nextNode, exists := nextNodes[currentNode.ID]
		if currentNode.Kind == workflow.NodeKindStart && (!exists || nextNode.Kind != workflow.NodeKindStart) {
			descriptor.StartNodeChangeCount++
		}
		if currentNode.Kind == workflow.NodeKindTerminal {
			currentTerminalCount++
		}
		if exists && currentNode.Kind != nextNode.Kind {
			descriptor.NodeKindChanges = append(descriptor.NodeKindChanges, currentNode.ID)
		}
	}
	if currentTerminalCount > 0 && nextTerminalCount == 0 {
		descriptor.LastTerminalChangeCount = 1
	}
	transitionDescriptor := describeWorkflowGraphTransitionChanges(current, next)
	descriptor.TransitionChanges = transitionDescriptor.TransitionChanges
	descriptor.HistoryEdgeChanges = transitionDescriptor.HistoryEdgeChanges
	return descriptor
}

type workflowGraphTransitionStructuralDescriptor struct {
	TransitionChanges  []workflowGraphTransitionChangeDescriptor
	HistoryEdgeChanges []workflow.EdgeID
}

func describeWorkflowGraphTransitionChanges(current preparedWorkflowGraphSave, next preparedWorkflowGraphSave) workflowGraphTransitionStructuralDescriptor {
	descriptor := workflowGraphTransitionStructuralDescriptor{}
	currentGroups := workflowGraphTransitionGroupsByID(current.transitionGroups)
	nextGroups := workflowGraphTransitionGroupsByID(next.transitionGroups)
	currentEdgesByGroupID := workflowGraphEdgesByTransitionGroupID(current.edges)
	for _, currentGroup := range current.transitionGroups {
		nextGroup, exists := nextGroups[currentGroup.ID]
		if exists && workflowTransitionGroupMetadataOnlyChange(currentGroup, nextGroup) {
			continue
		}
		descriptor.TransitionChanges = append(descriptor.TransitionChanges, workflowGraphTransitionChangeDescriptor{
			WorkflowID:   currentGroup.WorkflowID,
			SourceNodeID: currentGroup.SourceNodeID,
			EdgeIDs:      workflowGraphEdgeIDsSlice(currentEdgesByGroupID[currentGroup.ID]),
		})
	}
	nextEdges := workflowGraphEdgesByID(next.edges)
	for _, currentEdge := range current.edges {
		nextEdge, exists := nextEdges[currentEdge.ID]
		if !exists {
			if _, groupExists := nextGroups[currentEdge.TransitionGroupID]; !groupExists {
				continue
			}
			if currentGroup, hasGroup := currentGroups[currentEdge.TransitionGroupID]; hasGroup {
				descriptor.TransitionChanges = append(descriptor.TransitionChanges, workflowGraphTransitionChangeDescriptor{
					WorkflowID:   currentEdge.WorkflowID,
					SourceNodeID: currentGroup.SourceNodeID,
					EdgeIDs:      []workflow.EdgeID{currentEdge.ID},
				})
			}
			continue
		}
		if workflowEdgeHistoryReinterpretingChange(currentEdge, nextEdge) {
			descriptor.HistoryEdgeChanges = append(descriptor.HistoryEdgeChanges, currentEdge.ID)
		}
		if workflowEdgeMetadataOnlyChange(currentEdge, nextEdge) {
			continue
		}
		if currentGroup, hasGroup := currentGroups[currentEdge.TransitionGroupID]; hasGroup {
			descriptor.TransitionChanges = append(descriptor.TransitionChanges, workflowGraphTransitionChangeDescriptor{
				WorkflowID:   currentEdge.WorkflowID,
				SourceNodeID: currentGroup.SourceNodeID,
				EdgeIDs:      []workflow.EdgeID{currentEdge.ID},
			})
		}
	}
	return descriptor
}

func workflowGraphEdgeIDsSlice(edges []EdgeRecord) []workflow.EdgeID {
	ids := make([]workflow.EdgeID, 0, len(edges))
	for _, edge := range edges {
		ids = append(ids, edge.ID)
	}
	return ids
}

func evaluateWorkflowGraphSaveDynamicImpact(ctx context.Context, q *sqlitegen.Queries, workflowID workflow.WorkflowID, structural workflowGraphSaveStructuralDescriptor) (workflowGraphSaveDynamicImpact, error) {
	impact := WorkflowGraphSaveImpact{
		RemovedNodeCount:            int64(len(structural.Removed.nodes)),
		RemovedTransitionGroupCount: int64(len(structural.Removed.transitionGroups)),
		RemovedEdgeCount:            int64(len(structural.Removed.edges)),
	}
	for _, nodeID := range structural.Removed.nodes {
		count, err := q.CountTaskNodeReferences(ctx, nullableString(string(nodeID)))
		if err != nil {
			return workflowGraphSaveDynamicImpact{}, err
		}
		impact.NodeTaskReferenceCount += count
		currentCount, err := q.CountCurrentTaskNodeAnchorReferences(ctx, nullableString(string(nodeID)))
		if err != nil {
			return workflowGraphSaveDynamicImpact{}, err
		}
		impact.CurrentNodeTaskReferenceCount += currentCount
	}
	for _, edgeID := range structural.Removed.edges {
		count, err := q.CountTaskEdgeReferences(ctx, sql.NullString{String: string(edgeID), Valid: true})
		if err != nil {
			return workflowGraphSaveDynamicImpact{}, err
		}
		impact.EdgeTaskReferenceCount += count
	}

	activeImpact, err := q.GetWorkflowGraphActiveWorkPolicyImpact(ctx, string(workflowID))
	if err != nil {
		return workflowGraphSaveDynamicImpact{}, err
	}
	editPolicyImpact := WorkflowGraphEditPolicyImpact{
		ActiveNodePlacementCount: activeImpact.ActiveNodePlacementCount,
		PendingApprovalCount:     activeImpact.PendingApprovalCount,
		ActiveRunCount:           activeImpact.ActiveRunCount,
		RunnableRunCount:         activeImpact.RunnableRunCount,
		StartNodeChangeCount:     structural.EditPolicy.StartNodeChangeCount,
		LastTerminalChangeCount:  structural.EditPolicy.LastTerminalChangeCount,
	}
	for _, nodeID := range structural.EditPolicy.NodeKindChanges {
		refCount, err := q.CountTaskNodeReferences(ctx, nullableString(string(nodeID)))
		if err != nil {
			return workflowGraphSaveDynamicImpact{}, err
		}
		if refCount > 0 {
			editPolicyImpact.TaskReferencedNodeKindChangeCount++
			editPolicyImpact.TaskReferencedNodeKindChangeRefCount += refCount
		}
	}
	for _, change := range structural.EditPolicy.TransitionChanges {
		refCount := int64(0)
		for _, edgeID := range change.EdgeIDs {
			count, err := q.CountTaskEdgeReferences(ctx, sql.NullString{String: string(edgeID), Valid: true})
			if err != nil {
				return workflowGraphSaveDynamicImpact{}, err
			}
			refCount += count
		}
		runCount, err := q.CountUnresolvedTaskRunsAtWorkflowNode(ctx, sqlitegen.CountUnresolvedTaskRunsAtWorkflowNodeParams{
			WorkflowID: string(change.WorkflowID),
			NodeID:     sql.NullString{String: string(change.SourceNodeID), Valid: true},
		})
		if err != nil {
			return workflowGraphSaveDynamicImpact{}, err
		}
		refCount += runCount
		if refCount > 0 {
			editPolicyImpact.UnsafeTransitionChangeCount++
			editPolicyImpact.UnsafeTransitionChangeRefCount += refCount
		}
	}
	for _, edgeID := range structural.EditPolicy.HistoryEdgeChanges {
		refCount, err := q.CountAllTaskEdgeReferences(ctx, sql.NullString{String: string(edgeID), Valid: true})
		if err != nil {
			return workflowGraphSaveDynamicImpact{}, err
		}
		if refCount > 0 {
			editPolicyImpact.HistoryReinterpretingEdgeChangeCount++
			editPolicyImpact.HistoryReinterpretingEdgeRefCount += refCount
		}
	}
	editPolicy := WorkflowGraphEditPolicyResult{
		Impact:   editPolicyImpact,
		Blockers: workflowGraphEditPolicyBlockers(editPolicyImpact),
	}
	return workflowGraphSaveDynamicImpact{Impact: impact, EditPolicy: editPolicy}, nil
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
