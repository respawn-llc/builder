package workflowstore

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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

type ProjectDeleteRequest struct {
	ProjectID         string
	PreflightBlockers []serverapi.ProjectDeleteBlocker
	RuntimeBlocker    ProjectDeleteRuntimeBlocker
	DeleteArtifact    func(ProjectSessionArtifact, bool) error
}

func (s *Store) DeleteProject(ctx context.Context, req ProjectDeleteRequest) ([]serverapi.ProjectDeleteBlocker, error) {
	if s == nil || s.db == nil || s.queries == nil {
		return nil, errors.New("workflow store is required")
	}
	if req.DeleteArtifact == nil {
		return nil, errors.New("session artifact delete callback is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin project delete tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	projectID := strings.TrimSpace(req.ProjectID)
	q := s.queries.WithTx(tx)
	locked, err := q.AcquireProjectDeleteWriteLock(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("lock project delete: %w", err)
	}
	if locked == 0 {
		return nil, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
	}
	preflightBlockers := append([]serverapi.ProjectDeleteBlocker(nil), req.PreflightBlockers...)
	releaseRuntimeBlocker := func() {}
	if req.RuntimeBlocker != nil {
		sessionIDs, err := q.ListProjectSessionIDs(ctx, projectID)
		if err != nil {
			return nil, fmt.Errorf("list project sessions for runtime blockers: %w", err)
		}
		runtimeBlockers, release, err := req.RuntimeBlocker(ctx, sessionIDs)
		if release != nil {
			releaseRuntimeBlocker = release
			defer releaseRuntimeBlocker()
		}
		if err != nil {
			return nil, err
		}
		preflightBlockers = append(preflightBlockers, runtimeBlockers...)
	}
	counts, err := q.GetProjectDeleteBlockerCounts(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("count project delete blockers: %w", err)
	}
	blockers := append(preflightBlockers, projectDeleteBlockersFromCounts(counts)...)
	if len(blockers) > 0 {
		return blockers, nil
	}
	artifacts, err := q.ListProjectSessionArtifacts(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list project session artifacts: %w", err)
	}
	for _, artifact := range artifacts {
		if err := req.DeleteArtifact(ProjectSessionArtifact{
			SessionID:       artifact.ID,
			ArtifactRelpath: artifact.ArtifactRelpath,
		}, false); err != nil {
			return nil, err
		}
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
	_ = req.DeleteArtifact(ProjectSessionArtifact{
		ArtifactRelpath: filepath.ToSlash(filepath.Join("projects", projectID, "sessions")),
	}, true)
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
