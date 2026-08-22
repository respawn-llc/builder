package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/shared/serverapi"
)

const MaxProjectWorkspacePageSize = 100

type ProjectWorkspaceCatalogRow struct {
	WorkspaceID   string
	DisplayName   string
	CanonicalRoot string
	IsDefault     bool
}

type ProjectWorkspaceCatalogPage struct {
	Offset     int
	Workspaces []ProjectWorkspaceCatalogRow
	NextOffset *int
}

func (s *Store) ListProjectWorkspaceCatalogPage(
	ctx context.Context,
	projectID string,
	offset int,
	limit int,
) (ProjectWorkspaceCatalogPage, error) {
	if s == nil || s.queries == nil {
		return ProjectWorkspaceCatalogPage{}, errors.New("metadata store is required")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return ProjectWorkspaceCatalogPage{}, errors.New("project id is required")
	}
	if offset < 0 {
		return ProjectWorkspaceCatalogPage{}, errors.New("offset must be non-negative")
	}
	if limit < 1 || limit > MaxProjectWorkspacePageSize {
		return ProjectWorkspaceCatalogPage{}, fmt.Errorf(
			"limit must be between 1 and %d",
			MaxProjectWorkspacePageSize,
		)
	}
	rows, err := s.queries.ListProjectWorkspaceCatalogPage(ctx, sqlitegen.ListProjectWorkspaceCatalogPageParams{
		ProjectID:  projectID,
		OffsetRows: int64(offset),
		LimitRows:  int64(limit + 1),
	})
	if err != nil {
		return ProjectWorkspaceCatalogPage{}, fmt.Errorf("list project workspace catalog page: %w", err)
	}
	if len(rows) == 0 {
		return ProjectWorkspaceCatalogPage{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
	}
	if !rows[0].WorkspaceID.Valid {
		return ProjectWorkspaceCatalogPage{
			Offset:     offset,
			Workspaces: []ProjectWorkspaceCatalogRow{},
		}, nil
	}
	nextOffset := (*int)(nil)
	if len(rows) > limit {
		rows = rows[:limit]
		next := offset + limit
		nextOffset = &next
	}
	page := ProjectWorkspaceCatalogPage{
		Offset:     offset,
		Workspaces: make([]ProjectWorkspaceCatalogRow, 0, len(rows)),
		NextOffset: nextOffset,
	}
	for _, row := range rows {
		if !row.WorkspaceID.Valid || !row.CanonicalRoot.Valid || !row.IsDefault.Valid {
			return ProjectWorkspaceCatalogPage{}, fmt.Errorf(
				"list project workspace catalog page returned an incomplete Workspace row for Project %q",
				projectID,
			)
		}
		page.Workspaces = append(page.Workspaces, ProjectWorkspaceCatalogRow{
			WorkspaceID:   row.WorkspaceID.String,
			DisplayName:   displayNameForPath(row.CanonicalRoot.String),
			CanonicalRoot: row.CanonicalRoot.String,
			IsDefault:     row.IsDefault.Int64 != 0,
		})
	}
	return page, nil
}
