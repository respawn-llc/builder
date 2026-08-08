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

// ErrProjectDeletePreparationInvalidated is returned when the Project's
// Session set changes after Project Delete preparation has staged artifacts but
// before the authoritative write transaction can commit.
var ErrProjectDeletePreparationInvalidated = errors.New("project delete preparation was invalidated")

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
	projectID := strings.TrimSpace(req.ProjectID)

	if _, err := req.Artifacts.Recover(ProjectDeleteArtifactRecoveryProjectPresent); err != nil {
		return nil, fmt.Errorf("recover project session artifacts: %w", err)
	}
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

	artifacts, err := s.queries.ListProjectSessionArtifacts(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project session artifacts: %w", err)
	}
	for _, artifact := range artifacts {
		if err := req.Artifacts.Validate(ProjectSessionArtifact{
			SessionID:       artifact.ID,
			ArtifactRelpath: artifact.ArtifactRelpath,
		}); err != nil {
			return nil, fmt.Errorf("validate project session artifact %q: %w", artifact.ID, err)
		}
	}
	if err := req.Artifacts.Stage(); err != nil {
		return nil, fmt.Errorf("stage project session artifacts: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, restoreProjectDeleteArtifacts(req.Artifacts, fmt.Errorf("begin project delete tx: %w", err))
	}
	defer func() { _ = tx.Rollback() }()
	rollbackAndRestore := func(operation error) error {
		return rollbackProjectDeleteAndRestore(tx, req.Artifacts, operation)
	}
	q := s.queries.WithTx(tx)
	locked, err := q.AcquireProjectDeleteWriteLock(ctx, projectID)
	if err != nil {
		return nil, rollbackAndRestore(fmt.Errorf("lock project delete: %w", err))
	}
	if locked == 0 {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return nil, restoreProjectDeleteArtifacts(req.Artifacts, fmt.Errorf("rollback missing project delete tx: %w", err))
		}
		recovered, err := req.Artifacts.Recover(ProjectDeleteArtifactRecoveryProjectAbsent)
		if err != nil {
			return nil, fmt.Errorf("recover deleted project session artifacts: %w", err)
		}
		if recovered {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
	}

	counts, err = q.GetProjectDeleteBlockerCounts(ctx, projectID)
	if err != nil {
		return nil, rollbackAndRestore(fmt.Errorf("count project delete blockers: %w", err))
	}
	blockers := projectDeleteBlockersFromCounts(counts)
	if len(blockers) > 0 {
		if err := rollbackAndRestore(nil); err != nil {
			return nil, err
		}
		return blockers, nil
	}
	commitSessionIDs, err := q.ListProjectSessionIDs(ctx, projectID)
	if err != nil {
		return nil, rollbackAndRestore(fmt.Errorf("list project sessions for commit: %w", err))
	}
	if !metadata.SessionIDSetsEqual(preparedSessionIDs, commitSessionIDs) {
		return nil, rollbackAndRestore(ErrProjectDeletePreparationInvalidated)
	}
	if _, err := q.DeleteProjectTaskPendingApprovals(ctx, projectID); err != nil {
		return nil, rollbackAndRestore(fmt.Errorf("delete project task pending approvals: %w", err))
	}
	if err := q.DeleteProjectTasks(ctx, projectID); err != nil {
		return nil, rollbackAndRestore(fmt.Errorf("delete project tasks: %w", err))
	}
	rows, err := q.DeleteProject(ctx, projectID)
	if err != nil {
		return nil, rollbackAndRestore(fmt.Errorf("delete project: %w", err))
	}
	if rows == 0 {
		return nil, rollbackAndRestore(fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID))
	}
	if err := tx.Commit(); err != nil {
		return nil, rollbackAndRestore(fmt.Errorf("commit project delete tx: %w", err))
	}
	if err := req.Artifacts.Finalize(); err != nil {
		return nil, fmt.Errorf("finalize staged project session artifacts: %w", err)
	}
	return nil, nil
}

func rollbackProjectDeleteAndRestore(tx *sql.Tx, artifacts ProjectDeleteArtifacts, operation error) error {
	errs := []error{}
	if operation != nil {
		errs = append(errs, operation)
	}
	if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
		errs = append(errs, fmt.Errorf("rollback project delete tx: %w", rollbackErr))
	}
	if restoreErr := artifacts.Restore(); restoreErr != nil {
		errs = append(errs, fmt.Errorf("restore staged project session artifacts: %w", restoreErr))
	}
	return errors.Join(errs...)
}

func restoreProjectDeleteArtifacts(artifacts ProjectDeleteArtifacts, operation error) error {
	if restoreErr := artifacts.Restore(); restoreErr != nil {
		return errors.Join(operation, fmt.Errorf("restore staged project session artifacts: %w", restoreErr))
	}
	return operation
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
