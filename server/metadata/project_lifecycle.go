package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

var ErrProjectLifecycleConflict = errors.New("project lifecycle conflict")

type ProjectLifecycleState string

const (
	ProjectLifecycleStateActive   ProjectLifecycleState = "active"
	ProjectLifecycleStateDeleting ProjectLifecycleState = "deleting"
)

type ProjectLifecycle struct {
	State      ProjectLifecycleState
	Generation int64
}

// ProjectLifecycleConflictError reports a failed active-generation fence
// without requiring callers to parse a SQLite no-row result.
type ProjectLifecycleConflictError struct {
	ProjectID          string
	ExpectedGeneration int64
}

func (e ProjectLifecycleConflictError) Error() string {
	return fmt.Sprintf("project %q lifecycle generation %d is no longer active", e.ProjectID, e.ExpectedGeneration)
}

func (e ProjectLifecycleConflictError) Is(target error) bool {
	return target == ErrProjectLifecycleConflict
}

func (s *Store) GetProjectLifecycle(ctx context.Context, projectID string) (ProjectLifecycle, error) {
	if s == nil || s.queries == nil {
		return ProjectLifecycle{}, errors.New("metadata store is required")
	}
	trimmedProjectID, err := requiredProjectLifecycleProjectID(projectID)
	if err != nil {
		return ProjectLifecycle{}, err
	}
	row, err := s.queries.GetProjectLifecycle(ctx, trimmedProjectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectLifecycle{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, trimmedProjectID)
		}
		return ProjectLifecycle{}, fmt.Errorf("get project lifecycle: %w", err)
	}
	return projectLifecycleFromStorage(row.LifecycleState, row.LifecycleGeneration)
}

// RecheckProjectActiveLifecycle proves the caller's active lifecycle
// generation is still current at the point the query executes.
func (s *Store) RecheckProjectActiveLifecycle(ctx context.Context, projectID string, expectedGeneration int64) error {
	if s == nil || s.queries == nil {
		return errors.New("metadata store is required")
	}
	trimmedProjectID, err := requiredProjectLifecycleProjectID(projectID)
	if err != nil {
		return err
	}
	if expectedGeneration <= 0 {
		return errors.New("project lifecycle generation must be positive")
	}
	row, err := s.queries.GetActiveProjectLifecycle(ctx, sqlitegen.GetActiveProjectLifecycleParams{
		ProjectID:          trimmedProjectID,
		ExpectedGeneration: expectedGeneration,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectLifecycleConflictError{ProjectID: trimmedProjectID, ExpectedGeneration: expectedGeneration}
		}
		return fmt.Errorf("recheck active project lifecycle: %w", err)
	}
	_, err = projectLifecycleFromStorage(row.LifecycleState, row.LifecycleGeneration)
	return err
}

// TransitionProjectLifecycleToDeleting performs the durable active-to-deleting
// transition under the caller's exact lifecycle-generation fence.
func (s *Store) TransitionProjectLifecycleToDeleting(ctx context.Context, projectID string, expectedGeneration int64) (ProjectLifecycle, error) {
	if s == nil || s.queries == nil {
		return ProjectLifecycle{}, errors.New("metadata store is required")
	}
	trimmedProjectID, err := requiredProjectLifecycleProjectID(projectID)
	if err != nil {
		return ProjectLifecycle{}, err
	}
	if expectedGeneration <= 0 {
		return ProjectLifecycle{}, errors.New("project lifecycle generation must be positive")
	}
	row, err := s.queries.TransitionProjectLifecycleToDeleting(ctx, sqlitegen.TransitionProjectLifecycleToDeletingParams{
		UpdatedAtUnixMs:    time.Now().UTC().UnixMilli(),
		ProjectID:          trimmedProjectID,
		ExpectedGeneration: expectedGeneration,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectLifecycle{}, ProjectLifecycleConflictError{ProjectID: trimmedProjectID, ExpectedGeneration: expectedGeneration}
		}
		return ProjectLifecycle{}, fmt.Errorf("transition project lifecycle to deleting: %w", err)
	}
	return projectLifecycleFromStorage(row.LifecycleState, row.LifecycleGeneration)
}

func requiredProjectLifecycleProjectID(projectID string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", errors.New("project id is required")
	}
	return projectID, nil
}

func projectLifecycleFromStorage(rawState string, generation int64) (ProjectLifecycle, error) {
	if generation <= 0 {
		return ProjectLifecycle{}, fmt.Errorf("invalid project lifecycle generation %d", generation)
	}
	state := ProjectLifecycleState(rawState)
	switch state {
	case ProjectLifecycleStateActive, ProjectLifecycleStateDeleting:
		return ProjectLifecycle{State: state, Generation: generation}, nil
	default:
		return ProjectLifecycle{}, fmt.Errorf("invalid project lifecycle state %q", rawState)
	}
}
