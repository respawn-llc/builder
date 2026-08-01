package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

type ManualMovePreparation struct {
	request                 ManualMoveRequest
	target                  workflow.Node
	choice                  *ManualMoveTransitionChoice
	currentNodes            []workflow.CurrentNode
	noOp                    bool
	requiresExecutionTarget bool
}

func (p ManualMovePreparation) RequiresExecutionTarget() bool {
	return p.requiresExecutionTarget
}

func (p ManualMovePreparation) IsNoOp() bool {
	return p.noOp
}

func (p ManualMovePreparation) CurrentNodes() []workflow.CurrentNode {
	return append([]workflow.CurrentNode(nil), p.currentNodes...)
}

func (p ManualMovePreparation) TaskID() workflow.TaskID {
	return p.request.TaskID
}

func (s *Store) ManualMoveTask(ctx context.Context, req ManualMoveRequest) (ManualMoveResult, error) {
	prepared, err := s.PrepareManualMove(ctx, req)
	if err != nil {
		return ManualMoveResult{}, err
	}
	return s.ApplyManualMove(ctx, prepared, nil)
}

func (s *Store) PrepareManualMove(ctx context.Context, req ManualMoveRequest) (ManualMovePreparation, error) {
	preview, err := s.resolveManualMove(ctx, s.queries, req)
	if err != nil {
		return ManualMovePreparation{}, err
	}
	switch preview.Outcome {
	case ManualMovePreviewOutcomeNoOp:
		return ManualMovePreparation{request: req, noOp: true, currentNodes: preview.CurrentNodes}, nil
	case ManualMovePreviewOutcomeDirect:
		target, err := currentNodeDefinitionNodeFromTask(ctx, s.queries, req.TaskID, req.TargetNodeID)
		if err != nil {
			return ManualMovePreparation{}, err
		}
		return ManualMovePreparation{request: req, target: target, currentNodes: preview.CurrentNodes}, nil
	case ManualMovePreviewOutcomeTransition:
		if len(preview.Choices) != 1 {
			return ManualMovePreparation{}, ErrManualMoveTransitionSelectionRequired
		}
		target, err := currentNodeDefinitionNodeFromTask(ctx, s.queries, req.TaskID, req.TargetNodeID)
		if err != nil {
			return ManualMovePreparation{}, err
		}
		choice := preview.Choices[0]
		return ManualMovePreparation{
			request:                 req,
			target:                  target,
			choice:                  &choice,
			requiresExecutionTarget: executableNodeKind(target.Kind()),
			currentNodes:            preview.CurrentNodes,
		}, nil
	default:
		return ManualMovePreparation{}, fmt.Errorf("manual move preview cannot be prepared from outcome %q", preview.Outcome)
	}
}

func currentNodeDefinitionNodeFromTask(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID, nodeID workflow.NodeID) (workflow.Node, error) {
	task, err := q.GetTask(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	definition, _, err := workflowDefinitionFromQueries(ctx, q, workflow.WorkflowID(task.WorkflowID))
	if err != nil {
		return nil, err
	}
	return currentNodeDefinitionNode(definition, nodeID)
}

func (s *Store) ApplyManualMove(ctx context.Context, prepared ManualMovePreparation, executionTarget *ExecutionTargetCandidate) (ManualMoveResult, error) {
	if strings.TrimSpace(string(prepared.request.TaskID)) == "" || (!prepared.noOp && prepared.target == nil) {
		return ManualMoveResult{}, errors.New("manual move preparation is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManualMoveResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	task, err := q.GetTask(ctx, string(prepared.request.TaskID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	currentNodes, err := listTaskCurrentNodes(ctx, q, prepared.request.TaskID)
	if err != nil {
		return ManualMoveResult{}, err
	}
	for _, currentNode := range currentNodes {
		if currentNode.Reference.NodeID == prepared.request.TargetNodeID {
			return ManualMoveResult{
				Outcome:      ManualMoveResultOutcomeNoOp,
				CurrentNodes: append([]workflow.CurrentNode(nil), currentNodes...),
			}, nil
		}
	}
	preview, err := s.resolveManualMove(ctx, q, prepared.request)
	if err != nil {
		return ManualMoveResult{}, err
	}
	if preview.Outcome == ManualMovePreviewOutcomeNoOp {
		return ManualMoveResult{
			Outcome:      ManualMoveResultOutcomeNoOp,
			CurrentNodes: append([]workflow.CurrentNode(nil), preview.CurrentNodes...),
		}, nil
	}
	if preview.Outcome == ManualMovePreviewOutcomeBlocked {
		return ManualMoveResult{}, manualMovePreviewBlockerError(preview.Blocker)
	}
	definition, _, err := workflowDefinitionFromQueries(ctx, q, workflow.WorkflowID(task.WorkflowID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	targetDefinition, err := currentNodeDefinitionNode(definition, prepared.request.TargetNodeID)
	if err != nil {
		return ManualMoveResult{}, err
	}
	var targets []workflow.CurrentNode
	if executableNodeKind(targetDefinition.Kind()) {
		if len(preview.Choices) != 1 {
			return ManualMoveResult{}, ErrManualMoveTransitionSelectionRequired
		}
		choice := preview.Choices[0]
		valueEnvironment, err := s.manualMoveValueEnvironment(ctx, q, definition, prepared.request.TaskID, currentNodes)
		if err != nil {
			return ManualMoveResult{}, err
		}
		if err := validateManualMoveValues(choice, prepared.request.Values, valueEnvironment); err != nil {
			return ManualMoveResult{}, err
		}
		targetMutation, err := s.prepareExecutionTargetMutation(ctx, task, executionTarget)
		if err != nil {
			return ManualMoveResult{}, err
		}
		executionRoot := targetMutation.executionRoot
		if targetMutation.candidateToLock != nil {
			executionRoot = targetMutation.candidateToLock.Root
		}
		for _, edge := range choice.Edges {
			targetNode, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
			if err != nil {
				return ManualMoveResult{}, err
			}
			if targetNode.Kind() == workflow.NodeKindScript {
				if err := s.validateScriptNodeForExecution(ctx, q, workflow.NodeIDOf(targetNode), &executionRoot); err != nil {
					return ManualMoveResult{}, err
				}
			}
		}
		targets, err = s.materializeManualMoveTargets(ctx, q, definition, choice, currentNodes, valueEnvironment, prepared.request.Values)
		if err != nil {
			return ManualMoveResult{}, err
		}
		if err := applyPreparedExecutionTargetMutation(ctx, q, task, targetMutation, s.now().UnixMilli()); err != nil {
			return ManualMoveResult{}, err
		}
	} else {
		if executionTarget != nil {
			return ManualMoveResult{}, errors.New("manual move to a non-executable target does not accept an execution target")
		}
		targets = []workflow.CurrentNode{{
			Reference: workflow.CurrentNodeReference{},
		}}
		targets[0], err = newNonExecutableCurrentNode(prepared.request.TaskID, workflow.NodeIDOf(targetDefinition))
		if err != nil {
			return ManualMoveResult{}, err
		}
	}
	attentionResolution, err := taskApprovalAttentionResolution(ctx, q, prepared.request.TaskID)
	if err != nil {
		return ManualMoveResult{}, err
	}
	if _, err := q.DeleteTaskPendingApprovalsByTask(ctx, string(prepared.request.TaskID)); err != nil {
		return ManualMoveResult{}, err
	}
	removed, err := q.DeleteTaskCurrentNodes(ctx, string(prepared.request.TaskID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	if removed != int64(len(currentNodes)) {
		return ManualMoveResult{}, sql.ErrNoRows
	}
	if _, err := q.DeleteTaskActiveFanout(ctx, string(prepared.request.TaskID)); err != nil {
		return ManualMoveResult{}, err
	}
	if len(targets) == 1 {
		if err := insertTaskCurrentNode(ctx, q, targets[0]); err != nil {
			return ManualMoveResult{}, err
		}
	} else if err := insertTaskFanoutTargets(ctx, q, prepared.request.TaskID, targets); err != nil {
		return ManualMoveResult{}, err
	}
	if err := touchTaskUpdatedAt(ctx, q, string(prepared.request.TaskID), s.now().UnixMilli()); err != nil {
		return ManualMoveResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManualMoveResult{}, err
	}
	removedReferences := make([]workflow.CurrentNodeReference, 0, len(currentNodes))
	for _, currentNode := range currentNodes {
		removedReferences = append(removedReferences, currentNode.Reference)
	}
	result := ManualMoveResult{
		Outcome: ManualMoveResultOutcomeApplied,
		Mutation: workflow.CurrentNodeMutationResult{
			Removed: removedReferences,
			Created: append([]workflow.CurrentNode(nil), targets...),
		},
		TaskAttentionResolution: attentionResolution,
	}
	if err := result.Validate(); err != nil {
		return ManualMoveResult{}, err
	}
	return result, nil
}

func manualMovePreviewBlockerError(blocker ManualMoveBlocker) error {
	switch blocker {
	case ManualMoveBlockerNoSourcePosition:
		return ErrManualMoveNoSourcePosition
	case ManualMoveBlockerContextSessionUnavailable:
		return ErrManualMoveTransitionNotUsable
	case ManualMoveBlockerParallelBranchRequiresFanOut, ManualMoveBlockerNoUsableTransition:
		return ErrManualMoveTransitionNotUsable
	default:
		return fmt.Errorf("manual move is blocked: %s", blocker)
	}
}

func (s *Store) materializeManualMoveTargets(
	ctx context.Context,
	q *sqlitegen.Queries,
	definition workflow.Definition,
	choice ManualMoveTransitionChoice,
	currentNodes []workflow.CurrentNode,
	environment manualMoveValueEnvironment,
	submitted map[workflow.ModelKey]map[string]string,
) ([]workflow.CurrentNode, error) {
	if len(choice.Edges) == 0 {
		return nil, ErrManualMoveTransitionNotUsable
	}
	targets := make([]workflow.CurrentNode, 0, len(choice.Edges))
	contextSource := manualMoveContextCurrentNode(currentNodes)
	priorValues := manualMoveBasePriorValues(currentNodes)
	for _, edge := range choice.Edges {
		target, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
		if err != nil {
			return nil, err
		}
		var branchKey *workflow.TransitionBranchKey
		if len(choice.Edges) > 1 {
			value := workflow.TransitionBranchKey(strings.TrimSpace(string(edge.Key)))
			if value == "" {
				return nil, errors.New("manual move fan-out branch key is required")
			}
			branchKey = &value
		}
		targetCurrentNode, err := materializeTransitionTargetCurrentNode(ctx, q, transitionTargetMaterializationRequest{
			Definition:           definition,
			Edge:                 edge,
			Source:               choice.SourceNode,
			Target:               target,
			ContextCurrentSource: contextSource,
			ManualMoveContext:    true,
			PriorValues:          priorValues,
			Value: func(providerNode, _ workflow.ModelKey, outputName string) (string, bool) {
				value := manualMoveSubmittedOrResolved(providerNode, outputName, environment, submitted)
				if value == nil {
					return "", false
				}
				return *value, true
			},
			TransitionBranchKey: branchKey,
		})
		if err != nil {
			return nil, err
		}
		targets = append(targets, targetCurrentNode)
	}
	if len(targets) > 1 {
		if err := validateFanoutTargets(currentNodes[0].Reference.TaskID, targets); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func manualMoveBasePriorValues(currentNodes []workflow.CurrentNode) workflow.MaterializedPriorValues {
	priorValues := workflow.MaterializedPriorValues{}
	for _, currentNode := range currentNodes {
		for transitionKey, values := range currentNode.PriorValues.TransitionParameters {
			for parameterName, value := range values {
				if _, exists := priorValues.TransitionParameters[transitionKey][parameterName]; !exists {
					priorValues.SetTransitionParameter(transitionKey, parameterName, value)
				}
			}
		}
	}
	return priorValues
}

func manualMoveContextCurrentNode(currentNodes []workflow.CurrentNode) workflow.CurrentNode {
	if len(currentNodes) == 0 {
		return workflow.CurrentNode{}
	}
	current := currentNodes[0]
	if !current.Reference.IsBranchScoped() {
		return current
	}
	reference, err := workflow.NewCurrentNodeReference(current.Reference.TaskID, current.Reference.NodeID, nil)
	if err != nil {
		panic(fmt.Sprintf("manual move context reference: %v", err))
	}
	current.Reference = reference
	return current
}

func manualMoveTransitionGroup(definition workflow.Definition, edge workflow.Edge) (workflow.TransitionGroup, error) {
	for _, group := range definition.TransitionGroups {
		if group.ID == edge.TransitionGroupID {
			return group, nil
		}
	}
	return workflow.TransitionGroup{}, fmt.Errorf("manual move edge %q transition group %q is absent from workflow %q", edge.ID, edge.TransitionGroupID, definition.ID)
}
