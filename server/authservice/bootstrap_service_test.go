package authservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/auth"
	"core/shared/config"
	"core/shared/serverapi"
)

func TestCompleteBootstrapConfiguresAPIKeyWhenAuthNotReady(t *testing.T) {
	service, store := newTestAuthBootstrapService(auth.EmptyState())

	resp, err := service.CompleteBootstrap(context.Background(), serverapi.AuthCompleteBootstrapRequest{
		Mode:   serverapi.AuthBootstrapModeAPIKey,
		APIKey: "server-key",
	})
	if err != nil {
		t.Fatalf("CompleteBootstrap: %v", err)
	}
	if !resp.AuthReady {
		t.Fatal("expected auth ready after bootstrap completion")
	}
	if resp.MethodType != string(auth.MethodAPIKey) {
		t.Fatalf("method type = %q, want %q", resp.MethodType, auth.MethodAPIKey)
	}
	state, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "server-key" {
		t.Fatalf("stored method = %+v, want server-key", state.Method)
	}
}

func TestCompleteBootstrapReturnsSuccessWithoutOverwriteWhenAuthAlreadyReady(t *testing.T) {
	service, store := newTestAuthBootstrapService(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "server-key"},
		},
	})

	resp, err := service.CompleteBootstrap(context.Background(), serverapi.AuthCompleteBootstrapRequest{
		Mode:   serverapi.AuthBootstrapModeAPIKey,
		APIKey: "server-key-2",
	})
	if err != nil {
		t.Fatalf("CompleteBootstrap: %v", err)
	}
	if !resp.AuthReady {
		t.Fatal("expected ready auth to return successful no-op")
	}
	if resp.MethodType != string(auth.MethodAPIKey) {
		t.Fatalf("method type = %q, want %q", resp.MethodType, auth.MethodAPIKey)
	}
	state, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "server-key" {
		t.Fatalf("stored method = %+v, want original server-key", state.Method)
	}
}

func TestCompleteBootstrapNoneClearsAuthWhenAuthOptional(t *testing.T) {
	service, store := newTestAuthBootstrapServiceWithSettings(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "server-key"},
		},
	}, config.Settings{OpenAIBaseURL: "http://127.0.0.1:8080/v1"})

	resp, err := service.CompleteBootstrap(context.Background(), serverapi.AuthCompleteBootstrapRequest{Mode: serverapi.AuthBootstrapModeNone})
	if err != nil {
		t.Fatalf("CompleteBootstrap none: %v", err)
	}
	if !resp.AuthReady {
		t.Fatal("expected optional auth skip to be ready")
	}
	state, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if state.Method.Type != auth.MethodNone {
		t.Fatalf("stored method = %+v, want none", state.Method)
	}
	if state.EnvAPIKeyPreference != auth.EnvAPIKeyPreferencePreferSaved {
		t.Fatalf("env preference = %q, want no-auth preference", state.EnvAPIKeyPreference)
	}
}

func TestCompleteBootstrapNoneSavesNoAuthPreferenceWhenAuthRequired(t *testing.T) {
	service, store := newTestAuthBootstrapService(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "server-key"},
		},
	})

	resp, err := service.CompleteBootstrap(context.Background(), serverapi.AuthCompleteBootstrapRequest{Mode: serverapi.AuthBootstrapModeNone})
	if err != nil {
		t.Fatalf("CompleteBootstrap none: %v", err)
	}
	if resp.AuthReady {
		t.Fatal("did not expect no-auth preference to satisfy required startup readiness")
	}
	state, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !state.IsNoAuthSelected() {
		t.Fatalf("stored state = %+v, want no-auth preference", state)
	}
	if !resp.NoAuthSelected {
		t.Fatalf("NoAuthSelected = false, want true")
	}
}

func TestGetBootstrapStatusReportsPersistedNoAuthSelection(t *testing.T) {
	service, _ := newTestAuthBootstrapService(auth.State{
		Scope:               auth.ScopeGlobal,
		Method:              auth.Method{Type: auth.MethodNone},
		EnvAPIKeyPreference: auth.EnvAPIKeyPreferencePreferSaved,
	})

	resp, err := service.GetBootstrapStatus(context.Background(), serverapi.AuthGetBootstrapStatusRequest{})
	if err != nil {
		t.Fatalf("GetBootstrapStatus: %v", err)
	}
	if resp.AuthReady {
		t.Fatal("did not expect no-auth selection to satisfy required startup readiness")
	}
	if !resp.NoAuthSelected {
		t.Fatal("expected bootstrap status to report persisted no-auth selection")
	}
}

func TestGetBootstrapStatusDoesNotReportEmptyStateAsNoAuthSelection(t *testing.T) {
	service, _ := newTestAuthBootstrapService(auth.EmptyState())

	resp, err := service.GetBootstrapStatus(context.Background(), serverapi.AuthGetBootstrapStatusRequest{})
	if err != nil {
		t.Fatalf("GetBootstrapStatus: %v", err)
	}
	if resp.NoAuthSelected {
		t.Fatal("empty state must not be reported as explicit no-auth selection")
	}
}

func TestAcknowledgeNoAuthIsNonMutating(t *testing.T) {
	initial := auth.State{
		Scope:               auth.ScopeGlobal,
		Method:              auth.Method{Type: auth.MethodNone},
		EnvAPIKeyPreference: auth.EnvAPIKeyPreferencePreferSaved,
	}
	service, store := newTestAuthBootstrapService(initial)

	resp, err := service.AcknowledgeNoAuth(context.Background(), serverapi.AuthAcknowledgeNoAuthRequest{})
	if err != nil {
		t.Fatalf("AcknowledgeNoAuth: %v", err)
	}
	if resp.AuthReady {
		t.Fatal("did not expect acknowledged no-auth to satisfy raw readiness")
	}
	if !resp.NoAuthSelected {
		t.Fatal("expected no-auth acknowledgement response")
	}
	state, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if state != initial {
		t.Fatalf("stored state mutated: %+v, want %+v", state, initial)
	}
}

func TestAcknowledgeNoAuthReportsReadyRealAuthWithoutNoAuthSelection(t *testing.T) {
	service, _ := newTestAuthBootstrapService(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type:   auth.MethodAPIKey,
			APIKey: &auth.APIKeyMethod{Key: "server-key"},
		},
	})

	resp, err := service.AcknowledgeNoAuth(context.Background(), serverapi.AuthAcknowledgeNoAuthRequest{})
	if err != nil {
		t.Fatalf("AcknowledgeNoAuth: %v", err)
	}
	if !resp.AuthReady || resp.NoAuthSelected {
		t.Fatalf("ack response = %+v, want real-auth ready without no-auth selection", resp)
	}
}

func TestAcknowledgeNoAuthRejectsMissingAuthAndNoAuthSelection(t *testing.T) {
	service, _ := newTestAuthBootstrapService(auth.EmptyState())

	_, err := service.AcknowledgeNoAuth(context.Background(), serverapi.AuthAcknowledgeNoAuthRequest{})
	if !errors.Is(err, serverapi.ErrServerAuthRequired) {
		t.Fatalf("AcknowledgeNoAuth error = %v, want ErrServerAuthRequired", err)
	}
}

func newTestAuthBootstrapService(initial auth.State) (*BootstrapService, *auth.MemoryStore) {
	return newTestAuthBootstrapServiceWithSettings(initial, config.Settings{Model: "gpt-5"})
}

func newTestAuthBootstrapServiceWithSettings(initial auth.State, settings config.Settings) (*BootstrapService, *auth.MemoryStore) {
	store := auth.NewMemoryStore(initial)
	manager := auth.NewManager(store, nil, func() time.Time {
		return time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	})
	return NewBootstrapService(manager, auth.OpenAIOAuthOptions{}, settings, nil), store
}
