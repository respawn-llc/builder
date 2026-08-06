package serverapi

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"core/shared/protocol"
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

func TestTaskSearchRequestRequiresNonNegativeOffsetAndCanonicalStatusFilters(t *testing.T) {
	negative := -1
	request := validTaskSearchRequest()
	request.Offset = &negative
	if err := request.Validate(); err == nil {
		t.Fatal("request with a negative offset validated")
	}
	for _, statuses := range [][]WorkflowTaskStatusKind{
		{WorkflowTaskStatusKindDone, WorkflowTaskStatusKindBacklog},
		{WorkflowTaskStatusKindBacklog, WorkflowTaskStatusKindBacklog},
		{"other"},
	} {
		request := validTaskSearchRequest()
		request.StatusKinds = statuses
		if err := request.Validate(); err == nil {
			t.Fatalf("request with invalid status filters %v validated", statuses)
		}
	}
}

func TestTaskSearchPaginationJSONUsesNullableNumericOffsets(t *testing.T) {
	offset := 0
	nextOffset := 100
	request := validTaskSearchRequest()
	request.Offset = &offset
	response := validTaskSearchResponse()
	response.NextOffset = &nextOffset

	requestJSON, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal task search request: %v", err)
	}
	var requestShape map[string]any
	if err := json.Unmarshal(requestJSON, &requestShape); err != nil {
		t.Fatalf("decode task search request: %v", err)
	}
	if requestShape["offset"] != float64(offset) {
		t.Fatalf("request JSON = %s", requestJSON)
	}
	if _, exists := requestShape["page_token"]; exists {
		t.Fatalf("request JSON retains page_token: %s", requestJSON)
	}

	responseJSON, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal task search response: %v", err)
	}
	var responseShape map[string]any
	if err := json.Unmarshal(responseJSON, &responseShape); err != nil {
		t.Fatalf("decode task search response: %v", err)
	}
	if responseShape["next_offset"] != float64(nextOffset) {
		t.Fatalf("response JSON = %s", responseJSON)
	}
	if _, exists := responseShape["next_page_token"]; exists {
		t.Fatalf("response JSON retains next_page_token: %s", responseJSON)
	}
}

func TestTaskSearchRequestPinsDefaultsAndBounds(t *testing.T) {
	if TaskSearchDefaultContext != 20 || TaskSearchMinContext != 1 || TaskSearchMaxContext != 64 {
		t.Fatalf(
			"task search context contract = default %d, range %d..%d",
			TaskSearchDefaultContext,
			TaskSearchMinContext,
			TaskSearchMaxContext,
		)
	}
	if TaskSearchDefaultPageSize != 100 || TaskSearchMaxPageSize != 100 {
		t.Fatalf("task search page-size contract = default %d, max %d", TaskSearchDefaultPageSize, TaskSearchMaxPageSize)
	}
	if err := validTaskSearchRequest().Validate(); err != nil {
		t.Fatalf("explicit default request: %v", err)
	}
	for _, request := range []TaskSearchRequest{
		{Mode: TaskSearchModeLiteral, Query: "needle", Context: TaskSearchMaxContext + 1, PageSize: TaskSearchDefaultPageSize},
		{Mode: TaskSearchModeLiteral, Query: "needle", Context: TaskSearchDefaultContext, PageSize: TaskSearchMaxPageSize + 1},
		{Mode: TaskSearchModeLiteral, Query: strings.Repeat("a", TaskSearchMaxQueryRunes+1), Context: TaskSearchDefaultContext, PageSize: TaskSearchDefaultPageSize},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("out-of-contract request validated: %+v", request)
		}
	}
}

func TestTaskSearchReadContractVersionsArePinned(t *testing.T) {
	if TaskSearchSparseDocumentContractVersion != "kent-task-search-sparse-document-v1" {
		t.Fatalf("sparse document contract version = %q", TaskSearchSparseDocumentContractVersion)
	}
	if TaskSearchRankingContractVersion != "kent-task-search-ranking-v1" {
		t.Fatalf("ranking contract version = %q", TaskSearchRankingContractVersion)
	}
}

func TestTaskSearchRequestRejectsNonCanonicalQueryAndProjectFilters(t *testing.T) {
	for _, request := range []TaskSearchRequest{
		{Mode: TaskSearchModeLiteral, Query: "", Context: TaskSearchDefaultContext, PageSize: TaskSearchDefaultPageSize},
		{Mode: TaskSearchModeLiteral, Query: "\u00a0", Context: TaskSearchDefaultContext, PageSize: TaskSearchDefaultPageSize},
		{Mode: TaskSearchModeLiteral, Query: "needle ", Context: TaskSearchDefaultContext, PageSize: TaskSearchDefaultPageSize},
		{Mode: TaskSearchModeLiteral, Query: "needle", Context: TaskSearchDefaultContext, PageSize: TaskSearchDefaultPageSize, ProjectIDs: []string{"project-a", "project-a"}},
		{Mode: TaskSearchModeLiteral, Query: "needle", Context: TaskSearchDefaultContext, PageSize: TaskSearchDefaultPageSize, ProjectIDs: []string{" project-a"}},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("noncanonical request validated: %+v", request)
		}
	}
	request := validTaskSearchRequest()
	request.Query = "needle\twith\ninterior whitespace"
	if err := request.Validate(); err != nil {
		t.Fatalf("request with preserved interior whitespace: %v", err)
	}
}

func TestTaskSearchResponseJSONAndTaggedHits(t *testing.T) {
	response := TaskSearchResponse{
		Mode: TaskSearchModeLiteral,
		Groups: []TaskSearchGroup{{
			ProjectID:  "project-1",
			ProjectKey: "KNT",
			TaskID:     "task-1",
			ShortID:    "KNT-1",
			WorkflowID: "workflow-1",
			Title:      "Task",
			Status: WorkflowTaskStatus{
				Kind:        WorkflowTaskStatusKindBacklog,
				NativeState: WorkflowTaskNativeStateActive,
			},
			TotalHitCount: 1,
			Hits: []TaskSearchHit{{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindTitle},
				Literal: &TaskSearchLiteralHit{
					Before:        "before ",
					Match:         "needle",
					After:         " after",
					LeftTruncated: true,
				},
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
	if _, exists := shape["next_offset"]; exists {
		t.Fatalf("response unexpectedly has absent next_offset: %s", encoded)
	}
	if _, exists := shape["next_page_token"]; exists {
		t.Fatalf("response retains obsolete next_page_token: %s", encoded)
	}
	if _, exists := shape["hits"]; exists {
		t.Fatalf("response unexpectedly has flat hits: %s", encoded)
	}
	if _, exists := shape["score"]; exists {
		t.Fatalf("response unexpectedly has score: %s", encoded)
	}
	groups := shape["groups"].([]any)
	group := groups[0].(map[string]any)
	if _, exists := group["score"]; exists {
		t.Fatalf("task group unexpectedly has score: %s", encoded)
	}
	hit := group["hits"].([]any)[0].(map[string]any)
	if _, exists := hit["score"]; exists {
		t.Fatalf("task hit unexpectedly has score: %s", encoded)
	}
	if _, exists := hit["fts5"]; exists {
		t.Fatalf("literal task hit unexpectedly has fts5 payload: %s", encoded)
	}
	literal := hit["literal"].(map[string]any)
	for _, field := range []string{"before", "match", "after", "left_truncated", "right_truncated"} {
		if _, exists := literal[field]; !exists {
			t.Fatalf("literal task hit omitted %q: %s", field, encoded)
		}
	}
	var decoded TaskSearchResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal literal task search response: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded literal task search response: %v", err)
	}
	if !reflect.DeepEqual(decoded, response) {
		t.Fatalf("literal response round-trip = %+v, want %+v", decoded, response)
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

func TestTaskSearchResponseRoundTripsRawGroupedJSON(t *testing.T) {
	commentID := "comment-1"
	nextOffset := 7
	response := validTaskSearchResponse()
	response.Mode = TaskSearchModeFTS5
	response.NextOffset = &nextOffset
	response.Groups[0].Hits[0] = TaskSearchHit{
		Ordinal: 1,
		Source:  TaskSearchSource{Kind: TaskSearchSourceKindComment, CommentID: &commentID},
		FTS5:    &TaskSearchFTS5Hit{Snippet: "selected raw FTS5 snippet"},
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal raw task search response: %v", err)
	}
	var decoded TaskSearchResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal raw task search response: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded raw task search response: %v", err)
	}
	if !reflect.DeepEqual(decoded, response) {
		t.Fatalf("raw response round-trip = %+v, want %+v", decoded, response)
	}
}

func TestTaskSearchLiteralShortIDHitRoundTrips(t *testing.T) {
	response := validTaskSearchResponse()
	response.Groups[0].Hits[0] = TaskSearchHit{
		Ordinal: 1,
		Source:  TaskSearchSource{Kind: TaskSearchSourceKindShortID},
		Literal: &TaskSearchLiteralHit{Match: "KNT-1"},
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal Short ID task search response: %v", err)
	}
	var decoded TaskSearchResponse
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal Short ID task search response: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded Short ID task search response: %v", err)
	}
	if !reflect.DeepEqual(decoded, response) {
		t.Fatalf("Short ID response round-trip = %+v, want %+v", decoded, response)
	}
}

func TestTaskSearchShortIDHitRejectsCommentMetadata(t *testing.T) {
	commentID := "comment-1"
	hit := TaskSearchHit{
		Ordinal: 1,
		Source: TaskSearchSource{
			Kind:      TaskSearchSourceKindShortID,
			CommentID: &commentID,
		},
		Literal: &TaskSearchLiteralHit{Match: "KNT-1"},
	}

	if err := hit.Validate(TaskSearchModeLiteral); err == nil {
		t.Fatal("Short ID hit with Comment metadata validated")
	}
}

func TestTaskSearchRawResponseRejectsShortIDHit(t *testing.T) {
	hit := TaskSearchHit{
		Ordinal: 1,
		Source:  TaskSearchSource{Kind: TaskSearchSourceKindShortID},
		FTS5:    &TaskSearchFTS5Hit{Snippet: "KNT-1"},
	}

	if err := hit.Validate(TaskSearchModeFTS5); err == nil {
		t.Fatal("raw FTS5 Short ID hit validated")
	}
}

func TestTaskSearchErrorJSONRoundTripsEveryTypedReason(t *testing.T) {
	for _, reason := range []TaskSearchErrorReason{
		TaskSearchErrorReasonNormalizedTooShort,
	} {
		source := TaskSearchError{Reason: reason}
		if err := source.Validate(); err != nil {
			t.Fatalf("source error %q: %v", reason, err)
		}
		encoded, err := json.Marshal(source)
		if err != nil {
			t.Fatalf("marshal task search error %q: %v", reason, err)
		}
		var decoded TaskSearchError
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal task search error %q: %v", reason, err)
		}
		if err := decoded.Validate(); err != nil || decoded.Reason != reason {
			t.Fatalf("decoded task search error = %+v / %v, want reason %q", decoded, err, reason)
		}
	}
	if err := (TaskSearchError{Reason: "other"}).Validate(); err == nil {
		t.Fatal("unknown task search error reason validated")
	}
}

func TestTaskSearchErrorRPCRoundTripsEveryTypedReason(t *testing.T) {
	for _, reason := range []TaskSearchErrorReason{
		TaskSearchErrorReasonNormalizedTooShort,
	} {
		source := &TaskSearchError{Reason: reason}
		decodedErr := DecodeTaskSearchError(mustRPCErrorData(t, source), source.Error())
		var decoded *TaskSearchError
		if !errors.As(decodedErr, &decoded) {
			t.Fatalf("decoded error = %T %v, want TaskSearchError", decodedErr, decodedErr)
		}
		if decoded.Reason != reason {
			t.Fatalf("decoded reason = %q, want %q", decoded.Reason, reason)
		}
		if decoded.RPCErrorCode() != protocol.ErrCodeWorkflowTaskSearch {
			t.Fatalf("error code = %d, want %d", decoded.RPCErrorCode(), protocol.ErrCodeWorkflowTaskSearch)
		}
	}
	for _, payload := range []string{
		`{"type":"task_search_error","reason":"other"}`,
		`{"type":"other","reason":"invalid_cursor"}`,
	} {
		decodedErr := DecodeTaskSearchError(json.RawMessage(payload), "fallback")
		var typed *TaskSearchError
		if errors.As(decodedErr, &typed) {
			t.Fatalf("invalid payload decoded as typed TaskSearchError: %+v", typed)
		}
	}
}

func TestTaskSearchResponseRejectsGroupWithoutRequiredIdentity(t *testing.T) {
	response := TaskSearchResponse{
		Mode: TaskSearchModeLiteral,
		Groups: []TaskSearchGroup{{
			ProjectKey:    "KNT",
			TaskID:        "task-1",
			ShortID:       "KNT-1",
			WorkflowID:    "workflow-1",
			Title:         "Task",
			Status:        WorkflowTaskStatus{Kind: WorkflowTaskStatusKindBacklog, NativeState: WorkflowTaskNativeStateActive},
			TotalHitCount: 1,
			Hits: []TaskSearchHit{{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindTitle},
				Literal: &TaskSearchLiteralHit{Match: "needle"},
			}},
		}},
	}
	if err := response.Validate(); err == nil {
		t.Fatal("response without project id validated")
	}
}

func TestTaskSearchResponseRequiresEveryGroupIdentityField(t *testing.T) {
	for _, mutate := range []func(*TaskSearchGroup){
		func(group *TaskSearchGroup) { group.ProjectKey = "" },
		func(group *TaskSearchGroup) { group.TaskID = "" },
		func(group *TaskSearchGroup) { group.ShortID = "" },
		func(group *TaskSearchGroup) { group.WorkflowID = "" },
		func(group *TaskSearchGroup) { group.Title = "" },
	} {
		response := validTaskSearchResponse()
		mutate(&response.Groups[0])
		if err := response.Validate(); err == nil {
			t.Fatalf("response with missing group field validated: %+v", response.Groups[0])
		}
	}
}

func TestTaskSearchResponseRequiresCanonicalTaskStatus(t *testing.T) {
	for _, status := range []WorkflowTaskStatus{
		{},
		{Kind: WorkflowTaskStatusKindBacklog},
		{Kind: WorkflowTaskStatusKindBacklog, NativeState: WorkflowTaskNativeStateRunning},
		{Kind: "other", NativeState: WorkflowTaskNativeStateActive},
	} {
		response := validTaskSearchResponse()
		response.Groups[0].Status = status
		if err := response.Validate(); err == nil {
			t.Fatalf("response with invalid status validated: %+v", status)
		}
	}
}

func TestTaskSearchResponseRequiresOrderedInRangeGroupHitOrdinals(t *testing.T) {
	for _, mutate := range []func(*TaskSearchGroup){
		func(group *TaskSearchGroup) { group.Hits = nil },
		func(group *TaskSearchGroup) { group.Hits = []TaskSearchHit{} },
		func(group *TaskSearchGroup) { group.Hits[0].Ordinal = 2 },
		func(group *TaskSearchGroup) {
			group.TotalHitCount = 2
			group.Hits = append(group.Hits, TaskSearchHit{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindBody},
				Literal: &TaskSearchLiteralHit{Match: "needle"},
			})
			group.Hits[0].Ordinal = 2
		},
		func(group *TaskSearchGroup) {
			group.TotalHitCount = 2
			group.Hits = append(group.Hits, TaskSearchHit{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindBody},
				Literal: &TaskSearchLiteralHit{Match: "needle"},
			})
		},
	} {
		response := validTaskSearchResponse()
		mutate(&response.Groups[0])
		if err := response.Validate(); err == nil {
			t.Fatalf("response with invalid group hit ordinals validated: %+v", response.Groups[0])
		}
	}
}

func TestTaskSearchResponseRequiresUniqueTaskGroups(t *testing.T) {
	response := validTaskSearchResponse()
	response.Groups = append(response.Groups, response.Groups[0])
	if err := response.Validate(); err == nil {
		t.Fatal("response with duplicate task group validated")
	}
}

func TestTaskSearchResponseRejectsInvalidNextOffset(t *testing.T) {
	for _, offset := range []int{-1, 0} {
		response := validTaskSearchResponse()
		response.NextOffset = &offset
		if err := response.Validate(); err == nil {
			t.Fatalf("response with invalid next offset %d validated", offset)
		}
	}
}

func TestTaskSearchHitRequiresModePayloadContentsAndExactCommentID(t *testing.T) {
	commentID := " comment-1"
	for _, test := range []struct {
		hit  TaskSearchHit
		mode TaskSearchMode
	}{
		{
			hit: TaskSearchHit{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindTitle},
				Literal: &TaskSearchLiteralHit{},
			},
			mode: TaskSearchModeLiteral,
		},
		{
			hit: TaskSearchHit{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindTitle},
				FTS5:    &TaskSearchFTS5Hit{},
			},
			mode: TaskSearchModeFTS5,
		},
		{
			hit: TaskSearchHit{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindComment, CommentID: &commentID},
				Literal: &TaskSearchLiteralHit{Match: "needle"},
			},
			mode: TaskSearchModeLiteral,
		},
	} {
		if err := test.hit.Validate(test.mode); err == nil {
			t.Fatalf("invalid hit validated: %+v", test.hit)
		}
	}
}

func TestTaskSearchHitAcceptsAllSourceVariantsAndRejectsWrongModePayload(t *testing.T) {
	commentID := "comment-1"
	for _, test := range []struct {
		hit  TaskSearchHit
		mode TaskSearchMode
	}{
		{
			hit: TaskSearchHit{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindTitle},
				Literal: &TaskSearchLiteralHit{Match: "title"},
			},
			mode: TaskSearchModeLiteral,
		},
		{
			hit: TaskSearchHit{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindBody},
				Literal: &TaskSearchLiteralHit{Match: "body"},
			},
			mode: TaskSearchModeLiteral,
		},
		{
			hit: TaskSearchHit{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindComment, CommentID: &commentID},
				FTS5:    &TaskSearchFTS5Hit{Snippet: "comment"},
			},
			mode: TaskSearchModeFTS5,
		},
	} {
		if err := test.hit.Validate(test.mode); err != nil {
			t.Fatalf("valid source variant rejected: %+v / %v", test.hit, err)
		}
	}
	for _, test := range []struct {
		hit  TaskSearchHit
		mode TaskSearchMode
	}{
		{
			hit: TaskSearchHit{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindTitle},
				FTS5:    &TaskSearchFTS5Hit{Snippet: "raw"},
			},
			mode: TaskSearchModeLiteral,
		},
		{
			hit: TaskSearchHit{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindBody},
				Literal: &TaskSearchLiteralHit{Match: "literal"},
			},
			mode: TaskSearchModeFTS5,
		},
		{
			hit: TaskSearchHit{
				Ordinal: 0,
				Source:  TaskSearchSource{Kind: "other"},
				Literal: &TaskSearchLiteralHit{Match: "literal"},
			},
			mode: TaskSearchModeLiteral,
		},
	} {
		if err := test.hit.Validate(test.mode); err == nil {
			t.Fatalf("invalid source/mode variant validated: %+v", test.hit)
		}
	}
}

func validTaskSearchResponse() TaskSearchResponse {
	return TaskSearchResponse{
		Mode: TaskSearchModeLiteral,
		Groups: []TaskSearchGroup{{
			ProjectID:  "project-1",
			ProjectKey: "KNT",
			TaskID:     "task-1",
			ShortID:    "KNT-1",
			WorkflowID: "workflow-1",
			Title:      "Task",
			Status: WorkflowTaskStatus{
				Kind:        WorkflowTaskStatusKindBacklog,
				NativeState: WorkflowTaskNativeStateActive,
			},
			TotalHitCount: 1,
			Hits: []TaskSearchHit{{
				Ordinal: 1,
				Source:  TaskSearchSource{Kind: TaskSearchSourceKindTitle},
				Literal: &TaskSearchLiteralHit{Match: "needle"},
			}},
		}},
	}
}

func validTaskSearchRequest() TaskSearchRequest {
	return TaskSearchRequest{
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
}
