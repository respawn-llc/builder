package workflowstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

type joinArrival struct {
	PlacementID   string
	BranchEdgeID  string
	JoinEdgeID    workflow.EdgeID
	SourceNodeKey string
	OutputValues  map[string]string
}

func (s *Store) applyJoinIfReady(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, now int64, taskID string, sourcePlacementID string, sourceSnapshot runStartSnapshot, joinEdge edgeContractSnapshot) (CompleteRunResult, error) {
	batchID, err := q.GetContextSourceBatchScope(ctx, sourcePlacementID)
	if err != nil {
		return CompleteRunResult{}, err
	}
	if !batchID.Valid || strings.TrimSpace(batchID.String) == "" {
		return CompleteRunResult{}, nil
	}
	_, err = q.GetExistingJoinPlacement(ctx, sqlitegen.GetExistingJoinPlacementParams{TaskID: taskID, NodeID: nullableString(string(joinEdge.TargetNode.ID)), BatchID: sql.NullString{String: batchID.String, Valid: true}})
	if err == nil {
		return CompleteRunResult{}, nil
	}
	if err != nil && err != sql.ErrNoRows {
		return CompleteRunResult{}, err
	}
	expected, err := joinExpectedBranches(ctx, q, batchID.String)
	if err != nil {
		return CompleteRunResult{}, err
	}
	if len(expected) == 0 {
		return CompleteRunResult{}, nil
	}
	arrivals, err := joinArrivals(ctx, q, batchID.String, joinEdge.TargetNode.ID)
	if err != nil {
		return CompleteRunResult{}, err
	}
	arrivedExpected := map[string]bool{}
	for _, arrival := range arrivals {
		if expected[arrival.PlacementID] {
			arrivedExpected[arrival.PlacementID] = true
		}
	}
	if len(arrivedExpected) < len(expected) {
		return CompleteRunResult{}, nil
	}
	joinSnapshot, found, err := sourceSnapshot.forNode(joinEdge.TargetNode)
	if err != nil {
		return CompleteRunResult{}, err
	}
	if !found {
		return CompleteRunResult{}, fmt.Errorf("join node %q missing from run snapshot", joinEdge.TargetNode.ID)
	}
	groups := joinSnapshot.transitionGroupsForNode(joinEdge.TargetNode.ID)
	if len(groups) != 1 || len(groups[0].Edges) != 1 {
		return CompleteRunResult{}, fmt.Errorf("join node %q must have exactly one outgoing edge", joinEdge.TargetNode.ID)
	}
	group := groups[0]
	joinOutputValues, ready, err := selectedJoinOutputValues(joinSnapshot.Node, group.Edges[0], arrivals)
	if err != nil {
		return CompleteRunResult{}, err
	}
	if !ready {
		return CompleteRunResult{}, nil
	}
	joinOutputValuesJSON, err := workflow.MarshalString(joinOutputValues)
	if err != nil {
		return CompleteRunResult{}, err
	}
	joinPlacementID := prefixedID("placement")
	if err := q.InsertTaskNodePlacement(ctx, sqlitegen.InsertTaskNodePlacementParams{ID: joinPlacementID, TaskID: taskID, NodeID: nullableString(string(joinEdge.TargetNode.ID)), State: "completed", ParallelBatchTransitionID: sql.NullString{String: batchID.String, Valid: true}, CreatedAtUnixMs: now, UpdatedAtUnixMs: now}); err != nil {
		return CompleteRunResult{}, err
	}
	joinTransitionID := prefixedID("transition")
	if err := q.InsertTaskTransition(ctx, sqlitegen.InsertTaskTransitionParams{ID: joinTransitionID, TaskID: taskID, SourcePlacementID: sql.NullString{String: joinPlacementID, Valid: true}, SourceNodeKey: string(joinEdge.TargetNode.Key), SourceNodeDisplayName: joinEdge.TargetNode.DisplayName, TransitionID: group.TransitionID, TransitionDisplayName: group.DisplayName, WorkflowRevisionSeen: joinSnapshot.WorkflowRevisionSeen, Actor: "system", State: "applied", OutputValuesJson: joinOutputValuesJSON, CreatedAtUnixMs: now, AppliedAtUnixMs: now}); err != nil {
		return CompleteRunResult{}, err
	}
	result := CompleteRunResult{TransitionID: workflow.TransitionID(joinTransitionID), State: "applied"}
	outEdge := group.Edges[0]
	targetPlacementID := prefixedID("placement")
	if err := q.InsertTaskNodePlacement(ctx, sqlitegen.InsertTaskNodePlacementParams{ID: targetPlacementID, TaskID: taskID, NodeID: nullableString(string(outEdge.TargetNode.ID)), State: placementStateForNode(outEdge.TargetNode), CreatedAtUnixMs: now, UpdatedAtUnixMs: now}); err != nil {
		return CompleteRunResult{}, err
	}
	result.PlacementIDs = append(result.PlacementIDs, workflow.PlacementID(targetPlacementID))
	if err := insertTransitionEdgeSnapshotWithMetadata(ctx, q, joinTransitionID, outEdge, targetPlacementID, "applied", workflowRunMetadata{ContextSource: workflow.CanonicalContextSource(outEdge.ContextSource)}); err != nil {
		return CompleteRunResult{}, err
	}
	if executableNodeKind(outEdge.TargetNode.Kind) {
		task, err := q.GetTask(ctx, taskID)
		if err != nil {
			return CompleteRunResult{}, err
		}
		worktreeRoot, err := taskManagedWorktreeRoot(ctx, q, task)
		if err != nil {
			return CompleteRunResult{}, err
		}
		targetRunID := prefixedID("run")
		targetSnapshot, foundSnapshot, err := joinSnapshot.forNode(outEdge.TargetNode)
		if err != nil {
			return CompleteRunResult{}, err
		}
		if !foundSnapshot {
			return CompleteRunResult{}, fmt.Errorf("join target node %q missing from run snapshot", outEdge.TargetNode.ID)
		}
		targetSnapshotJSON, err := workflow.MarshalString(targetSnapshot)
		if err != nil {
			return CompleteRunResult{}, err
		}
		resolution, err := s.resolveContextInvocation(ctx, q, taskID, now, joinPlacementID, nil, joinSnapshot, outEdge)
		if err != nil {
			return CompleteRunResult{}, err
		}
		priorParameterValues, err := s.resolvePromptPriorParameterValues(ctx, q, taskID, now, joinPlacementID, outEdge)
		if err != nil {
			return CompleteRunResult{}, err
		}
		targetMetadata := resolution.runMetadata(outEdge)
		targetMetadata.PromptTemplate = strings.TrimSpace(outEdge.PromptTemplate)
		targetMetadata.Parameters = append([]workflow.Parameter(nil), outEdge.Parameters...)
		targetMetadata.PriorParameterValues = clonePriorParameterValues(priorParameterValues)
		targetMetadataJSON, err := workflow.MarshalString(targetMetadata)
		if err != nil {
			return CompleteRunResult{}, err
		}
		interruptionReason, interruptionDetail, invalidScript, err := s.scriptNodeInterruption(ctx, q, outEdge.TargetNode.ID, worktreeRoot)
		if err != nil {
			return CompleteRunResult{}, err
		}
		interruptedAt := int64(0)
		if invalidScript {
			interruptedAt = now
		}
		if err := q.InsertTaskRun(ctx, sqlitegen.InsertTaskRunParams{ID: targetRunID, PlacementID: targetPlacementID, WorkflowRevisionSeen: targetSnapshot.WorkflowRevisionSeen, AutomationRequestedAtUnixMs: now, CreatedAtUnixMs: now, UpdatedAtUnixMs: now, InterruptedAtUnixMs: interruptedAt, InterruptionReason: interruptionReason, InterruptionDetailJson: interruptionDetail, RunStartSnapshotJson: targetSnapshotJSON, MetadataJson: targetMetadataJSON}); err != nil {
			return CompleteRunResult{}, err
		}
		targetRun := workflow.RunID(targetRunID)
		result.RunIDs = append(result.RunIDs, targetRun)
		if invalidScript {
			result.InterruptedRunIDs = append(result.InterruptedRunIDs, targetRun)
		}
	}
	return result, nil
}

func joinExpectedBranches(ctx context.Context, q *sqlitegen.Queries, batchID string) (map[string]bool, error) {
	rows, err := q.ListJoinExpectedBranches(ctx, batchID)
	if err != nil {
		return nil, err
	}
	expected := map[string]bool{}
	for _, placementID := range rows {
		if placementID.Valid && strings.TrimSpace(placementID.String) != "" {
			expected[placementID.String] = true
		}
	}
	return expected, nil
}

func joinArrivals(ctx context.Context, q *sqlitegen.Queries, batchID string, joinNodeID workflow.NodeID) ([]joinArrival, error) {
	rows, err := q.ListJoinArrivals(ctx, sqlitegen.ListJoinArrivalsParams{BatchID: sql.NullString{String: batchID, Valid: true}, JoinNodeID: sql.NullString{String: string(joinNodeID), Valid: true}})
	if err != nil {
		return nil, err
	}
	arrivals := []joinArrival{}
	for _, row := range rows {
		if strings.TrimSpace(row.ID) == "" {
			continue
		}
		key := strings.TrimSpace(row.ParallelBranchEdgeID.String)
		if key == "" {
			continue
		}
		incomingJoinEdgeID := workflow.EdgeID(strings.TrimSpace(row.WorkflowEdgeID.String))
		if incomingJoinEdgeID == "" {
			continue
		}
		outputs := map[string]string{}
		if err := workflow.UnmarshalString(row.OutputValuesJson, &outputs); err != nil {
			return nil, err
		}
		arrivals = append(arrivals, joinArrival{PlacementID: row.ID, BranchEdgeID: key, JoinEdgeID: incomingJoinEdgeID, SourceNodeKey: row.SourceNodeKey, OutputValues: outputs})
	}
	return arrivals, nil
}

func selectedJoinOutputValues(join nodeContractSnapshot, outEdge edgeContractSnapshot, arrivals []joinArrival) (map[string]string, bool, error) {
	out := map[string]string{}
	for _, requirement := range outEdge.OutputRequirements {
		parameterKey := strings.TrimSpace(requirement.FieldName)
		if parameterKey == "" {
			continue
		}
		value := ""
		for _, arrival := range arrivals {
			candidate := arrival.OutputValues[parameterKey]
			if strings.TrimSpace(candidate) != "" {
				value = candidate
				break
			}
		}
		if strings.TrimSpace(value) == "" {
			return nil, false, fmt.Errorf("join node %q missing aggregate parameter %q", join.ID, parameterKey)
		}
		out[parameterKey] = value
	}
	return out, true, nil
}
