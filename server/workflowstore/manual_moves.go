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

func (s *Store) ManualMoveTask(ctx context.Context, req ManualMoveRequest) (ManualMoveResult, error) {
	preparation, err := s.PrepareManualMove(ctx, req)
	if err != nil {
		return ManualMoveResult{}, err
	}
	return s.ApplyManualMove(ctx, preparation, req.ExecutionTarget)
}

type ManualMovePreparation struct {
	request ManualMoveRequest
	target  workflow.Node
}

func (p ManualMovePreparation) RequiresExecutionTarget() bool {
	return false
}

func (s *Store) PrepareManualMove(ctx context.Context, req ManualMoveRequest) (ManualMovePreparation, error) {
	if strings.TrimSpace(string(req.TaskID)) == "" {
		return ManualMovePreparation{}, errors.New("task id is required")
	}
	if strings.TrimSpace(string(req.TargetNodeID)) == "" {
		return ManualMovePreparation{}, errors.New("target node id is required")
	}
	task, err := s.queries.GetTask(ctx, string(req.TaskID))
	if err != nil {
		return ManualMovePreparation{}, err
	}
	definition, _, err := s.GetDefinition(ctx, workflow.WorkflowID(task.WorkflowID))
	if err != nil {
		return ManualMovePreparation{}, err
	}
	target, err := currentNodeDefinitionNode(definition, req.TargetNodeID)
	if err != nil {
		return ManualMovePreparation{}, err
	}
	switch target.Kind() {
	case workflow.NodeKindStart, workflow.NodeKindTerminal:
	case workflow.NodeKindAgent, workflow.NodeKindScript:
		return ManualMovePreparation{}, ErrManualMoveExecutableTargetNeedsEdge
	default:
		return ManualMovePreparation{}, errors.New("manual move target must be backlog or terminal")
	}
	return ManualMovePreparation{request: req, target: target}, nil
}

func (s *Store) ApplyManualMove(ctx context.Context, preparation ManualMovePreparation, executionTarget *ExecutionTargetCandidate) (ManualMoveResult, error) {
	if executionTarget != nil {
		return ManualMoveResult{}, errors.New("manual move to a non-executable target does not accept an execution target")
	}
	if strings.TrimSpace(string(preparation.request.TaskID)) == "" {
		return ManualMoveResult{}, errors.New("manual move preparation is invalid")
	}
	if preparation.target == nil {
		return ManualMoveResult{}, errors.New("manual move preparation is invalid")
	}
	targetCurrentNode, err := newNonExecutableCurrentNode(preparation.request.TaskID, workflow.NodeIDOf(preparation.target))
	if err != nil {
		return ManualMoveResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManualMoveResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	currentNodes, err := listTaskCurrentNodes(ctx, q, preparation.request.TaskID)
	if err != nil {
		return ManualMoveResult{}, err
	}
	if len(currentNodes) == 0 {
		return ManualMoveResult{}, ErrManualMoveNoSourcePosition
	}
	if _, err := q.DeleteTaskPendingApprovalsByTask(ctx, string(preparation.request.TaskID)); err != nil {
		return ManualMoveResult{}, err
	}
	removed, err := q.DeleteTaskCurrentNodes(ctx, string(preparation.request.TaskID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	if removed != int64(len(currentNodes)) {
		return ManualMoveResult{}, sql.ErrNoRows
	}
	if _, err := q.DeleteTaskActiveFanout(ctx, string(preparation.request.TaskID)); err != nil {
		return ManualMoveResult{}, err
	}
	if err := insertTaskCurrentNode(ctx, q, targetCurrentNode); err != nil {
		return ManualMoveResult{}, err
	}
	if err := touchTaskUpdatedAt(ctx, q, string(preparation.request.TaskID), s.now().UnixMilli()); err != nil {
		return ManualMoveResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManualMoveResult{}, err
	}
	removedReferences := make([]workflow.CurrentNodeReference, 0, len(currentNodes))
	for _, currentNode := range currentNodes {
		removedReferences = append(removedReferences, currentNode.Reference)
	}
	return ManualMoveResult{CurrentNodeMutationResult: workflow.CurrentNodeMutationResult{
		Removed: removedReferences,
		Created: []workflow.CurrentNode{targetCurrentNode},
	}}, nil
}

type preparedManualMove struct {
	task                        sqlitegen.TaskRecord
	definition                  workflow.Definition
	workflow                    WorkflowRecord
	sourcePlacement             workflow.PlacementID
	sourceNode                  workflow.Node
	sourceRunID                 workflow.RunID
	sourcePlacementRunID        workflow.RunID
	pendingApprovalTransitionID string
	targetNode                  workflow.Node
	edge                        workflow.Edge
	groupSnapshot               transitionContractSnapshot
	transitionState             string
	edgeState                   string
	actor                       string
	outputValuesJSON            string
	autoApprovedExecutable      bool
}

func (s *Store) prepareManualMove(ctx context.Context, req ManualMoveRequest) (preparedManualMove, error) {
	if strings.TrimSpace(string(req.TaskID)) == "" {
		return preparedManualMove{}, errors.New("task id is required")
	}
	if strings.TrimSpace(string(req.TargetNodeID)) == "" {
		return preparedManualMove{}, errors.New("target node id is required")
	}
	actor := strings.TrimSpace(req.Actor)
	if actor == "" {
		actor = "user"
	}
	task, err := s.queries.GetTask(ctx, string(req.TaskID))
	if err != nil {
		return preparedManualMove{}, err
	}
	def, workflowRecord, err := s.GetDefinition(ctx, workflow.WorkflowID(task.WorkflowID))
	if err != nil {
		return preparedManualMove{}, err
	}
	derived := workflow.DeriveWiring(def)
	sourcePlacement, sourceNodeID, pendingApprovalTransitionID, err := s.manualMoveSource(ctx, req.TaskID)
	if err != nil {
		return preparedManualMove{}, err
	}
	sourceNode, ok := definitionNode(def, sourceNodeID)
	if !ok {
		return preparedManualMove{}, fmt.Errorf("source node %q missing", sourceNodeID)
	}
	targetNode, ok := definitionNode(def, req.TargetNodeID)
	if !ok {
		return preparedManualMove{}, fmt.Errorf("target node %q missing", req.TargetNodeID)
	}
	if pendingApprovalTransitionID == "" && sourceNode.Kind() == workflow.NodeKindStart && executableNodeKind(targetNode.Kind()) {
		if err := s.preflightInitialExecution(def); err != nil {
			return preparedManualMove{}, err
		}
	}
	group, edge, ok := definitionEdgeBetween(def, workflow.NodeIDOf(sourceNode), workflow.NodeIDOf(targetNode))
	sourcePlacementRunID, sourceSessionID, err := s.latestRunForPlacement(ctx, sourcePlacement)
	if err != nil {
		return preparedManualMove{}, err
	}
	sourceRunID := sourcePlacementRunID
	reusedOutputValues := map[string]string(nil)
	if targetNode.Kind() == workflow.NodeKindTerminal && sourceNode.Kind() != workflow.NodeKindTerminal {
		group, edge, ok = terminalArchiveManualMoveContract(sourceNode, targetNode)
	} else if !ok {
		group, edge, reusedOutputValues, sourceRunID, sourceSessionID, ok, err = s.backwardManualMoveEdge(ctx, sourcePlacement, targetNode)
		if err != nil {
			return preparedManualMove{}, err
		}
		if !ok && targetNode.Kind() == workflow.NodeKindStart {
			group, edge, ok = startResetManualMoveContract(sourceNode, targetNode)
		}
		if !ok && req.AllowMissingEdge {
			if executableNodeKind(targetNode.Kind()) {
				return preparedManualMove{}, ErrManualMoveExecutableTargetNeedsEdge
			}
			group, edge, ok = missingEdgeManualMoveContract(sourceNode, targetNode)
		}
		if !ok {
			return preparedManualMove{}, fmt.Errorf("no workflow edge from %s to %s", workflow.NodeKey(sourceNode), workflow.NodeKey(targetNode))
		}
	}
	outputValues := req.OutputValues
	if outputValues == nil {
		outputValues = map[string]string{}
	}
	if len(outputValues) == 0 && len(reusedOutputValues) > 0 {
		outputValues = reusedOutputValues
	}
	contextSource := workflow.CanonicalContextSource(edge.ContextSource)
	if contextSource.Kind == workflow.ContextSourceSelectedNode {
		return preparedManualMove{}, ErrManualMoveSelectedContextSource
	}
	if contextSource.Kind == workflow.ContextSourceImmediateSource && edge.ContextMode == workflow.ContextModeContinueSession && strings.TrimSpace(sourceSessionID) == "" {
		return preparedManualMove{}, ErrManualMoveContinueSessionNeedsSource
	}
	groupSnapshot := transitionContractSnapshot{
		ID:           group.ID,
		SourceNodeID: workflow.NodeIDOf(sourceNode),
		TransitionID: string(group.TransitionID),
		DisplayName:  group.DisplayName,
		Edges:        []edgeContractSnapshot{manualMoveEdgeSnapshot(edge, sourceNode, targetNode, derived)},
	}
	if issues := requiredOutputIssues(groupSnapshot, outputValues); len(issues) > 0 {
		return preparedManualMove{}, CompletionValidationError{Issues: issues}
	}
	transitionState := "applied"
	edgeState := "applied"
	if executableNodeKind(targetNode.Kind()) || edge.RequiresApproval {
		transitionState = "pending_approval"
		edgeState = "pending"
	}
	autoApprovedExecutable := executableNodeKind(targetNode.Kind()) && req.AutoApprove && !edge.RequiresApproval
	if autoApprovedExecutable {
		transitionState = "applied"
		edgeState = "applied"
	}
	if (transitionState == "pending_approval" || autoApprovedExecutable) && sourceRunID == "" && !req.AllowMissingEdge {
		return preparedManualMove{}, ErrManualMoveApprovalNeedsSourceRun
	}
	outputValuesJSON, err := workflow.MarshalString(outputValues)
	if err != nil {
		return preparedManualMove{}, err
	}
	return preparedManualMove{
		task:                        task,
		definition:                  def,
		workflow:                    workflowRecord,
		sourcePlacement:             sourcePlacement,
		sourceNode:                  sourceNode,
		sourceRunID:                 sourceRunID,
		sourcePlacementRunID:        sourcePlacementRunID,
		pendingApprovalTransitionID: pendingApprovalTransitionID,
		targetNode:                  targetNode,
		edge:                        edge,
		groupSnapshot:               groupSnapshot,
		transitionState:             transitionState,
		edgeState:                   edgeState,
		actor:                       actor,
		outputValuesJSON:            outputValuesJSON,
		autoApprovedExecutable:      autoApprovedExecutable,
	}, nil
}

func terminalArchiveManualMoveContract(sourceNode workflow.Node, targetNode workflow.Node) (workflow.TransitionGroup, workflow.Edge, bool) {
	group := workflow.TransitionGroup{
		ID:           "",
		SourceNodeID: workflow.NodeIDOf(sourceNode),
		TransitionID: "manual_done",
		DisplayName:  "Move to Done",
	}
	edge := workflow.Edge{
		ID:                 "",
		Key:                "manual_done",
		TargetNodeID:       workflow.NodeIDOf(targetNode),
		ContextMode:        workflow.ContextModeNewSession,
		ContextSource:      workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
		RequiresApproval:   false,
		InputBindings:      nil,
		OutputRequirements: nil,
	}
	return group, edge, true
}

func startResetManualMoveContract(sourceNode workflow.Node, targetNode workflow.Node) (workflow.TransitionGroup, workflow.Edge, bool) {
	group := workflow.TransitionGroup{
		ID:           "",
		SourceNodeID: workflow.NodeIDOf(sourceNode),
		TransitionID: "manual_start",
		DisplayName:  "Move to Backlog",
	}
	edge := workflow.Edge{
		ID:                 "",
		Key:                "manual_start",
		TargetNodeID:       workflow.NodeIDOf(targetNode),
		ContextMode:        workflow.ContextModeNewSession,
		ContextSource:      workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
		RequiresApproval:   false,
		InputBindings:      nil,
		OutputRequirements: nil,
	}
	return group, edge, true
}

func missingEdgeManualMoveContract(sourceNode workflow.Node, targetNode workflow.Node) (workflow.TransitionGroup, workflow.Edge, bool) {
	group := workflow.TransitionGroup{
		ID:           "",
		SourceNodeID: workflow.NodeIDOf(sourceNode),
		TransitionID: "manual_override",
		DisplayName:  "Manual Override",
	}
	edge := workflow.Edge{
		ID:                 "",
		Key:                "manual_override",
		TargetNodeID:       workflow.NodeIDOf(targetNode),
		ContextMode:        workflow.ContextModeNewSession,
		ContextSource:      workflow.ContextSource{Kind: workflow.ContextSourceImmediateSource},
		RequiresApproval:   false,
		InputBindings:      nil,
		OutputRequirements: nil,
	}
	return group, edge, true
}

func manualMoveEdgeSnapshot(edge workflow.Edge, sourceNode workflow.Node, targetNode workflow.Node, derived workflow.DerivedWiring) edgeContractSnapshot {
	inputBindings := edge.InputBindings
	outputRequirements := edge.OutputRequirements
	if strings.TrimSpace(string(edge.ID)) != "" {
		inputBindings = edgeInputBindingsSnapshot(edge, sourceNode, derived)
		outputRequirements = edgeOutputRequirementsSnapshot(edge, sourceNode, targetNode, derived)
	}
	return edgeContractSnapshot{
		ID:                 edge.ID,
		Key:                edge.Key,
		TargetNode:         nodeSnapshotWithDerivedWiring(targetNode, derived),
		ContextMode:        edge.ContextMode,
		ContextSource:      workflow.CanonicalContextSource(edge.ContextSource),
		RequiresApproval:   edge.RequiresApproval,
		PromptTemplate:     strings.TrimSpace(edge.PromptTemplate),
		Parameters:         edgeParametersSnapshot(edge, sourceNode, derived),
		InputBindings:      inputBindings,
		OutputRequirements: outputRequirements,
	}
}

func (s *Store) latestRunForPlacement(ctx context.Context, placementID workflow.PlacementID) (workflow.RunID, string, error) {
	row, err := s.queries.GetLatestRunForPlacement(ctx, string(placementID))
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	return workflow.RunID(row.ID), strings.TrimSpace(row.SessionID.String), nil
}

func (s *Store) backwardManualMoveEdge(ctx context.Context, sourcePlacement workflow.PlacementID, targetNode workflow.Node) (workflow.TransitionGroup, workflow.Edge, map[string]string, workflow.RunID, string, bool, error) {
	row, err := s.queries.GetManualMovePreviousTransition(ctx, sqlitegen.GetManualMovePreviousTransitionParams{
		SourcePlacementID: sql.NullString{String: string(sourcePlacement), Valid: true},
		TargetNodeID:      nullableString(string(workflow.NodeIDOf(targetNode))),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.TransitionGroup{}, workflow.Edge{}, nil, "", "", false, nil
	}
	if err != nil {
		return workflow.TransitionGroup{}, workflow.Edge{}, nil, "", "", false, err
	}
	outputValues := map[string]string{}
	if err := workflow.UnmarshalString(row.OutputValuesJson, &outputValues); err != nil {
		return workflow.TransitionGroup{}, workflow.Edge{}, nil, "", "", false, err
	}
	inputs := []workflow.InputBinding{}
	if err := workflow.UnmarshalString(row.InputBindingsJson, &inputs); err != nil {
		return workflow.TransitionGroup{}, workflow.Edge{}, nil, "", "", false, err
	}
	requirements := []workflow.OutputRequirement{}
	if err := workflow.UnmarshalString(row.OutputRequirementsJson, &requirements); err != nil {
		return workflow.TransitionGroup{}, workflow.Edge{}, nil, "", "", false, err
	}
	metadata := workflowRunMetadata{}
	if strings.TrimSpace(row.MetadataJson) != "" {
		if err := workflow.UnmarshalString(row.MetadataJson, &metadata); err != nil {
			return workflow.TransitionGroup{}, workflow.Edge{}, nil, "", "", false, err
		}
	}
	sessionID := ""
	if row.SourceRunID.Valid && strings.TrimSpace(row.SourceRunID.String) != "" {
		sourceRun, err := s.queries.GetTaskRun(ctx, row.SourceRunID.String)
		if err != nil {
			return workflow.TransitionGroup{}, workflow.Edge{}, nil, "", "", false, err
		}
		sessionID = strings.TrimSpace(sourceRun.SessionID.String)
	}
	group := workflow.TransitionGroup{ID: workflow.TransitionGroupID(row.TransitionGroupID.String), TransitionID: workflow.TransitionID(row.TransitionID), DisplayName: row.TransitionDisplayName}
	edge := workflow.Edge{ID: workflow.EdgeID(row.WorkflowEdgeID.String), Key: workflow.ModelKey(row.EdgeKey), TargetNodeID: workflow.NodeIDOf(targetNode), ContextMode: workflow.ContextMode(row.ContextMode), ContextSource: workflow.CanonicalContextSource(metadata.ContextSource), RequiresApproval: row.RequiresApproval != 0, InputBindings: inputs, OutputRequirements: requirements}
	return group, edge, outputValues, workflow.RunID(row.SourceRunID.String), sessionID, true, nil
}

// manualMoveSource resolves the placement and node a manual move starts from.
// A task with an active placement moves from it directly. A task awaiting
// approval has no active placement (its source placement is already completed);
// in that case the move starts from the single pending-approval transition and
// its ID is returned so the move can reject it and override the proposal.
func (s *Store) manualMoveSource(ctx context.Context, taskID workflow.TaskID) (workflow.PlacementID, workflow.NodeID, string, error) {
	placement, nodeID, err := s.activeManualMoveSource(ctx, taskID)
	if err == nil {
		return placement, nodeID, "", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", "", err
	}
	return s.pendingApprovalManualMoveSource(ctx, taskID)
}

func (s *Store) pendingApprovalManualMoveSource(ctx context.Context, taskID workflow.TaskID) (workflow.PlacementID, workflow.NodeID, string, error) {
	sources, err := s.queries.ListPendingApprovalManualMoveSources(ctx, string(taskID))
	if err != nil {
		return "", "", "", err
	}
	if len(sources) == 0 {
		return "", "", "", ErrManualMoveNoSourcePosition
	}
	if len(sources) != 1 {
		return "", "", "", ErrManualMoveMultiplePendingApprovals
	}
	return workflow.PlacementID(sources[0].SourcePlacementID.String), workflow.NodeID(sources[0].NodeID.String), sources[0].ID, nil
}

func rejectPendingApprovalTransition(ctx context.Context, q *sqlitegen.Queries, transitionID string) error {
	affected, err := q.RejectPendingApprovalTransition(ctx, transitionID)
	if err != nil {
		return fmt.Errorf("reject pending approval: %w", err)
	}
	if affected != 1 {
		return ErrManualMovePendingApprovalResolved
	}
	return nil
}

func (s *Store) activeManualMoveSource(ctx context.Context, taskID workflow.TaskID) (workflow.PlacementID, workflow.NodeID, error) {
	placements, err := s.queries.ListActiveManualMoveSources(ctx, string(taskID))
	if err != nil {
		return "", "", err
	}
	for _, placement := range placements {
		if placement.ParallelBatchTransitionID.Valid && strings.TrimSpace(placement.ParallelBatchTransitionID.String) != "" {
			return "", "", ErrManualMoveDuringParallelBatch
		}
	}
	if len(placements) == 0 {
		return "", "", sql.ErrNoRows
	}
	if len(placements) != 1 {
		return "", "", errors.New("manual move with multiple active placements is not supported")
	}
	return workflow.PlacementID(placements[0].ID), workflow.NodeID(placements[0].NodeID.String), nil
}

func definitionNode(def workflow.Definition, nodeID workflow.NodeID) (workflow.Node, bool) {
	for _, node := range def.Nodes {
		if workflow.NodeIDOf(node) == nodeID {
			return node, true
		}
	}
	return nil, false
}

func definitionEdgeBetween(def workflow.Definition, sourceNodeID workflow.NodeID, targetNodeID workflow.NodeID) (workflow.TransitionGroup, workflow.Edge, bool) {
	groups := map[workflow.TransitionGroupID]workflow.TransitionGroup{}
	for _, group := range def.TransitionGroups {
		if group.SourceNodeID == sourceNodeID {
			groups[group.ID] = group
		}
	}
	for _, edge := range def.Edges {
		if edge.TargetNodeID != targetNodeID {
			continue
		}
		group, ok := groups[edge.TransitionGroupID]
		if ok {
			return group, edge, true
		}
	}
	return workflow.TransitionGroup{}, workflow.Edge{}, false
}
