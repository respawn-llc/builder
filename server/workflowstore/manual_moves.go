package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

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

type ManualMoveTargetAssignment struct {
	CurrentNode workflow.CurrentNodeReference
	SessionID   runtimeids.SessionID
}

type ManualMoveTargetAssignmentPreparation struct {
	Assignments []ManualMoveTargetAssignment
	Diagnostic  error
	Abort       func(error) error
}

type ManualMoveTargetAssignmentPreparer func(context.Context, []CurrentNodeStartContext) (ManualMoveTargetAssignmentPreparation, error)

type preparedManualMoveAssignments struct {
	targets         []workflow.CurrentNode
	assignments     []ManualMoveTargetAssignment
	workflowVersion *int64
	abort           func(error) error
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
	if prepared.noOp || !executableNodeKind(prepared.target.Kind()) {
		return s.applyManualMove(ctx, prepared, nil, nil)
	}
	return ManualMoveResult{}, errors.New("executable Manual Move requires lifecycle assignment preparation")
}

func (s *Store) PrepareManualMove(ctx context.Context, req ManualMoveRequest) (preparation ManualMovePreparation, resultErr error) {
	defer func() {
		reportWorkflowInvariantError(s.invariantPolicy, resultErr)
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

// ApplyManualMove applies moves that cannot create Agent work. Agent
// placement must flow through ApplyManualMoveWithTargetAssignments.
func (s *Store) ApplyManualMove(
	ctx context.Context,
	prepared ManualMovePreparation,
	executionTarget *ExecutionTargetCandidate,
) (ManualMoveResult, error) {
	if !prepared.noOp && executableNodeKind(prepared.target.Kind()) {
		return ManualMoveResult{}, errors.New("executable Manual Move requires assignment preparation")
	}
	return s.applyManualMove(ctx, prepared, executionTarget, nil)
}

func (s *Store) ApplyManualMoveWithTargetAssignments(
	ctx context.Context,
	prepared ManualMovePreparation,
	executionTarget *ExecutionTargetCandidate,
	prepareAssignments ManualMoveTargetAssignmentPreparer,
) (result ManualMoveResult, resultErr error) {
	if !prepared.noOp && executableNodeKind(prepared.target.Kind()) && prepareAssignments == nil {
		return ManualMoveResult{}, errors.New("executable Manual Move requires assignment preparation")
	}
	return s.applyManualMove(ctx, prepared, executionTarget, prepareAssignments)
}

func (s *Store) applyManualMove(
	ctx context.Context,
	prepared ManualMovePreparation,
	executionTarget *ExecutionTargetCandidate,
	prepareAssignments ManualMoveTargetAssignmentPreparer,
) (result ManualMoveResult, resultErr error) {
	task, err := s.queries.GetTask(ctx, string(prepared.request.TaskID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	workflowID := runtimeids.WorkflowID(task.WorkflowID)
	lease, err := s.graphSaves.AcquireShared(ctx, workflowID)
	if err != nil {
		return ManualMoveResult{}, err
	}
	defer lease.Release()
	return s.applyManualMoveWithinWorkflowLane(ctx, prepared, executionTarget, prepareAssignments)
}

func (s *Store) applyManualMoveWithinWorkflowLane(
	ctx context.Context,
	prepared ManualMovePreparation,
	executionTarget *ExecutionTargetCandidate,
	prepareAssignments ManualMoveTargetAssignmentPreparer,
) (result ManualMoveResult, resultErr error) {
	defer func() {
		reportWorkflowInvariantError(s.invariantPolicy, resultErr)
	}()
	if strings.TrimSpace(string(prepared.request.TaskID)) == "" || (!prepared.noOp && prepared.target == nil) {
		return ManualMoveResult{}, errors.New("manual move preparation is invalid")
	}
	executionTargetPreparation, err := s.prepareManualMoveExecutionTarget(ctx, prepared, executionTarget)
	if err != nil {
		return ManualMoveResult{}, err
	}
	var preparedAssignments preparedManualMoveAssignments
	assignmentsConsumed := false
	defer func() {
		if !assignmentsConsumed && preparedAssignments.abort != nil {
			resultErr = preparedAssignments.abort(resultErr)
		}
	}()
	if prepareAssignments != nil && !prepared.noOp && executableNodeKind(prepared.target.Kind()) {
		targets, contexts, workflowVersion, err := s.prepareManualMoveAssignmentContexts(ctx, prepared, executionTargetPreparation)
		if err != nil {
			return ManualMoveResult{}, err
		}
		preparedAssignments.targets = targets
		preparedAssignments.workflowVersion = &workflowVersion
		assignmentPreparation, err := prepareAssignments(ctx, contexts)
		preparedAssignments.abort = assignmentPreparation.Abort
		if err != nil {
			return ManualMoveResult{}, err
		}
		preparedAssignments.assignments = assignmentPreparation.Assignments
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
	definition, workflowRecord, err := workflowDefinitionFromQueries(ctx, q, runtimeids.WorkflowID(task.WorkflowID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	if preparedAssignments.workflowVersion != nil && workflowRecord.Version != *preparedAssignments.workflowVersion {
		return ManualMoveResult{}, errors.New("workflow changed after Manual Move assignment preparation")
	}
	targetDefinition, err := currentNodeDefinitionNode(definition, prepared.request.TargetNodeID)
	if err != nil {
		return ManualMoveResult{}, err
	}
	if targetDefinition.Kind() != prepared.target.Kind() {
		return ManualMoveResult{}, errManualMoveTargetShapeChanged
	}
	var (
		targets         []workflow.CurrentNode
		invariants      []workflow.RetainedTargetInvariantDetail
		legacyFallbacks []legacyContinuationSourceFallbackDetail
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
		targets, invariants, legacyFallbacks, err = s.materializeManualMoveTargets(
			ctx,
			q,
			definition,
			choice,
			currentNodes,
			valueEnvironment,
			prepared.request.Values,
		)
		if err != nil {
			return ManualMoveResult{}, err
		}
		applyManualMoveCommentary(targets, prepared.request.Commentary)
		if prepareAssignments != nil {
			if !sameManualMoveTargets(targets, preparedAssignments.targets) {
				return ManualMoveResult{}, errors.New("manual move targets changed after assignment preparation")
			}
			targets, err = applyManualMoveTargetAssignments(targets, preparedAssignments.assignments)
			if err != nil {
				return ManualMoveResult{}, err
			}
			targets, err = prepareManualMoveAssignedTargetsForInsert(ctx, q, targets, nowTime)
			if err != nil {
				return ManualMoveResult{}, err
			}
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
		applyManualMoveCommentary(targets, prepared.request.Commentary)
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
	assignmentsConsumed = true
	for _, detail := range invariants {
		reportRetainedTargetInvariantAfterCommit(s.invariantPolicy, detail)
	}
	for _, detail := range legacyFallbacks {
		reportLegacyContinuationSourceAfterCommit(s.invariantPolicy, detail)
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

func (s *Store) prepareManualMoveAssignmentContexts(
	ctx context.Context,
	prepared ManualMovePreparation,
	executionTarget preparedManualMoveExecutionTarget,
) ([]workflow.CurrentNode, []CurrentNodeStartContext, int64, error) {
	taskRow, err := s.queries.GetTask(ctx, string(prepared.request.TaskID))
	if err != nil {
		return nil, nil, 0, err
	}
	task, err := taskRecordFromTask(taskRow)
	if err != nil {
		return nil, nil, 0, err
	}
	definition, workflowRecord, err := workflowDefinitionFromQueries(
		ctx,
		s.queries,
		runtimeids.WorkflowID(taskRow.WorkflowID),
	)
	if err != nil {
		return nil, nil, 0, err
	}
	currentNodes, err := s.listTaskCurrentNodes(ctx, s.queries, prepared.request.TaskID)
	if err != nil {
		return nil, nil, 0, err
	}
	preview, err := s.resolveManualMove(ctx, s.queries, prepared.request)
	if err != nil {
		return nil, nil, 0, err
	}
	if preview.Outcome == ManualMovePreviewOutcomeNoOp {
		return nil, nil, workflowRecord.Version, nil
	}
	if preview.Outcome != ManualMovePreviewOutcomeTransition || len(preview.Choices) != 1 {
		return nil, nil, 0, ErrManualMoveTransitionSelectionRequired
	}
	choice := preview.Choices[0]
	valueEnvironment, err := s.manualMoveValueEnvironment(
		ctx,
		s.queries,
		definition,
		prepared.request.TaskID,
		currentNodes,
	)
	if err != nil {
		return nil, nil, 0, err
	}
	if err := validateManualMoveValues(choice, prepared.request.Values, valueEnvironment); err != nil {
		return nil, nil, 0, err
	}
	targets, _, _, err := s.materializeManualMoveTargets(
		ctx,
		s.queries,
		definition,
		choice,
		currentNodes,
		valueEnvironment,
		prepared.request.Values,
	)
	if err != nil {
		return nil, nil, 0, err
	}
	applyManualMoveCommentary(targets, prepared.request.Commentary)
	contexts := make([]CurrentNodeStartContext, 0, len(targets))
	for _, target := range targets {
		context, err := s.resolveMaterializedCurrentNodeStartContext(
			ctx,
			s.queries,
			task,
			workflowRecord,
			definition,
			target,
			&executionTarget.mutation.executionRoot,
		)
		if err != nil {
			return nil, nil, 0, err
		}
		contexts = append(contexts, context)
	}
	return targets, contexts, workflowRecord.Version, nil
}

func applyManualMoveCommentary(targets []workflow.CurrentNode, commentary string) {
	if commentary == "" {
		return
	}
	for index := range targets {
		if targets[index].CurrentInputValues == nil {
			targets[index].CurrentInputValues = make(map[string]string)
		}
		targets[index].CurrentInputValues[workflow.RuntimePromptParameterCommentary] = commentary
	}
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
) (
	[]workflow.CurrentNode,
	[]workflow.RetainedTargetInvariantDetail,
	[]legacyContinuationSourceFallbackDetail,
	error,
) {
	if len(choice.Edges) == 0 {
		return nil, nil, nil, ErrManualMoveTransitionNotUsable
	}
	targets := make([]workflow.CurrentNode, 0, len(choice.Edges))
	invariants := make([]workflow.RetainedTargetInvariantDetail, 0, len(choice.Edges))
	legacyFallbacks := make([]legacyContinuationSourceFallbackDetail, 0, len(choice.Edges))
	contextSource := manualMoveContextCurrentNode(currentNodes)
	priorValues := manualMoveBasePriorValues(currentNodes)
	for _, edge := range choice.Edges {
		target, err := currentNodeDefinitionNode(definition, edge.TargetNodeID)
		if err != nil {
			return nil, nil, nil, err
		}
		var branchKey *workflow.TransitionBranchKey
		if len(choice.Edges) > 1 {
			value := workflow.TransitionBranchKey(strings.TrimSpace(string(edge.Key)))
			if value == "" {
				return nil, nil, nil, errors.New("manual move fan-out branch key is required")
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
			return nil, nil, nil, err
		}
		if materializedTarget.Invariant != nil {
			checkRetainedTargetInvariantBeforeMutation(s.invariantPolicy, *materializedTarget.Invariant)
			invariants = append(invariants, *materializedTarget.Invariant)
		}
		if materializedTarget.LegacyFallback != nil {
			checkLegacyContinuationSourceBeforeMutation(s.invariantPolicy, *materializedTarget.LegacyFallback)
			legacyFallbacks = append(legacyFallbacks, *materializedTarget.LegacyFallback)
		}
		targets = append(targets, materializedTarget.CurrentNode)
	}
	if len(targets) > 1 {
		if err := validateFanoutTargets(currentNodes[0].Reference.TaskID, targets); err != nil {
			return nil, nil, nil, err
		}
	}
	return targets, invariants, legacyFallbacks, nil
}

func applyManualMoveTargetAssignments(
	targets []workflow.CurrentNode,
	assignments []ManualMoveTargetAssignment,
) ([]workflow.CurrentNode, error) {
	if len(assignments) == 0 {
		return targets, nil
	}
	byReference := make(map[workflow.CurrentNodeReferenceKey]runtimeids.SessionID, len(assignments))
	for index, assignment := range assignments {
		if assignment.SessionID.IsZero() {
			return nil, fmt.Errorf("manual move target assignment at index %d has no Session", index)
		}
		key, err := assignment.CurrentNode.Key()
		if err != nil {
			return nil, fmt.Errorf("manual move target assignment at index %d: %w", index, err)
		}
		if _, duplicate := byReference[key]; duplicate {
			return nil, fmt.Errorf("manual move target assignment at index %d is duplicated", index)
		}
		byReference[key] = assignment.SessionID
	}
	result := append([]workflow.CurrentNode(nil), targets...)
	for index := range result {
		if result[index].AgentExecutionSelection == nil {
			continue
		}
		key, err := result[index].Reference.Key()
		if err != nil {
			return nil, err
		}
		sessionID, assigned := byReference[key]
		if !assigned {
			return nil, fmt.Errorf("manual move Agent target %v has no prepared assignment", result[index].Reference)
		}
		result[index].SessionID = &sessionID
		delete(byReference, key)
	}
	if len(byReference) != 0 {
		return nil, errors.New("manual move target assignments contain an unknown Current Node")
	}
	return result, nil
}

func sameManualMoveTargets(left, right []workflow.CurrentNode) bool {
	if len(left) != len(right) {
		return false
	}
	rightByReference := make(map[workflow.CurrentNodeReferenceKey]workflow.CurrentNode, len(right))
	for _, target := range right {
		key, err := target.Reference.Key()
		if err != nil {
			return false
		}
		rightByReference[key] = target
	}
	for _, target := range left {
		key, err := target.Reference.Key()
		if err != nil {
			return false
		}
		prepared, exists := rightByReference[key]
		if !exists ||
			!sameOptionalEdgeID(target.EnteredByEdgeID, prepared.EnteredByEdgeID) ||
			!maps.Equal(target.CurrentInputValues, prepared.CurrentInputValues) ||
			!sameMaterializedPriorValues(target.PriorValues, prepared.PriorValues) ||
			!sameOptionalSessionID(target.SessionID, prepared.SessionID) ||
			!sameMaterializedContinuationSource(target.ContinuationSource, prepared.ContinuationSource) ||
			!sameAgentExecutionSelection(target.AgentExecutionSelection, prepared.AgentExecutionSelection) {
			return false
		}
	}
	return true
}

func sameOptionalEdgeID(left, right *workflow.EdgeID) bool {
	return (left == nil && right == nil) ||
		(left != nil && right != nil && *left == *right)
}

func sameMaterializedPriorValues(left, right workflow.MaterializedPriorValues) bool {
	return maps.EqualFunc(
		left.TransitionParameters,
		right.TransitionParameters,
		func(left, right map[string]string) bool {
			return maps.Equal(left, right)
		},
	)
}

func sameAgentExecutionSelection(left, right *workflow.AgentExecutionSelection) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Assignee == right.Assignee &&
		left.Origin == right.Origin &&
		((left.Thinking == nil && right.Thinking == nil) ||
			(left.Thinking != nil && right.Thinking != nil && *left.Thinking == *right.Thinking))
}

func prepareManualMoveAssignedTargetsForInsert(
	ctx context.Context,
	q *sqlitegen.Queries,
	targets []workflow.CurrentNode,
	associatedAt time.Time,
) ([]workflow.CurrentNode, error) {
	result := append([]workflow.CurrentNode(nil), targets...)
	for index := range result {
		if result[index].SessionID == nil {
			continue
		}
		previous, hasPrevious, err := currentTaskSessionAssociationBeforeBinding(
			ctx,
			q,
			result[index].Reference,
		)
		if err != nil {
			return nil, err
		}
		sourceSessionID, err := bindingSourceSessionID(result[index], *result[index].SessionID)
		if err != nil {
			return nil, err
		}
		if result[index].ContinuationSource.Kind() == workflow.MaterializedContinuationSourceDeferredSelf {
			association, err := normalizeTaskSessionAssociationRequest(TaskSessionAssociationRequest{
				SessionID:    *result[index].SessionID,
				CurrentNode:  result[index].Reference,
				AssociatedAt: associatedAt,
			})
			if err != nil {
				return nil, err
			}
			if err := bindSessionToTask(ctx, q, association); err != nil {
				return nil, err
			}
			source, err := workflow.NewExactMaterializedContinuationSource(sourceSessionID)
			if err != nil {
				return nil, err
			}
			result[index].ContinuationSource = source
		}
		if hasPrevious && previous.SessionID != *result[index].SessionID {
			if err := retireDependentCurrentTaskSessionAssociations(
				ctx,
				q,
				result[index].Reference,
				previous.SessionID,
				sourceSessionID,
			); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
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
