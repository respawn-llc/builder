package workflowrunner

import (
	"testing"
	"time"

	"core/server/auth"
	"core/server/launch"
	"core/server/session"
	"core/shared/config"
)

func TestStarterResolvesEffectiveProviderUntilCapabilitiesAreEstablished(t *testing.T) {
	manager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{
			Type:  auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{AccessToken: "test-token"},
		},
	}), nil, time.Now)
	starter := &Starter{authManager: manager}

	provider, err := starter.resolveEffectiveProviderCapabilities(t.Context(), launch.SessionPlan{
		Locked: &session.LockedContract{},
	})
	if err != nil {
		t.Fatalf("resolveEffectiveProviderCapabilities without provider contract: %v", err)
	}
	if provider.AuthState.Method.Type != auth.MethodOAuth || provider.Capabilities.ProviderID != "chatgpt-codex" {
		t.Fatalf("effective provider = %+v, want OAuth-derived chatgpt-codex", provider)
	}

	provider, err = starter.resolveEffectiveProviderCapabilities(t.Context(), launch.SessionPlan{
		Locked: &session.LockedContract{
			ProviderContract: session.LockedProviderCapabilities{ProviderID: "chatgpt-codex"},
		},
	})
	if err != nil {
		t.Fatalf("resolveEffectiveProviderCapabilities with provider contract: %v", err)
	}
	if provider.AuthState.Method.Type != auth.MethodNone || provider.Capabilities.ProviderID != "chatgpt-codex" {
		t.Fatalf("locked provider = %+v, want locked capabilities without auth", provider)
	}

	provider, err = starter.resolveEffectiveProviderCapabilities(t.Context(), launch.SessionPlan{
		ActiveSettings: config.Settings{
			ProviderCapabilities: config.ProviderCapabilitiesOverride{ProviderID: "custom"},
		},
	})
	if err != nil {
		t.Fatalf("resolveEffectiveProviderCapabilities with explicit provider capabilities: %v", err)
	}
	if provider.AuthState.Method.Type != auth.MethodNone || provider.Capabilities.ProviderID != "custom" {
		t.Fatalf("explicit provider = %+v, want explicit capabilities without auth", provider)
	}
}
