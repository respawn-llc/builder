package serverapi

import (
	"encoding/json"
	"testing"
)

func runPromptStringPtr(value string) *string { return &value }

func TestRunPromptOverridesAgentRoleJSONRoundTrip(t *testing.T) {
	req := SessionPlanRequest{
		ClientRequestID: "req-1",
		Mode:            SessionLaunchModeHeadless,
		Intent:          CreateNewSessionLaunchIntent(nil),
		Overrides: RunPromptOverrides{
			AgentRole: runPromptStringPtr("default"),
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
		Intent:          CreateNewSessionLaunchIntent(&parentID),
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
	gotParentID, present := got.Intent.ParentID()
	if !present || gotParentID != parentID {
		t.Fatalf("ParentID = %q/%v, want %q/true", gotParentID.String(), present, parentID.String())
	}
}

func TestRunPromptOverridesAgentRoleContract(t *testing.T) {
	var got SessionPlanRequest
	if err := json.Unmarshal([]byte(`{"client_request_id":"req-1","mode":"headless","intent":{"kind":"create_new"},"overrides":{"agent_role":"worker"}}`), &got); err != nil {
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
