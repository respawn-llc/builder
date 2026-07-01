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
	if strings.TrimSpace(string(req.TaskID)) == "" {
		return ManualMoveResult{}, errors.New("task id is required")
	}
	if strings.TrimSpace(string(req.TargetNodeID)) == "" {
		return ManualMoveResult{}, errors.New("target node id is required")
	}
	actor := strings.TrimSpace(req.Actor)
	if actor == "" {
		actor = "user"
	}
	task, err := s.queries.GetTask(ctx, string(req.TaskID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	def, workflowRecord, err := s.GetDefinition(ctx, workflow.WorkflowID(task.WorkflowID))
	if err != nil {
		return ManualMoveResult{}, err
	}
	derived := workflow.DeriveWiring(def)
	sourcePlacement, sourceNodeID, pendingApprovalTransitionID, err := s.manualMoveSource(ctx, req.TaskID)
	if err != nil {
		return ManualMoveResult{}, err
	}
	sourceNode, ok := definitionNode(def, sourceNodeID)
	if !ok {
		return ManualMoveResult{}, fmt.Errorf("source node %q missing", sourceNodeID)
	}
	targetNode, ok := definitionNode(def, req.TargetNodeID)
	if !ok {
		return ManualMoveResult{}, fmt.Errorf("target node %q missing", req.TargetNodeID)
	}
	group, edge, ok := definitionEdgeBetween(def, sourceNode.ID, targetNode.ID)
	sourceRunID, sourceSessionID, err := s.latestRunForPlacement(ctx, sourcePlacement)
	if err != nil {
		return ManualMoveResult{}, err
	}
	reusedOutputValues := map[string]string(nil)
	if targetNode.Kind == workflow.NodeKindTerminal && sourceNode.Kind != workflow.NodeKindTerminal {
		group, edge, ok = terminalArchiveManualMoveContract(sourceNode, targetNode)
	} else if !ok {
		group, edge, reusedOutputValues, sourceRunID, sourceSessionID, ok, err = s.backwardManualMoveEdge(ctx, sourcePlacement, targetNode)
		if err != nil {
			return ManualMoveResult{}, err
		}
		if !ok && targetNode.Kind == workflow.NodeKindStart {
			group, edge, ok = startResetManualMoveContract(sourceNode, targetNode)
		}
		if !ok && req.AllowMissingEdge {
			group, edge, ok = missingEdgeManualMoveContract(sourceNode, targetNode)
		}
		if !ok {
			return ManualMoveResult{}, fmt.Errorf("no workflow edge from %s to %s", sourceNode.Key, targetNode.Key)
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
		return ManualMoveResult{}, ErrManualMoveSelectedContextSource
	}
	if contextSource.Kind == workflow.ContextSourcePreviousTarget {
		return ManualMoveResult{}, ErrManualMovePreviousTargetContext
	}
	if edge.ContextMode == workflow.ContextModeContinueSession && strings.TrimSpace(sourceSessionID) == "" {
		return ManualMoveResult{}, ErrManualMoveContinueSessionNeedsSource
	}
	groupSnapshot := transitionContractSnapshot{
		ID:           group.ID,
		SourceNodeID: sourceNode.ID,
		TransitionID: string(group.TransitionID),
		DisplayName:  group.DisplayName,
		Edges:        []edgeContractSnapshot{manualMoveEdgeSnapshot(edge, sourceNode, targetNode, derived)},
	}
	if issues := requiredOutputIssues(groupSnapshot, outputValues); len(issues) > 0 {
		return ManualMoveResult{}, CompletionValidationError{Issues: issues}
	}
	transitionState := "applied"
	edgeState := "applied"
	if targetNode.Kind == workflow.NodeKindAgent || edge.RequiresApproval {
		transitionState = "pending_approval"
		edgeState = "pending"
	}
	if transitionState == "pending_approval" && sourceRunID == "" && !req.AllowMissingEdge {
		return ManualMoveResult{}, ErrManualMoveApprovalNeedsSourceRun
	}
	outputValuesJSON, err := workflow.MarshalString(outputValues)
	if err != nil {
		return ManualMoveResult{}, err
	}
	now := s.now().UnixMilli()
	transitionID := prefixedID("transition")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ManualMoveResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if pendingApprovalTransitionID != "" {
		// The task is awaiting approval and has no active placement (its source
		// placement is already completed). Manually moving it overrides the
		// proposed transition: reject the pending approval so the task leaves
		// the approval state, then continue with the move below.
		if err := rejectPendingApprovalTransition(ctx, q, pendingApprovalTransitionID); err != nil {
			return ManualMoveResult{}, err
		}
	} else {
		updated, err := q.CompleteActiveManualMoveSourcePlacement(ctx, sqlitegen.CompleteActiveManualMoveSourcePlacementParams{
			UpdatedAtUnixMs: now,
			PlacementID:     string(sourcePlacement),
		})
		if err != nil {
			return ManualMoveResult{}, err
		}
		if updated != 1 {
			return ManualMoveResult{}, sql.ErrNoRows
		}
	}
	if err := touchTaskUpdatedAt(ctx, q, string(req.TaskID), now); err != nil {
		return ManualMoveResult{}, err
	}
	appliedAt := now
	if transitionState == "pending_approval" {
		appliedAt = 0
	}
	if err := q.InsertTaskTransition(ctx, sqlitegen.InsertTaskTransitionParams{ID: transitionID, TaskID: string(req.TaskID), SourceRunID: sql.NullString{String: string(sourceRunID), Valid: sourceRunID != ""}, SourcePlacementID: sql.NullString{String: string(sourcePlacement), Valid: true}, SourceNodeKey: string(sourceNode.Key), SourceNodeDisplayName: sourceNode.DisplayName, TransitionID: string(group.TransitionID), TransitionDisplayName: group.DisplayName, WorkflowRevisionSeen: task.WorkflowRevisionSeen, Actor: actor, State: transitionState, Commentary: strings.TrimSpace(req.Commentary), OutputValuesJson: outputValuesJSON, CreatedAtUnixMs: now, AppliedAtUnixMs: appliedAt}); err != nil {
		return ManualMoveResult{}, err
	}
	result := ManualMoveResult{TransitionID: workflow.TransitionID(transitionID), State: transitionState, RequiresApproval: edge.RequiresApproval}
	targetPlacementID := ""
	if transitionState == "applied" {
		targetPlacementID = prefixedID("placement")
		if err := q.InsertTaskNodePlacement(ctx, sqlitegen.InsertTaskNodePlacementParams{ID: targetPlacementID, TaskID: string(req.TaskID), NodeID: string(targetNode.ID), State: "active", CreatedAtUnixMs: now, UpdatedAtUnixMs: now}); err != nil {
			return ManualMoveResult{}, err
		}
		result.PlacementIDs = append(result.PlacementIDs, workflow.PlacementID(targetPlacementID))
	}
	edgeMetadata := workflowRunMetadata{ContextSource: workflow.CanonicalContextSource(groupSnapshot.Edges[0].ContextSource)}
	if transitionState == "pending_approval" && targetNode.Kind == workflow.NodeKindAgent {
		targetSnapshot, err := newRunStartSnapshot(def, workflowRecord, targetNode.ID)
		if err != nil {
			return ManualMoveResult{}, err
		}
		priorParameterValues, err := s.resolvePromptPriorParameterValues(ctx, q, string(req.TaskID), now, string(sourcePlacement), groupSnapshot.Edges[0])
		if err != nil {
			return ManualMoveResult{}, err
		}
		edgeMetadata.PriorParameterValues = priorParameterValues
		edgeMetadata.TargetRunStartSnapshot = &targetSnapshot
	}
	if err := insertTransitionEdgeSnapshotWithMetadata(ctx, q, transitionID, groupSnapshot.Edges[0], targetPlacementID, edgeState, edgeMetadata); err != nil {
		return ManualMoveResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ManualMoveResult{}, err
	}
	return result, nil
}

func terminalArchiveManualMoveContract(sourceNode workflow.Node, targetNode workflow.Node) (workflow.TransitionGroup, workflow.Edge, bool) {
	group := workflow.TransitionGroup{
		ID:           "",
		SourceNodeID: sourceNode.ID,
		TransitionID: "manual_done",
		DisplayName:  "Move to Done",
	}
	edge := workflow.Edge{
		ID:                 "",
		Key:                "manual_done",
		TargetNodeID:       targetNode.ID,
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
		SourceNodeID: sourceNode.ID,
		TransitionID: "manual_start",
		DisplayName:  "Move to Backlog",
	}
	edge := workflow.Edge{
		ID:                 "",
		Key:                "manual_start",
		TargetNodeID:       targetNode.ID,
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
		SourceNodeID: sourceNode.ID,
		TransitionID: "manual_override",
		DisplayName:  "Manual Override",
	}
	edge := workflow.Edge{
		ID:                 "",
		Key:                "manual_override",
		TargetNodeID:       targetNode.ID,
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
		TargetNodeID:      string(targetNode.ID),
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
	edge := workflow.Edge{ID: workflow.EdgeID(row.WorkflowEdgeID.String), Key: workflow.ModelKey(row.EdgeKey), TargetNodeID: targetNode.ID, ContextMode: workflow.ContextMode(row.ContextMode), ContextSource: workflow.CanonicalContextSource(metadata.ContextSource), RequiresApproval: row.RequiresApproval != 0, InputBindings: inputs, OutputRequirements: requirements}
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
	return workflow.PlacementID(sources[0].SourcePlacementID.String), workflow.NodeID(sources[0].NodeID), sources[0].ID, nil
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
	return workflow.PlacementID(placements[0].ID), workflow.NodeID(placements[0].NodeID), nil
}

func definitionNode(def workflow.Definition, nodeID workflow.NodeID) (workflow.Node, bool) {
	for _, node := range def.Nodes {
		if node.ID == nodeID {
			return node, true
		}
	}
	return workflow.Node{}, false
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
