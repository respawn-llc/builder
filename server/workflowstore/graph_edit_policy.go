package workflowstore

import (
	"context"
	"database/sql"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

type WorkflowGraphEditPolicyImpact struct {
	ActiveCurrentNodeCount               int64
	PendingApprovalCount                 int64
	StartNodeChangeCount                 int64
	LastTerminalChangeCount              int64
	TaskReferencedNodeKindChangeCount    int64
	TaskReferencedNodeKindChangeRefCount int64
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

func enforceWorkflowGraphEditPolicy(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID, prepared preparedWorkflowGraphSave) error {
	result, err := workflowGraphEditPolicy(ctx, q, workflowID, prepared)
	if err != nil {
		return err
	}
	if len(result.Blockers) > 0 {
		return WorkflowGraphEditPolicyError{Blockers: result.Blockers}
	}
	return nil
}

func workflowGraphEditPolicy(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID, prepared preparedWorkflowGraphSave) (WorkflowGraphEditPolicyResult, error) {
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
	HistoryEdgeChanges      []workflow.EdgeID
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
	descriptor.HistoryEdgeChanges = transitionDescriptor.HistoryEdgeChanges
	return descriptor
}

type workflowGraphTransitionStructuralDescriptor struct {
	HistoryEdgeChanges []workflow.EdgeID
}

func describeWorkflowGraphTransitionChanges(current preparedWorkflowGraphSave, next preparedWorkflowGraphSave) workflowGraphTransitionStructuralDescriptor {
	descriptor := workflowGraphTransitionStructuralDescriptor{}
	nextEdges := workflowGraphEdgesByID(next.edges)
	for _, currentEdge := range current.edges {
		nextEdge, exists := nextEdges[currentEdge.ID]
		if !exists {
			continue
		}
		if workflowEdgeHistoryReinterpretingChange(currentEdge, nextEdge) {
			descriptor.HistoryEdgeChanges = append(descriptor.HistoryEdgeChanges, currentEdge.ID)
		}
	}
	return descriptor
}

func evaluateWorkflowGraphSaveDynamicImpact(ctx context.Context, q *sqlitegen.Queries, workflowID runtimeids.WorkflowID, structural workflowGraphSaveStructuralDescriptor) (workflowGraphSaveDynamicImpact, error) {
	evaluation, err := evaluateWorkflowGraphSaveDynamicDecision(ctx, q, structural)
	if err != nil {
		return workflowGraphSaveDynamicImpact{}, err
	}
	activeImpact, err := q.GetWorkflowGraphActiveWorkPolicyImpact(ctx, workflowID)
	if err != nil {
		return workflowGraphSaveDynamicImpact{}, err
	}
	evaluation.EditPolicy.Impact.ActiveCurrentNodeCount = activeImpact.ActiveCurrentNodeCount
	evaluation.EditPolicy.Impact.PendingApprovalCount = activeImpact.PendingApprovalCount
	return evaluation, nil
}

func evaluateWorkflowGraphSaveDynamicDecision(ctx context.Context, q *sqlitegen.Queries, structural workflowGraphSaveStructuralDescriptor) (workflowGraphSaveDynamicImpact, error) {
	impact := WorkflowGraphSaveImpact{
		RemovedNodeCount:            int64(len(structural.Removed.nodes)),
		RemovedTransitionGroupCount: int64(len(structural.Removed.transitionGroups)),
		RemovedEdgeCount:            int64(len(structural.Removed.edges)),
	}
	for _, nodeID := range structural.Removed.nodes {
		count, err := q.CountTaskNodeReferences(ctx, string(nodeID))
		if err != nil {
			return workflowGraphSaveDynamicImpact{}, err
		}
		impact.NodeTaskReferenceCount += count
		currentCount, err := q.CountCurrentTaskNodeAnchorReferences(ctx, string(nodeID))
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

	editPolicyImpact := WorkflowGraphEditPolicyImpact{
		StartNodeChangeCount:    structural.EditPolicy.StartNodeChangeCount,
		LastTerminalChangeCount: structural.EditPolicy.LastTerminalChangeCount,
	}
	for _, nodeID := range structural.EditPolicy.NodeKindChanges {
		refCount, err := q.CountTaskNodeReferences(ctx, string(nodeID))
		if err != nil {
			return workflowGraphSaveDynamicImpact{}, err
		}
		if refCount > 0 {
			editPolicyImpact.TaskReferencedNodeKindChangeCount++
			editPolicyImpact.TaskReferencedNodeKindChangeRefCount += refCount
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

func workflowEdgeHistoryReinterpretingChange(current EdgeRecord, next EdgeRecord) bool {
	return current.TransitionGroupID != next.TransitionGroupID
}

func workflowGraphEdgesByID(edges []EdgeRecord) map[workflow.EdgeID]EdgeRecord {
	out := make(map[workflow.EdgeID]EdgeRecord, len(edges))
	for _, edge := range edges {
		out[edge.ID] = edge
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
	if impact.HistoryReinterpretingEdgeChangeCount > 0 {
		blockers = append(blockers, WorkflowGraphEditPolicyBlocker{Code: "task_referenced_edge_group_changed", Message: "Transition branch group changes are blocked for branches referenced by existing tasks.", Count: impact.HistoryReinterpretingEdgeRefCount})
	}
	return blockers
}
