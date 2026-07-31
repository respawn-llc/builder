package workflowstore

import (
	"context"
	"fmt"

	"core/server/metadata/sqlitegen"
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

func (s *Store) DeleteWorkflow(ctx context.Context, req WorkflowDeleteRequest) (WorkflowDeleteResult, error) {
	if req.WorkflowID.IsZero() {
		return WorkflowDeleteResult{}, ErrWorkflowIDRequired
	}
	impact, err := s.PreviewWorkflowDelete(ctx, req.WorkflowID)
	if err != nil {
		return WorkflowDeleteResult{}, err
	}
	if blockers := workflowDeleteBlockers(req, impact); len(blockers) > 0 {
		return WorkflowDeleteResult{Impact: impact, Blockers: blockers}, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkflowDeleteResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	current, err := q.GetWorkflowDeleteImpact(ctx, req.WorkflowID)
	if err != nil {
		return WorkflowDeleteResult{}, err
	}
	impact = workflowDeleteImpactFromRow(current)
	if blockers := workflowDeleteBlockers(req, impact); len(blockers) > 0 {
		return WorkflowDeleteResult{Impact: impact, Blockers: blockers}, nil
	}

	now := s.now().UnixMilli()
	resolution, err := workflowAttentionResolution(ctx, q, req.WorkflowID)
	if err != nil {
		return WorkflowDeleteResult{}, fmt.Errorf("project workflow attention resolution: %w", err)
	}
	if _, err := q.DeleteWorkflowTaskPendingApprovalsByWorkflowID(ctx, req.WorkflowID); err != nil {
		return WorkflowDeleteResult{}, fmt.Errorf("delete workflow task pending approvals: %w", err)
	}
	if _, err := q.DeleteWorkflowTaskCurrentNodesByWorkflowID(ctx, req.WorkflowID); err != nil {
		return WorkflowDeleteResult{}, fmt.Errorf("delete workflow task current nodes: %w", err)
	}
	if _, err := q.DeleteWorkflowTaskCommentsByWorkflowID(ctx, req.WorkflowID); err != nil {
		return WorkflowDeleteResult{}, fmt.Errorf("delete workflow task comments: %w", err)
	}
	if _, err := q.DeleteWorkflowTasksByWorkflowID(ctx, req.WorkflowID); err != nil {
		return WorkflowDeleteResult{}, fmt.Errorf("delete workflow tasks: %w", err)
	}
	if _, err := q.ClearDeletedWorkflowDefaultProjectLinks(ctx, sqlitegen.ClearDeletedWorkflowDefaultProjectLinksParams{UpdatedAtUnixMs: now, WorkflowID: req.WorkflowID}); err != nil {
		return WorkflowDeleteResult{}, fmt.Errorf("clear workflow default links: %w", err)
	}
	if _, err := q.DeleteProjectWorkflowLinksByWorkflowID(ctx, req.WorkflowID); err != nil {
		return WorkflowDeleteResult{}, fmt.Errorf("delete workflow project links: %w", err)
	}
	deletedCount, err := q.DeleteWorkflowByID(ctx, req.WorkflowID)
	if err != nil {
		return WorkflowDeleteResult{}, fmt.Errorf("delete workflow: %w", err)
	}
	if deletedCount != 1 {
		return WorkflowDeleteResult{}, fmt.Errorf("workflow %q was not deleted", req.WorkflowID)
	}
	if err := tx.Commit(); err != nil {
		return WorkflowDeleteResult{}, err
	}
	return WorkflowDeleteResult{
		Deleted:                 true,
		Impact:                  impact,
		TaskAttentionResolution: resolution,
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
