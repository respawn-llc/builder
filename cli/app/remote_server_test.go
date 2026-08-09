package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"core/server/auth"
	serverstartup "core/server/startup"
	"core/shared/client"
	"core/shared/config"
)

type remoteAuthTestFixture struct {
	daemon *serverstartup.ServeServer
	server *remoteAppServer
	config config.App
}

func startRemoteAuthTestFixture(t *testing.T, workspace string, allowUnauthenticated bool, authHandler memoryAuthHandler) remoteAuthTestFixture {
	t.Helper()
	cfg := loadAppTestConfig(t, workspace, config.LoadOptions{})
	daemon, err := serverstartup.StartServeServer(context.Background(), serverstartup.Request{
		WorkspaceRoot:         workspace,
		WorkspaceRootExplicit: true,
		AllowUnauthenticated:  allowUnauthenticated,
	}, authHandler, autoOnboarding)
	if err != nil {
		t.Fatalf("StartServeServer: %v", err)
	}
	t.Cleanup(func() { _ = daemon.Close() })
	t.Cleanup(serveAppServer(t, daemon))
	waitForConfiguredRemoteIdentity(t, workspace)
	remote, err := client.DialRemoteURL(context.Background(), config.ServerRPCURL(cfg))
	if err != nil {
		t.Fatalf("DialRemoteURL: %v", err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	return remoteAuthTestFixture{daemon: daemon, server: newRemoteAppServerWithAuth(remote, cfg), config: cfg}
}

func TestRemoteAppServerReauthenticateConfiguresServerOwnedAuth(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	t.Setenv("OPENAI_API_KEY", "reauthed-key")
	fixture := startRemoteAuthTestFixture(t, workspace, true, memoryAuthHandler{state: auth.EmptyState()})
	if err := fixture.server.Reauthenticate(context.Background(), newHeadlessAuthInteractor(), false); err != nil {
		t.Fatalf("Reauthenticate: %v", err)
	}

	state, err := fixture.daemon.AuthManager().StoredState(context.Background())
	if err != nil {
		t.Fatalf("StoredState: %v", err)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "reauthed-key" {
		t.Fatalf("unexpected stored auth state: %+v", state.Method)
	}
	if _, err := os.Stat(config.GlobalAuthConfigPath(fixture.config)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected client auth file to remain absent, got %v", err)
	}
}

func TestRemoteAppServerReauthenticatePromptsWhenServerAuthAlreadyReady(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	t.Setenv("OPENAI_API_KEY", "reauthed-key")
	fixture := startRemoteAuthTestFixture(t, workspace, false, apiKeyMemoryAuthHandlerWithoutTimestamp("old-key"))

	pickerCalls := 0
	interactor := &interactiveAuthInteractor{
		lookupEnv: func(key string) string {
			if key == "OPENAI_API_KEY" {
				return "reauthed-key"
			}
			return ""
		},
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			pickerCalls++
			return authMethodPickerResult{Choice: authMethodChoiceEnvAPIKey}, nil
		},
	}

	if err := fixture.server.Reauthenticate(context.Background(), interactor, true); err != nil {
		t.Fatalf("Reauthenticate: %v", err)
	}
	if pickerCalls != 1 {
		t.Fatalf("expected remote /login to open auth picker once, got %d", pickerCalls)
	}
	state, err := fixture.daemon.AuthManager().StoredState(context.Background())
	if err != nil {
		t.Fatalf("StoredState: %v", err)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "reauthed-key" {
		t.Fatalf("expected forced remote reauth to replace auth, got %+v", state.Method)
	}
}

func TestRemoteAppServerEnsureAuthReadySkipsPickerWhenServerAuthAlreadyReady(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	fixture := startRemoteAuthTestFixture(t, workspace, false, apiKeyMemoryAuthHandlerWithoutTimestamp("ready-key"))

	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			t.Fatal("startup auth readiness validation must not open auth picker when server auth is ready")
			return authMethodPickerResult{}, nil
		},
	}

	if err := fixture.server.EnsureAuthReady(context.Background(), interactor, true); err != nil {
		t.Fatalf("EnsureAuthReady: %v", err)
	}

	state, err := fixture.daemon.AuthManager().StoredState(context.Background())
	if err != nil {
		t.Fatalf("StoredState: %v", err)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "ready-key" {
		t.Fatalf("expected startup validation to preserve ready auth, got %+v", state.Method)
	}
}

func TestRemoteLoginTransitionWaitsForAuthChoiceWhenServerAuthAlreadyReady(t *testing.T) {
	_, workspace := newRegisteredAppWorkspace(t)
	t.Setenv("OPENAI_API_KEY", "reauthed-key")
	fixture := startRemoteAuthTestFixture(t, workspace, false, apiKeyMemoryAuthHandlerWithoutTimestamp("old-key"))

	pickerEntered := make(chan struct{})
	releasePicker := make(chan struct{})
	interactor := &interactiveAuthInteractor{
		lookupEnv: func(key string) string {
			if key == "OPENAI_API_KEY" {
				return "reauthed-key"
			}
			return ""
		},
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			close(pickerEntered)
			<-releasePicker
			return authMethodPickerResult{Choice: authMethodChoiceEnvAPIKey}, nil
		},
	}
	done := make(chan error, 1)
	go func() {
		_, err := resolveSessionAction(context.Background(), fixture.server, interactor, "", UITransition{Action: UIActionLogout})
		done <- err
	}()

	select {
	case <-pickerEntered:
	case err := <-done:
		t.Fatalf("login transition returned before auth picker opened: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("auth picker did not open")
	}
	select {
	case err := <-done:
		t.Fatalf("login transition returned while auth picker was waiting: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releasePicker)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("login transition after auth choice: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("login transition did not finish after auth choice")
	}

	state, err := fixture.daemon.AuthManager().StoredState(context.Background())
	if err != nil {
		t.Fatalf("StoredState: %v", err)
	}
	if state.Method.APIKey == nil || state.Method.APIKey.Key != "reauthed-key" {
		t.Fatalf("expected remote login transition to replace auth after choice, got %+v", state.Method)
	}
}
