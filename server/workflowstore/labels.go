package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow/label"
	"core/shared/serverapi"
)

type ProjectLabelRecord struct {
	ID        label.ID
	ProjectID string
	Name      label.Name
}

func (s *Store) CreateProjectLabel(ctx context.Context, projectID string, rawName string) (ProjectLabelRecord, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	name, err := label.PrepareName(rawName)
	if err != nil {
		return ProjectLabelRecord{}, err
	}
	id := label.NewID()
	now := s.now().UnixMilli()
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) (ProjectLabelRecord, error) {
		row, err := q.InsertProjectLabel(ctx, sqlitegen.InsertProjectLabelParams{
			ID:              id.String(),
			ProjectID:       trimmedProjectID,
			Name:            name.String(),
			CreatedAtUnixMs: now,
			UpdatedAtUnixMs: now,
			CatalogLimit:    label.MaxProjectLabels,
		})
		if err != nil {
			if metadata.IsSQLiteUniqueConstraint(err) {
				return ProjectLabelRecord{}, ProjectLabelNameConflictError{
					ProjectID: trimmedProjectID,
					Name:      name.String(),
				}
			}
			if errors.Is(err, sql.ErrNoRows) {
				if err := requireProjectForLabels(ctx, q, trimmedProjectID); err != nil {
					return ProjectLabelRecord{}, err
				}
				return ProjectLabelRecord{}, ProjectLabelLimitError{
					ProjectID: trimmedProjectID,
					Limit:     label.MaxProjectLabels,
				}
			}
			return ProjectLabelRecord{}, err
		}
		return projectLabelRecord(row.ID, row.ProjectID, row.Name)
	})
}

func (s *Store) ListProjectLabels(ctx context.Context, projectID string) ([]ProjectLabelRecord, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) ([]ProjectLabelRecord, error) {
		if err := requireProjectForLabels(ctx, q, trimmedProjectID); err != nil {
			return nil, err
		}
		rows, err := q.ListProjectLabels(ctx, trimmedProjectID)
		if err != nil {
			return nil, err
		}
		if len(rows) > label.MaxProjectLabels {
			return nil, fmt.Errorf(
				"project %q label catalog has %d rows, exceeding invariant limit %d",
				trimmedProjectID,
				len(rows),
				label.MaxProjectLabels,
			)
		}
		records := make([]ProjectLabelRecord, 0, len(rows))
		for _, row := range rows {
			record, err := projectLabelRecord(row.ID, row.ProjectID, row.Name)
			if err != nil {
				return nil, err
			}
			records = append(records, record)
		}
		return records, nil
	})
}

func (s *Store) RenameProjectLabel(ctx context.Context, projectID string, id label.ID, rawName string) (ProjectLabelRecord, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	name, err := label.PrepareName(rawName)
	if err != nil {
		return ProjectLabelRecord{}, err
	}
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) (ProjectLabelRecord, error) {
		row, err := q.RenameProjectLabel(ctx, sqlitegen.RenameProjectLabelParams{
			Name:            name.String(),
			UpdatedAtUnixMs: s.now().UnixMilli(),
			ID:              id.String(),
			ProjectID:       trimmedProjectID,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if err := requireProjectForLabels(ctx, q, trimmedProjectID); err != nil {
					return ProjectLabelRecord{}, err
				}
				return ProjectLabelRecord{}, ProjectLabelNotFoundError{
					ProjectID: trimmedProjectID,
					LabelID:   id.String(),
				}
			}
			if metadata.IsSQLiteUniqueConstraint(err) {
				return ProjectLabelRecord{}, ProjectLabelNameConflictError{
					ProjectID: trimmedProjectID,
					Name:      name.String(),
				}
			}
			return ProjectLabelRecord{}, err
		}
		return projectLabelRecord(row.ID, row.ProjectID, row.Name)
	})
}

func (s *Store) DeleteProjectLabel(ctx context.Context, projectID string, id label.ID) (ProjectLabelRecord, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) (ProjectLabelRecord, error) {
		row, err := q.DeleteProjectLabel(ctx, sqlitegen.DeleteProjectLabelParams{
			ID:        id.String(),
			ProjectID: trimmedProjectID,
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if err := requireProjectForLabels(ctx, q, trimmedProjectID); err != nil {
					return ProjectLabelRecord{}, err
				}
				return ProjectLabelRecord{}, ProjectLabelNotFoundError{
					ProjectID: trimmedProjectID,
					LabelID:   id.String(),
				}
			}
			return ProjectLabelRecord{}, err
		}
		return projectLabelRecord(row.ID, row.ProjectID, row.Name)
	})
}

func projectLabelRecord(id string, projectID string, name string) (ProjectLabelRecord, error) {
	parsedID, err := label.ParseID(id)
	if err != nil {
		return ProjectLabelRecord{}, err
	}
	preparedName, err := label.PrepareName(name)
	if err != nil {
		return ProjectLabelRecord{}, err
	}
	if strings.TrimSpace(projectID) == "" {
		return ProjectLabelRecord{}, errors.New("persisted project label project id is required")
	}
	return ProjectLabelRecord{
		ID:        parsedID,
		ProjectID: projectID,
		Name:      preparedName,
	}, nil
}

func requireProjectForLabels(ctx context.Context, q *sqlitegen.Queries, projectID string) error {
	if projectID == "" {
		return errors.New("project id is required")
	}
	if _, err := q.GetProjectDisplayName(ctx, projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
		}
		return err
	}
	return nil
}

func withProjectLabelTransaction[T any](
	ctx context.Context,
	s *Store,
	operation func(*sqlitegen.Queries) (T, error),
) (T, error) {
	var zero T
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := operation(s.queries.WithTx(tx))
	if err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return result, nil
}
