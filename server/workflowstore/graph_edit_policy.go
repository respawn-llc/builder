package workflowstore

import (
	"context"
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
	Code             string
	Message          string
	Count            int64
	AffectedEntities []WorkflowGraphEntityReference
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
	StartNodeChanges    []workflow.NodeID
	LastTerminalChanges []workflow.NodeID
	NodeKindChanges     []workflow.NodeID
	HistoryEdgeChanges  []workflow.EdgeID
}

type workflowGraphSaveDynamicImpact struct {
	Impact                           WorkflowGraphSaveImpact
	EditPolicy                       WorkflowGraphEditPolicyResult
	RemovedNodeTaskReferenceEntities []WorkflowGraphEntityReference
	RemovedEdgeTaskReferenceEntities []WorkflowGraphEntityReference
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
	currentTerminalIDs := []workflow.NodeID{}
	for _, currentNode := range current.nodes {
		nextNode, exists := nextNodes[currentNode.ID]
		if currentNode.Kind == workflow.NodeKindStart && (!exists || nextNode.Kind != workflow.NodeKindStart) {
			descriptor.StartNodeChanges = append(descriptor.StartNodeChanges, currentNode.ID)
		}
		if currentNode.Kind == workflow.NodeKindTerminal {
			currentTerminalIDs = append(currentTerminalIDs, currentNode.ID)
		}
		if exists && currentNode.Kind != nextNode.Kind {
			descriptor.NodeKindChanges = append(descriptor.NodeKindChanges, currentNode.ID)
		}
	}
	if len(currentTerminalIDs) > 0 && nextTerminalCount == 0 {
		descriptor.LastTerminalChanges = append(descriptor.LastTerminalChanges, currentTerminalIDs...)
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
	evaluation := workflowGraphSaveDynamicImpact{}
	impact := workflowGraphSaveRemovedImpact(structural.Removed)
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
		if currentCount > 0 {
			evaluation.RemovedNodeTaskReferenceEntities = append(evaluation.RemovedNodeTaskReferenceEntities, WorkflowGraphEntityReference{
				EntityType: WorkflowGraphEntityTypeNode,
				EntityID:   string(nodeID),
			})
		}
	}
	for _, edgeID := range structural.Removed.edges {
		count, err := q.CountTaskEdgeReferences(ctx, string(edgeID))
		if err != nil {
			return workflowGraphSaveDynamicImpact{}, err
		}
		impact.EdgeTaskReferenceCount += count
		if count > 0 {
			evaluation.RemovedEdgeTaskReferenceEntities = append(evaluation.RemovedEdgeTaskReferenceEntities, WorkflowGraphEntityReference{
				EntityType: WorkflowGraphEntityTypeEdge,
				EntityID:   string(edgeID),
			})
		}
	}

	editPolicyImpact := WorkflowGraphEditPolicyImpact{
		StartNodeChangeCount:    int64(len(structural.EditPolicy.StartNodeChanges)),
		LastTerminalChangeCount: int64(len(structural.EditPolicy.LastTerminalChanges)),
	}
	nodeKindChangeEntities := []WorkflowGraphEntityReference{}
	for _, nodeID := range structural.EditPolicy.NodeKindChanges {
		refCount, err := q.CountTaskNodeReferences(ctx, string(nodeID))
		if err != nil {
			return workflowGraphSaveDynamicImpact{}, err
		}
		if refCount > 0 {
			editPolicyImpact.TaskReferencedNodeKindChangeCount++
			editPolicyImpact.TaskReferencedNodeKindChangeRefCount += refCount
			nodeKindChangeEntities = append(nodeKindChangeEntities, WorkflowGraphEntityReference{
				EntityType: WorkflowGraphEntityTypeNode,
				EntityID:   string(nodeID),
			})
		}
	}
	historyEdgeChangeEntities := []WorkflowGraphEntityReference{}
	for _, edgeID := range structural.EditPolicy.HistoryEdgeChanges {
		refCount, err := q.CountAllTaskEdgeReferences(ctx, string(edgeID))
		if err != nil {
			return workflowGraphSaveDynamicImpact{}, err
		}
		if refCount > 0 {
			editPolicyImpact.HistoryReinterpretingEdgeChangeCount++
			editPolicyImpact.HistoryReinterpretingEdgeRefCount += refCount
			historyEdgeChangeEntities = append(historyEdgeChangeEntities, WorkflowGraphEntityReference{
				EntityType: WorkflowGraphEntityTypeEdge,
				EntityID:   string(edgeID),
			})
		}
	}
	editPolicy := WorkflowGraphEditPolicyResult{
		Impact: editPolicyImpact,
		Blockers: workflowGraphEditPolicyBlockers(
			editPolicyImpact,
			workflowGraphNodeEntityReferences(structural.EditPolicy.StartNodeChanges),
			workflowGraphNodeEntityReferences(structural.EditPolicy.LastTerminalChanges),
			nodeKindChangeEntities,
			historyEdgeChangeEntities,
		),
	}
	evaluation.Impact = impact
	evaluation.EditPolicy = editPolicy
	evaluation.RemovedNodeTaskReferenceEntities = canonicalWorkflowGraphEntityReferences(evaluation.RemovedNodeTaskReferenceEntities)
	evaluation.RemovedEdgeTaskReferenceEntities = canonicalWorkflowGraphEntityReferences(evaluation.RemovedEdgeTaskReferenceEntities)
	return evaluation, nil
}

func workflowGraphSaveRemovedImpact(removed removedWorkflowGraphRows) WorkflowGraphSaveImpact {
	entities := make([]WorkflowGraphEntityReference, 0, len(removed.nodeGroups)+len(removed.nodes)+len(removed.transitionGroups)+len(removed.edges))
	for _, id := range removed.nodeGroups {
		entities = append(entities, WorkflowGraphEntityReference{EntityType: WorkflowGraphEntityTypeNodeGroup, EntityID: id})
	}
	for _, id := range removed.nodes {
		entities = append(entities, WorkflowGraphEntityReference{EntityType: WorkflowGraphEntityTypeNode, EntityID: string(id)})
	}
	for _, id := range removed.transitionGroups {
		entities = append(entities, WorkflowGraphEntityReference{EntityType: WorkflowGraphEntityTypeTransitionGroup, EntityID: string(id)})
	}
	for _, id := range removed.edges {
		entities = append(entities, WorkflowGraphEntityReference{EntityType: WorkflowGraphEntityTypeEdge, EntityID: string(id)})
	}
	return WorkflowGraphSaveImpact{
		RemovedNodeGroupCount:       int64(len(removed.nodeGroups)),
		RemovedNodeCount:            int64(len(removed.nodes)),
		RemovedTransitionGroupCount: int64(len(removed.transitionGroups)),
		RemovedEdgeCount:            int64(len(removed.edges)),
		RemovedEntities:             canonicalWorkflowGraphEntityReferences(entities),
	}
}

func workflowEdgeHistoryReinterpretingChange(current EdgeRecord, next EdgeRecord) bool {
	return current.TransitionGroupID != next.TransitionGroupID ||
		current.TargetNodeID != next.TargetNodeID
}

func workflowGraphEdgesByID(edges []EdgeRecord) map[workflow.EdgeID]EdgeRecord {
	out := make(map[workflow.EdgeID]EdgeRecord, len(edges))
	for _, edge := range edges {
		out[edge.ID] = edge
	}
	return out
}

func workflowGraphEditPolicyBlockers(
	impact WorkflowGraphEditPolicyImpact,
	startNodeChanges []WorkflowGraphEntityReference,
	lastTerminalChanges []WorkflowGraphEntityReference,
	nodeKindChanges []WorkflowGraphEntityReference,
	historyEdgeChanges []WorkflowGraphEntityReference,
) []WorkflowGraphEditPolicyBlocker {
	blockers := []WorkflowGraphEditPolicyBlocker{}
	if impact.StartNodeChangeCount > 0 {
		blockers = append(blockers, WorkflowGraphEditPolicyBlocker{Code: "start_node_changed", Message: "The workflow start node cannot be removed, replaced, or changed to another kind.", Count: impact.StartNodeChangeCount, AffectedEntities: canonicalWorkflowGraphEntityReferences(startNodeChanges)})
	}
	if impact.LastTerminalChangeCount > 0 {
		blockers = append(blockers, WorkflowGraphEditPolicyBlocker{Code: "last_terminal_changed", Message: "Workflow graph changes must leave at least one terminal node.", Count: impact.LastTerminalChangeCount, AffectedEntities: canonicalWorkflowGraphEntityReferences(lastTerminalChanges)})
	}
	if impact.TaskReferencedNodeKindChangeCount > 0 {
		blockers = append(blockers, WorkflowGraphEditPolicyBlocker{Code: "task_referenced_node_kind_changed", Message: "Workflow node kind changes are blocked for nodes referenced by existing tasks.", Count: impact.TaskReferencedNodeKindChangeRefCount, AffectedEntities: canonicalWorkflowGraphEntityReferences(nodeKindChanges)})
	}
	if impact.HistoryReinterpretingEdgeChangeCount > 0 {
		blockers = append(blockers, WorkflowGraphEditPolicyBlocker{Code: "task_referenced_edge_group_changed", Message: "Transition branch routing changes are blocked for branches referenced by existing tasks.", Count: impact.HistoryReinterpretingEdgeRefCount, AffectedEntities: canonicalWorkflowGraphEntityReferences(historyEdgeChanges)})
	}
	return blockers
}

func workflowGraphNodeEntityReferences(nodeIDs []workflow.NodeID) []WorkflowGraphEntityReference {
	entities := make([]WorkflowGraphEntityReference, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		entities = append(entities, WorkflowGraphEntityReference{
			EntityType: WorkflowGraphEntityTypeNode,
			EntityID:   string(nodeID),
		})
	}
	return canonicalWorkflowGraphEntityReferences(entities)
}
