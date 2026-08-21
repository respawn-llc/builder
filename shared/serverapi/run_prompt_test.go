package serverapi

import (
	"encoding/json"
	"testing"
)

func runPromptStringPtr(value string) *string { return &value }

func TestRunPromptOverridesAgentRoleJSONRoundTrip(t *testing.T) {
	req := RunPromptRequest{
		ClientRequestID: "req-1",
		Intent:          CreateNewSessionLaunchIntent(IndependentSessionCreateOrigin()),
		Prompt:          "hello",
		Overrides: RunPromptOverrides{
			AgentRole: runPromptStringPtr("default"),
		},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RunPromptRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Overrides.AgentRole == nil || *got.Overrides.AgentRole != "default" {
		t.Fatalf("AgentRole = %v, want default after round trip: %s", got.Overrides.AgentRole, data)
	}
	if !got.Overrides.HasAgentRoleOverride() {
		t.Fatal("default role should count as a role override")
	}
}

func TestRunPromptRequestParentIntentJSONRoundTrip(t *testing.T) {
	parentID := mustSessionLaunchIntentID(t, "parent-session")
	req := RunPromptRequest{
		ClientRequestID: "req-1",
		Intent:          CreateNewSessionLaunchIntent(ParentAgentSessionCreateOrigin(parentID)),
		Prompt:          "hello",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RunPromptRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	origin, present := got.Intent.CreateOrigin()
	gotParentID, hasSource := origin.SessionID()
	if !present || origin.Kind() != SessionCreateOriginParentAgent || !hasSource || gotParentID != parentID {
		t.Fatalf("origin = %+v/%v, want parent-agent %q", origin, present, parentID.String())
	}
}

func TestRunPromptRequestJSONRejectsUnknownAndRemovedFields(t *testing.T) {
	for _, raw := range []string{
		`{"client_request_id":"req-1","intent":{"kind":"create_new","origin":{"kind":"independent"}},"prompt":"hello","unknown":true}`,
		`{"client_request_id":"req-1","intent":{"kind":"create_new","origin":{"kind":"independent"}},"selected_session_id":"legacy","prompt":"hello"}`,
		`{"client_request_id":"req-1","intent":{"kind":"create_new","origin":{"kind":"independent"}},"parent_session_id":"legacy","prompt":"hello"}`,
	} {
		var request RunPromptRequest
		if err := json.Unmarshal([]byte(raw), &request); err == nil {
			t.Fatalf("Unmarshal(%s) succeeded, want strict rejection", raw)
		}
	}
}

func TestRunPromptRequestJSONRequiresTypedIntentWhenLegacySelectorIsMixed(t *testing.T) {
	raw := `{"client_request_id":"req-1","selected_session_id":"legacy","intent":{"kind":"open_existing","session_id":"target"},"prompt":"hello"}`
	var request RunPromptRequest
	if err := json.Unmarshal([]byte(raw), &request); err == nil {
		t.Fatalf("Unmarshal(%s) succeeded, want legacy field rejection", raw)
	}
}

func TestRunPromptOverridesAgentRoleContract(t *testing.T) {
	var got RunPromptRequest
	if err := json.Unmarshal([]byte(`{"client_request_id":"req-1","intent":{"kind":"create_new","origin":{"kind":"independent"}},"prompt":"hello","overrides":{"agent_role":"worker"}}`), &got); err != nil {
		t.Fatalf("Unmarshal request: %v", err)
	}
	if got.Overrides.AgentRole == nil || *got.Overrides.AgentRole != "worker" {
		t.Fatalf("AgentRole = %v, want worker", got.Overrides.AgentRole)
	}
	if !got.Overrides.HasAny() {
		t.Fatal("AgentRole should count as an override")
	}
	if !got.Overrides.HasAgentRoleOverride() {
		t.Fatal("AgentRole should count as a role override")
	}
}

func TestRunPromptOverridesMarshalUsesSnakeCaseAndNullableSelector(t *testing.T) {
	data, err := json.Marshal(RunPromptOverrides{
		AgentRole:           runPromptStringPtr("worker"),
		Model:               "gpt-5",
		ProviderOverride:    "openai",
		ThinkingLevel:       "medium",
		Theme:               "dark",
		ModelTimeoutSeconds: 30,
		Tools:               "shell",
		OpenAIBaseURL:       "https://example.test",
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal marshaled overrides: %v", err)
	}
	for key := range got {
		if key == "AgentRole" || key == "Model" || key == "ProviderOverride" {
			t.Fatalf("legacy override key %q in %s", key, data)
		}
	}
	if got["agent_role"] != "worker" || got["model"] != "gpt-5" || got["provider_override"] != "openai" {
		t.Fatalf("snake_case overrides = %v", got)
	}
}

func TestRunPromptOverridesRolePresenceAndAuth(t *testing.T) {
	tests := []struct {
		name         string
		overrides    RunPromptOverrides
		wantAny      bool
		wantRole     bool
		wantAuth     bool
		wantDefault  bool
		wantRoleName string
	}{
		{name: "empty", overrides: RunPromptOverrides{}, wantAny: false, wantRole: false},
		{name: "config only", overrides: RunPromptOverrides{Model: "gpt-5.6-sol"}, wantAny: true, wantRole: false},
		{name: "default", overrides: RunPromptOverrides{AgentRole: runPromptStringPtr("default")}, wantAny: true, wantRole: true, wantAuth: false, wantDefault: true},
		{name: "named", overrides: RunPromptOverrides{AgentRole: runPromptStringPtr(" Worker ")}, wantAny: true, wantRole: true, wantAuth: true, wantRoleName: "worker"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.overrides.HasAny(); got != tt.wantAny {
				t.Fatalf("HasAny = %t, want %t", got, tt.wantAny)
			}
			if got := tt.overrides.HasAgentRoleOverride(); got != tt.wantRole {
				t.Fatalf("HasAgentRoleOverride = %t, want %t", got, tt.wantRole)
			}
			if got := tt.overrides.NeedsAuthState(); got != tt.wantAuth {
				t.Fatalf("NeedsAuthState = %t, want %t", got, tt.wantAuth)
			}
			role, err := tt.overrides.AgentRoleOverride()
			if err != nil {
				t.Fatalf("AgentRoleOverride: %v", err)
			}
			if role.Default != tt.wantDefault || role.Role != tt.wantRoleName {
				t.Fatalf("AgentRoleOverride = %+v, want default=%t role=%q", role, tt.wantDefault, tt.wantRoleName)
			}
		})
	}
}

func TestRunPromptOverridesRejectReservedNonDefaultRoles(t *testing.T) {
	for _, role := range []string{"none", "self"} {
		t.Run(role, func(t *testing.T) {
			if err := (RunPromptOverrides{AgentRole: runPromptStringPtr(role)}).ValidateAgentRoleOverride(); err == nil {
				t.Fatal("expected reserved non-default role to be invalid")
			}
		})
	}
}
