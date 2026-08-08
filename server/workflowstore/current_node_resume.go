package workflowstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/workflow"
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
	task, err := s.queries.GetTask(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	definition, _, err := s.GetDefinition(ctx, task.WorkflowID)
	if err != nil {
		return nil, err
	}
	derived := workflow.DeriveWiring(definition)
	currentNodes, err := s.interruptedExecutableCurrentNodesWithDefinition(ctx, taskID, definition)
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
	currentNodes, err := s.ListCurrentNodes(ctx, taskID)
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
		eligible, err := s.IsCurrentNodeExecutionEligible(ctx, currentNode.Reference)
		if err != nil {
			return nil, err
		}
		if eligible {
			interrupted = append(interrupted, currentNode)
		}
	}
	return interrupted, nil
}
