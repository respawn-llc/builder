package serverapi

import (
	"encoding/json"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

func TestSessionPageRequestUsesOptionalOffsetWindow(t *testing.T) {
	request := SessionPageRequest{
		ProjectID: "project-1",
		Category:  sessioncontract.SessionCategoryMain,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate defaults: %v", err)
	}
	window, err := request.ResolveWindow()
	if err != nil {
		t.Fatalf("ResolveWindow defaults: %v", err)
	}
	if window.Offset != 0 || window.Limit != MaxSessionPageSize {
		t.Fatalf("default window = %+v, want offset 0 and limit %d", window, MaxSessionPageSize)
	}

	offset := 0
	limit := 20
	request.Offset = &offset
	request.Limit = &limit
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate explicit window: %v", err)
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatalf("decode request shape: %v", err)
	}
	if _, ok := shape["offset"]; !ok {
		t.Fatalf("offset missing from %s", encoded)
	}
	if _, ok := shape["limit"]; !ok {
		t.Fatalf("limit missing from %s", encoded)
	}
	for _, obsolete := range []string{"page_size", "position", "token"} {
		if _, ok := shape[obsolete]; ok {
			t.Fatalf("obsolete field %q encoded in %s", obsolete, encoded)
		}
	}

	var decoded SessionPageRequest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("decoded Validate: %v", err)
	}
	decodedWindow, err := decoded.ResolveWindow()
	if err != nil {
		t.Fatalf("decoded ResolveWindow: %v", err)
	}
	if decodedWindow.Offset != offset || decodedWindow.Limit != limit {
		t.Fatalf("decoded window = %+v, want offset %d and limit %d", decodedWindow, offset, limit)
	}
}

func TestSessionPageRequestRejectsInvalidOffsetWindow(t *testing.T) {
	negativeOffset := -1
	zeroLimit := 0
	overLimit := MaxSessionPageSize + 1
	for _, test := range []struct {
		name   string
		offset *int
		limit  *int
	}{
		{name: "negative offset", offset: &negativeOffset},
		{name: "zero limit", limit: &zeroLimit},
		{name: "limit above maximum", limit: &overLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := SessionPageRequest{
				ProjectID: "project-1",
				Category:  sessioncontract.SessionCategoryMain,
				Offset:    test.offset,
				Limit:     test.limit,
			}
			if err := request.Validate(); err == nil {
				t.Fatalf("Validate accepted request %+v", request)
			}
		})
	}
}

func TestSessionPageRequestStrictDecodeRejectsObsoleteFields(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{
			name: "old only",
			body: `{"project_id":"project-1","category":"main","page_size":20,"position":{"kind":"newest"}}`,
		},
		{
			name: "mixed position and offset",
			body: `{"project_id":"project-1","category":"main","offset":0,"limit":20,"position":{"kind":"newest"}}`,
		},
		{
			name: "mixed token and offset",
			body: `{"project_id":"project-1","category":"main","offset":20,"limit":20,"token":"opaque"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request SessionPageRequest
			if err := json.Unmarshal([]byte(test.body), &request); err == nil {
				t.Fatalf("Unmarshal accepted obsolete fields: %s", test.body)
			}
		})
	}
}

func TestSessionPageResponseUsesOptionalPositiveNextOffset(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	nextOffset := 20
	response := SessionPageResponse{
		ProjectID: "project-1",
		Category:  sessioncontract.SessionCategoryMain,
		Sessions: []clientui.SessionSummary{{
			SessionID: sessionID,
			Category:  sessioncontract.SessionCategoryMain,
			UpdatedAt: time.Unix(1, 0).UTC(),
		}},
		NextOffset: &nextOffset,
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatalf("decode response shape: %v", err)
	}
	if _, ok := shape["next_offset"]; !ok {
		t.Fatalf("next_offset missing from %s", encoded)
	}
	for _, obsolete := range []string{"older", "newer", "continuation", "token"} {
		if _, ok := shape[obsolete]; ok {
			t.Fatalf("obsolete field %q encoded in %s", obsolete, encoded)
		}
	}

	response.NextOffset = nil
	encoded, err = json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal terminal response: %v", err)
	}
	shape = nil
	if err := json.Unmarshal(encoded, &shape); err != nil {
		t.Fatalf("decode terminal response shape: %v", err)
	}
	if _, ok := shape["next_offset"]; ok {
		t.Fatalf("absent next offset encoded in %s", encoded)
	}

	for _, invalid := range []int{0, -1} {
		response.NextOffset = &invalid
		if err := response.Validate(); err == nil {
			t.Fatalf("Validate accepted next offset %d", invalid)
		}
	}
}

func TestSessionPageResponseStrictDecodeRejectsObsoleteFields(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{
			name: "old only",
			body: `{"project_id":"project-1","category":"main","sessions":[],"older":"opaque"}`,
		},
		{
			name: "mixed older and next offset",
			body: `{"project_id":"project-1","category":"main","sessions":[],"next_offset":20,"older":"opaque"}`,
		},
		{
			name: "mixed newer and next offset",
			body: `{"project_id":"project-1","category":"main","sessions":[],"next_offset":20,"newer":"opaque"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response SessionPageResponse
			if err := json.Unmarshal([]byte(test.body), &response); err == nil {
				t.Fatalf("Unmarshal accepted obsolete fields: %s", test.body)
			}
		})
	}
}

func TestSessionPageResponseRejectsInvalidSummaryRecencyAndMembership(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	for _, test := range []struct {
		name      string
		category  sessioncontract.SessionCategory
		updatedAt time.Time
	}{
		{name: "zero recency", category: sessioncontract.SessionCategoryMain},
		{name: "epoch recency", category: sessioncontract.SessionCategoryMain, updatedAt: time.Unix(0, 0).UTC()},
		{name: "cross category", category: sessioncontract.SessionCategorySubagent, updatedAt: time.Unix(1, 0).UTC()},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := SessionPageResponse{
				ProjectID: "project-1",
				Category:  sessioncontract.SessionCategoryMain,
				Sessions: []clientui.SessionSummary{{
					SessionID: sessionID,
					Category:  test.category,
					UpdatedAt: test.updatedAt,
				}},
			}
			if err := response.Validate(); err == nil {
				t.Fatalf("response accepted invalid summary: %+v", response.Sessions[0])
			}
		})
	}
}
