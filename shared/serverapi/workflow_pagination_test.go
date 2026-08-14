package serverapi

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestResolveOffsetWindowDefaultsAndValidatesBounds(t *testing.T) {
	window, err := ResolveOffsetWindow(nil, nil)
	if err != nil {
		t.Fatalf("ResolveOffsetWindow defaults: %v", err)
	}
	if window.Offset != 0 || window.Limit != OffsetPaginationMaxLimit {
		t.Fatalf("default window = %+v, want offset 0 and limit %d", window, OffsetPaginationMaxLimit)
	}

	zero := 0
	limit := 1
	window, err = ResolveOffsetWindow(&zero, &limit)
	if err != nil {
		t.Fatalf("ResolveOffsetWindow explicit values: %v", err)
	}
	if window.Offset != zero || window.Limit != limit {
		t.Fatalf("explicit window = %+v, want offset %d and limit %d", window, zero, limit)
	}

	negativeOffset := -1
	zeroLimit := 0
	overLimit := OffsetPaginationMaxLimit + 1
	for _, test := range []struct {
		name   string
		offset *int
		limit  *int
		field  string
	}{
		{name: "negative offset", offset: &negativeOffset, field: "offset"},
		{name: "zero limit", limit: &zeroLimit, field: "limit"},
		{name: "limit above maximum", limit: &overLimit, field: "limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ResolveOffsetWindow(test.offset, test.limit)
			var windowError *offsetWindowError
			if !errors.As(err, &windowError) || windowError.Field != test.field {
				t.Fatalf("ResolveOffsetWindow error = %v, want %s OffsetWindowError", err, test.field)
			}
		})
	}
}

func TestOffsetWindowFromValidatedMaterializesCanonicalWindow(t *testing.T) {
	offset := 7
	limit := 12
	if got := OffsetWindowFromValidated(nil, nil); got != (OffsetWindow{Limit: OffsetPaginationMaxLimit}) {
		t.Fatalf("default window = %+v", got)
	}
	if got := OffsetWindowFromValidated(&offset, &limit); got != (OffsetWindow{Offset: offset, Limit: limit}) {
		t.Fatalf("explicit window = %+v", got)
	}
}

func TestResolveWorkflowOffsetWindowPreservesWorkflowErrors(t *testing.T) {
	negativeOffset := -1
	_, err := ResolveWorkflowOffsetWindow(&negativeOffset, nil)
	if !hasWorkflowRequestError(err, "offset", WorkflowRequestErrorInvalidMode) {
		t.Fatalf("ResolveWorkflowOffsetWindow error = %v, want typed offset error", err)
	}
}

func TestWorkflowListPaginationJSONContractUsesNullableOffsets(t *testing.T) {
	offset := 0
	limit := 25
	nextOffset := 25
	request := WorkflowListRequest{Offset: &offset, Limit: &limit}
	response := WorkflowListResponse{NextOffset: &nextOffset}

	requestJSON, requestShape := marshalWorkflowJSON[map[string]any](t, request)
	if requestShape["offset"] != float64(offset) || requestShape["limit"] != float64(limit) {
		t.Fatalf("request JSON = %s", requestJSON)
	}
	if _, exists := requestShape["page_token"]; exists {
		t.Fatalf("request JSON retains page token: %s", requestJSON)
	}

	responseJSON, responseShape := marshalWorkflowJSON[map[string]any](t, response)
	if responseShape["next_offset"] != float64(nextOffset) {
		t.Fatalf("response JSON = %s", responseJSON)
	}
	if _, exists := responseShape["next_page_token"]; exists {
		t.Fatalf("response JSON retains next page token: %s", responseJSON)
	}

	absentJSON, err := json.Marshal(WorkflowListResponse{})
	if err != nil {
		t.Fatalf("marshal absent response: %v", err)
	}
	var absentShape map[string]any
	if err := json.Unmarshal(absentJSON, &absentShape); err != nil {
		t.Fatalf("decode absent response: %v", err)
	}
	if _, exists := absentShape["next_offset"]; exists {
		t.Fatalf("absent next offset encoded as a value: %s", absentJSON)
	}
}

func TestWorkflowTaskOffsetPageJSONContractUsesItemsAndNextOffset(t *testing.T) {
	offset := 25
	limit := 50
	request := WorkflowTaskOffsetPageRequest{TaskID: "task-1", Offset: &offset, Limit: &limit}
	nextOffset := 75
	response := WorkflowOffsetPage[int]{
		Items:      []int{1, 2},
		NextOffset: &nextOffset,
	}

	requestJSON, requestShape := marshalWorkflowJSON[map[string]any](t, request)
	if requestShape["task_id"] != "task-1" ||
		requestShape["offset"] != float64(offset) ||
		requestShape["limit"] != float64(limit) {
		t.Fatalf("request JSON = %s", requestJSON)
	}

	responseJSON, responseShape := marshalWorkflowJSON[map[string]any](t, response)
	if len(responseShape["items"].([]any)) != 2 || responseShape["next_offset"] != float64(nextOffset) {
		t.Fatalf("response JSON = %s", responseJSON)
	}

	emptyJSON, emptyShape := marshalWorkflowJSON[map[string]any](t, WorkflowOffsetPage[int]{})
	if _, exists := emptyShape["comments"]; exists {
		t.Fatalf("response retains endpoint-specific comments field: %s", emptyJSON)
	}
	if _, exists := emptyShape["next_offset"]; exists {
		t.Fatalf("empty response encoded a next offset: %s", emptyJSON)
	}

	commentResponseJSON, commentResponseShape := marshalWorkflowJSON[map[string]any](t, WorkflowTaskCommentListResponse{
		WorkflowOffsetPage: WorkflowOffsetPage[WorkflowTaskComment]{
			Items: []WorkflowTaskComment{{ID: "comment-1"}},
		},
		TotalCount: 1,
	})
	if _, exists := commentResponseShape["comments"]; exists {
		t.Fatalf("comment response retains old collection key: %s", commentResponseJSON)
	}
	if len(commentResponseShape["items"].([]any)) != 1 {
		t.Fatalf("comment response = %s", commentResponseJSON)
	}
	if commentResponseShape["total_count"] != float64(1) {
		t.Fatalf("comment response total count = %s", commentResponseJSON)
	}

	activityResponseJSON, activityResponseShape := marshalWorkflowJSON[map[string]any](t, WorkflowTaskActivityListResponse{
		WorkflowOffsetPage: WorkflowOffsetPage[WorkflowTaskActivityItem]{
			Items: []WorkflowTaskActivityItem{{
				Type:   "session_started",
				TaskID: "task-1",
				SessionStarted: &WorkflowTaskSessionStarted{
					SessionID: "session-1",
					Name:      "Session",
				},
			}},
		},
	})
	if _, exists := activityResponseShape["next_page_token"]; exists {
		t.Fatalf("activity response retains next page token: %s", activityResponseJSON)
	}
	if _, exists := activityResponseShape["generated_at_unix_ms"]; exists {
		t.Fatalf("activity response retains generated timestamp: %s", activityResponseJSON)
	}
}

func TestTrimOffsetLookaheadCalculatesNextOffset(t *testing.T) {
	nextOffset := 2
	items, next := TrimOffsetLookahead(OffsetWindow{Offset: 0, Limit: 2}, []string{"a", "b", "c"})
	if len(items) != 2 || items[0] != "a" || items[1] != "b" ||
		next == nil || *next != nextOffset {
		t.Fatalf("items = %v, next = %v", items, next)
	}

	items, next = TrimOffsetLookahead(OffsetWindow{Offset: 2, Limit: 2}, []string{"c"})
	if len(items) != 1 || next != nil {
		t.Fatalf("terminal items = %v, next = %v", items, next)
	}
}

func TestFinalizeWorkflowOffsetPageUsesSharedLookahead(t *testing.T) {
	nextOffset := 2
	page := FinalizeWorkflowOffsetPage(WorkflowOffsetWindow{Offset: 0, Limit: 2}, []string{"a", "b", "c"})
	if len(page.Items) != 2 || page.NextOffset == nil || *page.NextOffset != nextOffset {
		t.Fatalf("page = %+v", page)
	}
}
