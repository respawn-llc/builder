package serverapi

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"core/shared/tasksearchtext"
)

const (
	TaskSearchMaxQueryRunes   = 4096
	TaskSearchDefaultContext  = 20
	TaskSearchMinContext      = 1
	TaskSearchMaxContext      = 64
	TaskSearchDefaultPageSize = 100
	TaskSearchMaxPageSize     = 100
)

type TaskSearchMode string

const (
	TaskSearchModeLiteral TaskSearchMode = "literal"
	TaskSearchModeFTS5    TaskSearchMode = "fts5"
)

type TaskSearchSourceKind string

const (
	TaskSearchSourceKindTitle   TaskSearchSourceKind = "title"
	TaskSearchSourceKindBody    TaskSearchSourceKind = "body"
	TaskSearchSourceKindComment TaskSearchSourceKind = "comment"
)

type TaskSearchRequest struct {
	Mode            TaskSearchMode           `json:"mode"`
	Query           string                   `json:"query"`
	Context         int                      `json:"context"`
	CaseSensitive   bool                     `json:"case_sensitive"`
	IncludeComments bool                     `json:"include_comments"`
	ProjectIDs      []string                 `json:"project_ids,omitempty"`
	StatusKinds     []WorkflowTaskStatusKind `json:"status_kinds,omitempty"`
	PageSize        int                      `json:"page_size"`
	PageToken       *string                  `json:"page_token,omitempty"`
}

func (r TaskSearchRequest) Validate() error {
	if r.Mode != TaskSearchModeLiteral && r.Mode != TaskSearchModeFTS5 {
		return taskSearchFieldError("mode", "mode is invalid")
	}
	if r.Query == "" || strings.TrimSpace(r.Query) == "" {
		return taskSearchFieldError("query", "query is required")
	}
	if strings.TrimSpace(r.Query) != r.Query {
		return taskSearchFieldError("query", "query must be trimmed")
	}
	if utf8.RuneCountInString(r.Query) > TaskSearchMaxQueryRunes {
		return taskSearchFieldError("query", "query is too long")
	}
	if r.Mode == TaskSearchModeLiteral && tasksearchtext.NormalizedLiteralRuneCount(r.Query) < 3 {
		return &TaskSearchError{Reason: TaskSearchErrorReasonNormalizedTooShort}
	}
	if r.Mode == TaskSearchModeFTS5 && r.CaseSensitive {
		return taskSearchFieldError("case_sensitive", "case_sensitive requires literal mode")
	}
	if r.Context < TaskSearchMinContext || r.Context > TaskSearchMaxContext {
		return taskSearchFieldError("context", "context is out of range")
	}
	if r.PageSize < 1 || r.PageSize > TaskSearchMaxPageSize {
		return taskSearchFieldError("page_size", "page_size is out of range")
	}
	if r.PageToken != nil && strings.TrimSpace(*r.PageToken) != *r.PageToken {
		return taskSearchFieldError("page_token", "page_token must be trimmed")
	}
	for index, projectID := range r.ProjectIDs {
		if strings.TrimSpace(projectID) == "" || strings.TrimSpace(projectID) != projectID {
			return taskSearchFieldError(fmt.Sprintf("project_ids[%d]", index), "project id is invalid")
		}
		if index > 0 && r.ProjectIDs[index-1] >= projectID {
			return taskSearchFieldError("project_ids", "project ids must be sorted and unique")
		}
	}
	for index, status := range r.StatusKinds {
		if _, valid := status.NativeState(); !valid {
			return taskSearchFieldError(fmt.Sprintf("status_kinds[%d]", index), "status kind is invalid")
		}
	}
	return nil
}

type TaskSearchResponse struct {
	Mode          TaskSearchMode    `json:"mode"`
	Groups        []TaskSearchGroup `json:"groups"`
	NextPageToken *string           `json:"next_page_token,omitempty"`
}

type TaskSearchGroup struct {
	ProjectID     string             `json:"project_id"`
	ProjectKey    string             `json:"project_key"`
	TaskID        string             `json:"task_id"`
	ShortID       string             `json:"short_id"`
	WorkflowID    string             `json:"workflow_id"`
	Title         string             `json:"title"`
	Status        WorkflowTaskStatus `json:"status"`
	TotalHitCount int                `json:"total_hit_count"`
	Hits          []TaskSearchHit    `json:"hits"`
}

type TaskSearchHit struct {
	Ordinal int                   `json:"ordinal"`
	Source  TaskSearchSource      `json:"source"`
	Literal *TaskSearchLiteralHit `json:"literal,omitempty"`
	FTS5    *TaskSearchFTS5Hit    `json:"fts5,omitempty"`
}

type TaskSearchSource struct {
	Kind      TaskSearchSourceKind `json:"kind"`
	CommentID *string              `json:"comment_id,omitempty"`
}

type TaskSearchLiteralHit struct {
	Before         string `json:"before"`
	Match          string `json:"match"`
	After          string `json:"after"`
	LeftTruncated  bool   `json:"left_truncated"`
	RightTruncated bool   `json:"right_truncated"`
}

type TaskSearchFTS5Hit struct {
	Snippet string `json:"snippet"`
}

func (r TaskSearchResponse) Validate() error {
	if r.Mode != TaskSearchModeLiteral && r.Mode != TaskSearchModeFTS5 {
		return errors.New("task search response mode is invalid")
	}
	if r.Groups == nil {
		return errors.New("task search response groups are required")
	}
	for groupIndex, group := range r.Groups {
		if group.TotalHitCount < len(group.Hits) || group.TotalHitCount < 1 {
			return fmt.Errorf("task search group %d total hit count is invalid", groupIndex)
		}
		for _, hit := range group.Hits {
			if err := hit.Validate(r.Mode); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h TaskSearchHit) Validate(mode TaskSearchMode) error {
	if h.Ordinal < 1 {
		return errors.New("task search hit ordinal must be positive")
	}
	switch h.Source.Kind {
	case TaskSearchSourceKindTitle, TaskSearchSourceKindBody:
		if h.Source.CommentID != nil {
			return errors.New("task search title/body source forbids comment id")
		}
	case TaskSearchSourceKindComment:
		if h.Source.CommentID == nil || strings.TrimSpace(*h.Source.CommentID) == "" {
			return errors.New("task search comment source requires comment id")
		}
	default:
		return errors.New("task search source kind is invalid")
	}
	if mode == TaskSearchModeLiteral && h.Literal != nil && h.FTS5 == nil {
		return nil
	}
	if mode == TaskSearchModeFTS5 && h.FTS5 != nil && h.Literal == nil {
		return nil
	}
	return errors.New("task search hit mode payload is invalid")
}

type TaskSearchErrorReason string

const (
	TaskSearchErrorReasonNormalizedTooShort TaskSearchErrorReason = "normalized_too_short"
	TaskSearchErrorReasonMalformedFTS5      TaskSearchErrorReason = "malformed_fts5"
	TaskSearchErrorReasonInvalidCursor      TaskSearchErrorReason = "invalid_cursor"
)

type TaskSearchError struct {
	Reason TaskSearchErrorReason
}

func (e *TaskSearchError) Error() string {
	if e == nil {
		return "task search error"
	}
	return "task search error: " + string(e.Reason)
}

func taskSearchFieldError(field string, message string) error {
	return WorkflowRequestValidationError{Code: WorkflowRequestErrorInvalidValue, Field: field, Message: message}
}
