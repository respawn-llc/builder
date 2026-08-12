package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/shared/serverapi"
)

type ProjectEditMetadata struct {
	ProjectID   string
	DisplayName string
	ProjectKey  string
}

func (s *Store) GetProjectEditMetadata(
	ctx context.Context,
	projectID string,
) (ProjectEditMetadata, error) {
	if s == nil || s.queries == nil {
		return ProjectEditMetadata{}, errors.New("metadata store is required")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ProjectEditMetadata{}, errors.New("project id is required")
	}
	row, err := s.queries.GetProjectEditMetadata(ctx, projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectEditMetadata{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
		}
		return ProjectEditMetadata{}, fmt.Errorf("get Project edit metadata: %w", err)
	}
	return ProjectEditMetadata{
		ProjectID:   row.ID,
		DisplayName: row.DisplayName,
		ProjectKey:  row.ProjectKey,
	}, nil
}

func (s *Store) GetProjectWorkspaceCatalogRow(
	ctx context.Context,
	projectID string,
	selector serverapi.ProjectWorkspaceSelector,
) (ProjectWorkspaceCatalogRow, error) {
	binding, err := s.ResolveProjectWorkspaceSelector(ctx, projectID, selector)
	if err != nil {
		return ProjectWorkspaceCatalogRow{}, err
	}
	defaultWorkspaceID, err := s.queries.GetProjectPrimaryWorkspaceID(ctx, binding.ProjectID)
	if err != nil {
		return ProjectWorkspaceCatalogRow{}, fmt.Errorf("get Project default Workspace: %w", err)
	}
	return ProjectWorkspaceCatalogRow{
		WorkspaceID:   binding.WorkspaceID,
		DisplayName:   displayNameForPath(binding.CanonicalRoot),
		CanonicalRoot: binding.CanonicalRoot,
		IsDefault:     binding.WorkspaceID == defaultWorkspaceID,
	}, nil
}
