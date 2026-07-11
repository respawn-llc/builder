package serverapi

import (
	"encoding/json"
	"testing"
)

func TestRunPromptOverridesAgentRoleJSONRoundTrip(t *testing.T) {
	req := SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            SessionLaunchModeHeadless,
		ForceNewSession: true,
		Overrides: RunPromptOverrides{
			AgentRole: "default",
		},
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got SessionPlanRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.Overrides.AgentRole != "default" {
		t.Fatalf("AgentRole = %q, want default after round trip: %s", got.Overrides.AgentRole, data)
	}
	if !got.Overrides.HasAgentRoleOverride() {
		t.Fatal("default role should count as a role override")
	}
}

func TestRunPromptRequestParentSessionIDJSONRoundTrip(t *testing.T) {
	req := RunPromptRequest{
		ClientRequestID:   "req-1",
		ParentSessionID:   "parent-session",
		SelectedSessionID: "selected-session",
		Prompt:            "hello",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got RunPromptRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.ParentSessionID != "parent-session" {
		t.Fatalf("ParentSessionID = %q, want parent-session", got.ParentSessionID)
	}
}

func TestRunPromptOverridesAgentRoleContract(t *testing.T) {
	var got SessionPlanRequest
	if err := json.Unmarshal([]byte(`{"ClientRequestID":"req-1","Mode":"headless","ForceNewSession":true,"Overrides":{"AgentRole":"worker"}}`), &got); err != nil {
		t.Fatalf("Unmarshal request: %v", err)
	}
	if got.Overrides.AgentRole != "worker" {
		t.Fatalf("AgentRole = %q, want worker", got.Overrides.AgentRole)
	}
	if !got.Overrides.HasAny() {
		t.Fatal("AgentRole should count as an override")
	}
	if !got.Overrides.HasAgentRoleOverride() {
		t.Fatal("AgentRole should count as a role override")
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
		{name: "empty role", overrides: RunPromptOverrides{AgentRole: " "}, wantAny: false, wantRole: false},
		{name: "config only", overrides: RunPromptOverrides{Model: "gpt-5.6-sol"}, wantAny: true, wantRole: false},
		{name: "default", overrides: RunPromptOverrides{AgentRole: "default"}, wantAny: true, wantRole: true, wantAuth: false, wantDefault: true},
		{name: "named", overrides: RunPromptOverrides{AgentRole: " Worker "}, wantAny: true, wantRole: true, wantAuth: true, wantRoleName: "worker"},
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
			if err := (RunPromptOverrides{AgentRole: role}).ValidateAgentRoleOverride(); err == nil {
				t.Fatal("expected reserved non-default role to be invalid")
			}
		})
	}
}
