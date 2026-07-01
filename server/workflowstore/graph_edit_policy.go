package workflowstore

import (
	"context"
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
	currentNodes, err := q.ListWorkflowNodes(ctx, string(workflowID))
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
	for _, current := range currentNodes {
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
			refCount, err := q.CountTaskNodeReferences(ctx, nullableString(current.ID))
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
	return impact, nil
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
	return blockers
}
