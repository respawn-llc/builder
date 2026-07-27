package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/shared/serverapi"
)

type ProjectSessionArtifact struct {
	SessionID       string
	ArtifactRelpath string
}

type ProjectDeleteRuntimeBlocker func(
	context.Context,
	[]string,
) ([]serverapi.ProjectDeleteBlocker, func(), error)

// ProjectDeleteArtifactRecovery identifies the durable project state used to
// recover a previous staged project-session deletion.
type ProjectDeleteArtifactRecovery int

const (
	ProjectDeleteArtifactRecoveryProjectPresent ProjectDeleteArtifactRecovery = iota + 1
	ProjectDeleteArtifactRecoveryProjectAbsent
)

// ProjectDeleteArtifacts owns the project sessions tree while a Project is
// deleted. Stage must atomically make the complete tree unavailable; Restore
// reverses that transition before a failed database deletion returns; Finalize
// irreversibly removes the staged tree after the database commit.
type ProjectDeleteArtifacts interface {
	Recover(ProjectDeleteArtifactRecovery) (recovered bool, err error)
	Validate(ProjectSessionArtifact) error
	Stage() error
	Restore() error
	Finalize() error
}

type ProjectDeleteRequest struct {
	ProjectID         string
	PreflightBlockers []serverapi.ProjectDeleteBlocker
	RuntimeBlocker    ProjectDeleteRuntimeBlocker
	Artifacts         ProjectDeleteArtifacts
}

func (s *Store) DeleteProject(ctx context.Context, req ProjectDeleteRequest) ([]serverapi.ProjectDeleteBlocker, error) {
	if s == nil || s.db == nil || s.queries == nil {
		return nil, errors.New("workflow store is required")
	}
	if req.Artifacts == nil {
		return nil, errors.New("project delete artifacts are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin project delete tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	rollback := func(operation error, staged bool) error {
		errs := []error{operation}
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			errs = append(errs, fmt.Errorf("rollback project delete tx: %w", rollbackErr))
		}
		if staged {
			if restoreErr := req.Artifacts.Restore(); restoreErr != nil {
				errs = append(errs, fmt.Errorf("restore staged project session artifacts: %w", restoreErr))
			}
		}
		return errors.Join(errs...)
	}
	projectID := strings.TrimSpace(req.ProjectID)
	q := s.queries.WithTx(tx)
	locked, err := q.AcquireProjectDeleteWriteLock(ctx, projectID)
	if err != nil {
		return nil, rollback(fmt.Errorf("lock project delete: %w", err), false)
	}
	if locked == 0 {
		recovered, err := req.Artifacts.Recover(ProjectDeleteArtifactRecoveryProjectAbsent)
		if err != nil {
			return nil, rollback(fmt.Errorf("recover deleted project session artifacts: %w", err), false)
		}
		if recovered {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				return nil, fmt.Errorf("rollback recovered project delete tx: %w", err)
			}
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
	}
	if _, err := req.Artifacts.Recover(ProjectDeleteArtifactRecoveryProjectPresent); err != nil {
		return nil, rollback(fmt.Errorf("recover project session artifacts: %w", err), false)
	}
	preflightBlockers := append([]serverapi.ProjectDeleteBlocker(nil), req.PreflightBlockers...)
	releaseRuntimeBlocker := func() {}
	if req.RuntimeBlocker != nil {
		sessionIDs, err := q.ListProjectSessionIDs(ctx, projectID)
		if err != nil {
			return nil, rollback(fmt.Errorf("list project sessions for runtime blockers: %w", err), false)
		}
		runtimeBlockers, release, err := req.RuntimeBlocker(ctx, sessionIDs)
		if release != nil {
			releaseRuntimeBlocker = release
			defer releaseRuntimeBlocker()
		}
		if err != nil {
			return nil, rollback(err, false)
		}
		preflightBlockers = append(preflightBlockers, runtimeBlockers...)
	}
	counts, err := q.GetProjectDeleteBlockerCounts(ctx, projectID)
	if err != nil {
		return nil, rollback(fmt.Errorf("count project delete blockers: %w", err), false)
	}
	blockers := append(preflightBlockers, projectDeleteBlockersFromCounts(counts)...)
	if len(blockers) > 0 {
		return blockers, nil
	}
	artifacts, err := q.ListProjectSessionArtifacts(ctx, projectID)
	if err != nil {
		return nil, rollback(fmt.Errorf("list project session artifacts: %w", err), false)
	}
	for _, artifact := range artifacts {
		if err := req.Artifacts.Validate(ProjectSessionArtifact{
			SessionID:       artifact.ID,
			ArtifactRelpath: artifact.ArtifactRelpath,
		}); err != nil {
			return nil, rollback(fmt.Errorf("validate project session artifact %q: %w", artifact.ID, err), false)
		}
	}
	if err := req.Artifacts.Stage(); err != nil {
		return nil, rollback(fmt.Errorf("stage project session artifacts: %w", err), false)
	}
	if _, err := q.DeleteProjectTaskPendingApprovals(ctx, projectID); err != nil {
		return nil, rollback(fmt.Errorf("delete project task pending approvals: %w", err), true)
	}
	if err := q.DeleteProjectTasks(ctx, projectID); err != nil {
		return nil, rollback(fmt.Errorf("delete project tasks: %w", err), true)
	}
	rows, err := q.DeleteProject(ctx, projectID)
	if err != nil {
		return nil, rollback(fmt.Errorf("delete project: %w", err), true)
	}
	if rows == 0 {
		return nil, rollback(fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID), true)
	}
	if err := tx.Commit(); err != nil {
		return nil, rollback(fmt.Errorf("commit project delete tx: %w", err), true)
	}
	committed = true
	if err := req.Artifacts.Finalize(); err != nil {
		return nil, fmt.Errorf("finalize staged project session artifacts: %w", err)
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
