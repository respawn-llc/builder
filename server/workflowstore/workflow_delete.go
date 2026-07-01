package workflowstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

type WorkflowDeleteImpact struct {
	WorkflowID                     workflow.WorkflowID
	Version                        int64
	ProjectCount                   int64
	LinkCount                      int64
	DefaultReplacementProjectCount int64
	TaskCount                      int64
	ActiveRunCount                 int64
	RunnableRunCount               int64
	BlockedTaskCount               int64
}

type WorkflowDeleteRequest struct {
	WorkflowID           workflow.WorkflowID
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
}

type WorkflowDeleteBlocker struct {
	Code    string
	Message string
	Count   int64
}

func (s *Store) PreviewWorkflowDelete(ctx context.Context, workflowID workflow.WorkflowID) (WorkflowDeleteImpact, error) {
	if strings.TrimSpace(string(workflowID)) == "" {
		return WorkflowDeleteImpact{}, errors.New("workflow id is required")
	}
	row, err := s.queries.GetWorkflowDeleteImpact(ctx, string(workflowID))
	if err != nil {
		return WorkflowDeleteImpact{}, err
	}
	return workflowDeleteImpactFromRow(row), nil
}

func (s *Store) DeleteWorkflow(ctx context.Context, req WorkflowDeleteRequest) (WorkflowDeleteResult, error) {
	if strings.TrimSpace(string(req.WorkflowID)) == "" {
		return WorkflowDeleteResult{}, errors.New("workflow id is required")
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
	current, err := q.GetWorkflowDeleteImpact(ctx, string(req.WorkflowID))
	if err != nil {
		return WorkflowDeleteResult{}, err
	}
	impact = workflowDeleteImpactFromRow(current)
	if blockers := workflowDeleteBlockers(req, impact); len(blockers) > 0 {
		return WorkflowDeleteResult{Impact: impact, Blockers: blockers}, nil
	}

	now := s.now().UnixMilli()
	if _, err := q.DeleteWorkflowTasksByWorkflowID(ctx, string(req.WorkflowID)); err != nil {
		return WorkflowDeleteResult{}, fmt.Errorf("delete workflow tasks: %w", err)
	}
	if _, err := q.ClearDeletedWorkflowDefaultProjectLinks(ctx, sqlitegen.ClearDeletedWorkflowDefaultProjectLinksParams{UpdatedAtUnixMs: now, WorkflowID: string(req.WorkflowID)}); err != nil {
		return WorkflowDeleteResult{}, fmt.Errorf("clear workflow default links: %w", err)
	}
	if _, err := q.DeleteProjectWorkflowLinksByWorkflowID(ctx, string(req.WorkflowID)); err != nil {
		return WorkflowDeleteResult{}, fmt.Errorf("delete workflow project links: %w", err)
	}
	deletedCount, err := q.DeleteWorkflowByID(ctx, string(req.WorkflowID))
	if err != nil {
		return WorkflowDeleteResult{}, fmt.Errorf("delete workflow: %w", err)
	}
	if deletedCount != 1 {
		return WorkflowDeleteResult{}, fmt.Errorf("workflow %q was not deleted", req.WorkflowID)
	}
	if err := tx.Commit(); err != nil {
		return WorkflowDeleteResult{}, err
	}
	return WorkflowDeleteResult{Deleted: true, Impact: impact}, nil
}

func workflowDeleteImpactFromRow(row sqlitegen.GetWorkflowDeleteImpactRow) WorkflowDeleteImpact {
	return WorkflowDeleteImpact{
		WorkflowID:                     workflow.WorkflowID(row.WorkflowID),
		Version:                        row.Version,
		ProjectCount:                   row.ProjectCount,
		LinkCount:                      row.LinkCount,
		DefaultReplacementProjectCount: row.DefaultReplacementProjectCount,
		TaskCount:                      row.TaskCount,
		ActiveRunCount:                 row.ActiveRunCount,
		RunnableRunCount:               row.RunnableRunCount,
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
	if impact.ActiveRunCount > 0 {
		blockers = append(blockers, WorkflowDeleteBlocker{Code: "active_runs", Message: "Workflow has active runs. Interrupt or finish affected tasks before deleting the workflow.", Count: impact.ActiveRunCount})
	}
	if impact.RunnableRunCount > 0 {
		blockers = append(blockers, WorkflowDeleteBlocker{Code: "runnable_runs", Message: "Workflow has runnable runs. Cancel or finish affected tasks before deleting the workflow.", Count: impact.RunnableRunCount})
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
