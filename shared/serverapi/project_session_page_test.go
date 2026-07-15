package serverapi

import (
	"encoding/json"
	"testing"
	"time"

	"core/shared/clientui"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

func TestSessionPageRequestPositionsValidateAndRoundTrip(t *testing.T) {
	continuation, err := ParseSessionPageContinuation("opaque-token")
	if err != nil {
		t.Fatalf("ParseSessionPageContinuation: %v", err)
	}
	tests := []struct {
		name     string
		position SessionPagePosition
		kind     SessionPagePositionKind
		hasToken bool
	}{
		{name: "newest", position: NewestSessionPagePosition(), kind: SessionPagePositionNewest},
		{name: "older", position: OlderSessionPagePosition(continuation), kind: SessionPagePositionOlder, hasToken: true},
		{name: "newer", position: NewerSessionPagePosition(continuation), kind: SessionPagePositionNewer, hasToken: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := SessionPageRequest{
				ProjectID: "project-1",
				Category:  sessioncontract.SessionCategoryMain,
				PageSize:  20,
				Position:  test.position,
			}
			if err := request.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var decoded SessionPageRequest
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if err := decoded.Validate(); err != nil {
				t.Fatalf("decoded Validate: %v", err)
			}
			if decoded.Position.Kind() != test.kind {
				t.Fatalf("position kind = %q, want %q", decoded.Position.Kind(), test.kind)
			}
			token, ok := decoded.Position.Continuation()
			if ok != test.hasToken {
				t.Fatalf("position continuation present = %v, want %v", ok, test.hasToken)
			}
			if ok && token.String() != continuation.String() {
				t.Fatalf("position continuation = %q, want %q", token.String(), continuation.String())
			}
		})
	}
}

func TestSessionPageRequestRejectsMalformedDiscriminatedPositions(t *testing.T) {
	for _, body := range []string{
		`{"project_id":"project-1","category":"main","page_size":20}`,
		`{"project_id":"project-1","category":"main","page_size":20,"position":{"kind":""}}`,
		`{"project_id":"project-1","category":"main","page_size":20,"position":{"kind":"unknown"}}`,
		`{"project_id":"project-1","category":"main","page_size":20,"position":{"kind":"newest","token":"unexpected"}}`,
		`{"project_id":"project-1","category":"main","page_size":20,"position":{"kind":"older"}}`,
		`{"project_id":"project-1","category":"main","page_size":20,"position":{"kind":"older","token":""}}`,
		`{"project_id":"project-1","category":"main","page_size":20,"position":{"kind":"newer","token":" token"}}`,
	} {
		t.Run(body, func(t *testing.T) {
			var request SessionPageRequest
			if err := json.Unmarshal([]byte(body), &request); err == nil {
				if err := request.Validate(); err == nil {
					t.Fatalf("request accepted malformed position: %s", body)
				}
			}
		})
	}
	for _, raw := range []string{"", " ", " token", "token "} {
		if _, err := ParseSessionPageContinuation(raw); err == nil {
			t.Fatalf("ParseSessionPageContinuation(%q) succeeded", raw)
		}
	}
}

func TestSessionPageResponseUsesOptionalValidatedContinuations(t *testing.T) {
	sessionID, err := runtimeids.ParseSessionID("session-1")
	if err != nil {
		t.Fatalf("ParseSessionID: %v", err)
	}
	older, err := ParseSessionPageContinuation("older-token")
	if err != nil {
		t.Fatalf("parse older continuation: %v", err)
	}
	response := SessionPageResponse{
		ProjectID: "project-1",
		Category:  sessioncontract.SessionCategoryMain,
		Sessions: []clientui.SessionSummary{{
			SessionID: sessionID,
			Category:  sessioncontract.SessionCategoryMain,
			UpdatedAt: time.Unix(1, 0).UTC(),
		}},
		Older: &older,
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode response wire: %v", err)
	}
	if _, ok := wire["older"]; !ok {
		t.Fatal("older continuation missing")
	}
	if _, ok := wire["newer"]; ok {
		t.Fatal("absent newer continuation was encoded")
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
