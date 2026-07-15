package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/runtimeids"
)

func TestSessionLaunchIntentVariantsValidateAndRoundTrip(t *testing.T) {
	parent := mustSessionLaunchIntentID(t, "parent-session")
	target := mustSessionLaunchIntentID(t, "target-session")
	tests := []struct {
		name       string
		intent     SessionLaunchIntent
		kind       SessionLaunchIntentKind
		parentID   *runtimeids.SessionID
		sessionID  *runtimeids.SessionID
		jsonFields map[string]any
	}{
		{
			name:       "create without parent",
			intent:     CreateNewSessionLaunchIntent(nil),
			kind:       SessionLaunchIntentCreateNew,
			jsonFields: map[string]any{"kind": "create_new"},
		},
		{
			name:       "create with parent",
			intent:     CreateNewSessionLaunchIntent(&parent),
			kind:       SessionLaunchIntentCreateNew,
			parentID:   &parent,
			jsonFields: map[string]any{"kind": "create_new", "parent_id": parent.String()},
		},
		{
			name:       "open existing",
			intent:     OpenExistingSessionLaunchIntent(target),
			kind:       SessionLaunchIntentOpenExisting,
			sessionID:  &target,
			jsonFields: map[string]any{"kind": "open_existing", "session_id": target.String()},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.intent.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			data, err := json.Marshal(test.intent)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var fields map[string]any
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatalf("Unmarshal fields: %v", err)
			}
			if len(fields) != len(test.jsonFields) {
				t.Fatalf("JSON fields = %+v, want %+v", fields, test.jsonFields)
			}
			for key, want := range test.jsonFields {
				if got := fields[key]; got != want {
					t.Fatalf("JSON field %q = %#v, want %#v", key, got, want)
				}
			}

			var decoded SessionLaunchIntent
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Unmarshal intent: %v", err)
			}
			if decoded.Kind() != test.kind {
				t.Fatalf("Kind = %q, want %q", decoded.Kind(), test.kind)
			}
			parentID, hasParent := decoded.ParentID()
			if test.parentID == nil {
				if hasParent {
					t.Fatalf("unexpected parent ID %q", parentID.String())
				}
			} else if !hasParent || parentID != *test.parentID {
				t.Fatalf("ParentID = %q/%v, want %q", parentID.String(), hasParent, test.parentID.String())
			}
			sessionID, hasSession := decoded.SessionID()
			if test.sessionID == nil {
				if hasSession {
					t.Fatalf("unexpected session ID %q", sessionID.String())
				}
			} else if !hasSession || sessionID != *test.sessionID {
				t.Fatalf("SessionID = %q/%v, want %q", sessionID.String(), hasSession, test.sessionID.String())
			}
		})
	}
}

func TestSessionLaunchIntentRejectsMalformedAndLegacyShapes(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"kind":"unknown"}`,
		`{"kind":"create_new","parent_id":""}`,
		`{"kind":"create_new","session_id":"target-session"}`,
		`{"kind":"open_existing"}`,
		`{"kind":"open_existing","session_id":""}`,
		`{"kind":"open_existing","session_id":"target-session","parent_id":"parent-session"}`,
		`{"kind":"open_existing","session_id":"../escape"}`,
	} {
		var intent SessionLaunchIntent
		if err := json.Unmarshal([]byte(raw), &intent); err == nil {
			t.Fatalf("Unmarshal(%s) succeeded: %+v", raw, intent)
		}
	}
}

func TestSessionPlanRequestOwnsExactlyOneTypedLaunchIntent(t *testing.T) {
	target := mustSessionLaunchIntentID(t, "target-session")
	request := SessionPlanRequest{
		ClientRequestID: "request-1",
		Mode:            SessionLaunchModeInteractive,
		Intent:          OpenExistingSessionLaunchIntent(target),
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded SessionPlanRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Intent.Kind() != SessionLaunchIntentOpenExisting {
		t.Fatalf("intent kind = %q, want open_existing", decoded.Intent.Kind())
	}

	for _, raw := range []string{
		`{"client_request_id":"request-1","mode":"interactive"}`,
		`{"client_request_id":"request-1","mode":"interactive","selected_session_id":"target-session"}`,
		`{"client_request_id":"request-1","mode":"interactive","force_new_session":true}`,
		`{"client_request_id":"request-1","mode":"interactive","parent_session_id":"parent-session","intent":{"kind":"open_existing","session_id":"target-session"}}`,
	} {
		var legacy SessionPlanRequest
		if err := json.Unmarshal([]byte(raw), &legacy); err == nil {
			if validateErr := legacy.Validate(); validateErr == nil {
				t.Fatalf("legacy request shape succeeded: %s", raw)
			}
		}
	}
}

func mustSessionLaunchIntentID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
