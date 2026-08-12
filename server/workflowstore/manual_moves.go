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

type ManualMovePreparation struct {
	request                 ManualMoveRequest
	target                  workflow.Node
	choice                  *ManualMoveTransitionChoice
	currentNodes            []workflow.CurrentNode
	noOp                    bool
	requiresExecutionTarget bool
}

type preparedManualMoveExecutionTarget struct {
	mutation    preparedExecutionTargetMutation
	targetShape map[workflow.EdgeID]manualMoveTargetShape
}

type manualMoveTargetShape struct {
	nodeID     workflow.NodeID
	kind       workflow.NodeKind
	scriptPath workflow.OptionalScriptPath
}

var errManualMoveTargetShapeChanged = errors.New("manual move target shape changed after execution-target preparation")

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

func (s *Store) PrepareManualMove(ctx context.Context, req ManualMoveRequest) (preparation ManualMovePreparation, resultErr error) {
	defer func() {
		reportRetainedTargetInvariantError(resultErr)
	}()
	if err := validateCommentarySize(req.Commentary); err != nil {
		return ManualMovePreparation{}, err
	}
	req.Commentary = strings.TrimSpace(req.Commentary)
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
	case ManualMovePreviewOutcomeBlocked:
		return ManualMovePreparation{}, manualMovePreviewBlockerError(preview.Blocker)
	default:
		return ManualMovePreparation{}, fmt.Errorf("manual move preview cannot be prepared from outcome %q", preview.Outcome)
	}
}

func currentNodeDefinitionNodeFromTask(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID, nodeID workflow.NodeID) (workflow.Node, error) {
	task, err := q.GetTask(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	definition, _, err := workflowDefinitionFromQueries(ctx, q, runtimeids.WorkflowID(task.WorkflowID))
	if err != nil {
		return nil, err
	}
	return currentNodeDefinitionNode(definition, nodeID)
}

func (s *Store) ApplyManualMove(ctx context.Context, prepared ManualMovePreparation, executionTarget *ExecutionTargetCandidate) (result ManualMoveResult, resultErr error) {
	defer func() {
		reportRetainedTargetInvariantError(resultErr)
	}()
	if strings.TrimSpace(string(prepared.request.TaskID)) == "" || (!prepared.noOp && prepared.target == nil) {
		return ManualMoveResult{}, errors.New("manual move preparation is invalid")
	}
	executionTargetPreparation, err := s.prepareManualMoveExecutionTarget(ctx, prepared, executionTarget)
	if err != nil {
		return ManualMoveResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManualMoveResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	nowTime := s.now().UTC()
	locked, err := q.AcquireManualMoveTaskWriteLock(ctx, string(prepared.request.TaskID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	if locked != 1 {
		return ManualMoveResult{}, sql.ErrNoRows
	}
	task, err := q.GetTask(ctx, string(prepared.request.TaskID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	currentNodes, err := s.listTaskCurrentNodes(ctx, q, prepared.request.TaskID)
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
	definition, _, err := workflowDefinitionFromQueries(ctx, q, runtimeids.WorkflowID(task.WorkflowID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	targetDefinition, err := currentNodeDefinitionNode(definition, prepared.request.TargetNodeID)
	if err != nil {
		return ManualMoveResult{}, err
	}
	if targetDefinition.Kind() != prepared.target.Kind() {
		return ManualMoveResult{}, errManualMoveTargetShapeChanged
	}
	var (
		targets    []workflow.CurrentNode
		invariants []workflow.RetainedTargetInvariantDetail
	)
	if executableNodeKind(targetDefinition.Kind()) {
		if len(preview.Choices) != 1 {
			return ManualMoveResult{}, ErrManualMoveTransitionSelectionRequired
		}
		choice := preview.Choices[0]
		if err := validatePreparedManualMoveTargetShape(definition, choice, executionTargetPreparation.targetShape); err != nil {
			return ManualMoveResult{}, err
		}
		valueEnvironment, err := s.manualMoveValueEnvironment(ctx, q, definition, prepared.request.TaskID, currentNodes)
		if err != nil {
			return ManualMoveResult{}, err
		}
		if err := validateManualMoveValues(choice, prepared.request.Values, valueEnvironment); err != nil {
			return ManualMoveResult{}, err
		}
		targets, invariants, err = s.materializeManualMoveTargets(ctx, q, definition, choice, currentNodes, valueEnvironment, prepared.request.Values)
		if err != nil {
			return ManualMoveResult{}, err
		}
		if err := applyPreparedExecutionTargetMutation(ctx, q, task, executionTargetPreparation.mutation, nowTime.UnixMilli()); err != nil {
			return ManualMoveResult{}, err
		}
	} else {
		targets = []workflow.CurrentNode{{
			Reference: workflow.CurrentNodeReference{},
		}}
		targets[0], err = newNonExecutableCurrentNode(prepared.request.TaskID, workflow.NodeIDOf(targetDefinition))
		if err != nil {
			return ManualMoveResult{}, err
		}
	}
	if commentary := prepared.request.Commentary; commentary != "" {
		for index := range targets {
			if targets[index].CurrentInputValues == nil {
				targets[index].CurrentInputValues = make(map[string]string)
			}
			targets[index].CurrentInputValues[workflow.RuntimePromptParameterCommentary] = commentary
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
		if err := insertTaskCurrentNode(ctx, q, targets[0], nowTime); err != nil {
			return ManualMoveResult{}, err
		}
	} else if err := insertTaskFanoutTargets(ctx, q, prepared.request.TaskID, targets, nowTime); err != nil {
		return ManualMoveResult{}, err
	}
	if err := touchTaskUpdatedAt(ctx, q, string(prepared.request.TaskID), nowTime.UnixMilli()); err != nil {
		return ManualMoveResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManualMoveResult{}, err
	}
	for _, detail := range invariants {
		reportRetainedTargetInvariantAfterCommit(detail)
	}
	removedReferences := make([]workflow.CurrentNodeReference, 0, len(currentNodes))
	for _, currentNode := range currentNodes {
		removedReferences = append(removedReferences, currentNode.Reference)
	}
	result = ManualMoveResult{
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

func (s *Store) prepareManualMoveExecutionTarget(
	ctx context.Context,
	prepared ManualMovePreparation,
	executionTarget *ExecutionTargetCandidate,
) (preparedManualMoveExecutionTarget, error) {
	if prepared.noOp {
		return preparedManualMoveExecutionTarget{}, nil
	}
	if !executableNodeKind(prepared.target.Kind()) {
		if executionTarget != nil {
			return preparedManualMoveExecutionTarget{}, errors.New("manual move to a non-executable target does not accept an execution target")
		}
		return preparedManualMoveExecutionTarget{}, nil
	}
	if prepared.choice == nil {
		return preparedManualMoveExecutionTarget{}, ErrManualMoveTransitionSelectionRequired
	}
	task, err := s.queries.GetTask(ctx, string(prepared.request.TaskID))
	if err != nil {
		return preparedManualMoveExecutionTarget{}, err
	}
	targetMutation, err := s.prepareExecutionTargetMutation(ctx, task, executionTarget)
	if err != nil {
		return preparedManualMoveExecutionTarget{}, err
	}
	definition, _, err := workflowDefinitionFromQueries(ctx, s.queries, runtimeids.WorkflowID(task.WorkflowID))
	if err != nil {
		return preparedManualMoveExecutionTarget{}, err
	}
	targetShape := make(map[workflow.EdgeID]manualMoveTargetShape, len(prepared.choice.Edges))
	for _, edge := range prepared.choice.Edges {
		targetNode, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
		if err != nil {
			return preparedManualMoveExecutionTarget{}, err
		}
		if _, exists := targetShape[edge.ID]; exists {
			return preparedManualMoveExecutionTarget{}, fmt.Errorf("manual move target preparation contains duplicate edge %q", edge.ID)
		}
		targetShape[edge.ID] = manualMoveTargetShape{
			nodeID:     edge.TargetNodeID,
			kind:       targetNode.Kind(),
			scriptPath: workflow.NodeScriptPath(targetNode),
		}
		if targetNode.Kind() == workflow.NodeKindScript {
			if err := s.validateScriptNodeForExecution(
				ctx,
				s.queries,
				workflow.NodeIDOf(targetNode),
				&targetMutation.executionRoot,
			); err != nil {
				return preparedManualMoveExecutionTarget{}, err
			}
		}
	}
	return preparedManualMoveExecutionTarget{mutation: targetMutation, targetShape: targetShape}, nil
}

func validatePreparedManualMoveTargetShape(
	definition workflow.Definition,
	choice ManualMoveTransitionChoice,
	prepared map[workflow.EdgeID]manualMoveTargetShape,
) error {
	if len(choice.Edges) != len(prepared) {
		return errManualMoveTargetShapeChanged
	}
	for _, edge := range choice.Edges {
		expected, exists := prepared[edge.ID]
		if !exists || expected.nodeID != edge.TargetNodeID {
			return errManualMoveTargetShapeChanged
		}
		targetNode, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
		if err != nil {
			return err
		}
		if targetNode.Kind() != expected.kind ||
			workflow.NodeScriptPath(targetNode) != expected.scriptPath {
			return errManualMoveTargetShapeChanged
		}
	}
	return nil
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
) ([]workflow.CurrentNode, []workflow.RetainedTargetInvariantDetail, error) {
	if len(choice.Edges) == 0 {
		return nil, nil, ErrManualMoveTransitionNotUsable
	}
	targets := make([]workflow.CurrentNode, 0, len(choice.Edges))
	invariants := make([]workflow.RetainedTargetInvariantDetail, 0, len(choice.Edges))
	contextSource := manualMoveContextCurrentNode(currentNodes)
	priorValues := manualMoveBasePriorValues(currentNodes)
	for _, edge := range choice.Edges {
		target, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
		if err != nil {
			return nil, nil, err
		}
		var branchKey *workflow.TransitionBranchKey
		if len(choice.Edges) > 1 {
			value := workflow.TransitionBranchKey(strings.TrimSpace(string(edge.Key)))
			if value == "" {
				return nil, nil, errors.New("manual move fan-out branch key is required")
			}
			branchKey = &value
		}
		materializedTarget, err := materializeTransitionTargetCurrentNode(ctx, q, transitionTargetMaterializationRequest{
			Definition:                      definition,
			Edge:                            edge,
			Source:                          choice.SourceNode,
			Target:                          target,
			Catalog:                         s.roleResolver,
			ResolveRetainedSessionSelection: s.resolveRetainedSessionSelection,
			ContextTaskID:                   currentNodes[0].Reference.TaskID,
			ContextCurrentSource:            contextSource,
			ManualMoveContext:               true,
			PriorValues:                     priorValues,
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
			return nil, nil, err
		}
		if materializedTarget.Invariant != nil {
			checkRetainedTargetInvariantBeforeMutation(*materializedTarget.Invariant)
			invariants = append(invariants, *materializedTarget.Invariant)
		}
		targets = append(targets, materializedTarget.CurrentNode)
	}
	if len(targets) > 1 {
		if err := validateFanoutTargets(currentNodes[0].Reference.TaskID, targets); err != nil {
			return nil, nil, err
		}
	}
	return targets, invariants, nil
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

func manualMoveContextCurrentNode(currentNodes []workflow.CurrentNode) *workflow.CurrentNode {
	if len(currentNodes) != 1 || currentNodes[0].Reference.IsBranchScoped() {
		return nil
	}
	current := currentNodes[0]
	return &current
}
