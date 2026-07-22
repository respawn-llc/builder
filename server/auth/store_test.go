package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStoreSaveWritesWithSecurePermissions(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "auth-state.json")
	store := NewFileStore(statePath)

	state := testAuthStateAt(testAPIKeyState("secret-key"), 10)

	if err := store.Save(context.Background(), state); err != nil {
		t.Fatalf("save auth state: %v", err)
	}

	assertAuthStateFileMode(t, statePath, authStateFileMode)
}

type persistedDecoratorStore struct {
	loadState      State
	persistedState State
}

func (s *persistedDecoratorStore) Load(context.Context) (State, error) {
	return s.loadState, nil
}

func (s *persistedDecoratorStore) LoadPersisted(context.Context) (State, error) {
	return s.persistedState, nil
}

func (s *persistedDecoratorStore) Save(context.Context, State) error {
	return nil
}

func testAPIKeyState(key string) State {
	return State{
		Scope:  ScopeGlobal,
		Method: Method{Type: MethodAPIKey, APIKey: &APIKeyMethod{Key: key}},
	}
}

func testOAuthState() State {
	return State{
		Scope: ScopeGlobal,
		Method: Method{
			Type: MethodOAuth,
			OAuth: &OAuthMethod{
				AccessToken:  "oauth-access",
				RefreshToken: "oauth-refresh",
				TokenType:    "Bearer",
				Expiry:       time.Date(2026, time.January, 1, 11, 0, 0, 0, time.UTC),
			},
		},
	}
}

func testAuthStateAt(state State, hour int) State {
	state.UpdatedAt = time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC)
	return state
}

func testEnvAPIKey(value string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		if key == "OPENAI_API_KEY" {
			return value, true
		}
		return "", false
	}
}

func writeAuthStateFile(t *testing.T, path string, state State, mode os.FileMode) {
	t.Helper()
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal auth state: %v", err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write auth state: %v", err)
	}
}

func assertAuthStateFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat auth state: %v", err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("auth state mode = %04o, want %04o", got, want)
	}
}

func requireAuthState(t *testing.T, load func(context.Context) (State, error)) State {
	t.Helper()
	state, err := load(context.Background())
	if err != nil {
		t.Fatalf("load auth state: %v", err)
	}
	return state
}
