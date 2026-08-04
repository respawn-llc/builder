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
		for _, binding := range derived.CurrentNodeInputBindingsForEdge(edge.ID) {
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
