package serverapi

import (
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
	response.NextOffset = nil
	if err := response.Validate(); err != nil {
		t.Fatalf("Validate terminal response: %v", err)
	}

	for _, invalid := range []int{0, -1} {
		response.NextOffset = &invalid
		if err := response.Validate(); err == nil {
			t.Fatalf("Validate accepted next offset %d", invalid)
		}
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
