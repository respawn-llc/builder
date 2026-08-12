package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

type PendingApprovalApplyResult struct {
	Mutation         workflow.CurrentNodeMutationResult
	ResolvedApproval workflow.PendingApproval
	Handoff          CompletionHandoff
	AutomaticIntents []workflow.CurrentNodeReference
	TaskAttentionResolution
}

type pendingApprovalTransitionSnapshot struct {
	WorkflowID        runtimeids.WorkflowID      `json:"workflow_id"`
	ID                workflow.TransitionGroupID `json:"id"`
	SourceNodeID      workflow.NodeID            `json:"source_node_id"`
	TransitionID      workflow.TransitionID      `json:"transition_id"`
	DisplayName       string                     `json:"display_name"`
	Description       string                     `json:"description"`
	SourceDisplayName string                     `json:"source_display_name"`
	Commentary        string                     `json:"commentary,omitempty"`
}

type pendingApprovalTargetSnapshot struct {
	NodeID                  workflow.NodeID                      `json:"node_id"`
	NodeKind                workflow.NodeKind                    `json:"node_kind"`
	TransitionBranchKey     *workflow.TransitionBranchKey        `json:"transition_branch_key,omitempty"`
	EnteredByEdgeID         *workflow.EdgeID                     `json:"entered_by_edge_id,omitempty"`
	DisplayName             string                               `json:"display_name"`
	CurrentInputValues      map[string]string                    `json:"current_input_values"`
	PriorValues             workflow.MaterializedPriorValues     `json:"prior_values"`
	SessionID               *string                              `json:"session_id,omitempty"`
	SchedulingState         *workflow.CurrentNodeSchedulingState `json:"scheduling_state,omitempty"`
	AgentExecutionSelection *workflow.AgentExecutionSelection    `json:"agent_execution_selection,omitempty"`
}

type pendingApprovalEffectiveEdgeSnapshot struct {
	WorkflowID         runtimeids.WorkflowID        `json:"workflow_id"`
	ID                 workflow.EdgeID              `json:"id"`
	Key                workflow.ModelKey            `json:"key"`
	TransitionGroupID  workflow.TransitionGroupID   `json:"transition_group_id"`
	TargetNodeID       workflow.NodeID              `json:"target_node_id"`
	AssigneeSelection  workflow.AssigneeSelection   `json:"assignee_selection"`
	ThinkingSelection  workflow.ThinkingSelection   `json:"thinking_selection"`
	ContextMode        workflow.ContextMode         `json:"context_mode"`
	ContextSource      workflow.ContextSource       `json:"context_source"`
	RequiresApproval   bool                         `json:"requires_approval"`
	PromptTemplate     string                       `json:"prompt_template"`
	Parameters         []workflow.Parameter         `json:"parameters"`
	InputBindings      []workflow.InputBinding      `json:"input_bindings"`
	OutputRequirements []workflow.OutputRequirement `json:"output_requirements"`
}

type pendingApprovalContextSourceResolutionSnapshot struct {
	SessionID     *string                                  `json:"session_id,omitempty"`
	TargetSession *workflow.TargetSessionIntent            `json:"target_session,omitempty"`
	ActiveSource  *workflow.MaterializedContinuationSource `json:"active_source,omitempty"`
}

func (s *Store) ListPendingApprovals(ctx context.Context, taskID workflow.TaskID) ([]workflow.PendingApproval, error) {
	if strings.TrimSpace(string(taskID)) == "" {
		return nil, errors.New("task id is required")
	}
	rows, err := s.queries.ListTaskPendingApprovals(ctx, string(taskID))
	if err != nil {
		return nil, err
	}
	approvals := make([]workflow.PendingApproval, 0, len(rows))
	for _, row := range rows {
		approval, err := pendingApprovalFromRow(ctx, s.queries, row)
		if err != nil {
			return nil, err
		}
		approvals = append(approvals, approval)
	}
	return approvals, nil
}

func (s *Store) PendingApproval(ctx context.Context, approvalID workflow.ApprovalID) (workflow.PendingApproval, error) {
	normalizedID, err := normalizeApprovalID(approvalID)
	if err != nil {
		return workflow.PendingApproval{}, err
	}
	return pendingApprovalByID(ctx, s.queries, normalizedID)
}

func (s *Store) IsCurrentNodeExecutionEligible(ctx context.Context, reference workflow.CurrentNodeReference) (bool, error) {
	if _, err := currentNodeForReference(ctx, s.queries, reference); err != nil {
		return false, err
	}
	_, pending, err := currentNodePendingApprovalID(ctx, s.queries, reference)
	if err != nil {
		return false, err
	}
	return !pending, nil
}

func (s *Store) ApplyPendingApproval(ctx context.Context, approvalID workflow.ApprovalID) (PendingApprovalApplyResult, error) {
	normalizedID, err := normalizeApprovalID(approvalID)
	if err != nil {
		return PendingApprovalApplyResult{}, err
	}
	select {
	case s.approvalGate <- struct{}{}:
		defer func() { <-s.approvalGate }()
	case <-ctx.Done():
		return PendingApprovalApplyResult{}, ctx.Err()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PendingApprovalApplyResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	approval, err := pendingApprovalByID(ctx, q, normalizedID)
	if err != nil {
		return PendingApprovalApplyResult{}, err
	}
	approvalAttention, found, err := pendingApprovalAttentionProjection(ctx, q, normalizedID)
	if err != nil {
		return PendingApprovalApplyResult{}, err
	}
	if !found {
		return PendingApprovalApplyResult{}, fmt.Errorf("pending approval %q disappeared during attention resolution", normalizedID)
	}
	if _, err := currentNodeForReference(ctx, q, approval.Source); err != nil {
		return PendingApprovalApplyResult{}, err
	}
	targets := make([]workflow.CurrentNode, 0, len(approval.Branches))
	for _, branch := range approval.Branches {
		targets = append(targets, branch.Target.CurrentNode)
	}
	if len(targets) == 0 {
		return PendingApprovalApplyResult{}, errors.New("pending approval has no target branches")
	}
	var fanoutTargets []currentNodeFanoutTarget
	if len(targets) == 1 {
		if err := validatePendingApprovalSequentialTarget(approval.Source, targets[0].Reference); err != nil {
			return PendingApprovalApplyResult{}, err
		}
	} else {
		fanoutTargets, err = pendingApprovalFanoutTargets(approval.Source, approval.Branches)
		if err != nil {
			return PendingApprovalApplyResult{}, err
		}
	}
	removedApproval, err := q.DeleteTaskPendingApproval(ctx, normalizedID.String())
	if err != nil {
		return PendingApprovalApplyResult{}, err
	}
	if removedApproval != 1 {
		return PendingApprovalApplyResult{}, sql.ErrNoRows
	}
	if len(targets) == 1 {
		removedCurrentNode, err := deleteTaskCurrentNode(ctx, q, approval.Source)
		if err != nil {
			return PendingApprovalApplyResult{}, err
		}
		if removedCurrentNode != 1 {
			return PendingApprovalApplyResult{}, sql.ErrNoRows
		}
		if err := updateActiveFanoutBranchContinuationSource(ctx, q, approval.Source, targets[0].ContinuationSource); err != nil {
			return PendingApprovalApplyResult{}, err
		}
		if err := insertTaskCurrentNodeWithKind(ctx, q, targets[0], approval.Branches[0].Target.NodeKind); err != nil {
			return PendingApprovalApplyResult{}, err
		}
	} else if err := replaceCurrentNodeWithFanout(ctx, q, approval.Source, fanoutTargets); err != nil {
		return PendingApprovalApplyResult{}, err
	}
	if err := touchTaskUpdatedAt(ctx, q, string(approval.Source.TaskID), s.now().UnixMilli()); err != nil {
		return PendingApprovalApplyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PendingApprovalApplyResult{}, err
	}
	result := PendingApprovalApplyResult{
		Mutation: workflow.CurrentNodeMutationResult{
			Removed: []workflow.CurrentNodeReference{approval.Source},
			Created: targets,
		},
		ResolvedApproval: approval,
		Handoff:          pendingApprovalHandoff(approval),
		TaskAttentionResolution: TaskAttentionResolution{
			Approvals: []ApprovalAttentionProjection{approvalAttention},
		},
	}
	for _, target := range targets {
		if target.Scheduling != nil {
			result.AutomaticIntents = append(result.AutomaticIntents, target.Reference)
		}
	}
	return result, nil
}

func pendingApprovalByID(
	ctx context.Context,
	q *sqlitegen.Queries,
	approvalID workflow.ApprovalID,
) (workflow.PendingApproval, error) {
	row, err := q.GetTaskPendingApproval(ctx, approvalID.String())
	if err != nil {
		return workflow.PendingApproval{}, err
	}
	return pendingApprovalFromRow(ctx, q, row)
}

func pendingApprovalHandoff(approval workflow.PendingApproval) CompletionHandoff {
	destination := approval.Transition.Group.DisplayName
	if len(approval.Branches) == 1 {
		destination = approval.Branches[0].Target.DisplayName
	}
	return CompletionHandoff{
		SourceNodeDisplayName:  approval.Transition.SourceDisplayName,
		DestinationDisplayName: destination,
	}
}

func validatePendingApprovalSequentialTarget(source workflow.CurrentNodeReference, target workflow.CurrentNodeReference) error {
	sourceBranchKey, sourceBranchScoped := source.TransitionBranchKey()
	targetBranchKey, targetBranchScoped := target.TransitionBranchKey()
	if sourceBranchScoped != targetBranchScoped {
		return errors.New("pending approval target scope must match its source")
	}
	if sourceBranchScoped && sourceBranchKey != targetBranchKey {
		return errors.New("pending approval target branch must match its source branch")
	}
	return nil
}

func pendingApprovalFanoutTargets(source workflow.CurrentNodeReference, branches []workflow.PendingApprovalBranch) ([]currentNodeFanoutTarget, error) {
	if source.IsBranchScoped() {
		return nil, errors.New("branch-scoped pending approval cannot create a nested fanout")
	}
	if len(branches) < 2 {
		return nil, errors.New("fanout pending approval requires multiple target branches")
	}
	targets := make([]currentNodeFanoutTarget, 0, len(branches))
	seen := make(map[workflow.TransitionBranchKey]struct{}, len(branches))
	for _, branch := range branches {
		branchKey := workflow.TransitionBranchKey(strings.TrimSpace(string(branch.TransitionBranchKey)))
		if branchKey == "" {
			return nil, errors.New("fanout pending approval branch key is required")
		}
		if _, exists := seen[branchKey]; exists {
			return nil, fmt.Errorf("fanout pending approval branch key %q is duplicated", branchKey)
		}
		seen[branchKey] = struct{}{}
		target := branch.Target.CurrentNode
		if target.Reference.TaskID != source.TaskID {
			return nil, errors.New("fanout pending approval target task must match its source")
		}
		targetBranchKey, branchScoped := target.Reference.TransitionBranchKey()
		if !branchScoped || targetBranchKey != branchKey {
			return nil, errors.New("fanout pending approval target branch must match its frozen branch key")
		}
		targets = append(targets, currentNodeFanoutTarget{
			BranchKey:   branchKey,
			CurrentNode: target,
			NodeKind:    branch.Target.NodeKind,
		})
	}
	return targets, nil
}

func newPendingApproval(
	source workflow.CurrentNode,
	workflowVersion int64,
	group workflow.TransitionGroup,
	sourceDisplayName string,
	edge workflow.Edge,
	target workflow.Node,
	targetCurrentNode workflow.CurrentNode,
	commentary string,
	outputValues map[string]string,
	createdAt time.Time,
) (workflow.PendingApproval, error) {
	resolution, err := pendingApprovalContextSourceResolution(target.Kind(), targetCurrentNode)
	if err != nil {
		return workflow.PendingApproval{}, err
	}
	return newPendingApprovalWithBranches(
		source,
		workflowVersion,
		group,
		sourceDisplayName,
		commentary,
		outputValues,
		[]workflow.PendingApprovalBranch{{
			TransitionBranchKey: workflow.TransitionBranchKey(edge.Key),
			Target: workflow.PendingApprovalTarget{
				CurrentNode: targetCurrentNode,
				DisplayName: workflow.NodeDisplayName(target),
				NodeKind:    target.Kind(),
			},
			EffectiveEdge:           edge,
			ContextSourceResolution: resolution,
		}},
		createdAt,
	)
}

func newPendingApprovalWithBranches(
	source workflow.CurrentNode,
	workflowVersion int64,
	group workflow.TransitionGroup,
	sourceDisplayName string,
	commentary string,
	outputValues map[string]string,
	branches []workflow.PendingApprovalBranch,
	createdAt time.Time,
) (workflow.PendingApproval, error) {
	if err := source.Reference.Validate(); err != nil {
		return workflow.PendingApproval{}, err
	}
	if workflowVersion < 1 {
		return workflow.PendingApproval{}, errors.New("pending approval workflow version is required")
	}
	if strings.TrimSpace(string(group.ID)) == "" || strings.TrimSpace(string(group.TransitionID)) == "" {
		return workflow.PendingApproval{}, errors.New("pending approval transition snapshot is invalid")
	}
	if createdAt.IsZero() || createdAt.UnixMilli() <= 0 {
		return workflow.PendingApproval{}, errors.New("pending approval creation time is required")
	}
	if strings.TrimSpace(sourceDisplayName) == "" || len(branches) == 0 {
		return workflow.PendingApproval{}, errors.New("pending approval handoff labels and branch snapshots are required")
	}
	seenBranchKeys := make(map[workflow.TransitionBranchKey]struct{}, len(branches))
	for _, branch := range branches {
		branchKey := workflow.TransitionBranchKey(strings.TrimSpace(string(branch.TransitionBranchKey)))
		if branchKey == "" ||
			strings.TrimSpace(branch.Target.DisplayName) == "" ||
			strings.TrimSpace(string(branch.EffectiveEdge.Key)) == "" ||
			workflow.TransitionBranchKey(branch.EffectiveEdge.Key) != branchKey {
			return workflow.PendingApproval{}, errors.New("pending approval branch snapshot is invalid")
		}
		if err := branch.Target.CurrentNode.Reference.Validate(); err != nil {
			return workflow.PendingApproval{}, err
		}
		switch branch.Target.NodeKind {
		case workflow.NodeKindAgent:
			if branch.Target.CurrentNode.AgentExecutionSelection == nil {
				return workflow.PendingApproval{}, errors.New("pending approval Agent target requires execution selection")
			}
		case workflow.NodeKindStart, workflow.NodeKindScript, workflow.NodeKindJoin, workflow.NodeKindTerminal:
			if branch.Target.CurrentNode.AgentExecutionSelection != nil {
				return workflow.PendingApproval{}, fmt.Errorf("pending approval %s target cannot carry Agent execution selection", branch.Target.NodeKind)
			}
		default:
			return workflow.PendingApproval{}, errors.New("pending approval target node kind is invalid")
		}
		if _, exists := seenBranchKeys[branchKey]; exists {
			return workflow.PendingApproval{}, fmt.Errorf("pending approval branch key %q is duplicated", branchKey)
		}
		seenBranchKeys[branchKey] = struct{}{}
	}
	approval := workflow.PendingApproval{
		ID:              workflow.NewApprovalID(),
		Source:          source.Reference,
		SourceSessionID: clonePendingApprovalSessionID(source.SessionID),
		WorkflowVersion: workflowVersion,
		Transition: workflow.PendingApprovalTransition{
			Group:             group,
			SourceDisplayName: sourceDisplayName,
		},
		Commentary:   commentary,
		OutputValues: cloneCurrentNodeOutputValues(outputValues),
		Branches:     append([]workflow.PendingApprovalBranch(nil), branches...),
		CreatedAt:    createdAt.UTC().Truncate(time.Millisecond),
	}
	return approval, nil
}

func insertPendingApproval(ctx context.Context, q *sqlitegen.Queries, approval workflow.PendingApproval) error {
	transitionSnapshotJSON, err := workflow.MarshalString(pendingApprovalTransitionSnapshot{
		WorkflowID:        approval.Transition.Group.WorkflowID,
		ID:                approval.Transition.Group.ID,
		SourceNodeID:      approval.Transition.Group.SourceNodeID,
		TransitionID:      approval.Transition.Group.TransitionID,
		DisplayName:       approval.Transition.Group.DisplayName,
		Description:       approval.Transition.Group.Description,
		SourceDisplayName: approval.Transition.SourceDisplayName,
		Commentary:        approval.Commentary,
	})
	if err != nil {
		return fmt.Errorf("encode pending approval transition snapshot: %w", err)
	}
	outputValuesJSON, err := workflow.MarshalString(approval.OutputValues)
	if err != nil {
		return fmt.Errorf("encode pending approval materialized values: %w", err)
	}
	sourceBranchKey := sql.NullString{}
	if branchKey, branchScoped := approval.Source.TransitionBranchKey(); branchScoped {
		sourceBranchKey = sql.NullString{String: string(branchKey), Valid: true}
	}
	sourceSessionID := sql.NullString{}
	if approval.SourceSessionID != nil {
		sourceSessionID = sql.NullString{String: approval.SourceSessionID.String(), Valid: true}
	}
	if err := q.InsertTaskPendingApproval(ctx, sqlitegen.InsertTaskPendingApprovalParams{
		ID:                        approval.ID.String(),
		SourceTaskID:              string(approval.Source.TaskID),
		SourceNodeID:              string(approval.Source.NodeID),
		SourceTransitionBranchKey: sourceBranchKey,
		SourceSessionID:           sourceSessionID,
		WorkflowVersion:           approval.WorkflowVersion,
		TransitionSnapshotJson:    transitionSnapshotJSON,
		MaterializedValuesJson:    outputValuesJSON,
		CreatedAtUnixMs:           approval.CreatedAt.UnixMilli(),
	}); err != nil {
		return err
	}
	for _, branch := range approval.Branches {
		if err := insertPendingApprovalBranch(ctx, q, approval.ID, branch); err != nil {
			return err
		}
	}
	return nil
}

func insertPendingApprovalBranch(ctx context.Context, q *sqlitegen.Queries, approvalID workflow.ApprovalID, branch workflow.PendingApprovalBranch) error {
	targetSnapshotJSON, err := pendingApprovalTargetSnapshotJSON(branch.Target)
	if err != nil {
		return err
	}
	edgeSnapshotJSON, err := workflow.MarshalString(pendingApprovalEffectiveEdgeSnapshot{
		WorkflowID:         branch.EffectiveEdge.WorkflowID,
		ID:                 branch.EffectiveEdge.ID,
		Key:                branch.EffectiveEdge.Key,
		TransitionGroupID:  branch.EffectiveEdge.TransitionGroupID,
		TargetNodeID:       branch.EffectiveEdge.TargetNodeID,
		AssigneeSelection:  workflow.CanonicalAssigneeSelection(branch.EffectiveEdge.AssigneeSelection),
		ThinkingSelection:  workflow.CanonicalThinkingSelection(branch.EffectiveEdge.ThinkingSelection),
		ContextMode:        branch.EffectiveEdge.ContextMode,
		ContextSource:      workflow.CanonicalContextSource(branch.EffectiveEdge.ContextSource),
		RequiresApproval:   branch.EffectiveEdge.RequiresApproval,
		PromptTemplate:     branch.EffectiveEdge.PromptTemplate,
		Parameters:         append([]workflow.Parameter(nil), branch.EffectiveEdge.Parameters...),
		InputBindings:      append([]workflow.InputBinding(nil), branch.EffectiveEdge.InputBindings...),
		OutputRequirements: append([]workflow.OutputRequirement(nil), branch.EffectiveEdge.OutputRequirements...),
	})
	if err != nil {
		return fmt.Errorf("encode pending approval effective edge: %w", err)
	}
	contextResolutionJSON, err := pendingApprovalContextSourceResolutionJSON(branch)
	if err != nil {
		return err
	}
	return q.InsertTaskPendingApprovalBranch(ctx, sqlitegen.InsertTaskPendingApprovalBranchParams{
		ApprovalID:                     approvalID.String(),
		TransitionBranchKey:            string(branch.TransitionBranchKey),
		TargetSnapshotJson:             targetSnapshotJSON,
		EffectiveEdgeConfigurationJson: edgeSnapshotJSON,
		ContextSourceResolutionJson:    contextResolutionJSON,
	})
}

func pendingApprovalFromRow(ctx context.Context, q *sqlitegen.Queries, row sqlitegen.TaskPendingApproval) (workflow.PendingApproval, error) {
	approvalID, err := workflow.ParseApprovalID(row.ID)
	if err != nil {
		return workflow.PendingApproval{}, fmt.Errorf("decode pending approval id: %w", err)
	}
	var sourceBranchKey *workflow.TransitionBranchKey
	if row.SourceTransitionBranchKey.Valid {
		value := workflow.TransitionBranchKey(row.SourceTransitionBranchKey.String)
		sourceBranchKey = &value
	}
	source, err := workflow.NewCurrentNodeReference(workflow.TaskID(row.SourceTaskID), workflow.NodeID(row.SourceNodeID), sourceBranchKey)
	if err != nil {
		return workflow.PendingApproval{}, fmt.Errorf("decode pending approval source: %w", err)
	}
	var sourceSessionID *runtimeids.SessionID
	if row.SourceSessionID.Valid {
		parsed, err := runtimeids.ParseSessionID(row.SourceSessionID.String)
		if err != nil {
			return workflow.PendingApproval{}, fmt.Errorf("decode pending approval source session: %w", err)
		}
		sourceSessionID = &parsed
	}
	var transitionSnapshot pendingApprovalTransitionSnapshot
	if err := workflow.UnmarshalString(row.TransitionSnapshotJson, &transitionSnapshot); err != nil {
		return workflow.PendingApproval{}, fmt.Errorf("decode pending approval transition snapshot: %w", err)
	}
	if strings.TrimSpace(string(transitionSnapshot.ID)) == "" || strings.TrimSpace(string(transitionSnapshot.TransitionID)) == "" || strings.TrimSpace(transitionSnapshot.SourceDisplayName) == "" {
		return workflow.PendingApproval{}, errors.New("pending approval transition snapshot is invalid")
	}
	outputValues := map[string]string{}
	if err := workflow.UnmarshalString(row.MaterializedValuesJson, &outputValues); err != nil {
		return workflow.PendingApproval{}, fmt.Errorf("decode pending approval materialized values: %w", err)
	}
	if outputValues == nil {
		return workflow.PendingApproval{}, errors.New("pending approval materialized values are invalid")
	}
	rows, err := q.ListTaskPendingApprovalBranches(ctx, approvalID.String())
	if err != nil {
		return workflow.PendingApproval{}, err
	}
	branches := make([]workflow.PendingApprovalBranch, 0, len(rows))
	for _, branchRow := range rows {
		branch, err := pendingApprovalBranchFromRow(source.TaskID, branchRow)
		if err != nil {
			return workflow.PendingApproval{}, err
		}
		branches = append(branches, branch)
	}
	if len(branches) == 0 {
		return workflow.PendingApproval{}, errors.New("pending approval has no branch snapshots")
	}
	return workflow.PendingApproval{
		ID:              approvalID,
		Source:          source,
		SourceSessionID: sourceSessionID,
		WorkflowVersion: row.WorkflowVersion,
		Transition: workflow.PendingApprovalTransition{
			Group: workflow.TransitionGroup{
				WorkflowID:   transitionSnapshot.WorkflowID,
				ID:           transitionSnapshot.ID,
				SourceNodeID: transitionSnapshot.SourceNodeID,
				TransitionID: transitionSnapshot.TransitionID,
				DisplayName:  transitionSnapshot.DisplayName,
				Description:  transitionSnapshot.Description,
			},
			SourceDisplayName: transitionSnapshot.SourceDisplayName,
		},
		Commentary:   transitionSnapshot.Commentary,
		OutputValues: outputValues,
		Branches:     branches,
		CreatedAt:    time.UnixMilli(row.CreatedAtUnixMs).UTC(),
	}, nil
}

func pendingApprovalBranchFromRow(taskID workflow.TaskID, row sqlitegen.TaskPendingApprovalBranch) (workflow.PendingApprovalBranch, error) {
	branchKey := workflow.TransitionBranchKey(strings.TrimSpace(row.TransitionBranchKey))
	if branchKey == "" {
		return workflow.PendingApprovalBranch{}, errors.New("pending approval branch key is required")
	}
	var targetSnapshot pendingApprovalTargetSnapshot
	if err := workflow.UnmarshalString(row.TargetSnapshotJson, &targetSnapshot); err != nil {
		return workflow.PendingApprovalBranch{}, fmt.Errorf("decode pending approval target snapshot: %w", err)
	}
	if targetSnapshot.PriorValues.TransitionParameters == nil {
		return workflow.PendingApprovalBranch{}, errors.New("pending approval target prior Transition parameters are invalid")
	}
	target, err := pendingApprovalTargetFromSnapshot(taskID, targetSnapshot)
	if err != nil {
		return workflow.PendingApprovalBranch{}, err
	}
	var edgeSnapshot pendingApprovalEffectiveEdgeSnapshot
	if err := workflow.UnmarshalString(row.EffectiveEdgeConfigurationJson, &edgeSnapshot); err != nil {
		return workflow.PendingApprovalBranch{}, fmt.Errorf("decode pending approval effective edge: %w", err)
	}
	if strings.TrimSpace(string(edgeSnapshot.ID)) == "" || strings.TrimSpace(string(edgeSnapshot.TargetNodeID)) == "" {
		return workflow.PendingApprovalBranch{}, errors.New("pending approval effective edge is invalid")
	}
	var resolutionSnapshot pendingApprovalContextSourceResolutionSnapshot
	if err := workflow.UnmarshalString(row.ContextSourceResolutionJson, &resolutionSnapshot); err != nil {
		return workflow.PendingApprovalBranch{}, fmt.Errorf("decode pending approval context source resolution: %w", err)
	}
	resolution, activeSource, err := pendingApprovalContextSourceResolutionFromSnapshot(
		resolutionSnapshot,
		target,
	)
	if err != nil {
		return workflow.PendingApprovalBranch{}, err
	}
	target.CurrentNode.SessionID = targetSessionIntentSessionID(resolution.TargetSession)
	target.CurrentNode.ContinuationSource = activeSource
	return workflow.PendingApprovalBranch{
		TransitionBranchKey: branchKey,
		Target:              target,
		EffectiveEdge: workflow.Edge{
			WorkflowID:         edgeSnapshot.WorkflowID,
			ID:                 edgeSnapshot.ID,
			Key:                edgeSnapshot.Key,
			TransitionGroupID:  edgeSnapshot.TransitionGroupID,
			TargetNodeID:       edgeSnapshot.TargetNodeID,
			AssigneeSelection:  workflow.CanonicalAssigneeSelection(edgeSnapshot.AssigneeSelection),
			ThinkingSelection:  workflow.CanonicalThinkingSelection(edgeSnapshot.ThinkingSelection),
			ContextMode:        edgeSnapshot.ContextMode,
			ContextSource:      workflow.CanonicalContextSource(edgeSnapshot.ContextSource),
			RequiresApproval:   edgeSnapshot.RequiresApproval,
			PromptTemplate:     edgeSnapshot.PromptTemplate,
			Parameters:         append([]workflow.Parameter(nil), edgeSnapshot.Parameters...),
			InputBindings:      append([]workflow.InputBinding(nil), edgeSnapshot.InputBindings...),
			OutputRequirements: append([]workflow.OutputRequirement(nil), edgeSnapshot.OutputRequirements...),
		},
		ContextSourceResolution: resolution,
	}, nil
}

func pendingApprovalTargetSnapshotJSON(target workflow.PendingApprovalTarget) (string, error) {
	currentNode := target.CurrentNode
	if err := currentNode.Reference.Validate(); err != nil {
		return "", err
	}
	var branchKey *workflow.TransitionBranchKey
	if value, present := currentNode.Reference.TransitionBranchKey(); present {
		branchKey = &value
	}
	var enteredByEdgeID *workflow.EdgeID
	if currentNode.EnteredByEdgeID != nil {
		value := *currentNode.EnteredByEdgeID
		enteredByEdgeID = &value
	}
	var schedulingState *workflow.CurrentNodeSchedulingState
	if currentNode.Scheduling != nil {
		if currentNode.Scheduling.Interruption != nil {
			return "", errors.New("pending approval target snapshot cannot retain an interruption")
		}
		value := currentNode.Scheduling.State
		schedulingState = &value
	}
	return workflow.MarshalString(pendingApprovalTargetSnapshot{
		NodeID:                  currentNode.Reference.NodeID,
		NodeKind:                target.NodeKind,
		TransitionBranchKey:     branchKey,
		EnteredByEdgeID:         enteredByEdgeID,
		DisplayName:             target.DisplayName,
		CurrentInputValues:      cloneCurrentNodeOutputValues(currentNode.CurrentInputValues),
		PriorValues:             currentNode.PriorValues.Clone(),
		SchedulingState:         schedulingState,
		AgentExecutionSelection: cloneCurrentNodeExecutionSelection(currentNode.AgentExecutionSelection),
	})
}

func pendingApprovalTargetFromSnapshot(taskID workflow.TaskID, snapshot pendingApprovalTargetSnapshot) (workflow.PendingApprovalTarget, error) {
	if strings.TrimSpace(snapshot.DisplayName) == "" {
		return workflow.PendingApprovalTarget{}, errors.New("pending approval target display name is required")
	}
	switch snapshot.NodeKind {
	case workflow.NodeKindAgent:
		if snapshot.AgentExecutionSelection == nil {
			return workflow.PendingApprovalTarget{}, errors.New("pending approval Agent target requires execution selection")
		}
	case workflow.NodeKindStart, workflow.NodeKindScript, workflow.NodeKindJoin, workflow.NodeKindTerminal:
		if snapshot.AgentExecutionSelection != nil {
			return workflow.PendingApprovalTarget{}, errors.New("pending approval non-Agent target cannot carry execution selection")
		}
	default:
		return workflow.PendingApprovalTarget{}, errors.New("pending approval target node kind is invalid")
	}
	reference, err := workflow.NewCurrentNodeReference(taskID, snapshot.NodeID, snapshot.TransitionBranchKey)
	if err != nil {
		return workflow.PendingApprovalTarget{}, fmt.Errorf("decode pending approval target reference: %w", err)
	}
	var sessionID *runtimeids.SessionID
	if snapshot.SessionID != nil {
		parsed, err := runtimeids.ParseSessionID(*snapshot.SessionID)
		if err != nil {
			return workflow.PendingApprovalTarget{}, fmt.Errorf("decode pending approval target session: %w", err)
		}
		sessionID = &parsed
	}
	var scheduling *workflow.CurrentNodeScheduling
	if snapshot.SchedulingState != nil {
		scheduling = &workflow.CurrentNodeScheduling{State: *snapshot.SchedulingState}
	}
	currentNode, err := workflow.NewCurrentNodeWithExecutionSelection(
		reference,
		snapshot.CurrentInputValues,
		snapshot.PriorValues,
		sessionID,
		scheduling,
		snapshot.AgentExecutionSelection,
	)
	if err != nil {
		return workflow.PendingApprovalTarget{}, fmt.Errorf("decode pending approval target current node: %w", err)
	}
	currentNode.EnteredByEdgeID = snapshot.EnteredByEdgeID
	return workflow.PendingApprovalTarget{
		CurrentNode: currentNode,
		DisplayName: snapshot.DisplayName,
		NodeKind:    snapshot.NodeKind,
	}, nil
}

func cloneCurrentNodeExecutionSelection(selection *workflow.AgentExecutionSelection) *workflow.AgentExecutionSelection {
	if selection == nil {
		return nil
	}
	cloned := selection.Clone()
	return &cloned
}

func pendingApprovalContextSourceResolutionJSON(branch workflow.PendingApprovalBranch) (string, error) {
	if err := validatePendingApprovalContextSourceResolution(
		branch.Target.NodeKind,
		branch.Target.CurrentNode,
		branch.ContextSourceResolution,
	); err != nil {
		return "", err
	}
	targetSession := branch.ContextSourceResolution.TargetSession
	activeSource := branch.ContextSourceResolution.ActiveSource
	return workflow.MarshalString(pendingApprovalContextSourceResolutionSnapshot{
		TargetSession: &targetSession,
		ActiveSource:  &activeSource,
	})
}

func pendingApprovalContextSourceResolutionFromSnapshot(
	snapshot pendingApprovalContextSourceResolutionSnapshot,
	target workflow.PendingApprovalTarget,
) (
	workflow.PendingApprovalContextSourceResolution,
	workflow.MaterializedContinuationSource,
	error,
) {
	typed := snapshot.TargetSession != nil || snapshot.ActiveSource != nil
	if typed {
		if snapshot.SessionID != nil || snapshot.TargetSession == nil || snapshot.ActiveSource == nil {
			return workflow.PendingApprovalContextSourceResolution{},
				workflow.MaterializedContinuationSource{},
				errors.New("pending approval context source resolution snapshot mixes incompatible shapes")
		}
		if target.CurrentNode.SessionID != nil &&
			snapshot.ActiveSource.Kind() != workflow.MaterializedContinuationSourceLegacy {
			return workflow.PendingApprovalContextSourceResolution{},
				workflow.MaterializedContinuationSource{},
				errors.New("typed pending approval target snapshot duplicates Session authority")
		}
		resolution := workflow.PendingApprovalContextSourceResolution{
			TargetSession: *snapshot.TargetSession,
			ActiveSource:  *snapshot.ActiveSource,
		}
		reconstructed := target.CurrentNode
		reconstructed.SessionID = targetSessionIntentSessionID(resolution.TargetSession)
		reconstructed.ContinuationSource = resolution.ActiveSource
		if err := validatePendingApprovalContextSourceResolution(target.NodeKind, reconstructed, resolution); err != nil {
			return workflow.PendingApprovalContextSourceResolution{},
				workflow.MaterializedContinuationSource{},
				err
		}
		return resolution,
			*snapshot.ActiveSource,
			nil
	}
	var sessionID *runtimeids.SessionID
	if snapshot.SessionID != nil {
		parsed, err := runtimeids.ParseSessionID(*snapshot.SessionID)
		if err != nil {
			return workflow.PendingApprovalContextSourceResolution{},
				workflow.MaterializedContinuationSource{},
				fmt.Errorf("decode pending approval context session: %w", err)
		}
		sessionID = &parsed
	}
	var (
		targetSession workflow.TargetSessionIntent
		source        workflow.MaterializedContinuationSource
	)
	switch target.NodeKind {
	case workflow.NodeKindAgent:
		if target.CurrentNode.SessionID == nil {
			targetSession = workflow.CreateTargetSessionIntent()
			source = workflow.DeferredSelfMaterializedContinuationSource()
		} else {
			reused, err := workflow.NewReuseTargetSessionIntent(*target.CurrentNode.SessionID)
			if err != nil {
				return workflow.PendingApprovalContextSourceResolution{},
					workflow.MaterializedContinuationSource{},
					err
			}
			targetSession = reused
			source = workflow.LegacyMaterializedContinuationSource()
		}
	case workflow.NodeKindScript, workflow.NodeKindJoin:
		targetSession = workflow.NoAgentTargetSessionIntent()
		source = workflow.LegacyMaterializedContinuationSource()
	case workflow.NodeKindTerminal:
		targetSession = workflow.NoAgentTargetSessionIntent()
		source = workflow.AbsentMaterializedContinuationSource()
	default:
		return workflow.PendingApprovalContextSourceResolution{},
			workflow.MaterializedContinuationSource{},
			fmt.Errorf("pending approval target node kind %q is invalid", target.NodeKind)
	}
	resolution := workflow.PendingApprovalContextSourceResolution{
		TargetSession: targetSession,
		ActiveSource:  source,
	}
	if reused, ok := targetSession.SessionID(); ok {
		if sessionID == nil || *sessionID != reused {
			return workflow.PendingApprovalContextSourceResolution{},
				workflow.MaterializedContinuationSource{},
				errors.New("legacy pending approval target and context source sessions differ")
		}
	} else if sessionID != nil {
		return workflow.PendingApprovalContextSourceResolution{},
			workflow.MaterializedContinuationSource{},
			errors.New("legacy pending approval context source Session is invalid for target")
	}
	return resolution, source, nil
}

func targetSessionIntentSessionID(intent workflow.TargetSessionIntent) *runtimeids.SessionID {
	sessionID, reused := intent.SessionID()
	if !reused {
		return nil
	}
	return &sessionID
}

func pendingApprovalContextSourceResolution(
	nodeKind workflow.NodeKind,
	currentNode workflow.CurrentNode,
) (workflow.PendingApprovalContextSourceResolution, error) {
	targetSession := workflow.NoAgentTargetSessionIntent()
	if nodeKind == workflow.NodeKindAgent {
		if currentNode.SessionID == nil {
			targetSession = workflow.CreateTargetSessionIntent()
		} else {
			reused, err := workflow.NewReuseTargetSessionIntent(*currentNode.SessionID)
			if err != nil {
				return workflow.PendingApprovalContextSourceResolution{}, err
			}
			targetSession = reused
		}
	}
	resolution := workflow.PendingApprovalContextSourceResolution{
		TargetSession: targetSession,
		ActiveSource:  currentNode.ContinuationSource,
	}
	if err := validatePendingApprovalContextSourceResolution(nodeKind, currentNode, resolution); err != nil {
		return workflow.PendingApprovalContextSourceResolution{}, err
	}
	return resolution, nil
}

func validatePendingApprovalContextSourceResolution(
	nodeKind workflow.NodeKind,
	currentNode workflow.CurrentNode,
	resolution workflow.PendingApprovalContextSourceResolution,
) error {
	if err := resolution.TargetSession.Validate(); err != nil {
		return fmt.Errorf("pending approval target Session intent: %w", err)
	}
	if err := resolution.ActiveSource.Validate(); err != nil {
		return fmt.Errorf("pending approval active source: %w", err)
	}
	resolvedSessionID, reused := resolution.TargetSession.SessionID()
	switch nodeKind {
	case workflow.NodeKindAgent:
		if resolution.TargetSession.Kind() == workflow.TargetSessionIntentNoAgent {
			return errors.New("pending approval Agent target cannot omit Agent Session intent")
		}
	case workflow.NodeKindScript, workflow.NodeKindJoin, workflow.NodeKindTerminal:
		if resolution.TargetSession.Kind() != workflow.TargetSessionIntentNoAgent {
			return fmt.Errorf("pending approval %s target cannot carry Agent Session intent", nodeKind)
		}
	default:
		return fmt.Errorf("pending approval target node kind %q is invalid", nodeKind)
	}
	if reused {
		if currentNode.SessionID == nil || *currentNode.SessionID != resolvedSessionID {
			return errors.New("pending approval target and context source Session intent differ")
		}
	} else if currentNode.SessionID != nil {
		return errors.New("pending approval target duplicates a Session absent from context resolution")
	}
	if !sameMaterializedContinuationSource(currentNode.ContinuationSource, resolution.ActiveSource) {
		return errors.New("pending approval target and context source active source differ")
	}
	return nil
}

func sameMaterializedContinuationSource(
	left workflow.MaterializedContinuationSource,
	right workflow.MaterializedContinuationSource,
) bool {
	if left.Kind() != right.Kind() {
		return false
	}
	leftSessionID, leftExact := left.ExactSessionID()
	rightSessionID, rightExact := right.ExactSessionID()
	return leftExact == rightExact && (!leftExact || leftSessionID == rightSessionID)
}

func normalizeApprovalID(id workflow.ApprovalID) (workflow.ApprovalID, error) {
	if strings.TrimSpace(id.String()) == "" {
		return "", ErrApprovalIDRequired
	}
	return workflow.ParseApprovalID(id.String())
}

func currentNodePendingApprovalID(ctx context.Context, q *sqlitegen.Queries, reference workflow.CurrentNodeReference) (workflow.ApprovalID, bool, error) {
	if err := reference.Validate(); err != nil {
		return "", false, err
	}
	branchKey := sql.NullString{}
	if value, present := reference.TransitionBranchKey(); present {
		branchKey = sql.NullString{String: string(value), Valid: true}
	}
	pending, err := q.HasTaskPendingApprovalForCurrentNode(ctx, sqlitegen.HasTaskPendingApprovalForCurrentNodeParams{
		TaskID:              string(reference.TaskID),
		NodeID:              string(reference.NodeID),
		TransitionBranchKey: branchKey,
	})
	if err != nil {
		return "", false, err
	}
	return "", pending, nil
}

func sameOptionalSessionID(left, right *runtimeids.SessionID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func clonePendingApprovalSessionID(sessionID *runtimeids.SessionID) *runtimeids.SessionID {
	if sessionID == nil {
		return nil
	}
	cloned := *sessionID
	return &cloned
}
