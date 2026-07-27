package serverapi

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestTaskSearchRequestValidation(t *testing.T) {
	valid := TaskSearchRequest{
		Mode:       TaskSearchModeLiteral,
		Query:      "needle",
		Context:    TaskSearchDefaultContext,
		PageSize:   TaskSearchDefaultPageSize,
		ProjectIDs: []string{"project-a", "project-b"},
		StatusKinds: []WorkflowTaskStatusKind{
			WorkflowTaskStatusKindBacklog,
			WorkflowTaskStatusKindDone,
		},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid literal search request: %v", err)
	}
	if err := (TaskSearchRequest{
		Mode:     TaskSearchModeFTS5,
		Query:    "a",
		Context:  TaskSearchDefaultContext,
		PageSize: TaskSearchDefaultPageSize,
	}).Validate(); err != nil {
		t.Fatalf("valid raw FTS5 request: %v", err)
	}

	for _, request := range []TaskSearchRequest{
		{Mode: TaskSearchModeLiteral, Query: "ab", Context: 20, PageSize: 100},
		{Mode: TaskSearchModeLiteral, Query: "\u0301\u0301\u0301", Context: 20, PageSize: 100},
	} {
		var searchErr *TaskSearchError
		if err := request.Validate(); !errors.As(err, &searchErr) || searchErr.Reason != TaskSearchErrorReasonNormalizedTooShort {
			t.Fatalf("too-short request error = %v", err)
		}
	}

	invalid := []TaskSearchRequest{
		{Mode: "other", Query: "needle", Context: 20, PageSize: 100},
		{Mode: TaskSearchModeLiteral, Query: " needle", Context: 20, PageSize: 100},
		{Mode: TaskSearchModeFTS5, Query: "needle", Context: 20, PageSize: 100, CaseSensitive: true},
		{Mode: TaskSearchModeLiteral, Query: "needle", Context: 0, PageSize: 100},
		{Mode: TaskSearchModeLiteral, Query: "needle", Context: 20, PageSize: 0},
		{Mode: TaskSearchModeLiteral, Query: "needle", Context: 20, PageSize: 100, ProjectIDs: []string{"project-b", "project-a"}},
	}
	for _, request := range invalid {
		if err := request.Validate(); err == nil {
			t.Fatalf("invalid task search request accepted: %+v", request)
		}
	}
}

func TestTaskSearchResponseJSONAndTaggedHits(t *testing.T) {
	response := TaskSearchResponse{
		Mode: TaskSearchModeLiteral,
		Groups: []TaskSearchGroup{{
			ProjectID:     "project-1",
			ProjectKey:    "KNT",
			TaskID:        "task-1",
			ShortID:       "KNT-1",
			WorkflowID:    "workflow-1",
			Title:         "Task",
			Status:        WorkflowTaskStatus{Kind: WorkflowTaskStatusKindBacklog},
			TotalHitCount: 1,
			Hits: []TaskSearchHit{{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindTitle},
				Literal: &TaskSearchLiteralHit{Match: "needle"},
			}},
		}},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("valid literal response: %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal task search response: %v", err)
	}
	var shape map[string]any
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatalf("unmarshal task search response: %v", err)
	}
	if _, exists := shape["next_page_token"]; exists {
		t.Fatalf("response unexpectedly has absent next_page_token: %s", encoded)
	}
	if _, exists := shape["hits"]; exists {
		t.Fatalf("response unexpectedly has flat hits: %s", encoded)
	}
	if _, exists := shape["score"]; exists {
		t.Fatalf("response unexpectedly has score: %s", encoded)
	}

	empty := TaskSearchResponse{Mode: TaskSearchModeFTS5, Groups: []TaskSearchGroup{}}
	if err := empty.Validate(); err != nil {
		t.Fatalf("empty response: %v", err)
	}
	if encoded, err := json.Marshal(empty); err != nil || string(encoded) != `{"mode":"fts5","groups":[]}` {
		t.Fatalf("empty response JSON = %s / %v", encoded, err)
	}
	if err := (TaskSearchResponse{Mode: TaskSearchModeLiteral}).Validate(); err == nil {
		t.Fatal("response with absent groups validated")
	}

	invalid := TaskSearchHit{
		Ordinal: 1,
		Source:  TaskSearchSource{Kind: TaskSearchSourceKindTitle},
		Literal: &TaskSearchLiteralHit{},
		FTS5:    &TaskSearchFTS5Hit{},
	}
	if err := invalid.Validate(TaskSearchModeLiteral); err == nil {
		t.Fatal("hit with both payloads validated")
	}
	if err := (TaskSearchHit{
		Ordinal: 1,
		Source:  TaskSearchSource{Kind: TaskSearchSourceKindComment},
		FTS5:    &TaskSearchFTS5Hit{},
	}).Validate(TaskSearchModeFTS5); err == nil {
		t.Fatal("comment hit without comment_id validated")
	}
}
