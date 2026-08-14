package workflowrunner

import (
	"testing"
	"time"

	"core/server/auth"
	"core/server/session"
)

func TestStarterLoadsEffectiveAuthUntilProviderContractIsEstablished(t *testing.T) {
	manager := auth.NewManager(auth.NewMemoryStore(auth.State{
		Method: auth.Method{
			Type:  auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{AccessToken: "test-token"},
		},
	}), nil, time.Now)
	starter := &Starter{authManager: manager}

	state, err := starter.loadEffectiveAuthState(t.Context(), &session.LockedContract{})
	if err != nil {
		t.Fatalf("loadEffectiveAuthState without provider contract: %v", err)
	}
	if state.Method.Type != auth.MethodOAuth {
		t.Fatalf("effective auth method = %q, want OAuth", state.Method.Type)
	}

	state, err = starter.loadEffectiveAuthState(t.Context(), &session.LockedContract{
		ProviderContract: session.LockedProviderCapabilities{ProviderID: "chatgpt-codex"},
	})
	if err != nil {
		t.Fatalf("loadEffectiveAuthState with provider contract: %v", err)
	}
	if state.Method.Type != auth.MethodNone {
		t.Fatalf("established provider contract auth method = %q, want no auth read", state.Method.Type)
	}
}
