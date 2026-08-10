package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/shared/serverapi"
)

type ProjectDeleteRuntimeBlocker func(
	context.Context,
	[]string,
) ([]serverapi.ProjectDeleteBlocker, func(), error)

// ErrProjectDeletePreparationInvalidated is returned when the Project's Session
// set changes after Project Delete preparation but before the authoritative
// write transaction can commit.
var ErrProjectDeletePreparationInvalidated = errors.New("project delete preparation was invalidated")

type ProjectDeleteRequest struct {
	ProjectID         string
	PreflightBlockers []serverapi.ProjectDeleteBlocker
	RuntimeBlocker    ProjectDeleteRuntimeBlocker
}

func (s *Store) DeleteProject(ctx context.Context, req ProjectDeleteRequest) ([]serverapi.ProjectDeleteBlocker, error) {
	if s == nil || s.db == nil || s.queries == nil {
		return nil, errors.New("workflow store is required")
	}
	projectID := strings.TrimSpace(req.ProjectID)

	preflightBlockers := append([]serverapi.ProjectDeleteBlocker(nil), req.PreflightBlockers...)
	counts, err := s.queries.GetProjectDeleteBlockerCounts(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("count project delete blockers: %w", err)
	}
	preflightBlockers = append(preflightBlockers, projectDeleteBlockersFromCounts(counts)...)
	if len(preflightBlockers) > 0 {
		return preflightBlockers, nil
	}

	preparedSessionIDs, err := s.queries.ListProjectSessionIDs(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project sessions for preparation: %w", err)
	}
	releaseRuntimeBlocker := func() {}
	if req.RuntimeBlocker != nil {
		runtimeBlockers, release, err := req.RuntimeBlocker(ctx, preparedSessionIDs)
		if release != nil {
			releaseRuntimeBlocker = release
			defer releaseRuntimeBlocker()
		}
		if err != nil {
			return nil, err
		}
		if len(runtimeBlockers) > 0 {
			return runtimeBlockers, nil
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin project delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rollback := func(operation error) error {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(operation, fmt.Errorf("rollback project delete tx: %w", rollbackErr))
		}
		return operation
	}
	q := s.queries.WithTx(tx)
	locked, err := q.AcquireProjectDeleteWriteLock(ctx, projectID)
	if err != nil {
		return nil, rollback(fmt.Errorf("lock project delete: %w", err))
	}
	if locked == 0 {
		return nil, rollback(fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID))
	}

	counts, err = q.GetProjectDeleteBlockerCounts(ctx, projectID)
	if err != nil {
		return nil, rollback(fmt.Errorf("count project delete blockers: %w", err))
	}
	blockers := projectDeleteBlockersFromCounts(counts)
	if len(blockers) > 0 {
		if err := rollback(nil); err != nil {
			return nil, err
		}
		return blockers, nil
	}
	commitSessionIDs, err := q.ListProjectSessionIDs(ctx, projectID)
	if err != nil {
		return nil, rollback(fmt.Errorf("list project sessions for commit: %w", err))
	}
	if !metadata.SessionIDSetsEqual(preparedSessionIDs, commitSessionIDs) {
		return nil, rollback(ErrProjectDeletePreparationInvalidated)
	}
	if _, err := q.DeleteProjectTaskPendingApprovals(ctx, projectID); err != nil {
		return nil, rollback(fmt.Errorf("delete project task pending approvals: %w", err))
	}
	if err := q.DeleteProjectTasks(ctx, projectID); err != nil {
		return nil, rollback(fmt.Errorf("delete project tasks: %w", err))
	}
	rows, err := q.DeleteProject(ctx, projectID)
	if err != nil {
		return nil, rollback(fmt.Errorf("delete project: %w", err))
	}
	if rows == 0 {
		return nil, rollback(fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID))
	}
	if err := tx.Commit(); err != nil {
		return nil, rollback(fmt.Errorf("commit project delete tx: %w", err))
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
