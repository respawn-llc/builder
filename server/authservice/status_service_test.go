package authservice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"core/server/auth"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestFetchUsagePayloadHandlesNonOAuthState(t *testing.T) {
	var accountHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountHeader = r.Header.Get("ChatGPT-Account-Id")
		_ = json.NewEncoder(w).Encode(usagePayload{
			PlanType: "pro",
		})
	}))
	defer server.Close()

	_, err := fetchUsagePayload(context.Background(), server.URL, auth.State{Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "test-key"}}})
	if err != nil {
		t.Fatalf("fetchUsagePayload: %v", err)
	}
	if accountHeader != "" {
		t.Fatalf("ChatGPT-Account-Id = %q, want empty for API key auth", accountHeader)
	}
}

func TestLimitDuration(t *testing.T) {
	tests := []struct {
		name          string
		windowMinutes int
		want          string
	}{
		{name: "daily", windowMinutes: 24 * 60, want: "24h"},
		{name: "weekly", windowMinutes: 7 * 24 * 60, want: "weekly"},
		{name: "monthly", windowMinutes: 30 * 24 * 60, want: "monthly"},
		{name: "ninety days", windowMinutes: 90 * 24 * 60, want: "90d"},
		{name: "annual", windowMinutes: 365 * 24 * 60, want: "annual"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := limitDuration(tt.windowMinutes); got != tt.want {
				t.Fatalf("limitDuration(%d) = %q, want %q", tt.windowMinutes, got, tt.want)
			}
		})
	}
}

func TestServiceUsesServerSettingsForAPIKeyProviderLabel(t *testing.T) {
	mgr := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "sk-test"}},
	}), nil, time.Now)
	svc := NewStatusService(mgr, config.Settings{ProviderOverride: "internal-openai"})

	resp, err := svc.GetAuthStatus(context.Background(), serverapi.AuthStatusRequest{})
	if err != nil {
		t.Fatalf("GetAuthStatus: %v", err)
	}
	if resp.Auth.Summary != "API Key ...test" {
		t.Fatalf("auth summary = %q, want masked API key", resp.Auth.Summary)
	}
	if len(resp.Auth.Details) != 1 || resp.Auth.Details[0] != "internal-openai" {
		t.Fatalf("auth details = %+v, want server provider override", resp.Auth.Details)
	}
}
