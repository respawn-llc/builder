package status

import (
	"context"
	"core/server/auth"
	"core/shared/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCollectEnvironmentDisabledSkillRemainsVisibleAndStillCollectsAgents(t *testing.T) {
	workspace := t.TempDir()
	persistenceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "AGENTS.md"), []byte("workspace guidance"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	skillDir := filepath.Join(workspace, config.ConfigDirName, "skills", "hidden-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: hidden-skill\ndescription: hidden\n---\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	result := (Collector{}).CollectEnvironment(context.Background(), Request{
		WorkspaceRoot:   workspace,
		PersistenceRoot: persistenceRoot,
		Settings: config.Settings{
			SkillToggles: map[string]bool{"hidden-skill": false},
		},
	}, Snapshot{})

	if len(result.Skills) != 1 || result.Skills[0].Name != "hidden-skill" || !result.Skills[0].Loaded || !result.Skills[0].Disabled {
		t.Fatalf("disabled skill inspection = %+v, want visible loaded disabled skill", result.Skills)
	}
	if len(result.SkillTokenCounts) != 0 {
		t.Fatalf("disabled skill must not be tokenized for model visibility: %+v", result.SkillTokenCounts)
	}
	if result.CollectorWarning != "" {
		t.Fatalf("disabled skills emitted a collector warning: %q", result.CollectorWarning)
	}
	if len(result.AgentsPaths) == 0 || len(result.AgentTokenCounts) == 0 {
		t.Fatalf("AGENTS.md collection did not run: %+v", result)
	}
}

func TestCollectorUsesRefreshedOAuthStateForUsageFetch(t *testing.T) {
	now := time.Date(2026, time.January, 1, 10, 0, 0, 0, time.UTC)
	store := auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type: auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{
				AccessToken:  "stale-token",
				RefreshToken: "refresh-token",
				TokenType:    "Bearer",
				Expiry:       now.Add(-time.Minute),
				AccountID:    "acct-456",
			},
		},
	})
	refresher := auth.NewOAuthRefresher(
		func() time.Time { return now },
		30*time.Second,
		func(context.Context, auth.Method) (auth.Method, error) {
			return auth.Method{
				Type: auth.MethodOAuth,
				OAuth: &auth.OAuthMethod{
					AccessToken:  "fresh-token",
					RefreshToken: "refresh-token",
					TokenType:    "Bearer",
					Expiry:       now.Add(time.Hour),
					AccountID:    "acct-456",
				},
			}, nil
		},
	)
	manager := auth.NewManager(store, refresher, func() time.Time { return now.Add(time.Minute) })
	collector := Collector{
		AuthManager:  manager,
		UsageBaseURL: DefaultUsageBaseURL,
		UsagePayloadFetcher: func(_ context.Context, baseURL string, state auth.State) (UsagePayload, error) {
			if baseURL != DefaultUsageBaseURL {
				t.Fatalf("base URL = %q", baseURL)
			}
			authorization, err := state.Method.AuthHeaderValue()
			if err != nil {
				t.Fatalf("auth header value: %v", err)
			}
			if got := authorization; got != "Bearer fresh-token" {
				t.Fatalf("authorization header value = %q", got)
			}
			if got := strings.TrimSpace(state.Method.OAuth.AccountID); got != "acct-456" {
				t.Fatalf("ChatGPT-Account-Id value = %q", got)
			}
			return UsagePayload{PlanType: "pro", RateLimit: &UsageRateLimit{PrimaryWindow: &UsageWindow{UsedPercent: 12.5, LimitWindowSeconds: 18000, ResetAt: 1704069000}}}, nil
		},
	}

	snapshot, err := collector.Collect(context.Background(), Request{
		WorkspaceRoot: t.TempDir(),
		Settings:      config.Settings{},
	})
	if err != nil {
		t.Fatalf("collect status: %v", err)
	}
	if !strings.Contains(snapshot.Auth.Summary, "Subscription") {
		t.Fatalf("auth summary = %q", snapshot.Auth.Summary)
	}
	if snapshot.Subscription.Summary != "Pro subscription" {
		t.Fatalf("subscription summary = %q", snapshot.Subscription.Summary)
	}
	if len(snapshot.Subscription.Windows) != 1 || snapshot.Subscription.Windows[0].Label != "5h" {
		t.Fatalf("windows = %#v", snapshot.Subscription.Windows)
	}
}

func TestAuthInfoSummarizesNoAuthAndAPIKey(t *testing.T) {
	noAuth := BuildAuthInfo(auth.State{}, config.Settings{}, nil)
	if noAuth.Summary != "No Auth" || !noAuth.Visible {
		t.Fatalf("no-auth summary = %+v, want visible No Auth", noAuth)
	}
	apiKey := BuildAuthInfo(auth.State{
		Method: auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: "sk-test-1234"}},
	}, config.Settings{}, nil)
	if apiKey.Summary != "API Key ...1234" || !apiKey.Visible {
		t.Fatalf("api-key summary = %+v, want masked API key", apiKey)
	}
}

func TestCollectorPreservesStoredAuthStateWhenRefreshFails(t *testing.T) {
	now := time.Date(2026, time.January, 1, 10, 0, 0, 0, time.UTC)
	store := auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type: auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{
				AccessToken:  "stale-token",
				RefreshToken: "refresh-token",
				TokenType:    "Bearer",
				Expiry:       now.Add(-time.Minute),
				AccountID:    "acct-789",
				Email:        "user@example.com",
			},
		},
		EnvAPIKeyPreference: auth.EnvAPIKeyPreferencePreferSaved,
	})
	refresher := auth.NewOAuthRefresher(
		func() time.Time { return now },
		30*time.Second,
		func(context.Context, auth.Method) (auth.Method, error) {
			return auth.Method{}, auth.ErrOAuthRefreshFailed
		},
	)
	manager := auth.NewManager(store, refresher, func() time.Time { return now.Add(time.Minute) })

	collector := Collector{AuthManager: manager}
	snapshot, err := collector.Collect(context.Background(), Request{
		WorkspaceRoot: t.TempDir(),
		Settings:      config.Settings{},
	})
	if err != nil {
		t.Fatalf("collect status: %v", err)
	}
	if !strings.Contains(snapshot.Auth.Summary, "user@example.com") {
		t.Fatalf("auth summary = %q", snapshot.Auth.Summary)
	}
	if !snapshot.Subscription.Applicable {
		t.Fatal("expected subscription section to stay applicable")
	}
	if !strings.Contains(snapshot.Subscription.Summary, auth.ErrOAuthRefreshFailed.Error()) {
		t.Fatalf("subscription summary = %q", snapshot.Subscription.Summary)
	}
	if !strings.Contains(snapshot.CollectorWarning, auth.ErrOAuthRefreshFailed.Error()) {
		t.Fatalf("collector warning = %q", snapshot.CollectorWarning)
	}
}

func TestCollectBasePreservesOptionalAgentRole(t *testing.T) {
	role := "worker"
	for name, agentRole := range map[string]*string{
		"named agent":   &role,
		"default agent": nil,
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := (Collector{}).CollectBase(Request{AgentRole: agentRole})
			if agentRole == nil {
				if snapshot.AgentRole != nil {
					t.Fatalf("snapshot agent role = %v, want nil", snapshot.AgentRole)
				}
				return
			}
			if snapshot.AgentRole == nil || *snapshot.AgentRole != *agentRole {
				t.Fatalf("snapshot agent role = %v, want %q", snapshot.AgentRole, *agentRole)
			}
			if snapshot.AgentRole == agentRole {
				t.Fatal("snapshot agent role aliases request")
			}
		})
	}
}
