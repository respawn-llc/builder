package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"core/server/metadata"
	"core/server/metadata/sqlitegen"
	"core/server/workflow"
	"core/server/workflow/label"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type ProjectLabelRecord struct {
	ID        label.ID
	ProjectID string
	Name      label.Name
}

type TaskLabelUpdateRequest struct {
	TaskID         workflow.TaskID
	AddLabelIDs    []string
	RemoveLabelIDs []string
}

type TaskLabelScope struct {
	ProjectID  string
	WorkflowID runtimeids.WorkflowID
}

type ProjectLabelReorderOutcome string

const (
	ProjectLabelReorderApplied   ProjectLabelReorderOutcome = "applied"
	ProjectLabelReorderUnchanged ProjectLabelReorderOutcome = "unchanged"
)

type ProjectLabelReorderResult struct {
	Labels  []ProjectLabelRecord
	Outcome ProjectLabelReorderOutcome
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
		if err := acquireProjectLabelWriteLock(ctx, q, trimmedProjectID); err != nil {
			return ProjectLabelRecord{}, err
		}
		labels, err := listProjectLabelCatalog(ctx, q, trimmedProjectID)
		if err != nil {
			return ProjectLabelRecord{}, err
		}
		ordinal := len(labels) + 1
		if ordinal > label.MaxProjectLabels {
			return ProjectLabelRecord{}, ProjectLabelLimitError{
				ProjectID: trimmedProjectID,
				Limit:     label.MaxProjectLabels,
			}
		}
		row, err := q.InsertProjectLabel(ctx, sqlitegen.InsertProjectLabelParams{
			ID:              id.String(),
			ProjectID:       trimmedProjectID,
			Name:            name.String(),
			Ordinal:         int64(ordinal),
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
			return ProjectLabelRecord{}, err
		}
		record, err := projectLabelRecord(row.ID, row.ProjectID, row.Name)
		if err != nil {
			return ProjectLabelRecord{}, err
		}
		if _, err := listProjectLabelCatalog(ctx, q, trimmedProjectID); err != nil {
			return ProjectLabelRecord{}, err
		}
		return record, nil
	})
}

func (s *Store) ListProjectLabels(ctx context.Context, projectID string) ([]ProjectLabelRecord, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) ([]ProjectLabelRecord, error) {
		if err := requireProjectForLabels(ctx, q, trimmedProjectID); err != nil {
			return nil, err
		}
		return listProjectLabelCatalog(ctx, q, trimmedProjectID)
	})
}

func (s *Store) RenameProjectLabel(ctx context.Context, projectID string, id label.ID, rawName string) (ProjectLabelRecord, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	name, err := label.PrepareName(rawName)
	if err != nil {
		return ProjectLabelRecord{}, err
	}
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) (ProjectLabelRecord, error) {
		if err := acquireProjectLabelWriteLock(ctx, q, trimmedProjectID); err != nil {
			return ProjectLabelRecord{}, err
		}
		if _, err := listProjectLabelCatalog(ctx, q, trimmedProjectID); err != nil {
			return ProjectLabelRecord{}, err
		}
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
		record, err := projectLabelRecord(row.ID, row.ProjectID, row.Name)
		if err != nil {
			return ProjectLabelRecord{}, err
		}
		if _, err := listProjectLabelCatalog(ctx, q, trimmedProjectID); err != nil {
			return ProjectLabelRecord{}, err
		}
		return record, nil
	})
}

func (s *Store) DeleteProjectLabel(ctx context.Context, projectID string, id label.ID) (ProjectLabelRecord, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) (ProjectLabelRecord, error) {
		if err := acquireProjectLabelWriteLock(ctx, q, trimmedProjectID); err != nil {
			return ProjectLabelRecord{}, err
		}
		if _, err := listProjectLabelCatalog(ctx, q, trimmedProjectID); err != nil {
			return ProjectLabelRecord{}, err
		}
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
		record, err := projectLabelRecord(row.ID, row.ProjectID, row.Name)
		if err != nil {
			return ProjectLabelRecord{}, err
		}
		if err := compactProjectLabelOrdinals(ctx, q, trimmedProjectID); err != nil {
			return ProjectLabelRecord{}, err
		}
		return record, nil
	})
}

func (s *Store) ReorderProjectLabels(
	ctx context.Context,
	projectID string,
	orderedIDs []label.ID,
) (ProjectLabelReorderResult, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	if len(orderedIDs) > label.MaxProjectLabels {
		return ProjectLabelReorderResult{}, ProjectLabelOrderError{
			ProjectID: trimmedProjectID,
			Reason:    fmt.Sprintf("contains more than %d labels", label.MaxProjectLabels),
		}
	}
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) (ProjectLabelReorderResult, error) {
		if err := acquireProjectLabelWriteLock(ctx, q, trimmedProjectID); err != nil {
			return ProjectLabelReorderResult{}, err
		}
		current, err := listProjectLabelCatalog(ctx, q, trimmedProjectID)
		if err != nil {
			return ProjectLabelReorderResult{}, err
		}
		byID := make(map[string]ProjectLabelRecord, len(current))
		for _, record := range current {
			byID[record.ID.String()] = record
		}
		if len(orderedIDs) != len(current) {
			return ProjectLabelReorderResult{}, ProjectLabelOrderError{
				ProjectID: trimmedProjectID,
				Reason:    "must contain every current label exactly once",
			}
		}
		seen := make(map[string]struct{}, len(orderedIDs))
		unchanged := true
		for index, id := range orderedIDs {
			canonicalID := id.String()
			if _, exists := byID[canonicalID]; !exists {
				return ProjectLabelReorderResult{}, ProjectLabelOrderError{
					ProjectID: trimmedProjectID,
					LabelID:   &canonicalID,
					Reason:    "label is not in the current catalog",
				}
			}
			if _, exists := seen[canonicalID]; exists {
				return ProjectLabelReorderResult{}, ProjectLabelOrderError{
					ProjectID: trimmedProjectID,
					LabelID:   &canonicalID,
					Reason:    "label occurs more than once",
				}
			}
			seen[canonicalID] = struct{}{}
			if current[index].ID != id {
				unchanged = false
			}
		}
		if unchanged {
			return ProjectLabelReorderResult{
				Labels:  current,
				Outcome: ProjectLabelReorderUnchanged,
			}, nil
		}
		if err := rewriteProjectLabelOrdinals(ctx, q, trimmedProjectID, orderedIDs); err != nil {
			return ProjectLabelReorderResult{}, err
		}
		labels, err := listProjectLabelCatalog(ctx, q, trimmedProjectID)
		if err != nil {
			return ProjectLabelReorderResult{}, err
		}
		return ProjectLabelReorderResult{
			Labels:  labels,
			Outcome: ProjectLabelReorderApplied,
		}, nil
	})
}

func acquireProjectLabelWriteLock(ctx context.Context, q *sqlitegen.Queries, projectID string) error {
	if projectID == "" {
		return errors.New("project id is required")
	}
	if _, err := q.AcquireProjectLabelWriteLock(ctx, projectID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
		}
		return err
	}
	return nil
}

func listProjectLabelCatalog(ctx context.Context, q *sqlitegen.Queries, projectID string) ([]ProjectLabelRecord, error) {
	rows, err := q.ListProjectLabels(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if len(rows) > label.MaxProjectLabels {
		return nil, fmt.Errorf(
			"project %q label catalog has %d rows, exceeding invariant limit %d",
			projectID,
			len(rows),
			label.MaxProjectLabels,
		)
	}
	records := make([]ProjectLabelRecord, 0, len(rows))
	for index, row := range rows {
		expectedOrdinal := int64(index + 1)
		if row.ProjectID != projectID {
			return nil, fmt.Errorf(
				"project label %q belongs to project %q while reading project %q",
				row.ID,
				row.ProjectID,
				projectID,
			)
		}
		if row.Ordinal != expectedOrdinal {
			return nil, fmt.Errorf(
				"project %q label catalog ordinal %d at position %d violates contiguous order",
				projectID,
				row.Ordinal,
				expectedOrdinal,
			)
		}
		record, err := projectLabelRecord(row.ID, row.ProjectID, row.Name)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func compactProjectLabelOrdinals(ctx context.Context, q *sqlitegen.Queries, projectID string) error {
	rows, err := q.ListProjectLabels(ctx, projectID)
	if err != nil {
		return err
	}
	if len(rows) > label.MaxProjectLabels {
		return fmt.Errorf("project %q label catalog exceeds the bounded limit", projectID)
	}
	if _, err := q.MoveProjectLabelOrdinalsToTemporaryBand(ctx, projectID); err != nil {
		return err
	}
	return rewriteProjectLabelOrdinalsFromRows(ctx, q, projectID, rows)
}

func rewriteProjectLabelOrdinals(
	ctx context.Context,
	q *sqlitegen.Queries,
	projectID string,
	orderedIDs []label.ID,
) error {
	if _, err := q.MoveProjectLabelOrdinalsToTemporaryBand(ctx, projectID); err != nil {
		return err
	}
	for index, id := range orderedIDs {
		if err := setProjectLabelOrdinal(ctx, q, projectID, id.String(), int64(index+1)); err != nil {
			return err
		}
	}
	return nil
}

func rewriteProjectLabelOrdinalsFromRows(
	ctx context.Context,
	q *sqlitegen.Queries,
	projectID string,
	rows []sqlitegen.ListProjectLabelsRow,
) error {
	for index, row := range rows {
		if err := setProjectLabelOrdinal(ctx, q, projectID, row.ID, int64(index+1)); err != nil {
			return err
		}
	}
	_, err := listProjectLabelCatalog(ctx, q, projectID)
	return err
}

func setProjectLabelOrdinal(
	ctx context.Context,
	q *sqlitegen.Queries,
	projectID string,
	id string,
	ordinal int64,
) error {
	affected, err := q.SetProjectLabelOrdinal(ctx, sqlitegen.SetProjectLabelOrdinalParams{
		Ordinal:   ordinal,
		ID:        id,
		ProjectID: projectID,
	})
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("project %q label %q ordinal update affected %d rows", projectID, id, affected)
	}
	return nil
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

func (s *Store) GetTaskLabelIDs(ctx context.Context, taskID workflow.TaskID) ([]label.ID, error) {
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) ([]label.ID, error) {
		projectID, err := taskProjectForLabels(ctx, q, taskID)
		if err != nil {
			return nil, err
		}
		if _, err := listProjectLabelCatalog(ctx, q, projectID); err != nil {
			return nil, err
		}
		return listTaskLabelIDs(ctx, q, taskID)
	})
}

func (s *Store) GetTaskLabelScope(ctx context.Context, taskID workflow.TaskID) (TaskLabelScope, error) {
	return taskLabelScope(ctx, s.queries, taskID)
}

func (s *Store) UpdateTaskLabels(ctx context.Context, req TaskLabelUpdateRequest) ([]label.ID, error) {
	addIDs, removeIDs, err := prepareTaskLabelUpdate(req)
	if err != nil {
		return nil, err
	}
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) ([]label.ID, error) {
		taskProjectID, err := q.AcquireTaskLabelWriteLock(ctx, string(req.TaskID))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, TaskLabelTaskNotFoundError{TaskID: string(req.TaskID)}
			}
			return nil, err
		}
		if _, err := listProjectLabelCatalog(ctx, q, taskProjectID); err != nil {
			return nil, err
		}
		referenced := make([]label.ID, 0, len(addIDs)+len(removeIDs))
		referenced = append(referenced, addIDs...)
		referenced = append(referenced, removeIDs...)
		if err := validateTaskLabelReferences(ctx, q, req.TaskID, taskProjectID, referenced); err != nil {
			return nil, err
		}
		for _, id := range addIDs {
			if err := q.InsertTaskLabelAssignment(ctx, sqlitegen.InsertTaskLabelAssignmentParams{
				TaskID:  string(req.TaskID),
				LabelID: id.String(),
			}); err != nil {
				return nil, err
			}
		}
		for _, id := range removeIDs {
			if _, err := q.DeleteTaskLabelAssignment(ctx, sqlitegen.DeleteTaskLabelAssignmentParams{
				TaskID:  string(req.TaskID),
				LabelID: id.String(),
			}); err != nil {
				return nil, err
			}
		}
		return listTaskLabelIDs(ctx, q, req.TaskID)
	})
}

func prepareTaskLabelUpdate(req TaskLabelUpdateRequest) ([]label.ID, []label.ID, error) {
	if strings.TrimSpace(string(req.TaskID)) == "" {
		return nil, nil, errors.New("task id is required")
	}
	if len(req.AddLabelIDs) > label.MaxProjectLabels {
		limit := label.MaxProjectLabels
		return nil, nil, TaskLabelMutationError{
			Reason: TaskLabelMutationTooManyAdd,
			Field:  "add_label_ids",
			Limit:  &limit,
		}
	}
	if len(req.RemoveLabelIDs) > label.MaxProjectLabels {
		limit := label.MaxProjectLabels
		return nil, nil, TaskLabelMutationError{
			Reason: TaskLabelMutationTooManyRemove,
			Field:  "remove_label_ids",
			Limit:  &limit,
		}
	}
	addIDs, addSet, err := parseUniqueLabelIDs(
		req.AddLabelIDs,
		"add_label_ids",
		TaskLabelMutationDuplicateAdd,
	)
	if err != nil {
		return nil, nil, err
	}
	removeIDs, _, err := parseUniqueLabelIDs(
		req.RemoveLabelIDs,
		"remove_label_ids",
		TaskLabelMutationDuplicateRemove,
	)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range removeIDs {
		if addSet[id.String()] {
			labelID := id.String()
			return nil, nil, TaskLabelMutationError{
				Reason:  TaskLabelMutationOverlap,
				LabelID: &labelID,
			}
		}
	}
	return addIDs, removeIDs, nil
}

func parseUniqueLabelIDs(
	rawIDs []string,
	field string,
	duplicateReason TaskLabelMutationErrorReason,
) ([]label.ID, map[string]bool, error) {
	parsed := make([]label.ID, 0, len(rawIDs))
	seen := make(map[string]bool, len(rawIDs))
	for _, raw := range rawIDs {
		id, err := label.ParseID(raw)
		if err != nil {
			labelID := raw
			return nil, nil, TaskLabelMutationError{
				Reason:  TaskLabelMutationInvalidID,
				Field:   field,
				LabelID: &labelID,
				Cause:   err,
			}
		}
		canonical := id.String()
		if seen[canonical] {
			labelID := canonical
			return nil, nil, TaskLabelMutationError{
				Reason:  duplicateReason,
				Field:   field,
				LabelID: &labelID,
			}
		}
		seen[canonical] = true
		parsed = append(parsed, id)
	}
	return parsed, seen, nil
}

func taskProjectForLabels(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID) (string, error) {
	scope, err := taskLabelScope(ctx, q, taskID)
	if err != nil {
		return "", err
	}
	return scope.ProjectID, nil
}

func taskLabelScope(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID) (TaskLabelScope, error) {
	row, err := q.GetTaskProjectWorkflowIDs(ctx, string(taskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TaskLabelScope{}, TaskLabelTaskNotFoundError{TaskID: string(taskID)}
		}
		return TaskLabelScope{}, err
	}
	return TaskLabelScope{
		ProjectID:  row.ProjectID,
		WorkflowID: row.WorkflowID,
	}, nil
}

func validateTaskLabelReferences(
	ctx context.Context,
	q *sqlitegen.Queries,
	taskID workflow.TaskID,
	taskProjectID string,
	ids []label.ID,
) error {
	if len(ids) == 0 {
		return nil
	}
	rawIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		rawIDs = append(rawIDs, id.String())
	}
	rows, err := q.ListProjectLabelsByIDs(ctx, rawIDs)
	if err != nil {
		return err
	}
	projectsByID := make(map[string]string, len(rows))
	for _, row := range rows {
		projectsByID[row.ID] = row.ProjectID
	}
	for _, id := range ids {
		projectID, exists := projectsByID[id.String()]
		if !exists {
			return TaskLabelNotFoundError{LabelID: id.String()}
		}
		if projectID != taskProjectID {
			return TaskLabelWrongProjectError{
				TaskID:         string(taskID),
				TaskProjectID:  taskProjectID,
				LabelID:        id.String(),
				LabelProjectID: projectID,
			}
		}
	}
	return nil
}

func listTaskLabelIDs(ctx context.Context, q *sqlitegen.Queries, taskID workflow.TaskID) ([]label.ID, error) {
	rows, err := q.ListTaskAssignedLabelIDsByTasks(ctx, []string{string(taskID)})
	if err != nil {
		return nil, err
	}
	if len(rows) > label.MaxProjectLabels {
		return nil, fmt.Errorf(
			"task %q has %d label assignments, exceeding invariant limit %d",
			taskID,
			len(rows),
			label.MaxProjectLabels,
		)
	}
	ids := make([]label.ID, 0, len(rows))
	for _, row := range rows {
		if row.TaskID != string(taskID) {
			return nil, fmt.Errorf(
				"task %q label read returned unrequested task %q",
				taskID,
				row.TaskID,
			)
		}
		id, err := label.ParseID(row.LabelID)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
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
