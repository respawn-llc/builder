package workflowstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/shared/serverapi"
)

// ErrProjectDeletePreparationInvalidated is returned when the Project's Session
// set changes after Project Delete preparation but before the authoritative
// write transaction can commit.
var ErrProjectDeletePreparationInvalidated = errors.New("project delete preparation was invalidated")

type ProjectDeleteRequest struct {
	ProjectID          string
	ExpectedSessionIDs []string
}

func (s *Store) DeleteProject(ctx context.Context, req ProjectDeleteRequest) (_ []serverapi.ProjectDeleteBlocker, metadataOperationErr error) {
	if s == nil || s.metadata == nil || s.queries == nil {
		return nil, errors.New("workflow store is required")
	}
	projectID := strings.TrimSpace(req.ProjectID)

	counts, err := s.queries.GetProjectDeleteBlockerCounts(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("count project delete blockers: %w", err)
	}
	if blockers := projectDeleteBlockersFromCounts(counts); len(blockers) > 0 {
		return blockers, nil
	}

	tx, err := s.metadata.BeginTransaction(ctx, "DeleteProject", nil)
	if err != nil {
		return nil, fmt.Errorf("begin project delete tx: %w", err)
	}
	defer tx.Settle(ctx, &metadataOperationErr)
	q := tx.Queries()
	locked, err := q.AcquireProjectDeleteWriteLock(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("lock project delete: %w", err)
	}
	if locked == 0 {
		return nil, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
	}

	counts, err = q.GetProjectDeleteBlockerCounts(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("count project delete blockers: %w", err)
	}
	blockers := projectDeleteBlockersFromCounts(counts)
	if len(blockers) > 0 {
		return blockers, nil
	}
	commitSessionIDs, err := q.ListProjectSessionIDs(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project sessions for commit: %w", err)
	}
	if !metadata.StringSetsEqual(req.ExpectedSessionIDs, commitSessionIDs) {
		return nil, ErrProjectDeletePreparationInvalidated
	}
	if _, err := q.DeleteProjectTaskPendingApprovals(ctx, projectID); err != nil {
		return nil, fmt.Errorf("delete project task pending approvals: %w", err)
	}
	if err := q.DeleteProjectTasks(ctx, projectID); err != nil {
		return nil, fmt.Errorf("delete project tasks: %w", err)
	}
	rows, err := q.DeleteProject(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("delete project: %w", err)
	}
	if rows == 0 {
		return nil, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit project delete tx: %w", err)
	}
	return nil, nil
}

func projectDeleteBlockersFromCounts(nonTerminalTasks int64) []serverapi.ProjectDeleteBlocker {
	if nonTerminalTasks <= 0 {
		return nil
	}
	return []serverapi.ProjectDeleteBlocker{{
		Code:    "non_terminal_tasks",
		Message: "Project has active or non-terminal tasks.",
		Count:   int(nonTerminalTasks),
	}}
}
