package serverapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"core/shared/protocol"
	"core/shared/tasksearchtext"
)

const (
	TaskSearchMaxQueryRunes   = 4096
	TaskSearchDefaultContext  = 20
	TaskSearchMinContext      = 1
	TaskSearchMaxContext      = 64
	TaskSearchDefaultPageSize = 100
	TaskSearchMaxPageSize     = 100

	TaskSearchSparseDocumentContractVersion = "kent-task-search-sparse-document-v1"
	TaskSearchRankingContractVersion        = "kent-task-search-ranking-v1"
)

type TaskSearchMode string

const (
	TaskSearchModeLiteral TaskSearchMode = "literal"
	TaskSearchModeFTS5    TaskSearchMode = "fts5"
)

type TaskSearchSourceKind string

const (
	TaskSearchSourceKindShortID TaskSearchSourceKind = "short_id"
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
	Offset          *int                     `json:"offset,omitempty"`
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
	if r.Offset != nil && *r.Offset < 0 {
		return taskSearchFieldError("offset", "offset must be non-negative")
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
		if index > 0 && r.StatusKinds[index-1] >= status {
			return taskSearchFieldError("status_kinds", "status kinds must be sorted and unique")
		}
	}
	return nil
}

type TaskSearchResponse struct {
	Mode       TaskSearchMode    `json:"mode"`
	Groups     []TaskSearchGroup `json:"groups"`
	NextOffset *int              `json:"next_offset,omitempty"`
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
	if r.NextOffset != nil && *r.NextOffset <= 0 {
		return errors.New("task search response next offset is invalid")
	}
	groupTaskIDs := make(map[string]struct{}, len(r.Groups))
	for groupIndex, group := range r.Groups {
		for _, field := range []struct {
			name  string
			value string
		}{
			{name: "project id", value: group.ProjectID},
			{name: "project key", value: group.ProjectKey},
			{name: "task id", value: group.TaskID},
			{name: "short id", value: group.ShortID},
			{name: "workflow id", value: group.WorkflowID},
			{name: "title", value: group.Title},
		} {
			if err := validateTaskSearchResponseString(field.name, field.value); err != nil {
				return fmt.Errorf("task search group %d: %w", groupIndex, err)
			}
		}
		if _, exists := groupTaskIDs[group.TaskID]; exists {
			return fmt.Errorf("task search response duplicates task group %q", group.TaskID)
		}
		groupTaskIDs[group.TaskID] = struct{}{}
		if len(group.Hits) == 0 || group.TotalHitCount < len(group.Hits) || group.TotalHitCount < 1 {
			return fmt.Errorf("task search group %d total hit count is invalid", groupIndex)
		}
		if err := validateTaskSearchStatus(group.Status); err != nil {
			return fmt.Errorf("task search group %d: %w", groupIndex, err)
		}
		for hitIndex, hit := range group.Hits {
			if err := hit.Validate(r.Mode); err != nil {
				return err
			}
			if hit.Ordinal > group.TotalHitCount {
				return fmt.Errorf("task search group %d hit %d ordinal exceeds total hit count", groupIndex, hitIndex)
			}
			if hitIndex > 0 && group.Hits[hitIndex-1].Ordinal >= hit.Ordinal {
				return fmt.Errorf("task search group %d hit ordinals are not strictly ascending", groupIndex)
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
	case TaskSearchSourceKindShortID:
		if mode != TaskSearchModeLiteral {
			return errors.New("task search Short ID source requires literal mode")
		}
		if h.Source.CommentID != nil {
			return errors.New("task search Short ID source forbids comment id")
		}
	case TaskSearchSourceKindTitle, TaskSearchSourceKindBody:
		if h.Source.CommentID != nil {
			return errors.New("task search title/body source forbids comment id")
		}
	case TaskSearchSourceKindComment:
		if h.Source.CommentID == nil {
			return errors.New("task search comment source requires comment id")
		}
		if err := validateTaskSearchResponseString("comment id", *h.Source.CommentID); err != nil {
			return err
		}
	default:
		return errors.New("task search source kind is invalid")
	}
	if mode == TaskSearchModeLiteral && h.Literal != nil && h.FTS5 == nil {
		if h.Literal.Match == "" {
			return errors.New("task search literal hit match is required")
		}
		return nil
	}
	if mode == TaskSearchModeFTS5 && h.FTS5 != nil && h.Literal == nil {
		if h.FTS5.Snippet == "" {
			return errors.New("task search FTS5 hit snippet is required")
		}
		return nil
	}
	return errors.New("task search hit mode payload is invalid")
}

type TaskSearchErrorReason string

const (
	TaskSearchErrorReasonNormalizedTooShort TaskSearchErrorReason = "normalized_too_short"
)

type TaskSearchError struct {
	Reason TaskSearchErrorReason `json:"reason"`
}

func (e *TaskSearchError) Error() string {
	if e == nil {
		return "task search error"
	}
	return "task search error: " + string(e.Reason)
}

func (e TaskSearchError) Validate() error {
	switch e.Reason {
	case TaskSearchErrorReasonNormalizedTooShort:
		return nil
	default:
		return errors.New("task search error reason is invalid")
	}
}

func (e *TaskSearchError) RPCErrorCode() int {
	return protocol.ErrCodeWorkflowTaskSearch
}

func (e *TaskSearchError) RPCErrorData() json.RawMessage {
	if e == nil || e.Validate() != nil {
		return nil
	}
	return marshalRPCErrorData(struct {
		Type   string                `json:"type"`
		Reason TaskSearchErrorReason `json:"reason"`
	}{
		Type:   "task_search_error",
		Reason: e.Reason,
	})
}

func DecodeTaskSearchError(data json.RawMessage, message string) error {
	var envelope struct {
		Type   string                `json:"type"`
		Reason TaskSearchErrorReason `json:"reason"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Type != "task_search_error" {
		return taskSearchFallbackError(message)
	}
	decoded := &TaskSearchError{Reason: envelope.Reason}
	if err := decoded.Validate(); err != nil {
		return taskSearchFallbackError(message)
	}
	return decoded
}

func taskSearchFieldError(field string, message string) error {
	return WorkflowRequestValidationError{Code: WorkflowRequestErrorInvalidValue, Field: field, Message: message}
}

func taskSearchFallbackError(message string) error {
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		return errors.New(trimmed)
	}
	return errors.New("task search error")
}

func validateTaskSearchResponseString(field string, value string) error {
	if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
		return fmt.Errorf("task search response %s is invalid", field)
	}
	return nil
}

func validateTaskSearchStatus(status WorkflowTaskStatus) error {
	nativeState, valid := status.Kind.NativeState()
	if !valid || status.NativeState != nativeState {
		return errors.New("task search response status is invalid")
	}
	return nil
}
