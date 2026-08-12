package metadata

import (
	"context"
	"fmt"

	"core/shared/serverapi"
)

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
