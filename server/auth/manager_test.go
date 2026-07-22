package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

var managerTestNow = time.Date(2026, time.January, 1, 10, 0, 0, 0, time.UTC)

func TestSwitchMethodRequiresIdle(t *testing.T) {
	store := NewMemoryStore(testAuthStateAt(testAPIKeyState("old-key"), 10))
	mgr := NewManager(store, nil, func() time.Time { return managerTestNow.Add(time.Minute) })

	_, err := mgr.SwitchMethod(
		context.Background(),
		managerTestOAuthMethod("token-a", "refresh-a", managerTestNow.Add(time.Hour)),
		false,
	)
	if !errors.Is(err, ErrSwitchRequiresIdle) {
		t.Fatalf("expected ErrSwitchRequiresIdle, got %v", err)
	}

	state := requireAuthState(t, store.Load)
	if state.Method.Type != MethodAPIKey {
		t.Fatalf("expected api key method to remain unchanged, got %q", state.Method.Type)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "old-key" {
		t.Fatalf("unexpected api key state after failed switch: %+v", state.Method.APIKey)
	}
}

func managerTestOAuthMethod(accessToken string, refreshToken string, expiry time.Time) Method {
	return Method{
		Type: MethodOAuth,
		OAuth: &OAuthMethod{
			AccessToken:  accessToken,
			RefreshToken: refreshToken,
			TokenType:    "Bearer",
			Expiry:       expiry,
		},
	}
}

func managerTestOAuthState(accessToken string, refreshToken string, expiry time.Time) State {
	return State{
		Scope:     ScopeGlobal,
		Method:    managerTestOAuthMethod(accessToken, refreshToken, expiry),
		UpdatedAt: managerTestNow,
	}
}
