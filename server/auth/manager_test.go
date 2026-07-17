package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/oauth2"
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

func TestAuthorizationHeaderSurfacesOAuthRefreshFailure(t *testing.T) {
	store := NewMemoryStore(managerTestOAuthState("stale-token", "refresh-token", managerTestNow.Add(-time.Minute)))

	refreshErr := errors.New("refresh failed")
	refresher := NewOAuthRefresher(stubTokenFactory{source: stubTokenSource{err: refreshErr}}, func() time.Time {
		return managerTestNow
	}, 30*time.Second)
	mgr := NewManager(store, refresher, func() time.Time { return managerTestNow })

	_, err := mgr.AuthorizationHeader(context.Background())
	if !errors.Is(err, ErrOAuthRefreshFailed) {
		t.Fatalf("expected ErrOAuthRefreshFailed, got %v", err)
	}

	state := requireAuthState(t, store.Load)
	if state.Method.OAuth == nil || state.Method.OAuth.AccessToken != "stale-token" {
		t.Fatalf("oauth state changed on refresh failure: %+v", state.Method.OAuth)
	}
}

func TestCurrentStateRefreshesAndPersistsOAuthState(t *testing.T) {
	initial := managerTestOAuthState("stale-token", "refresh-token", managerTestNow.Add(-time.Minute))
	initial.Method.OAuth.AccountID = "acct-123"
	store := NewMemoryStore(initial)
	refresher := NewOAuthRefresher(nil, func() time.Time { return managerTestNow }, 30*time.Second)
	refresher.Refresh = func(context.Context, Method) (Method, error) {
		method := managerTestOAuthMethod("fresh-token", "refresh-token", managerTestNow.Add(time.Hour))
		method.OAuth.AccountID = "acct-123"
		return method, nil
	}
	mgr := NewManager(store, refresher, func() time.Time { return managerTestNow.Add(2 * time.Minute) })

	state, err := mgr.CurrentState(context.Background())
	if err != nil {
		t.Fatalf("current state: %v", err)
	}
	if state.Method.OAuth == nil || state.Method.OAuth.AccessToken != "fresh-token" {
		t.Fatalf("expected refreshed oauth state, got %+v", state.Method.OAuth)
	}
	persisted := requireAuthState(t, store.Load)
	if persisted.Method.OAuth == nil || persisted.Method.OAuth.AccessToken != "fresh-token" {
		t.Fatalf("expected persisted refreshed oauth state, got %+v", persisted.Method.OAuth)
	}
}

func TestSetEnvAPIKeyPreferencePersistsChoice(t *testing.T) {
	store := NewMemoryStore(EmptyState())
	mgr := NewManager(store, nil, func() time.Time { return managerTestNow })

	state, err := mgr.SetEnvAPIKeyPreference(context.Background(), EnvAPIKeyPreferencePreferEnv, true)
	if err != nil {
		t.Fatalf("set env api key preference: %v", err)
	}
	if state.EnvAPIKeyPreference != EnvAPIKeyPreferencePreferEnv {
		t.Fatalf("expected env preference saved, got %q", state.EnvAPIKeyPreference)
	}
	persisted := requireAuthState(t, store.Load)
	if persisted.EnvAPIKeyPreference != EnvAPIKeyPreferencePreferEnv {
		t.Fatalf("expected persisted env preference saved, got %q", persisted.EnvAPIKeyPreference)
	}
}

func TestSwitchMethodAndSetEnvAPIKeyPreferencePersistsBoth(t *testing.T) {
	store := NewMemoryStore(EmptyState())
	mgr := NewManager(store, nil, func() time.Time { return managerTestNow })

	state, err := mgr.SwitchMethodAndSetEnvAPIKeyPreference(
		context.Background(),
		managerTestOAuthMethod("token-a", "refresh-a", managerTestNow.Add(time.Hour)),
		EnvAPIKeyPreferencePreferSaved,
		true,
		true,
	)
	if err != nil {
		t.Fatalf("switch method and set env preference: %v", err)
	}
	if state.Method.Type != MethodOAuth {
		t.Fatalf("expected oauth method, got %q", state.Method.Type)
	}
	if state.EnvAPIKeyPreference != EnvAPIKeyPreferencePreferSaved {
		t.Fatalf("expected saved-auth preference, got %q", state.EnvAPIKeyPreference)
	}
	persisted := requireAuthState(t, store.Load)
	if persisted.Method.Type != MethodOAuth {
		t.Fatalf("expected persisted oauth method, got %q", persisted.Method.Type)
	}
	if persisted.EnvAPIKeyPreference != EnvAPIKeyPreferencePreferSaved {
		t.Fatalf("expected persisted saved-auth preference, got %q", persisted.EnvAPIKeyPreference)
	}
}

func TestClearMethodResetsEnvAPIKeyPreference(t *testing.T) {
	persistedState := testAuthStateAt(testAPIKeyState("sk-test"), 10)
	persistedState.EnvAPIKeyPreference = EnvAPIKeyPreferencePreferEnv
	store := NewMemoryStore(persistedState)
	mgr := NewManager(store, nil, func() time.Time { return managerTestNow.Add(time.Minute) })

	state, err := mgr.ClearMethod(context.Background(), true)
	if err != nil {
		t.Fatalf("clear method: %v", err)
	}
	if state.Method.Type != MethodNone {
		t.Fatalf("expected cleared method, got %q", state.Method.Type)
	}
	if state.EnvAPIKeyPreference != EnvAPIKeyPreferenceUnspecified {
		t.Fatalf("expected env preference reset, got %q", state.EnvAPIKeyPreference)
	}
	persisted := requireAuthState(t, store.Load)
	if persisted.Method.Type != MethodNone {
		t.Fatalf("expected persisted cleared method, got %q", persisted.Method.Type)
	}
	if persisted.EnvAPIKeyPreference != EnvAPIKeyPreferenceUnspecified {
		t.Fatalf("expected persisted env preference reset, got %q", persisted.EnvAPIKeyPreference)
	}
}

func TestSetEnvAPIKeyPreferenceDoesNotPersistBootstrapEnvMethod(t *testing.T) {
	base := NewMemoryStore(managerTestOAuthState("oauth-token", "oauth-refresh", managerTestNow.Add(time.Hour)))
	store := NewEnvAPIKeyOverrideStore(base, func(string) (string, bool) {
		return "sk-env", true
	})
	mgr := NewManager(store, nil, func() time.Time { return managerTestNow.Add(time.Minute) })

	state, err := mgr.SetEnvAPIKeyPreference(context.Background(), EnvAPIKeyPreferencePreferSaved, true)
	if err != nil {
		t.Fatalf("set env api key preference: %v", err)
	}
	if state.Method.Type != MethodOAuth {
		t.Fatalf("expected stored oauth method to remain durable, got %q", state.Method.Type)
	}
	persisted := requireAuthState(t, base.Load)
	if persisted.Method.Type != MethodOAuth {
		t.Fatalf("expected persisted oauth method, got %q", persisted.Method.Type)
	}
	if persisted.Method.APIKey != nil {
		t.Fatalf("did not expect bootstrap env key to persist, got %+v", persisted.Method.APIKey)
	}
	if persisted.EnvAPIKeyPreference != EnvAPIKeyPreferencePreferSaved {
		t.Fatalf("expected persisted saved-auth preference, got %q", persisted.EnvAPIKeyPreference)
	}
}

func TestSwitchMethodDoesNotPersistBootstrapEnvMethod(t *testing.T) {
	base := NewMemoryStore(managerTestOAuthState("oauth-token", "oauth-refresh", managerTestNow.Add(time.Hour)))
	store := NewEnvAPIKeyOverrideStore(base, func(string) (string, bool) {
		return "sk-env", true
	})
	mgr := NewManager(store, nil, func() time.Time { return managerTestNow.Add(time.Minute) })

	state, err := mgr.SwitchMethod(context.Background(), Method{
		Type:   MethodAPIKey,
		APIKey: &APIKeyMethod{Key: "sk-saved"},
	}, true)
	if err != nil {
		t.Fatalf("switch method: %v", err)
	}
	if state.Method.Type != MethodAPIKey {
		t.Fatalf("expected api key method, got %q", state.Method.Type)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "sk-saved" {
		t.Fatalf("expected switched saved api key, got %+v", state.Method.APIKey)
	}
	persisted := requireAuthState(t, base.Load)
	if persisted.Method.Type != MethodAPIKey {
		t.Fatalf("expected persisted api key method, got %q", persisted.Method.Type)
	}
	if persisted.Method.APIKey == nil || persisted.Method.APIKey.Key != "sk-saved" {
		t.Fatalf("expected persisted switched api key, got %+v", persisted.Method.APIKey)
	}
	if persisted.Method.APIKey.Key == "sk-env" {
		t.Fatal("did not expect bootstrap env api key to persist")
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

type stubTokenFactory struct {
	source OAuthTokenSource
}

func (f stubTokenFactory) TokenSource(context.Context, oauth2.Token) OAuthTokenSource {
	return f.source
}

type stubTokenSource struct {
	tok *oauth2.Token
	err error
}

func (s stubTokenSource) Token() (*oauth2.Token, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tok, nil
}
