package workflowstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"core/server/metadata/sqlitegen"
	"core/server/workflow"
)

func (s *Store) AddComment(ctx context.Context, taskID workflow.TaskID, body string, authorKind string, authorID string) (CommentRecord, error) {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return CommentRecord{}, errors.New("comment body is required")
	}
	trimmedAuthorKind := strings.TrimSpace(authorKind)
	switch trimmedAuthorKind {
	case "user", "agent":
	default:
		return CommentRecord{}, ErrCommentAuthorKindInvalid
	}
	now := s.now().UnixMilli()
	id := prefixedID("comment")
	if err := s.queries.InsertTaskComment(ctx, sqlitegen.InsertTaskCommentParams{ID: id, TaskID: string(taskID), Body: trimmed, AuthorKind: trimmedAuthorKind, AuthorID: strings.TrimSpace(authorID), CreatedAtUnixMs: now, UpdatedAtUnixMs: now}); err != nil {
		return CommentRecord{}, err
	}
	return CommentRecord{ID: id, TaskID: taskID, Body: trimmed, Author: trimmedAuthorKind, AuthorID: strings.TrimSpace(authorID), CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) ReplaceComment(ctx context.Context, commentID string, body string) error {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return errors.New("comment body is required")
	}
	updated, err := s.queries.UpdateTaskCommentBody(ctx, sqlitegen.UpdateTaskCommentBodyParams{ID: strings.TrimSpace(commentID), Body: trimmed, UpdatedAtUnixMs: s.now().UnixMilli()})
	if err != nil {
		return err
	}
	if updated == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteComment(ctx context.Context, commentID string) error {
	updated, err := s.queries.DeleteTaskComment(ctx, strings.TrimSpace(commentID))
	if err != nil {
		return err
	}
	if updated == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CountTaskComments(ctx context.Context, taskID workflow.TaskID) (int64, error) {
	return s.queries.CountTaskComments(ctx, string(taskID))
}

func (s *Store) TaskIdentityForComment(ctx context.Context, commentID string) (taskID string, projectID string, workflowID workflow.WorkflowID, err error) {
	row, err := s.queries.GetTaskIdentityForComment(ctx, strings.TrimSpace(commentID))
	if err != nil {
		return "", "", workflow.WorkflowID{}, err
	}
	return row.TaskID, row.ProjectID, row.WorkflowID, nil
}

func (s *Store) ListComments(ctx context.Context, taskID workflow.TaskID) ([]CommentRecord, error) {
	return s.listComments(ctx, taskID, 0, -1)
}

func (s *Store) ListCommentsPage(ctx context.Context, taskID workflow.TaskID, offset int, limit int) ([]CommentRecord, error) {
	if limit < 1 {
		return nil, errors.New("comment limit must be positive")
	}
	return s.listComments(ctx, taskID, offset, limit)
}

func (s *Store) listComments(ctx context.Context, taskID workflow.TaskID, offset int, limit int) ([]CommentRecord, error) {
	rows, err := s.queries.ListTaskComments(ctx, sqlitegen.ListTaskCommentsParams{TaskID: string(taskID), OffsetRows: int64(offset), LimitRows: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]CommentRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, CommentRecord{ID: row.ID, TaskID: workflow.TaskID(row.TaskID), Body: row.Body, Author: row.AuthorKind, AuthorID: row.AuthorID, CreatedAt: row.CreatedAtUnixMs, UpdatedAt: row.UpdatedAtUnixMs})
	}
	return out, nil
}
