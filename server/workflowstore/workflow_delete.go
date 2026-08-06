package workflowstore

import (
	"context"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/shared/runtimeids"
)

type WorkflowDeleteImpact struct {
	WorkflowID                     runtimeids.WorkflowID
	Version                        int64
	ProjectCount                   int64
	LinkCount                      int64
	DefaultReplacementProjectCount int64
	TaskCount                      int64
	CurrentNodeCount               int64
	PendingApprovalCount           int64
	BlockedTaskCount               int64
}

type WorkflowDeleteRequest struct {
	WorkflowID           runtimeids.WorkflowID
	Confirmed            bool
	ExpectedVersion      int64
	ExpectedProjectCount int64
	ExpectedLinkCount    int64
	ExpectedTaskCount    int64
	CleanupArtifacts     bool
}

type WorkflowDeleteResult struct {
	Deleted  bool
	Impact   WorkflowDeleteImpact
	Blockers []WorkflowDeleteBlocker
	TaskAttentionResolution
}

type WorkflowDeleteBlocker struct {
	Code    string
	Message string
	Count   int64
}

type preparedWorkflowDeletion struct {
	*preparedSQLLifecycleMutation
	result  WorkflowDeleteResult
	taskIDs []workflow.TaskID
}

func (s *Store) PreviewWorkflowDelete(ctx context.Context, workflowID runtimeids.WorkflowID) (WorkflowDeleteImpact, error) {
	if workflowID.IsZero() {
		return WorkflowDeleteImpact{}, ErrWorkflowIDRequired
	}
	row, err := s.queries.GetWorkflowDeleteImpact(ctx, workflowID)
	if err != nil {
		return WorkflowDeleteImpact{}, err
	}
	return workflowDeleteImpactFromRow(row), nil
}

func (s *Store) prepareWorkflowDeletion(
	ctx context.Context,
	req WorkflowDeleteRequest,
) (WorkflowDeleteResult, *preparedWorkflowDeletion, error) {
	if req.WorkflowID.IsZero() {
		return WorkflowDeleteResult{}, nil, ErrWorkflowIDRequired
	}
	impact, err := s.PreviewWorkflowDelete(ctx, req.WorkflowID)
	if err != nil {
		return WorkflowDeleteResult{}, nil, err
	}
	if blockers := workflowDeleteBlockers(req, impact); len(blockers) > 0 {
		return WorkflowDeleteResult{Impact: impact, Blockers: blockers}, nil, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowDeleteResult{}, nil, err
	}
	handedOff := false
	defer func() {
		if !handedOff {
			_ = tx.Rollback()
		}
	}()
	q := s.queries.WithTx(tx)
	if _, err := q.AcquireWorkflowDependencyWriteLock(ctx, req.WorkflowID); err != nil {
		return WorkflowDeleteResult{}, nil, fmt.Errorf("lock workflow dependency projects: %w", err)
	}
	current, err := q.GetWorkflowDeleteImpact(ctx, req.WorkflowID)
	if err != nil {
		return WorkflowDeleteResult{}, nil, err
	}
	impact = workflowDeleteImpactFromRow(current)
	if blockers := workflowDeleteBlockers(req, impact); len(blockers) > 0 {
		return WorkflowDeleteResult{Impact: impact, Blockers: blockers}, nil, nil
	}
	rawTaskIDs, err := q.ListWorkflowTaskIDs(ctx, req.WorkflowID)
	if err != nil {
		return WorkflowDeleteResult{}, nil, err
	}
	taskIDs := make([]workflow.TaskID, 0, len(rawTaskIDs))
	for _, rawTaskID := range rawTaskIDs {
		taskID := workflow.TaskID(strings.TrimSpace(rawTaskID))
		if taskID == "" {
			return WorkflowDeleteResult{}, nil, fmt.Errorf("workflow %q has a blank Task id", req.WorkflowID)
		}
		taskIDs = append(taskIDs, taskID)
	}

	now := s.now().UnixMilli()
	resolution, err := workflowAttentionResolution(ctx, q, req.WorkflowID)
	if err != nil {
		return WorkflowDeleteResult{}, nil, fmt.Errorf("project workflow attention resolution: %w", err)
	}
	if _, err := q.TouchWorkflowDependencySurvivors(ctx, sqlitegen.TouchWorkflowDependencySurvivorsParams{
		WorkflowID:      req.WorkflowID,
		UpdatedAtUnixMs: now,
	}); err != nil {
		return WorkflowDeleteResult{}, nil, fmt.Errorf("touch workflow dependency survivors: %w", err)
	}
	if _, err := q.DeleteWorkflowTaskDependenciesByWorkflowID(ctx, req.WorkflowID); err != nil {
		return WorkflowDeleteResult{}, nil, fmt.Errorf("delete workflow task dependencies: %w", err)
	}
	if _, err := q.DeleteWorkflowTaskPendingApprovalsByWorkflowID(ctx, req.WorkflowID); err != nil {
		return WorkflowDeleteResult{}, nil, fmt.Errorf("delete workflow task pending approvals: %w", err)
	}
	if _, err := q.DeleteWorkflowTaskCurrentNodesByWorkflowID(ctx, req.WorkflowID); err != nil {
		return WorkflowDeleteResult{}, nil, fmt.Errorf("delete workflow task current nodes: %w", err)
	}
	if _, err := q.DeleteWorkflowTaskCommentsByWorkflowID(ctx, req.WorkflowID); err != nil {
		return WorkflowDeleteResult{}, nil, fmt.Errorf("delete workflow task comments: %w", err)
	}
	if _, err := q.DeleteWorkflowTasksByWorkflowID(ctx, req.WorkflowID); err != nil {
		return WorkflowDeleteResult{}, nil, fmt.Errorf("delete workflow tasks: %w", err)
	}
	if _, err := q.ClearDeletedWorkflowDefaultProjectLinks(ctx, sqlitegen.ClearDeletedWorkflowDefaultProjectLinksParams{UpdatedAtUnixMs: now, WorkflowID: req.WorkflowID}); err != nil {
		return WorkflowDeleteResult{}, nil, fmt.Errorf("clear workflow default links: %w", err)
	}
	if _, err := q.DeleteProjectWorkflowLinksByWorkflowID(ctx, req.WorkflowID); err != nil {
		return WorkflowDeleteResult{}, nil, fmt.Errorf("delete workflow project links: %w", err)
	}
	deletedCount, err := q.DeleteWorkflowByID(ctx, req.WorkflowID)
	if err != nil {
		return WorkflowDeleteResult{}, nil, fmt.Errorf("delete workflow: %w", err)
	}
	if deletedCount != 1 {
		return WorkflowDeleteResult{}, nil, fmt.Errorf("workflow %q was not deleted", req.WorkflowID)
	}
	result := WorkflowDeleteResult{
		Deleted:                 true,
		Impact:                  impact,
		TaskAttentionResolution: resolution,
	}
	handedOff = true
	return result, &preparedWorkflowDeletion{
		preparedSQLLifecycleMutation: newPreparedSQLLifecycleMutation(tx),
		result:                       result,
		taskIDs:                      taskIDs,
	}, nil
}

func workflowDeleteImpactFromRow(row sqlitegen.GetWorkflowDeleteImpactRow) WorkflowDeleteImpact {
	return WorkflowDeleteImpact{
		WorkflowID:                     row.WorkflowID,
		Version:                        row.Version,
		ProjectCount:                   row.ProjectCount,
		LinkCount:                      row.LinkCount,
		DefaultReplacementProjectCount: row.DefaultReplacementProjectCount,
		TaskCount:                      row.TaskCount,
		CurrentNodeCount:               row.CurrentNodeCount,
		PendingApprovalCount:           row.PendingApprovalCount,
		BlockedTaskCount:               row.BlockedTaskCount,
	}
}

func workflowDeleteBlockers(req WorkflowDeleteRequest, impact WorkflowDeleteImpact) []WorkflowDeleteBlocker {
	blockers := []WorkflowDeleteBlocker{}
	if req.CleanupArtifacts {
		blockers = append(blockers, WorkflowDeleteBlocker{Code: "artifact_cleanup_unsupported", Message: "Artifact and worktree cleanup is not wired yet. Delete the workflow without cleanup to remove only database rows.", Count: 1})
	}
	if impact.DefaultReplacementProjectCount > 0 {
		blockers = append(blockers, WorkflowDeleteBlocker{Code: "default_replacement_required", Message: "Workflow is the default for projects that still have other workflow links. Set replacement defaults before deleting this workflow.", Count: impact.DefaultReplacementProjectCount})
	}
	if impact.CurrentNodeCount > 0 {
		blockers = append(blockers, WorkflowDeleteBlocker{Code: "current_nodes", Message: "Workflow has non-terminal current nodes. Interrupt or finish affected tasks before deleting the workflow.", Count: impact.CurrentNodeCount})
	}
	if impact.PendingApprovalCount > 0 {
		blockers = append(blockers, WorkflowDeleteBlocker{Code: "pending_approvals", Message: "Workflow has pending approvals. Resolve affected tasks before deleting the workflow.", Count: impact.PendingApprovalCount})
	}
	if !req.Confirmed {
		blockers = append(blockers, WorkflowDeleteBlocker{Code: "confirmation_required", Message: "Workflow deletion will delete the workflow and any affected task database rows. Confirm with the current impact counts before deleting.", Count: workflowDeleteConfirmationCount(impact)})
	}
	if req.Confirmed && (req.ExpectedVersion != impact.Version || req.ExpectedProjectCount != impact.ProjectCount || req.ExpectedLinkCount != impact.LinkCount || req.ExpectedTaskCount != impact.TaskCount) {
		blockers = append(blockers, WorkflowDeleteBlocker{Code: "impact_changed", Message: "Workflow deletion impact changed. Refresh the preview before deleting.", Count: 1})
	}
	return blockers
}

func workflowDeleteConfirmationCount(impact WorkflowDeleteImpact) int64 {
	if impact.TaskCount > 0 {
		return impact.TaskCount
	}
	if impact.LinkCount > 0 {
		return impact.LinkCount
	}
	return 1
}
