package serverapi

import (
	"encoding/json"
	"testing"

	"core/shared/runtimeids"
)

func TestSessionLifecycleResultJSONRoundTrip(t *testing.T) {
	parent := mustLifecycleResultSessionID(t, "parent-session")
	target := mustLifecycleResultSessionID(t, "target-session")
	prompt := SessionInitialPromptMetadata{
		Text:            "seed prompt",
		HistoryRecorded: true,
	}

	tests := []struct {
		name string
		want SessionLifecycleResult
		json string
	}{
		{
			name: "stop",
			want: StopSessionLifecycleResult(),
			json: `{"kind":"stop"}`,
		},
		{
			name: "select session with current auth",
			want: SelectSessionLifecycleResult(SessionAuthPreparationKeepCurrent),
			json: `{"kind":"select_session","auth":"keep_current_auth"}`,
		},
		{
			name: "select session after reauthentication",
			want: SelectSessionLifecycleResult(SessionAuthPreparationReauthenticate),
			json: `{"kind":"select_session","auth":"reauthenticate"}`,
		},
		{
			name: "launch create with prompt and restored draft",
			want: LaunchSessionLifecycleResult(
				CreateNewSessionLaunchIntent(&parent),
				NewSessionLaunchPreparation(
					&prompt,
					RestoreStoredDraftSessionInitialInputPolicy(),
					SessionAuthPreparationKeepCurrent,
				),
			),
			json: `{"kind":"launch","intent":{"kind":"create_new","parent_id":"parent-session"},"preparation":{"initial_prompt":{"text":"seed prompt","history_recorded":true},"input_policy":{"kind":"restore_stored_draft"},"auth":"keep_current_auth"}}`,
		},
		{
			name: "launch open with legitimate empty input override",
			want: LaunchSessionLifecycleResult(
				OpenExistingSessionLaunchIntent(target),
				NewSessionLaunchPreparation(
					nil,
					OverrideStoredDraftSessionInitialInputPolicy(""),
					SessionAuthPreparationReauthenticate,
				),
			),
			json: `{"kind":"launch","intent":{"kind":"open_existing","session_id":"target-session"},"preparation":{"input_policy":{"kind":"override_stored_draft","text":""},"auth":"reauthenticate"}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(test.want)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(encoded) != test.json {
				t.Fatalf("JSON = %s, want %s", encoded, test.json)
			}

			var decoded SessionLifecycleResult
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !decoded.Equal(test.want) {
				t.Fatalf("decoded result = %+v, want %+v", decoded, test.want)
			}
			assertLifecycleResultVariant(t, decoded)
		})
	}
}

func TestSessionLifecycleResultRejectsInvalidAndLegacyJSON(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"kind":"unknown"}`,
		`{"kind":"stop","auth":"keep_current_auth"}`,
		`{"kind":"stop","intent":{"kind":"create_new"}}`,
		`{"kind":"stop","preparation":{"input_policy":{"kind":"restore_stored_draft"},"auth":"keep_current_auth"}}`,
		`{"kind":"select_session"}`,
		`{"kind":"select_session","auth":"unknown"}`,
		`{"kind":"select_session","auth":"keep_current_auth","intent":{"kind":"create_new"}}`,
		`{"kind":"select_session","auth":"keep_current_auth","preparation":{"input_policy":{"kind":"restore_stored_draft"},"auth":"keep_current_auth"}}`,
		`{"kind":"launch"}`,
		`{"kind":"launch","intent":{"kind":"create_new"}}`,
		`{"kind":"launch","preparation":{"input_policy":{"kind":"restore_stored_draft"},"auth":"keep_current_auth"}}`,
		`{"kind":"launch","intent":{"kind":"open_existing","session_id":""},"preparation":{"input_policy":{"kind":"restore_stored_draft"},"auth":"keep_current_auth"}}`,
		`{"kind":"launch","intent":{"kind":"open_existing","session_id":"target-session"},"preparation":{"auth":"keep_current_auth"}}`,
		`{"kind":"launch","intent":{"kind":"open_existing","session_id":"target-session"},"preparation":{"input_policy":{"kind":"unknown"},"auth":"keep_current_auth"}}`,
		`{"kind":"launch","intent":{"kind":"open_existing","session_id":"target-session"},"preparation":{"input_policy":{"kind":"override_stored_draft"},"auth":"keep_current_auth"}}`,
		`{"kind":"launch","intent":{"kind":"create_new"},"preparation":{"initial_prompt":{"text":""},"input_policy":{"kind":"restore_stored_draft"},"auth":"keep_current_auth"}}`,
		`{"kind":"launch","intent":{"kind":"create_new"},"preparation":{"input_policy":{"kind":"restore_stored_draft"},"auth":"unknown"}}`,
		`{"kind":"stop","should_continue":false}`,
		`{"kind":"stop","requires_reauth":false}`,
		`{"kind":"stop","next_session_id":"target-session"}`,
		`{"kind":"stop","force_new_session":true}`,
		`{"kind":"stop","parent_session_id":"parent-session"}`,
		`{"kind":"stop","initial_prompt":"seed"}`,
		`{"kind":"stop","initial_prompt_history_recorded":true}`,
		`{"kind":"stop","initial_input":"draft"}`,
		`{"kind":"stop"}{"kind":"stop"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var got SessionLifecycleResult
			if err := json.Unmarshal([]byte(raw), &got); err == nil {
				t.Fatalf("Unmarshal(%s) unexpectedly succeeded: %+v", raw, got)
			}
		})
	}
}

func TestInitialInputPolicyPreservesEmptyOverride(t *testing.T) {
	override := OverrideStoredDraftSessionInitialInputPolicy("")
	if override.Kind() != SessionInitialInputPolicyOverrideStoredDraft {
		t.Fatalf("kind = %q, want override stored draft", override.Kind())
	}
	text, ok := override.OverrideText()
	if !ok {
		t.Fatal("empty override was treated as absence")
	}
	if text != "" {
		t.Fatalf("override text = %q, want legitimate empty text", text)
	}

	restore := RestoreStoredDraftSessionInitialInputPolicy()
	if _, ok := restore.OverrideText(); ok {
		t.Fatal("restore-stored-draft policy exposed an override")
	}
}

func assertLifecycleResultVariant(t *testing.T, result SessionLifecycleResult) {
	t.Helper()
	switch result.Kind() {
	case SessionLifecycleResultStop:
		if _, ok := result.AuthPreparation(); ok {
			t.Fatal("stop result exposed auth preparation")
		}
		if _, ok := result.LaunchIntent(); ok {
			t.Fatal("stop result exposed launch intent")
		}
		if _, ok := result.LaunchPreparation(); ok {
			t.Fatal("stop result exposed launch preparation")
		}
	case SessionLifecycleResultSelectSession:
		if _, ok := result.AuthPreparation(); !ok {
			t.Fatal("select-session result omitted auth preparation")
		}
		if _, ok := result.LaunchIntent(); ok {
			t.Fatal("select-session result exposed launch intent")
		}
		if _, ok := result.LaunchPreparation(); ok {
			t.Fatal("select-session result exposed launch preparation")
		}
	case SessionLifecycleResultLaunch:
		if _, ok := result.AuthPreparation(); ok {
			t.Fatal("launch result exposed select-session auth")
		}
		if _, ok := result.LaunchIntent(); !ok {
			t.Fatal("launch result omitted launch intent")
		}
		if _, ok := result.LaunchPreparation(); !ok {
			t.Fatal("launch result omitted launch preparation")
		}
	default:
		t.Fatalf("unexpected lifecycle result kind %q", result.Kind())
	}
}

func mustLifecycleResultSessionID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}
