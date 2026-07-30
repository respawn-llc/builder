package serverapi

import (
	"encoding/json"
	"testing"
)

func TestResolveWorkflowOffsetWindowDefaultsAndValidatesBounds(t *testing.T) {
	window, err := ResolveWorkflowOffsetWindow(nil, nil)
	if err != nil {
		t.Fatalf("ResolveWorkflowOffsetWindow defaults: %v", err)
	}
	if window.Offset != 0 || window.Limit != WorkflowPaginationMaxLimit {
		t.Fatalf("default window = %+v, want offset 0 and limit %d", window, WorkflowPaginationMaxLimit)
	}

	zero := 0
	limit := 1
	window, err = ResolveWorkflowOffsetWindow(&zero, &limit)
	if err != nil {
		t.Fatalf("ResolveWorkflowOffsetWindow explicit values: %v", err)
	}
	if window.Offset != zero || window.Limit != limit {
		t.Fatalf("explicit window = %+v, want offset %d and limit %d", window, zero, limit)
	}

	negativeOffset := -1
	zeroLimit := 0
	overLimit := WorkflowPaginationMaxLimit + 1
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
			_, err := ResolveWorkflowOffsetWindow(test.offset, test.limit)
			if !hasWorkflowRequestError(err, test.field, WorkflowRequestErrorInvalidMode) {
				t.Fatalf("ResolveWorkflowOffsetWindow error = %v, want typed %s error", err, test.field)
			}
		})
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
