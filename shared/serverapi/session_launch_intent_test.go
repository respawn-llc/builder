package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/runtimeids"
)

func TestSessionLaunchIntentVariantsValidateAndRoundTrip(t *testing.T) {
	previous := mustSessionLaunchIntentID(t, "previous-session")
	parentAgent := mustSessionLaunchIntentID(t, "parent-agent-session")
	target := mustSessionLaunchIntentID(t, "target-session")
	tests := []struct {
		name       string
		intent     SessionLaunchIntent
		kind       SessionLaunchIntentKind
		originKind SessionCreateOriginKind
		sourceID   *runtimeids.SessionID
		sessionID  *runtimeids.SessionID
		jsonFields map[string]any
	}{
		{
			name:       "independent creation",
			intent:     CreateNewSessionLaunchIntent(IndependentSessionCreateOrigin()),
			kind:       SessionLaunchIntentCreateNew,
			originKind: SessionCreateOriginIndependent,
			jsonFields: map[string]any{
				"kind":   "create_new",
				"origin": map[string]any{"kind": "independent"},
			},
		},
		{
			name:       "previous session creation",
			intent:     CreateNewSessionLaunchIntent(PreviousSessionCreateOrigin(previous)),
			kind:       SessionLaunchIntentCreateNew,
			originKind: SessionCreateOriginPreviousSession,
			sourceID:   &previous,
			jsonFields: map[string]any{
				"kind":   "create_new",
				"origin": map[string]any{"kind": "previous_session", "session_id": previous.String()},
			},
		},
		{
			name:       "parent agent creation",
			intent:     CreateNewSessionLaunchIntent(ParentAgentSessionCreateOrigin(parentAgent)),
			kind:       SessionLaunchIntentCreateNew,
			originKind: SessionCreateOriginParentAgent,
			sourceID:   &parentAgent,
			jsonFields: map[string]any{
				"kind":   "create_new",
				"origin": map[string]any{"kind": "parent_agent", "session_id": parentAgent.String()},
			},
		},
		{
			name:      "open existing",
			intent:    OpenExistingSessionLaunchIntent(target),
			kind:      SessionLaunchIntentOpenExisting,
			sessionID: &target,
			jsonFields: map[string]any{
				"kind":       "open_existing",
				"session_id": target.String(),
			},
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
			if !jsonObjectsEqual(fields, test.jsonFields) {
				t.Fatalf("JSON fields = %+v, want %+v", fields, test.jsonFields)
			}

			var decoded SessionLaunchIntent
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Unmarshal intent: %v", err)
			}
			if !decoded.Equal(test.intent) {
				t.Fatalf("decoded intent = %+v, want equality with %+v", decoded, test.intent)
			}
			if decoded.Kind() != test.kind {
				t.Fatalf("Kind = %q, want %q", decoded.Kind(), test.kind)
			}
			origin, hasOrigin := decoded.CreateOrigin()
			if test.kind == SessionLaunchIntentOpenExisting {
				if hasOrigin {
					t.Fatalf("open-existing intent unexpectedly has origin %+v", origin)
				}
			} else {
				if !hasOrigin || origin.Kind() != test.originKind {
					t.Fatalf("origin = %+v/%v, want kind %q", origin, hasOrigin, test.originKind)
				}
				sourceID, hasSource := origin.SessionID()
				if test.sourceID == nil {
					if hasSource {
						t.Fatalf("unexpected origin session ID %q", sourceID.String())
					}
				} else if !hasSource || sourceID != *test.sourceID {
					t.Fatalf("origin session ID = %q/%v, want %q", sourceID.String(), hasSource, test.sourceID.String())
				}
			}
			sessionID, hasSession := decoded.SessionID()
			if test.sessionID == nil {
				if hasSession {
					t.Fatalf("unexpected target session ID %q", sessionID.String())
				}
			} else if !hasSession || sessionID != *test.sessionID {
				t.Fatalf("target session ID = %q/%v, want %q", sessionID.String(), hasSession, test.sessionID.String())
			}
		})
	}
}

func TestSessionLaunchIntentRejectsMalformedUnknownMixedAndLegacyShapes(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"kind":"unknown"}`,
		`{"kind":"create_new"}`,
		`{"kind":"create_new","origin":{}}`,
		`{"kind":"create_new","origin":{"kind":"unknown"}}`,
		`{"kind":"create_new","origin":{"kind":"independent","session_id":"source-session"}}`,
		`{"kind":"create_new","origin":{"kind":"previous_session"}}`,
		`{"kind":"create_new","origin":{"kind":"previous_session","session_id":""}}`,
		`{"kind":"create_new","origin":{"kind":"parent_agent"}}`,
		`{"kind":"create_new","origin":{"kind":"parent_agent","session_id":"../escape"}}`,
		`{"kind":"create_new","origin":{"kind":"previous_session","session_id":"source-session"},"session_id":"target-session"}`,
		`{"kind":"create_new","origin":{"kind":"parent_agent","session_id":"source-session","previous_session_id":"mixed-session"}}`,
		`{"kind":"create_new","parent_id":"legacy-parent"}`,
		`{"kind":"open_existing"}`,
		`{"kind":"open_existing","session_id":""}`,
		`{"kind":"open_existing","session_id":"target-session","origin":{"kind":"independent"}}`,
		`{"kind":"open_existing","session_id":"target-session","parent_id":"legacy-parent"}`,
		`{"kind":"open_existing","session_id":"../escape"}`,
	} {
		var intent SessionLaunchIntent
		if err := json.Unmarshal([]byte(raw), &intent); err == nil {
			t.Fatalf("Unmarshal(%s) succeeded: %+v", raw, intent)
		}
	}
}

func jsonObjectsEqual(left map[string]any, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func mustSessionLaunchIntentID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
