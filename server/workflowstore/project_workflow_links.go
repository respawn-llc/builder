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
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

const projectWorkflowUnlinkTaskPreviewLimit = 10

func (s *Store) LinkWorkflow(ctx context.Context, projectID string, workflowID runtimeids.WorkflowID, isDefault bool) (ProjectWorkflowLinkRecord, error) {
	policy := WorkflowLinkDefaultNever
	if isDefault {
		policy = WorkflowLinkDefaultAlways
	}
	return s.LinkWorkflowWithDefaultPolicy(ctx, projectID, workflowID, policy)
}

func (s *Store) LinkWorkflowWithDefaultPolicy(ctx context.Context, projectID string, workflowID runtimeids.WorkflowID, policy WorkflowLinkDefaultPolicy) (_ ProjectWorkflowLinkRecord, metadataOperationErr error) {
	now := s.now().UnixMilli()
	tx, err := s.metadata.BeginTransaction(ctx, "LinkWorkflowWithDefaultPolicy", nil)
	if err != nil {
		return ProjectWorkflowLinkRecord{}, err
	}
	defer tx.Settle(ctx, &metadataOperationErr)
	q := tx.Queries()
	link, err := s.linkWorkflowInTx(ctx, q, now, projectID, workflowID, policy)
	if err != nil {
		return ProjectWorkflowLinkRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProjectWorkflowLinkRecord{}, err
	}
	return link, nil
}

func (s *Store) linkWorkflowInTx(ctx context.Context, q *sqlitegen.Queries, now int64, projectID string, workflowID runtimeids.WorkflowID, policy WorkflowLinkDefaultPolicy) (ProjectWorkflowLinkRecord, error) {
	shouldDefault, err := s.shouldSetWorkflowLinkDefault(ctx, q, projectID, policy)
	if err != nil {
		return ProjectWorkflowLinkRecord{}, err
	}
	linkID := prefixedID("workflow-link")
	inserted, err := q.InsertProjectWorkflowLink(ctx, sqlitegen.InsertProjectWorkflowLinkParams{ID: linkID, ProjectID: projectID, WorkflowID: workflowID, CreatedAtUnixMs: now, UpdatedAtUnixMs: now})
	if err != nil {
		return ProjectWorkflowLinkRecord{}, translateProjectWorkflowLinkInsertError(ctx, q, projectID, workflowID, err)
	}
	isDefault := shouldDefault
	if inserted == 0 {
		existing, err := q.GetActiveProjectWorkflowLinkByWorkflow(ctx, sqlitegen.GetActiveProjectWorkflowLinkByWorkflowParams{ProjectID: projectID, WorkflowID: workflowID})
		if err != nil {
			return ProjectWorkflowLinkRecord{}, fmt.Errorf("get existing project workflow link: %w", err)
		}
		linkID = existing.ID
		isDefault = existing.IsDefault != 0
	}
	if shouldDefault {
		if err := setProjectDefaultWorkflowLink(ctx, q, now, projectID, linkID); err != nil {
			return ProjectWorkflowLinkRecord{}, err
		}
		isDefault = true
	}
	return ProjectWorkflowLinkRecord{ID: linkID, ProjectID: projectID, WorkflowID: workflowID, IsDefault: isDefault}, nil
}

func translateProjectWorkflowLinkInsertError(ctx context.Context, q *sqlitegen.Queries, projectID string, workflowID runtimeids.WorkflowID, err error) error {
	if !metadata.IsSQLiteForeignKeyConstraint(err) {
		return fmt.Errorf("insert project workflow link: %w", err)
	}
	if _, projectErr := q.GetProjectKeyState(ctx, projectID); errors.Is(projectErr, sql.ErrNoRows) {
		return fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
	} else if projectErr != nil {
		return fmt.Errorf("classify project workflow link project: %w", projectErr)
	}
	if _, workflowErr := q.GetWorkflow(ctx, workflowID); errors.Is(workflowErr, sql.ErrNoRows) {
		return fmt.Errorf("%w: %q", ErrWorkflowNotFound, workflowID)
	} else if workflowErr != nil {
		return fmt.Errorf("classify project workflow link workflow: %w", workflowErr)
	}
	return ErrProjectWorkflowLinkInvalid
}

func (s *Store) shouldSetWorkflowLinkDefault(ctx context.Context, q *sqlitegen.Queries, projectID string, policy WorkflowLinkDefaultPolicy) (bool, error) {
	switch policy {
	case WorkflowLinkDefaultAlways:
		return true, nil
	case WorkflowLinkDefaultIfProjectHasNone:
		count, err := q.CountActiveProjectWorkflowLinks(ctx, projectID)
		if err != nil {
			return false, err
		}
		if count == 0 {
			return true, nil
		}
		_, err = q.GetDefaultProjectWorkflowLink(ctx, projectID)
		if errors.Is(err, sql.ErrNoRows) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return false, nil
	case "", WorkflowLinkDefaultNever:
		return false, nil
	default:
		panic(fmt.Sprintf("validated workflow link default policy is invalid: %q", policy))
	}
}

func setProjectDefaultWorkflowLink(ctx context.Context, q *sqlitegen.Queries, now int64, projectID string, linkID string) error {
	count, err := q.SetProjectDefaultWorkflowLink(ctx, sqlitegen.SetProjectDefaultWorkflowLinkParams{
		ProjectWorkflowLinkID: sql.NullString{String: linkID, Valid: true},
		UpdatedAtUnixMs:       now,
		ProjectID:             projectID,
	})
	if err != nil {
		return fmt.Errorf("set default workflow link: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("project workflow link is invalid")
	}
	return nil
}

func (s *Store) ListProjectWorkflowLinks(ctx context.Context, projectID string) ([]ProjectWorkflowLinkRecord, error) {
	rows, err := s.queries.ListProjectWorkflowLinks(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectWorkflowLinkRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, linkRecordFromRow(row))
	}
	return out, nil
}

func (s *Store) ListWorkflowProjectLinks(ctx context.Context, workflowID runtimeids.WorkflowID) ([]ProjectWorkflowLinkRecord, error) {
	rows, err := s.queries.ListWorkflowProjectLinks(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectWorkflowLinkRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, linkRecordFromRow(row))
	}
	return out, nil
}

func (s *Store) GetProjectWorkflowLink(ctx context.Context, linkID string) (ProjectWorkflowLinkRecord, error) {
	row, err := s.queries.GetProjectWorkflowLink(ctx, strings.TrimSpace(linkID))
	if err != nil {
		return ProjectWorkflowLinkRecord{}, err
	}
	return linkRecordFromRow(row), nil
}

func (s *Store) SetDefaultProjectWorkflowLink(ctx context.Context, projectID string, workflowID runtimeids.WorkflowID) (_ ProjectWorkflowLinkRecord, metadataOperationErr error) {
	now := s.now().UnixMilli()
	tx, err := s.metadata.BeginTransaction(ctx, "SetDefaultProjectWorkflowLink", nil)
	if err != nil {
		return ProjectWorkflowLinkRecord{}, err
	}
	defer tx.Settle(ctx, &metadataOperationErr)
	q := tx.Queries()
	updated, err := q.SetProjectDefaultWorkflowLinkByWorkflow(ctx, sqlitegen.SetProjectDefaultWorkflowLinkByWorkflowParams{
		UpdatedAtUnixMs: now,
		ProjectID:       projectID,
		WorkflowID:      workflowID,
	})
	if err != nil {
		return ProjectWorkflowLinkRecord{}, fmt.Errorf("set default project workflow link: %w", err)
	}
	if updated == 0 {
		if _, projectErr := q.GetProjectKeyState(ctx, projectID); errors.Is(projectErr, sql.ErrNoRows) {
			return ProjectWorkflowLinkRecord{}, fmt.Errorf("%w: %q", serverapi.ErrProjectNotFound, projectID)
		} else if projectErr != nil {
			return ProjectWorkflowLinkRecord{}, fmt.Errorf("classify default workflow project: %w", projectErr)
		}
		return ProjectWorkflowLinkRecord{}, ErrProjectWorkflowLinkNotFound
	}
	link, err := q.GetActiveProjectWorkflowLinkByWorkflow(ctx, sqlitegen.GetActiveProjectWorkflowLinkByWorkflowParams{ProjectID: projectID, WorkflowID: workflowID})
	if err != nil {
		return ProjectWorkflowLinkRecord{}, fmt.Errorf("get default project workflow link: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ProjectWorkflowLinkRecord{}, err
	}
	return s.GetProjectWorkflowLink(ctx, link.ID)
}

func (s *Store) UnlinkProjectWorkflow(ctx context.Context, linkID string, replacementDefaultLinkID string) (_ ProjectWorkflowUnlinkResult, metadataOperationErr error) {
	now := s.now().UnixMilli()
	linkID = strings.TrimSpace(linkID)
	replacementDefaultLinkID = strings.TrimSpace(replacementDefaultLinkID)
	tx, err := s.metadata.BeginTransaction(ctx, "UnlinkProjectWorkflow", nil)
	if err != nil {
		return ProjectWorkflowUnlinkResult{}, err
	}
	defer tx.Settle(ctx, &metadataOperationErr)
	q := tx.Queries()
	link, err := q.GetProjectWorkflowLink(ctx, linkID)
	if err != nil {
		return ProjectWorkflowUnlinkResult{}, err
	}
	result := ProjectWorkflowUnlinkResult{LinkID: link.ID, ProjectID: link.ProjectID, WorkflowID: link.WorkflowID}
	if replacementDefaultLinkID != "" && replacementDefaultLinkID == link.ID {
		return ProjectWorkflowUnlinkResult{}, ErrReplacementDefaultInvalid
	}
	taskRefs, err := q.CountTasksByProjectWorkflowLink(ctx, link.ID)
	if err != nil {
		return ProjectWorkflowUnlinkResult{}, err
	}
	if taskRefs > 0 {
		tasks, err := q.ListProjectWorkflowLinkTaskReferences(ctx, sqlitegen.ListProjectWorkflowLinkTaskReferencesParams{ProjectWorkflowLinkID: link.ID, Limit: projectWorkflowUnlinkTaskPreviewLimit})
		if err != nil {
			return ProjectWorkflowUnlinkResult{}, err
		}
		refs := make([]ProjectWorkflowUnlinkTaskReference, 0, len(tasks))
		for _, task := range tasks {
			refs = append(refs, ProjectWorkflowUnlinkTaskReference{TaskID: workflow.TaskID(task.ID), ShortID: task.ShortID, Title: task.Title})
		}
		result.Blockers = append(result.Blockers, ProjectWorkflowUnlinkBlocker{
			Code:    "task_references",
			Message: "Workflow link still has tasks. Move or delete those tasks before unlinking this workflow from the project.",
			Count:   int(taskRefs),
			Tasks:   refs,
		})
		return result, nil
	}
	if replacementDefaultLinkID != "" {
		if _, err := q.DeleteProjectWorkflowLink(ctx, link.ID); err != nil {
			return ProjectWorkflowUnlinkResult{}, err
		}
		if err := setProjectDefaultWorkflowLink(ctx, q, now, link.ProjectID, replacementDefaultLinkID); err != nil {
			if metadata.IsSQLiteTriggerConstraint(err) {
				return ProjectWorkflowUnlinkResult{}, ErrReplacementDefaultInvalid
			}
			return ProjectWorkflowUnlinkResult{}, err
		}
	} else {
		deletedCount, err := q.DeleteProjectWorkflowLinkUnlessDefaultNeedsReplacement(ctx, link.ID)
		if err != nil {
			return ProjectWorkflowUnlinkResult{}, err
		}
		if deletedCount != 1 {
			state, err := q.GetProjectWorkflowUnlinkState(ctx, link.ProjectID)
			if err != nil {
				return ProjectWorkflowUnlinkResult{}, err
			}
			if state.DefaultProjectWorkflowLinkID.Valid && state.DefaultProjectWorkflowLinkID.String == link.ID && state.ActiveLinkCount > 1 {
				result.Blockers = append(result.Blockers, ProjectWorkflowUnlinkBlocker{
					Code:    "default_replacement_required",
					Message: "Default workflow link requires a replacement before unlinking because this project has other linked workflows.",
					Count:   int(state.ActiveLinkCount - 1),
				})
				return result, nil
			}
			return ProjectWorkflowUnlinkResult{}, fmt.Errorf("project workflow link is invalid")
		}
	}
	if err := tx.Commit(); err != nil {
		return ProjectWorkflowUnlinkResult{}, err
	}
	result.Unlinked = true
	return result, nil
}

func resolveTaskWorkflowLinkWithQueries(ctx context.Context, q *sqlitegen.Queries, projectID string, workflowID *runtimeids.WorkflowID) (sqlitegen.ProjectWorkflowLinkRecord, error) {
	if workflowID == nil {
		link, err := q.GetDefaultProjectWorkflowLink(ctx, projectID)
		if err == nil {
			return link, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return sqlitegen.ProjectWorkflowLinkRecord{}, err
		}
		links, err := q.ListProjectWorkflowLinksForTaskSelection(ctx, projectID)
		if err != nil {
			return sqlitegen.ProjectWorkflowLinkRecord{}, err
		}
		if len(links) == 1 {
			return links[0], nil
		}
		if len(links) == 0 {
			return sqlitegen.ProjectWorkflowLinkRecord{}, TaskWorkflowSelectionError{
				Reason:    TaskWorkflowSelectionNoLinkedWorkflows,
				ProjectID: projectID,
			}
		}
		return sqlitegen.ProjectWorkflowLinkRecord{}, TaskWorkflowSelectionError{
			Reason:    TaskWorkflowSelectionAmbiguousWithoutDefault,
			ProjectID: projectID,
		}
	}
	link, err := q.GetActiveProjectWorkflowLinkByWorkflow(ctx, sqlitegen.GetActiveProjectWorkflowLinkByWorkflowParams{ProjectID: projectID, WorkflowID: *workflowID})
	if errors.Is(err, sql.ErrNoRows) {
		return sqlitegen.ProjectWorkflowLinkRecord{}, TaskWorkflowSelectionError{
			Reason:     TaskWorkflowSelectionWorkflowNotLinked,
			ProjectID:  projectID,
			WorkflowID: workflowID,
		}
	}
	return link, err
}

func linkRecordFromRow(row sqlitegen.ProjectWorkflowLinkRecord) ProjectWorkflowLinkRecord {
	return ProjectWorkflowLinkRecord{ID: row.ID, ProjectID: row.ProjectID, WorkflowID: row.WorkflowID, IsDefault: row.IsDefault != 0}
}
