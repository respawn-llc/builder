package workflowstore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/server/workflow"
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

type preparedProjectDeletion struct {
	*preparedSQLLifecycleMutation
	taskIDs               []workflow.TaskID
	artifacts             ProjectDeleteArtifacts
	releaseRuntimeBlocker func()
}

func (p *preparedProjectDeletion) commit() error {
	if err := p.preparedSQLLifecycleMutation.commit(); err != nil {
		return restoreProjectDeleteArtifacts(
			p.artifacts,
			fmt.Errorf("commit project delete tx: %w", err),
		)
	}
	return nil
}

func (p *preparedProjectDeletion) rollback() error {
	err := p.preparedSQLLifecycleMutation.rollback()
	if err != nil {
		err = fmt.Errorf("rollback project delete tx: %w", err)
	}
	return restoreProjectDeleteArtifacts(p.artifacts, err)
}

func (p *preparedProjectDeletion) finalize() error {
	if err := p.artifacts.Finalize(); err != nil {
		return fmt.Errorf("finalize staged project session artifacts: %w", err)
	}
	return nil
}

func (s *Store) prepareProjectDeletion(
	ctx context.Context,
	req ProjectDeleteRequest,
) ([]serverapi.ProjectDeleteBlocker, *preparedProjectDeletion, error) {
	if s == nil || s.db == nil || s.queries == nil {
		return nil, nil, errors.New("workflow store is required")
	}
	if req.Artifacts == nil {
		return nil, nil, errors.New("project delete artifacts are required")
	}
	projectID := strings.TrimSpace(req.ProjectID)

	if _, err := req.Artifacts.Recover(ProjectDeleteArtifactRecoveryProjectPresent); err != nil {
		return nil, nil, fmt.Errorf("recover project session artifacts: %w", err)
	}
	preflightBlockers := append([]serverapi.ProjectDeleteBlocker(nil), req.PreflightBlockers...)
	counts, err := s.queries.GetProjectDeleteBlockerCounts(ctx, projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("count project delete blockers: %w", err)
	}
	preflightBlockers = append(preflightBlockers, projectDeleteBlockersFromCounts(counts)...)
	if len(preflightBlockers) > 0 {
		return preflightBlockers, nil, nil
	}

	preparedSessionIDs, err := s.queries.ListProjectSessionIDs(ctx, projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("list project sessions for preparation: %w", err)
	}
	releaseRuntimeBlocker := func() {}
	if req.RuntimeBlocker != nil {
		runtimeBlockers, release, err := req.RuntimeBlocker(ctx, preparedSessionIDs)
		if release != nil {
			releaseRuntimeBlocker = release
		}
		if err != nil {
			releaseRuntimeBlocker()
			return nil, nil, err
		}
		if len(runtimeBlockers) > 0 {
			releaseRuntimeBlocker()
			return runtimeBlockers, nil, nil
		}
	}
	releaseOnReturn := true
	defer func() {
		if releaseOnReturn {
			releaseRuntimeBlocker()
		}
	}()

	artifacts, err := s.queries.ListProjectSessionArtifacts(ctx, projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("list project session artifacts: %w", err)
	}
	for _, artifact := range artifacts {
		if err := req.Artifacts.Validate(ProjectSessionArtifact{
			SessionID:       artifact.ID,
			ArtifactRelpath: artifact.ArtifactRelpath,
		}); err != nil {
			return nil, nil, fmt.Errorf("validate project session artifact %q: %w", artifact.ID, err)
		}
	}
	if err := req.Artifacts.Stage(); err != nil {
		return nil, nil, fmt.Errorf("stage project session artifacts: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, restoreProjectDeleteArtifacts(req.Artifacts, fmt.Errorf("begin project delete tx: %w", err))
	}
	prepared := &preparedProjectDeletion{
		preparedSQLLifecycleMutation: newPreparedSQLLifecycleMutation(tx),
		artifacts:                    req.Artifacts,
		releaseRuntimeBlocker:        releaseRuntimeBlocker,
	}
	rollbackAndRestore := func(operation error) error {
		return errors.Join(operation, prepared.rollback())
	}
	q := s.queries.WithTx(tx)
	locked, err := q.AcquireProjectDeleteWriteLock(ctx, projectID)
	if err != nil {
		return nil, nil, rollbackAndRestore(fmt.Errorf("lock project delete: %w", err))
	}
	if locked == 0 {
		if err := prepared.preparedSQLLifecycleMutation.rollback(); err != nil {
			return nil, nil, restoreProjectDeleteArtifacts(req.Artifacts, fmt.Errorf("rollback missing project delete tx: %w", err))
		}
		recovered, err := req.Artifacts.Recover(ProjectDeleteArtifactRecoveryProjectAbsent)
		if err != nil {
			return nil, nil, fmt.Errorf("recover deleted project session artifacts: %w", err)
		}
		if recovered {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
	}

	counts, err = q.GetProjectDeleteBlockerCounts(ctx, projectID)
	if err != nil {
		return nil, nil, rollbackAndRestore(fmt.Errorf("count project delete blockers: %w", err))
	}
	blockers := projectDeleteBlockersFromCounts(counts)
	if len(blockers) > 0 {
		if err := rollbackAndRestore(nil); err != nil {
			return nil, nil, err
		}
		return blockers, nil, nil
	}
	commitSessionIDs, err := q.ListProjectSessionIDs(ctx, projectID)
	if err != nil {
		return nil, nil, rollbackAndRestore(fmt.Errorf("list project sessions for commit: %w", err))
	}
	if !metadata.SessionIDSetsEqual(preparedSessionIDs, commitSessionIDs) {
		return nil, nil, rollbackAndRestore(ErrProjectDeletePreparationInvalidated)
	}
	rawTaskIDs, err := q.ListProjectTaskIDs(ctx, projectID)
	if err != nil {
		return nil, nil, rollbackAndRestore(fmt.Errorf("list project Tasks for deletion: %w", err))
	}
	prepared.taskIDs = make([]workflow.TaskID, 0, len(rawTaskIDs))
	for _, rawTaskID := range rawTaskIDs {
		taskID := workflow.TaskID(strings.TrimSpace(rawTaskID))
		if taskID == "" {
			return nil, nil, rollbackAndRestore(fmt.Errorf("Project %q has a blank Task id", projectID))
		}
		prepared.taskIDs = append(prepared.taskIDs, taskID)
	}
	if _, err := q.DeleteProjectTaskPendingApprovals(ctx, projectID); err != nil {
		return nil, nil, rollbackAndRestore(fmt.Errorf("delete project task pending approvals: %w", err))
	}
	if err := q.DeleteProjectTasks(ctx, projectID); err != nil {
		return nil, nil, rollbackAndRestore(fmt.Errorf("delete project tasks: %w", err))
	}
	rows, err := q.DeleteProject(ctx, projectID)
	if err != nil {
		return nil, nil, rollbackAndRestore(fmt.Errorf("delete project: %w", err))
	}
	if rows == 0 {
		return nil, nil, rollbackAndRestore(fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID))
	}
	releaseOnReturn = false
	return nil, prepared, nil
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
