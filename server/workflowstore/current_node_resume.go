package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

const CurrentNodeResumeParameterNotMaterializedCode = "workflow.resume.parameter_not_materialized"

type CurrentNodeResumeValidationDiagnostic struct {
	Code           string
	CurrentNode    workflow.CurrentNodeReference
	EnteringEdgeID workflow.EdgeID
	ParameterKey   string
}

type CurrentNodeResumeValidationError struct {
	Diagnostics []CurrentNodeResumeValidationDiagnostic
}

func (e *CurrentNodeResumeValidationError) Error() string {
	if e == nil || len(e.Diagnostics) == 0 {
		return CurrentNodeResumeParameterNotMaterializedCode
	}
	return fmt.Sprintf(
		"%s: Current Node %v requires Parameter %q from Edge %s",
		e.Diagnostics[0].Code,
		e.Diagnostics[0].CurrentNode,
		e.Diagnostics[0].ParameterKey,
		e.Diagnostics[0].EnteringEdgeID,
	)
}

type CurrentNodeResumeClassification struct {
	CurrentNode workflow.CurrentNode
	Diagnostics []CurrentNodeResumeValidationDiagnostic
}

func (c CurrentNodeResumeClassification) ValidationError() error {
	if len(c.Diagnostics) == 0 {
		return nil
	}
	return &CurrentNodeResumeValidationError{
		Diagnostics: append([]CurrentNodeResumeValidationDiagnostic(nil), c.Diagnostics...),
	}
}

func (s *Store) PreflightTaskResume(
	ctx context.Context,
	taskID workflow.TaskID,
) ([]CurrentNodeResumeClassification, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, errors.New("task id is required")
	}
	return s.preflightTaskResumeWithQueries(ctx, s.queries, taskID)
}

func (s *Store) preflightTaskResumeWithQueries(
	ctx context.Context,
	q *sqlitegen.Queries,
	taskID workflow.TaskID,
) ([]CurrentNodeResumeClassification, error) {
	task, err := q.GetTask(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	definition, _, err := GetDefinitionWithQueries(ctx, q, task.WorkflowID)
	if err != nil {
		return nil, err
	}
	derived := workflow.DeriveWiring(definition)
	currentNodes, err := s.interruptedExecutableCurrentNodesWithDefinitionAndQueries(ctx, q, taskID, definition)
	if err != nil {
		return nil, err
	}
	classifications := make([]CurrentNodeResumeClassification, 0, len(currentNodes))
	for _, currentNode := range currentNodes {
		classification := CurrentNodeResumeClassification{CurrentNode: currentNode}
		edge, err := currentNodeDefinitionEnteringEdge(definition, currentNode)
		if err != nil {
			return nil, err
		}
		target, err := currentNodeDefinitionNode(definition, currentNode.Reference.NodeID)
		if err != nil {
			return nil, err
		}
		consumption, err := resumeTransitionProtectedParameterConsumption(definition, edge, target, currentNode, s.roleResolver)
		if err != nil {
			return nil, err
		}
		for _, binding := range resumeCurrentNodeInputBindings(edge, derived.CurrentNodeInputBindingsForEdge(edge.ID), consumption) {
			if _, materialized := currentNode.CurrentInputValues[binding.Name]; materialized {
				continue
			}
			classification.Diagnostics = append(classification.Diagnostics, CurrentNodeResumeValidationDiagnostic{
				Code:           CurrentNodeResumeParameterNotMaterializedCode,
				CurrentNode:    currentNode.Reference,
				EnteringEdgeID: edge.ID,
				ParameterKey:   binding.Name,
			})
		}
		classifications = append(classifications, classification)
	}
	return classifications, nil
}

func (s *Store) PrepareTaskResume(
	ctx context.Context,
	taskID workflow.TaskID,
) (PreparedCurrentNodeMutation, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, errors.New("task id is required")
	}
	return s.prepareCurrentNodeMutation(ctx, taskID, func(ctx context.Context, q *sqlitegen.Queries) (PreparedCurrentNodeMutationResult, error) {
		classifications, err := s.preflightTaskResumeWithQueries(ctx, q, taskID)
		if err != nil {
			return PreparedCurrentNodeMutationResult{}, err
		}
		var validationErrors []error
		for _, classification := range classifications {
			if err := classification.ValidationError(); err != nil {
				validationErrors = append(validationErrors, err)
			}
		}
		if err := errors.Join(validationErrors...); err != nil {
			return PreparedCurrentNodeMutationResult{}, err
		}
		ensuredSessions := make(map[runtimeids.SessionID]struct{})
		for _, classification := range classifications {
			if classification.CurrentNode.SessionID == nil {
				continue
			}
			sessionID := *classification.CurrentNode.SessionID
			if _, ensured := ensuredSessions[sessionID]; ensured {
				continue
			}
			if _, err := s.ensureCurrentNodeSessionAssociationWithQueries(ctx, q, sessionID); err != nil {
				return PreparedCurrentNodeMutationResult{}, err
			}
			ensuredSessions[sessionID] = struct{}{}
		}
		result := PreparedCurrentNodeMutationResult{}
		for _, classification := range classifications {
			currentNode := classification.CurrentNode
			projection, found, err := pendingInterruptedCurrentNodeAttentionProjection(ctx, q, currentNode.Reference)
			if err != nil {
				return PreparedCurrentNodeMutationResult{}, err
			}
			if found {
				result.TaskAttentionResolution.InterruptedCurrentNodes = append(
					result.TaskAttentionResolution.InterruptedCurrentNodes,
					projection,
				)
			}
			resumed, err := resumeCurrentNodeWithQueries(ctx, q, currentNode.Reference)
			if err != nil {
				return PreparedCurrentNodeMutationResult{}, err
			}
			if resumed != 1 {
				return PreparedCurrentNodeMutationResult{}, sql.ErrNoRows
			}
			ready := currentNode
			ready.Scheduling = &workflow.CurrentNodeScheduling{State: workflow.CurrentNodeSchedulingReady}
			result.Mutation.Updated = append(result.Mutation.Updated, ready)
			result.CreatedExecutableCurrentNodes = append(result.CreatedExecutableCurrentNodes, ready)
		}
		return result, nil
	})
}

func resumeCurrentNodeWithQueries(
	ctx context.Context,
	q *sqlitegen.Queries,
	reference workflow.CurrentNodeReference,
) (int64, error) {
	if branchKey, branchScoped := reference.TransitionBranchKey(); branchScoped {
		return q.ResumeBranchCurrentNode(ctx, sqlitegen.ResumeBranchCurrentNodeParams{
			TaskID:              string(reference.TaskID),
			NodeID:              string(reference.NodeID),
			TransitionBranchKey: sql.NullString{String: string(branchKey), Valid: true},
		})
	}
	return q.ResumeSerialCurrentNode(ctx, sqlitegen.ResumeSerialCurrentNodeParams{
		TaskID: string(reference.TaskID),
		NodeID: string(reference.NodeID),
	})
}

func resumeTransitionProtectedParameterConsumption(
	definition workflow.Definition,
	edge workflow.Edge,
	target workflow.Node,
	currentNode workflow.CurrentNode,
	catalog workflow.TargetAgentCatalog,
) (workflow.ProtectedParameterConsumption, error) {
	edgeCount := 0
	for _, candidate := range definition.Edges {
		if candidate.TransitionGroupID == edge.TransitionGroupID {
			edgeCount++
		}
	}
	request := workflow.TransitionParameterContractRequest{
		Edge:                  edge,
		TargetKind:            target.Kind(),
		TargetRole:            workflow.NodeSubagentRole(target),
		FanOut:                edgeCount > 1,
		Catalog:               catalog,
		TargetSessionPolicy:   workflow.AssigneeSessionPolicyEstablishTarget,
		TargetSessionResolved: false,
	}
	if selection := currentNode.AgentExecutionSelection; selection != nil &&
		selection.Origin == workflow.AssigneeOriginRetainedSession {
		retainedRole := workflow.TargetAgentRole{Identity: selection.Assignee}
		if catalog != nil {
			if resolved, ok := catalog.ResolveConfiguredRole(selection.Assignee); ok {
				retainedRole = resolved
			}
		}
		request.RetainedTargetRole = &retainedRole
		request.TargetSessionPolicy = workflow.AssigneeSessionPolicyPreserve
		request.TargetSessionResolved = true
	}
	group, err := transitionGroupForEdge(definition, edge)
	if err != nil {
		return workflow.ProtectedParameterConsumption{}, err
	}
	source, err := currentNodeDefinitionNode(definition, group.SourceNodeID)
	if err != nil {
		return workflow.ProtectedParameterConsumption{}, err
	}
	request.SourceKind = source.Kind()
	return workflow.PlanTransitionProtectedParameterConsumption(request), nil
}

func resumeCurrentNodeInputBindings(
	edge workflow.Edge,
	bindings []workflow.InputBinding,
	consumption workflow.ProtectedParameterConsumption,
) []workflow.InputBinding {
	filtered := make([]workflow.InputBinding, 0, len(bindings))
	for _, binding := range bindings {
		parameter, protected := transitionParameterByKey(edge, binding.Field)
		if !protected {
			filtered = append(filtered, binding)
			continue
		}
		switch workflow.CanonicalParameterPurpose(parameter.Purpose) {
		case workflow.ParameterPurposeTargetAssignee:
			if consumption.Assignee != workflow.ProtectedParameterConsumptionRequiredValidate {
				continue
			}
		case workflow.ParameterPurposeTargetThinking:
			if consumption.Thinking != workflow.ProtectedParameterConsumptionRequiredValidate {
				continue
			}
		}
		filtered = append(filtered, binding)
	}
	return filtered
}

func (s *Store) interruptedExecutableCurrentNodesWithDefinition(
	ctx context.Context,
	taskID workflow.TaskID,
	definition workflow.Definition,
) ([]workflow.CurrentNode, error) {
	return s.interruptedExecutableCurrentNodesWithDefinitionAndQueries(ctx, s.queries, taskID, definition)
}

func (s *Store) interruptedExecutableCurrentNodesWithDefinitionAndQueries(
	ctx context.Context,
	q *sqlitegen.Queries,
	taskID workflow.TaskID,
	definition workflow.Definition,
) ([]workflow.CurrentNode, error) {
	currentNodes, err := listTaskCurrentNodes(ctx, q, taskID)
	if err != nil {
		return nil, err
	}
	interrupted := make([]workflow.CurrentNode, 0, len(currentNodes))
	for _, currentNode := range currentNodes {
		if currentNode.Scheduling == nil || currentNode.Scheduling.State != workflow.CurrentNodeSchedulingInterrupted {
			continue
		}
		node, err := currentNodeDefinitionNode(definition, currentNode.Reference.NodeID)
		if err != nil {
			return nil, err
		}
		if !executableNodeKind(node.Kind()) {
			continue
		}
		_, pending, err := currentNodePendingApprovalID(ctx, q, currentNode.Reference)
		if err != nil {
			return nil, err
		}
		if !pending {
			interrupted = append(interrupted, currentNode)
		}
	}
	return interrupted, nil
}
