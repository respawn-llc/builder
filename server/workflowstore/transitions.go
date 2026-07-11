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

func (s *Store) ListTransitions(ctx context.Context, taskID workflow.TaskID) ([]TransitionRecord, error) {
	rows, err := s.queries.ListTaskTransitions(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	out := make([]TransitionRecord, 0, len(rows))
	for _, row := range rows {
		outputs := map[string]string{}
		if err := workflow.UnmarshalString(row.OutputValuesJson, &outputs); err != nil {
			return nil, err
		}
		out = append(out, TransitionRecord{ID: workflow.TransitionID(row.ID), TaskID: workflow.TaskID(row.TaskID), TransitionID: row.TransitionID, State: row.State, Commentary: row.Commentary, OutputValues: outputs, CreatedAt: row.CreatedAtUnixMs})
	}
	return out, nil
}

func (s *Store) ListPendingApprovalTransitionIDs(ctx context.Context, taskID workflow.TaskID) ([]workflow.TransitionID, error) {
	rows, err := s.queries.ListPendingApprovalTransitionIDsByTask(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	out := make([]workflow.TransitionID, 0, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row) != "" {
			out = append(out, workflow.TransitionID(row))
		}
	}
	return out, nil
}

func (s *Store) ListTransitionEdges(ctx context.Context, transitionID workflow.TransitionID) ([]TransitionEdgeRecord, error) {
	rows, err := s.queries.ListTaskTransitionEdges(ctx, string(transitionID))
	if err != nil {
		return nil, err
	}
	out := make([]TransitionEdgeRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, TransitionEdgeRecord{
			ID:                   row.ID,
			TaskTransitionID:     workflow.TransitionID(row.TaskTransitionID),
			WorkflowEdgeID:       workflow.EdgeID(row.WorkflowEdgeID.String),
			EdgeKey:              row.EdgeKey,
			TargetNodeID:         workflow.NodeID(row.TargetNodeID.String),
			TargetPlacementID:    workflow.PlacementID(row.TargetPlacementID.String),
			State:                row.State,
			WorkflowRevisionSeen: row.WorkflowRevisionSeen,
		})
	}
	return out, nil
}

func (s *Store) TaskIdentityForTransition(ctx context.Context, transitionID workflow.TransitionID) (taskID string, projectID string, workflowID string, err error) {
	id := strings.TrimSpace(string(transitionID))
	if id == "" {
		return "", "", "", ErrTransitionIDRequired
	}
	row, err := s.queries.GetTaskIdentityForTransition(ctx, id)
	if err != nil {
		return "", "", "", err
	}
	return row.ID, row.ProjectID, row.WorkflowID, nil
}

func (s *Store) PendingTransitionTargetsExecutableNode(ctx context.Context, transitionID workflow.TransitionID) (bool, error) {
	id := strings.TrimSpace(string(transitionID))
	if id == "" {
		return false, ErrTransitionIDRequired
	}
	edges, err := s.queries.ListTaskTransitionEdges(ctx, id)
	if err != nil {
		return false, err
	}
	for _, edge := range edges {
		if executableNodeKind(workflow.NodeKind(edge.TargetNodeKind)) {
			return true, nil
		}
	}
	return false, nil
}

type ApprovalTransitionProjection struct {
	TransitionID     workflow.TransitionID
	ProjectID        string
	WorkflowID       string
	TaskID           workflow.TaskID
	TaskShortID      string
	TaskTitle        string
	SourceRunID      workflow.RunID
	SessionID        string
	OccurredAtUnixMs int64
}

func (s *Store) ApprovalTransitionProjection(ctx context.Context, transitionID workflow.TransitionID) (ApprovalTransitionProjection, error) {
	id := strings.TrimSpace(string(transitionID))
	if id == "" {
		return ApprovalTransitionProjection{}, ErrTransitionIDRequired
	}
	transition, err := s.queries.GetTransitionApprovalState(ctx, id)
	if err != nil {
		return ApprovalTransitionProjection{}, err
	}
	task, err := s.queries.GetTask(ctx, transition.TaskID)
	if err != nil {
		return ApprovalTransitionProjection{}, err
	}
	runID := workflow.RunID("")
	sessionID := ""
	if transition.SourceRunID.Valid && strings.TrimSpace(transition.SourceRunID.String) != "" {
		runID = workflow.RunID(transition.SourceRunID.String)
		if run, runErr := s.GetRun(ctx, runID); runErr == nil {
			sessionID = run.SessionID
		}
	}
	return ApprovalTransitionProjection{
		TransitionID:     workflow.TransitionID(id),
		ProjectID:        task.ProjectID,
		WorkflowID:       task.WorkflowID,
		TaskID:           workflow.TaskID(task.ID),
		TaskShortID:      task.ShortID,
		TaskTitle:        task.Title,
		SourceRunID:      runID,
		SessionID:        sessionID,
		OccurredAtUnixMs: transition.CreatedAtUnixMs,
	}, nil
}

func (s *Store) ApproveTransition(ctx context.Context, transitionID workflow.TransitionID) (CompleteRunResult, error) {
	return s.approveTransition(ctx, transitionID, nil, nil)
}

// ApproveTransitionWithExecutionTarget commits a validated first execution
// target and its pending approval in one transaction. It currently
// materializes source-root targets; managed targets require provisioning
// before they can safely reach this action boundary.
func (s *Store) ApproveTransitionWithExecutionTarget(ctx context.Context, target workflow.ExecutionTarget, transitionID workflow.TransitionID) (CompleteRunResult, error) {
	if err := target.Validate(); err != nil {
		return CompleteRunResult{}, err
	}
	if target.Policy != workflow.ExecutionPolicyNone {
		return CompleteRunResult{}, errors.New("managed execution targets require provisioning before approval")
	}
	return s.approveTransition(ctx, transitionID, &target, nil)
}

// ReplaceManualExecutionTargetAndApprove atomically replaces a
// manual-recovery target with an operator-selected source-root target and
// applies the pending approval.
func (s *Store) ReplaceManualExecutionTargetAndApprove(ctx context.Context, target workflow.ExecutionTarget, transitionID workflow.TransitionID, expectedNegotiation workflow.ExecutionTargetNegotiation) (CompleteRunResult, error) {
	if err := target.Validate(); err != nil {
		return CompleteRunResult{}, err
	}
	if target.Policy != workflow.ExecutionPolicyNone {
		return CompleteRunResult{}, errors.New("managed execution targets require provisioning before approval")
	}
	return s.approveTransition(ctx, transitionID, &target, &expectedNegotiation)
}

func (s *Store) approveTransition(ctx context.Context, transitionID workflow.TransitionID, target *workflow.ExecutionTarget, manualReplacement *workflow.ExecutionTargetNegotiation) (CompleteRunResult, error) {
	id := strings.TrimSpace(string(transitionID))
	if id == "" {
		return CompleteRunResult{}, ErrTransitionIDRequired
	}
	transition, err := s.queries.GetTransitionApprovalState(ctx, id)
	if err != nil {
		return CompleteRunResult{}, err
	}
	if transition.State == "approved" || transition.State == "applied" {
		return s.approvedTransitionResult(ctx, id, transition.State)
	}
	if transition.State != "pending_approval" {
		return CompleteRunResult{}, fmt.Errorf("transition %s is not pending approval", id)
	}
	edges, err := s.queries.ListTaskTransitionEdges(ctx, id)
	if err != nil {
		return CompleteRunResult{}, err
	}
	if len(edges) == 0 {
		return CompleteRunResult{}, errors.New("pending approval has no edge snapshots")
	}
	task, err := s.queries.GetTask(ctx, transition.TaskID)
	if err != nil {
		return CompleteRunResult{}, err
	}
	worktreeRoot := ""
	if target == nil {
		if err := ensureTaskExecutionTargetNegotiationIsNotActive(ctx, s.queries, workflow.TaskID(task.ID)); err != nil {
			return CompleteRunResult{}, err
		}
		existingTarget, targetErr := s.GetTaskExecutionTarget(ctx, workflow.TaskID(task.ID))
		if targetErr != nil {
			return CompleteRunResult{}, targetErr
		}
		if existingTarget != nil {
			executionRoot, rootErr := s.ResolveTaskExecutionRoot(ctx, workflow.TaskID(task.ID))
			if rootErr != nil {
				return CompleteRunResult{}, rootErr
			}
			worktreeRoot = executionRoot.EffectiveRoot
		} else {
			worktreeRoot, err = taskManagedWorktreeRoot(ctx, s.queries, task)
			if err != nil {
				return CompleteRunResult{}, err
			}
		}
	} else {
		if target.TaskID != workflow.TaskID(task.ID) {
			return CompleteRunResult{}, errors.New("approval execution target must belong to the transition task")
		}
		workspaceID := strings.TrimSpace(task.SourceWorkspaceID.String)
		if workspaceID == "" {
			return CompleteRunResult{}, errors.New("task source workspace is required for none execution target")
		}
		workspace, err := s.metadata.GetWorkspaceByID(ctx, workspaceID)
		if err != nil {
			return CompleteRunResult{}, err
		}
		worktreeRoot = workspace.CanonicalRootPath
	}
	for _, edge := range edges {
		if edge.State != "pending" || workflow.NodeKind(edge.TargetNodeKind) != workflow.NodeKindScript {
			continue
		}
		if err := s.validateScriptNodeForExecution(ctx, s.queries, workflow.NodeID(edge.TargetNodeID.String), worktreeRoot); err != nil {
			if target != nil {
				return CompleteRunResult{}, s.clearNoneExecutionTargetNegotiationAfterValidationFailure(ctx, target.TaskID, err)
			}
			return CompleteRunResult{}, err
		}
	}
	hasSourceRun := transition.SourceRunID.Valid && strings.TrimSpace(transition.SourceRunID.String) != ""
	sourceRun := sqlitegen.TaskRunRecord{}
	sourceSnapshot := runStartSnapshot{}
	if hasSourceRun {
		sourceRun, err = s.queries.GetTaskRun(ctx, transition.SourceRunID.String)
		if err != nil {
			return CompleteRunResult{}, err
		}
		if err := workflow.UnmarshalString(sourceRun.RunStartSnapshotJson, &sourceSnapshot); err != nil {
			return CompleteRunResult{}, err
		}
	}
	now := s.now().UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CompleteRunResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if target == nil {
		if err := ensureTaskExecutionTargetNegotiationIsNotActive(ctx, q, workflow.TaskID(task.ID)); err != nil {
			return CompleteRunResult{}, err
		}
	} else {
		if manualReplacement != nil {
			if err := replaceManualExecutionTarget(ctx, q, *target, *manualReplacement); err != nil {
				return CompleteRunResult{}, err
			}
		} else {
			if _, err := q.DeleteTaskExecutionTargetNegotiation(ctx, string(target.TaskID)); err != nil {
				return CompleteRunResult{}, err
			}
			if err := insertValidatedTaskExecutionTarget(ctx, q, *target); err != nil {
				return CompleteRunResult{}, err
			}
		}
	}
	updatedCount, err := q.ApprovePendingTransition(ctx, sqlitegen.ApprovePendingTransitionParams{
		AppliedAtUnixMs: sql.NullInt64{Int64: now, Valid: true},
		TransitionID:    id,
	})
	if err != nil {
		return CompleteRunResult{}, err
	}
	if updatedCount != 1 {
		_ = tx.Rollback()
		currentState, scanErr := s.queries.GetTaskTransitionState(ctx, id)
		if scanErr != nil {
			return CompleteRunResult{}, scanErr
		}
		if currentState == "approved" || currentState == "applied" {
			return s.approvedTransitionResult(ctx, id, currentState)
		}
		return CompleteRunResult{}, sql.ErrNoRows
	}
	if err := touchTaskUpdatedAt(ctx, q, transition.TaskID, now); err != nil {
		return CompleteRunResult{}, err
	}
	result := CompleteRunResult{TransitionID: workflow.TransitionID(id), State: "approved"}
	for _, edge := range edges {
		if edge.State != "pending" {
			continue
		}
		targetEdge, err := edgeContractSnapshotFromTransitionEdge(edge)
		if err != nil {
			return CompleteRunResult{}, err
		}
		if targetEdge.TargetNode.Kind == workflow.NodeKindJoin {
			if !hasSourceRun {
				return CompleteRunResult{}, errors.New("pending approval to join has no source run")
			}
			if _, err := q.ApplyPendingTransitionEdgeToJoin(ctx, edge.ID); err != nil {
				return CompleteRunResult{}, fmt.Errorf("update approved join edge snapshot: %w", err)
			}
			joined, err := s.applyJoinIfReady(ctx, tx, q, now, transition.TaskID, sourceRun.PlacementID, sourceSnapshot, targetEdge, worktreeRoot)
			if err != nil {
				return CompleteRunResult{}, err
			}
			result.PlacementIDs = append(result.PlacementIDs, joined.PlacementIDs...)
			result.RunIDs = append(result.RunIDs, joined.RunIDs...)
			result.InterruptedRunIDs = append(result.InterruptedRunIDs, joined.InterruptedRunIDs...)
			continue
		}
		targetPlacementID := prefixedID("placement")
		if err := q.InsertTaskNodePlacement(ctx, sqlitegen.InsertTaskNodePlacementParams{ID: targetPlacementID, TaskID: transition.TaskID, NodeID: edge.TargetNodeID, State: "active", ParallelBatchTransitionID: sql.NullString{String: id, Valid: len(edges) > 1}, ParallelBranchEdgeID: sql.NullString{String: edge.WorkflowEdgeID.String, Valid: len(edges) > 1 && edge.WorkflowEdgeID.Valid}, CreatedAtUnixMs: now, UpdatedAtUnixMs: now}); err != nil {
			return CompleteRunResult{}, fmt.Errorf("insert approved target placement: %w", err)
		}
		result.PlacementIDs = append(result.PlacementIDs, workflow.PlacementID(targetPlacementID))
		if _, err := q.ApplyPendingTransitionEdgeToPlacement(ctx, sqlitegen.ApplyPendingTransitionEdgeToPlacementParams{
			TargetPlacementID: sql.NullString{String: targetPlacementID, Valid: true},
			EdgeID:            edge.ID,
		}); err != nil {
			return CompleteRunResult{}, fmt.Errorf("update approved edge snapshot: %w", err)
		}
		if !executableNodeKind(workflow.NodeKind(edge.TargetNodeKind)) {
			continue
		}
		targetRunID := prefixedID("run")
		edgeMetadata, err := transitionEdgeMetadata(edge)
		if err != nil {
			return CompleteRunResult{}, err
		}
		targetSnapshot := runStartSnapshot{}
		foundSnapshot := false
		if hasSourceRun {
			targetSnapshot, foundSnapshot, err = sourceSnapshot.forNode(targetEdge.TargetNode)
			if err != nil {
				return CompleteRunResult{}, err
			}
		}
		if !foundSnapshot {
			if edgeMetadata.TargetRunStartSnapshot == nil {
				return CompleteRunResult{}, fmt.Errorf("pending approval edge %q has no frozen run-start snapshot for target node %q", edge.ID, targetEdge.TargetNode.ID)
			}
			targetSnapshot = *edgeMetadata.TargetRunStartSnapshot
		}
		targetSnapshotJSON, err := workflow.MarshalString(targetSnapshot)
		if err != nil {
			return CompleteRunResult{}, err
		}
		resolution, ok, err := resolvedContextInvocationFromMetadata(ctx, q, edgeMetadata, targetEdge.ContextMode)
		if err != nil {
			return CompleteRunResult{}, err
		}
		if !ok {
			resolution, err = s.resolveContextInvocation(ctx, q, transition.TaskID, transition.CreatedAtUnixMs, sourceRun.PlacementID, &sourceRun, sourceSnapshot, targetEdge)
			if err != nil {
				return CompleteRunResult{}, err
			}
		}
		targetMetadata := resolution.runMetadata(targetEdge)
		targetMetadata.PromptTemplate = strings.TrimSpace(targetEdge.PromptTemplate)
		targetMetadata.Parameters = append([]workflow.Parameter(nil), targetEdge.Parameters...)
		targetMetadata.PriorParameterValues = clonePriorParameterValues(edgeMetadata.PriorParameterValues)
		targetMetadataJSON, err := workflow.MarshalString(targetMetadata)
		if err != nil {
			return CompleteRunResult{}, err
		}
		interruptionReason, interruptionDetail, invalidScript, err := s.scriptNodeInterruption(ctx, q, targetEdge.TargetNode.ID, worktreeRoot)
		if err != nil {
			return CompleteRunResult{}, err
		}
		interruptedAt := sql.NullInt64{}
		if invalidScript {
			interruptedAt = sql.NullInt64{Int64: now, Valid: true}
		}
		if err := q.InsertTaskRun(ctx, sqlitegen.InsertTaskRunParams{ID: targetRunID, PlacementID: targetPlacementID, WorkflowRevisionSeen: targetSnapshot.WorkflowRevisionSeen, AutomationRequestedAtUnixMs: sql.NullInt64{Int64: now, Valid: true}, CreatedAtUnixMs: now, UpdatedAtUnixMs: now, InterruptedAtUnixMs: interruptedAt, InterruptionReason: nullableString(interruptionReason), InterruptionDetailJson: interruptionDetail, RunStartSnapshotJson: targetSnapshotJSON, MetadataJson: targetMetadataJSON}); err != nil {
			return CompleteRunResult{}, fmt.Errorf("insert approved target run: %w", err)
		}
		targetRun := workflow.RunID(targetRunID)
		result.RunIDs = append(result.RunIDs, targetRun)
		if invalidScript {
			result.InterruptedRunIDs = append(result.InterruptedRunIDs, targetRun)
		}
	}
	if err := tx.Commit(); err != nil {
		return CompleteRunResult{}, err
	}
	return result, nil
}

func (s *Store) approvedTransitionResult(ctx context.Context, transitionID string, state string) (CompleteRunResult, error) {
	edges, err := s.queries.ListTaskTransitionEdges(ctx, transitionID)
	if err != nil {
		return CompleteRunResult{}, err
	}
	result := CompleteRunResult{TransitionID: workflow.TransitionID(transitionID), State: state}
	placementIDs := map[string]bool{}
	runIDs := map[string]bool{}
	for _, edge := range edges {
		if edge.TargetPlacementID.Valid && strings.TrimSpace(edge.TargetPlacementID.String) != "" && !placementIDs[edge.TargetPlacementID.String] {
			placementIDs[edge.TargetPlacementID.String] = true
			result.PlacementIDs = append(result.PlacementIDs, workflow.PlacementID(edge.TargetPlacementID.String))
		}
		if !edge.TargetPlacementID.Valid || strings.TrimSpace(edge.TargetPlacementID.String) == "" {
			continue
		}
		rows, err := s.queries.ListTaskRunIDsByPlacementForTransitionResult(ctx, strings.TrimSpace(edge.TargetPlacementID.String))
		if err != nil {
			return CompleteRunResult{}, err
		}
		for _, runID := range rows {
			if trimmed := strings.TrimSpace(runID); trimmed != "" && !runIDs[trimmed] {
				runIDs[trimmed] = true
				result.RunIDs = append(result.RunIDs, workflow.RunID(trimmed))
			}
		}
	}
	return result, nil
}

func edgeContractSnapshotFromTransitionEdge(edge sqlitegen.TaskTransitionEdgeRecord) (edgeContractSnapshot, error) {
	inputs := []workflow.InputBinding{}
	if err := workflow.UnmarshalString(edge.InputBindingsJson, &inputs); err != nil {
		return edgeContractSnapshot{}, err
	}
	requirements := []workflow.OutputRequirement{}
	if err := workflow.UnmarshalString(edge.OutputRequirementsJson, &requirements); err != nil {
		return edgeContractSnapshot{}, err
	}
	metadata, err := transitionEdgeMetadata(edge)
	if err != nil {
		return edgeContractSnapshot{}, err
	}
	return edgeContractSnapshot{
		ID:  workflow.EdgeID(edge.WorkflowEdgeID.String),
		Key: workflow.ModelKey(edge.EdgeKey),
		TargetNode: nodeContractSnapshot{
			ID:          workflow.NodeID(edge.TargetNodeID.String),
			Key:         workflow.ModelKey(edge.TargetNodeKey),
			DisplayName: edge.TargetNodeDisplayName,
			Kind:        workflow.NodeKind(edge.TargetNodeKind),
		},
		ContextMode:        workflow.ContextMode(edge.ContextMode),
		ContextSource:      workflow.CanonicalContextSource(metadata.ContextSource),
		RequiresApproval:   edge.RequiresApproval != 0,
		PromptTemplate:     strings.TrimSpace(metadata.PromptTemplate),
		Parameters:         append([]workflow.Parameter(nil), metadata.Parameters...),
		InputBindings:      inputs,
		OutputRequirements: requirements,
	}, nil
}

func transitionEdgeMetadata(edge sqlitegen.TaskTransitionEdgeRecord) (workflowRunMetadata, error) {
	metadata := workflowRunMetadata{}
	if strings.TrimSpace(edge.MetadataJson) != "" {
		if err := workflow.UnmarshalString(edge.MetadataJson, &metadata); err != nil {
			return workflowRunMetadata{}, err
		}
	}
	return metadata, nil
}

func insertTransitionEdgeSnapshotWithMetadata(ctx context.Context, q *sqlitegen.Queries, transitionID string, edge edgeContractSnapshot, targetPlacementID string, state string, metadata workflowRunMetadata) error {
	metadata.ContextSource = workflow.CanonicalContextSource(edge.ContextSource)
	metadata.PromptTemplate = strings.TrimSpace(edge.PromptTemplate)
	metadata.Parameters = append([]workflow.Parameter(nil), edge.Parameters...)
	metadataJSON, err := workflow.MarshalString(metadata)
	if err != nil {
		return err
	}
	requiresApproval := int64(0)
	if edge.RequiresApproval {
		requiresApproval = 1
	}
	if err := q.InsertTaskTransitionEdge(ctx, sqlitegen.InsertTaskTransitionEdgeParams{
		ID:                     prefixedID("transition-edge"),
		TaskTransitionID:       transitionID,
		WorkflowEdgeID:         sql.NullString{String: string(edge.ID), Valid: edge.ID != ""},
		EdgeKey:                string(edge.Key),
		TargetNodeID:           sql.NullString{String: string(edge.TargetNode.ID), Valid: edge.TargetNode.ID != ""},
		TargetNodeKey:          string(edge.TargetNode.Key),
		TargetNodeDisplayName:  edge.TargetNode.DisplayName,
		TargetNodeKind:         string(edge.TargetNode.Kind),
		TargetPlacementID:      sql.NullString{String: targetPlacementID, Valid: targetPlacementID != ""},
		State:                  state,
		ContextMode:            string(edge.ContextMode),
		RequiresApproval:       requiresApproval,
		InputBindingsJson:      mustInputBindingsJSON(edge.InputBindings),
		OutputRequirementsJson: mustOutputRequirementsJSON(edge.OutputRequirements),
		MetadataJson:           metadataJSON,
	}); err != nil {
		return fmt.Errorf("insert transition edge snapshot: %w", err)
	}
	return nil
}
