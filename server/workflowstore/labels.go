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
	Ordinal   int64
}

type ProjectLabelReorderResult struct {
	Labels  []ProjectLabelRecord
	Changed bool
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

func (s *Store) CreateProjectLabel(ctx context.Context, projectID string, rawName string) (ProjectLabelRecord, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	name, err := label.PrepareName(rawName)
	if err != nil {
		return ProjectLabelRecord{}, err
	}
	id := label.NewID()
	now := s.now().UnixMilli()
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) (ProjectLabelRecord, error) {
		current, err := loadProjectLabelCatalog(ctx, q, trimmedProjectID)
		if err != nil {
			return ProjectLabelRecord{}, err
		}
		if len(current) >= label.MaxProjectLabels {
			return ProjectLabelRecord{}, ProjectLabelLimitError{
				ProjectID: trimmedProjectID,
				Limit:     label.MaxProjectLabels,
			}
		}
		if err := q.MoveProjectLabelOrdinalsToTemporaryBand(ctx, trimmedProjectID); err != nil {
			return ProjectLabelRecord{}, err
		}
		for index := len(current) - 1; index >= 0; index-- {
			if err := q.SetProjectLabelOrdinal(ctx, sqlitegen.SetProjectLabelOrdinalParams{
				Ordinal:   int64(index + 2),
				ID:        current[index].ID.String(),
				ProjectID: trimmedProjectID,
			}); err != nil {
				return ProjectLabelRecord{}, err
			}
		}
		row, err := q.InsertProjectLabel(ctx, sqlitegen.InsertProjectLabelParams{
			ID:              id.String(),
			ProjectID:       trimmedProjectID,
			Name:            name.String(),
			CreatedAtUnixMs: now,
			UpdatedAtUnixMs: now,
			Ordinal:         1,
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
		return projectLabelRecord(row.ID, row.ProjectID, row.Name, row.Ordinal)
	})
}

func (s *Store) ListProjectLabels(ctx context.Context, projectID string) ([]ProjectLabelRecord, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) ([]ProjectLabelRecord, error) {
		return loadProjectLabelCatalog(ctx, q, trimmedProjectID)
	})
}

func (s *Store) RenameProjectLabel(ctx context.Context, projectID string, id label.ID, rawName string) (ProjectLabelRecord, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	name, err := label.PrepareName(rawName)
	if err != nil {
		return ProjectLabelRecord{}, err
	}
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) (ProjectLabelRecord, error) {
		current, err := loadProjectLabelCatalog(ctx, q, trimmedProjectID)
		if err != nil {
			return ProjectLabelRecord{}, err
		}
		if !containsProjectLabel(current, id) {
			return ProjectLabelRecord{}, ProjectLabelNotFoundError{
				ProjectID: trimmedProjectID,
				LabelID:   id.String(),
			}
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
		return projectLabelRecord(row.ID, row.ProjectID, row.Name, row.Ordinal)
	})
}

func (s *Store) DeleteProjectLabel(ctx context.Context, projectID string, id label.ID) (ProjectLabelRecord, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) (ProjectLabelRecord, error) {
		current, err := loadProjectLabelCatalog(ctx, q, trimmedProjectID)
		if err != nil {
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
		if err := q.MoveProjectLabelOrdinalsToTemporaryBand(ctx, trimmedProjectID); err != nil {
			return ProjectLabelRecord{}, err
		}
		nextOrdinal := int64(1)
		for _, record := range current {
			if record.ID == id {
				continue
			}
			if err := q.SetProjectLabelOrdinal(ctx, sqlitegen.SetProjectLabelOrdinalParams{
				Ordinal:   nextOrdinal,
				ID:        record.ID.String(),
				ProjectID: trimmedProjectID,
			}); err != nil {
				return ProjectLabelRecord{}, err
			}
			nextOrdinal++
		}
		return projectLabelRecord(row.ID, row.ProjectID, row.Name, row.Ordinal)
	})
}

func (s *Store) ReorderProjectLabels(ctx context.Context, projectID string, ids []label.ID) (ProjectLabelReorderResult, error) {
	trimmedProjectID := strings.TrimSpace(projectID)
	return withProjectLabelTransaction(ctx, s, func(q *sqlitegen.Queries) (ProjectLabelReorderResult, error) {
		current, err := loadProjectLabelCatalog(ctx, q, trimmedProjectID)
		if err != nil {
			return ProjectLabelReorderResult{}, err
		}
		if len(ids) != len(current) {
			return ProjectLabelReorderResult{}, ProjectLabelReorderError{
				ProjectID: trimmedProjectID,
				Reason:    ProjectLabelReorderWrongCount,
			}
		}
		recordsByID := make(map[label.ID]ProjectLabelRecord, len(current))
		for _, record := range current {
			recordsByID[record.ID] = record
		}
		seen := make(map[label.ID]struct{}, len(ids))
		for _, id := range ids {
			if _, ok := seen[id]; ok {
				labelID := id.String()
				return ProjectLabelReorderResult{}, ProjectLabelReorderError{
					ProjectID: trimmedProjectID,
					LabelID:   &labelID,
					Reason:    ProjectLabelReorderDuplicateID,
				}
			}
			seen[id] = struct{}{}
			if _, ok := recordsByID[id]; !ok {
				return ProjectLabelReorderResult{}, projectLabelReorderReferenceError(ctx, q, trimmedProjectID, id)
			}
		}
		unchanged := true
		for index, id := range ids {
			if current[index].ID != id {
				unchanged = false
				break
			}
		}
		if unchanged {
			return ProjectLabelReorderResult{Labels: current}, nil
		}
		if err := q.MoveProjectLabelOrdinalsToTemporaryBand(ctx, trimmedProjectID); err != nil {
			return ProjectLabelReorderResult{}, err
		}
		for index, id := range ids {
			if err := q.SetProjectLabelOrdinal(ctx, sqlitegen.SetProjectLabelOrdinalParams{
				Ordinal:   int64(index + 1),
				ID:        id.String(),
				ProjectID: trimmedProjectID,
			}); err != nil {
				return ProjectLabelReorderResult{}, err
			}
		}
		updated, err := loadProjectLabelCatalog(ctx, q, trimmedProjectID)
		if err != nil {
			return ProjectLabelReorderResult{}, err
		}
		return ProjectLabelReorderResult{Labels: updated, Changed: true}, nil
	})
}

func loadProjectLabelCatalog(ctx context.Context, q *sqlitegen.Queries, projectID string) ([]ProjectLabelRecord, error) {
	if err := requireProjectForLabels(ctx, q, projectID); err != nil {
		return nil, err
	}
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
	seenIDs := make(map[label.ID]struct{}, len(rows))
	for index, row := range rows {
		if row.ProjectID != projectID {
			return nil, fmt.Errorf("project label %q belongs to project %q, want %q", row.ID, row.ProjectID, projectID)
		}
		if row.Ordinal != int64(index+1) {
			return nil, fmt.Errorf("project %q label ordinal %d is not contiguous at position %d", projectID, row.Ordinal, index+1)
		}
		record, err := projectLabelRecord(row.ID, row.ProjectID, row.Name, row.Ordinal)
		if err != nil {
			return nil, err
		}
		if _, exists := seenIDs[record.ID]; exists {
			return nil, fmt.Errorf("project %q label catalog contains duplicate label ID %q", projectID, record.ID)
		}
		seenIDs[record.ID] = struct{}{}
		records = append(records, record)
	}
	return records, nil
}

func containsProjectLabel(records []ProjectLabelRecord, id label.ID) bool {
	for _, record := range records {
		if record.ID == id {
			return true
		}
	}
	return false
}

func projectLabelReorderReferenceError(ctx context.Context, q *sqlitegen.Queries, projectID string, id label.ID) error {
	rows, err := q.ListProjectLabelsByIDs(ctx, []string{id.String()})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		labelID := id.String()
		return ProjectLabelReorderError{ProjectID: projectID, LabelID: &labelID, Reason: ProjectLabelReorderUnknownID}
	}
	if rows[0].ProjectID != projectID {
		labelID := id.String()
		return ProjectLabelReorderError{ProjectID: projectID, LabelID: &labelID, Reason: ProjectLabelReorderWrongProject}
	}
	labelID := id.String()
	return ProjectLabelReorderError{ProjectID: projectID, LabelID: &labelID, Reason: ProjectLabelReorderUnknownID}
}

func projectLabelRecord(id string, projectID string, name string, ordinal int64) (ProjectLabelRecord, error) {
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
	if ordinal < 1 || ordinal > 200 {
		return ProjectLabelRecord{}, fmt.Errorf("persisted project label %q has invalid ordinal %d", id, ordinal)
	}
	return ProjectLabelRecord{
		ID:        parsedID,
		ProjectID: projectID,
		Name:      preparedName,
		Ordinal:   ordinal,
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
		if _, err := taskProjectForLabels(ctx, q, taskID); err != nil {
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
