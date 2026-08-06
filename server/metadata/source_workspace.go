package metadata

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
)

// ResolveProjectSourceWorkspaceID resolves the Project's authoritative source
// Workspace. The primary Workspace is checked directly so it remains
// resolvable when it falls outside the bounded Workspace collection.
func ResolveProjectSourceWorkspaceID(ctx context.Context, q *sqlitegen.Queries, projectID string) (string, error) {
	if q == nil {
		return "", errors.New("metadata queries are required")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", errors.New("project id is required")
	}

	primaryWorkspaceID, err := q.GetProjectPrimaryWorkspaceID(ctx, projectID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil && strings.TrimSpace(primaryWorkspaceID) != "" {
		primaryWorkspace, workspaceErr := q.GetWorkspaceByID(ctx, strings.TrimSpace(primaryWorkspaceID))
		if workspaceErr == nil && strings.TrimSpace(primaryWorkspace.ProjectID) == projectID {
			return strings.TrimSpace(primaryWorkspace.ID), nil
		}
		if workspaceErr != nil && !errors.Is(workspaceErr, sql.ErrNoRows) {
			return "", workspaceErr
		}
	}

	workspaces, err := q.ListProjectWorkspaces(ctx, sqlitegen.ListProjectWorkspacesParams{
		ProjectID:                projectID,
		WorkspaceCollectionLimit: int64(ProjectWorkspaceCollectionLimit),
	})
	if err != nil {
		return "", err
	}
	for _, workspace := range workspaces {
		if workspace.IsPrimary != 0 && strings.TrimSpace(workspace.ID) != "" {
			return strings.TrimSpace(workspace.ID), nil
		}
	}
	for _, workspace := range workspaces {
		if strings.TrimSpace(workspace.ID) != "" {
			return strings.TrimSpace(workspace.ID), nil
		}
	}
	return "", fmt.Errorf("project %q has no source workspace", projectID)
}
