package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"core/server/auth"

	tea "github.com/charmbracelet/bubbletea"
)

type stubOAuthCallbackListener struct {
	callback auth.BrowserCallback
	waitErr  error
	closed   int
}

type oauthIssuerRewriteRoundTripper func(*http.Request) (*http.Response, error)

func (fn oauthIssuerRewriteRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func rewriteOAuthIssuerClient(server *httptest.Server) *http.Client {
	target, err := url.Parse(server.URL)
	if err != nil {
		panic(err)
	}
	client := server.Client()
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = oauthIssuerRewriteRoundTripper(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		clone.Host = target.Host
		return base.RoundTrip(clone)
	})
	return client
}

func (l *stubOAuthCallbackListener) RedirectURI() string {
	return "http://127.0.0.1:0/callback"
}

func (l *stubOAuthCallbackListener) Wait(context.Context, time.Duration) (auth.BrowserCallback, error) {
	if l.waitErr != nil {
		return auth.BrowserCallback{}, l.waitErr
	}
	return l.callback, nil
}

func (l *stubOAuthCallbackListener) Close() error {
	l.closed++
	return nil
}

func TestAuthMethodPickerSelectsSecondOption(t *testing.T) {
	m := newAuthMethodPickerModel("dark", startupPickerNotice{}, false, true)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = next.(*startupPickerModel)
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*startupPickerModel)
	if m.result.ChoiceID != string(authMethodChoiceDevice) {
		t.Fatalf("choice=%q want %q", m.result.ChoiceID, authMethodChoiceDevice)
	}
}

func TestStartupPickerEnterDoesNothingWhenThereAreNoItems(t *testing.T) {
	m := newStartupPickerModel("**Header**", "Header", "dark", startupPickerNotice{}, nil)
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated := next.(*startupPickerModel)
	if cmd != nil {
		t.Fatal("did not expect quit command for empty picker")
	}
	if updated.result.ChoiceID != "" || updated.result.Canceled {
		t.Fatalf("expected empty result for empty picker, got %+v", updated.result)
	}
}

func TestAuthMethodPickerCancel(t *testing.T) {
	m := newAuthMethodPickerModel("dark", startupPickerNotice{}, false, true)
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	m = next.(*startupPickerModel)
	if !m.result.Canceled {
		t.Fatal("expected canceled result")
	}
}

func TestInteractiveAuthInteractorOffersEnvAPIKeyChoiceWhenAvailable(t *testing.T) {
	ctx := context.Background()
	mgr := auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now)
	pickerCalled := false
	successCalls := 0
	interactor := &interactiveAuthInteractor{
		stderr: io.Discard,
		pickMethod: func(req authInteraction) (authMethodPickerResult, error) {
			pickerCalled = true
			if !req.HasEnvAPIKey {
				t.Fatal("expected env api key to be offered in auth flow")
			}
			return authMethodPickerResult{Choice: authMethodChoiceEnvAPIKey}, nil
		},
		showSuccess: func(authSuccessScreenData) error {
			successCalls++
			return nil
		},
	}

	_, err := interactor.Interact(ctx, authInteraction{
		Manager:      mgr,
		State:        auth.EmptyState(),
		Gate:         auth.StartupGate{Reason: auth.ErrAuthNotConfigured.Error()},
		Theme:        "dark",
		HasEnvAPIKey: true,
	})
	if err != nil {
		t.Fatalf("interact: %v", err)
	}
	if !pickerCalled {
		t.Fatal("expected auth picker to run")
	}
	if successCalls != 1 {
		t.Fatalf("expected success screen once, got %d", successCalls)
	}
	state, err := mgr.Load(ctx)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.EnvAPIKeyPreference != auth.EnvAPIKeyPreferencePreferEnv {
		t.Fatalf("expected env preference saved, got %q", state.EnvAPIKeyPreference)
	}
}

func TestInteractiveAuthInteractorRejectsEnvAPIKeyChoiceWithoutAvailableKey(t *testing.T) {
	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			return authMethodPickerResult{Choice: authMethodChoiceEnvAPIKey}, nil
		},
	}

	_, err := interactor.Interact(context.Background(), authInteraction{
		Manager:      auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now),
		State:        auth.EmptyState(),
		Gate:         auth.StartupGate{Reason: auth.ErrAuthNotConfigured.Error()},
		Theme:        "dark",
		HasEnvAPIKey: false,
	})
	if !errors.Is(err, errEnvAPIKeyUnavailable) {
		t.Fatalf("expected missing OPENAI_API_KEY error, got %v", err)
	}
}

func TestInteractiveAuthInteractorRejectsUnknownAuthMethodChoice(t *testing.T) {
	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			return authMethodPickerResult{Choice: authMethodChoice("bogus")}, nil
		},
	}

	_, err := interactor.Interact(context.Background(), authInteraction{
		Manager: auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now),
		State:   auth.EmptyState(),
		Gate:    auth.StartupGate{Reason: auth.ErrAuthNotConfigured.Error()},
		Theme:   "dark",
	})
	if !errors.Is(err, errUnknownAuthMethod) {
		t.Fatalf("expected unknown auth method error, got %v", err)
	}
}

func TestInteractiveAuthInteractorResolvesEnvConflictAndRemembersPreference(t *testing.T) {
	ctx := context.Background()
	mgr := auth.NewManager(auth.NewMemoryStore(auth.State{
		Scope: auth.ScopeGlobal,
		Method: auth.Method{
			Type: auth.MethodOAuth,
			OAuth: &auth.OAuthMethod{
				AccessToken: "oauth-token",
			},
		},
	}), nil, time.Now)
	successCalls := 0
	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			return authMethodPickerResult{}, errors.New("unexpected auth method picker invocation")
		},
		pickConflict: func(authInteraction) (authConflictPickerResult, error) {
			return authConflictPickerResult{Choice: authConflictChoiceEnvAPIKey}, nil
		},
		showSuccess: func(authSuccessScreenData) error {
			successCalls++
			return nil
		},
	}
	called := false
	interactor.pickConflict = func(req authInteraction) (authConflictPickerResult, error) {
		called = true
		return authConflictPickerResult{Choice: authConflictChoiceEnvAPIKey}, nil
	}

	_, err := interactor.Interact(ctx, authInteraction{
		Manager:      mgr,
		State:        auth.State{Scope: auth.ScopeGlobal, Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{AccessToken: "oauth-token"}}},
		StoredState:  auth.State{Scope: auth.ScopeGlobal, Method: auth.Method{Type: auth.MethodOAuth, OAuth: &auth.OAuthMethod{AccessToken: "oauth-token"}}},
		Gate:         auth.StartupGate{Ready: true},
		Theme:        "dark",
		HasEnvAPIKey: true,
	})
	if err != nil {
		t.Fatalf("interact: %v", err)
	}
	if !called {
		t.Fatal("expected conflict picker to run")
	}
	if successCalls != 0 {
		t.Fatalf("expected no success screen for conflict-only resolution, got %d", successCalls)
	}
	state, err := mgr.Load(ctx)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.EnvAPIKeyPreference != auth.EnvAPIKeyPreferencePreferEnv {
		t.Fatalf("expected env preference saved, got %q", state.EnvAPIKeyPreference)
	}
}

func TestInteractiveAuthInteractorChoosingOAuthWithEnvRemembersSavedPreference(t *testing.T) {
	ctx := context.Background()
	mgr := auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now)
	successCalls := 0
	interactor := &interactiveAuthInteractor{
		stderr: io.Discard,
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			return authMethodPickerResult{Choice: authMethodChoiceDevice}, nil
		},
		runDeviceFlow: func(context.Context, auth.OpenAIOAuthOptions, func(auth.DeviceCode)) (auth.Method, error) {
			return auth.Method{
				Type: auth.MethodOAuth,
				OAuth: &auth.OAuthMethod{
					AccessToken:  "access-token",
					RefreshToken: "refresh-token",
					TokenType:    "Bearer",
					Expiry:       time.Now().Add(time.Hour),
				},
			}, nil
		},
		showSuccess: func(authSuccessScreenData) error {
			successCalls++
			return nil
		},
	}

	_, err := interactor.Interact(ctx, authInteraction{
		Manager:      mgr,
		State:        auth.EmptyState(),
		Gate:         auth.StartupGate{Reason: auth.ErrAuthNotConfigured.Error()},
		Theme:        "dark",
		HasEnvAPIKey: true,
	})
	if err != nil {
		t.Fatalf("interact: %v", err)
	}
	if successCalls != 1 {
		t.Fatalf("expected success screen once, got %d", successCalls)
	}
	state, err := mgr.Load(ctx)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Method.Type != auth.MethodOAuth {
		t.Fatalf("expected oauth auth, got %q", state.Method.Type)
	}
	if state.EnvAPIKeyPreference != auth.EnvAPIKeyPreferencePreferSaved {
		t.Fatalf("expected saved-auth preference after choosing oauth, got %q", state.EnvAPIKeyPreference)
	}
}

func TestInteractiveAuthInteractorRetriesWithFlowErrorAndClearsOnSuccess(t *testing.T) {
	ctx := context.Background()
	mgr := auth.NewManager(auth.NewMemoryStore(auth.EmptyState()), nil, time.Now)
	pickCalls := 0
	deviceCalls := 0
	successCalls := 0
	interactor := &interactiveAuthInteractor{
		stderr: io.Discard,
		pickMethod: func(req authInteraction) (authMethodPickerResult, error) {
			pickCalls++
			switch pickCalls {
			case 1:
				if req.FlowErr != nil {
					t.Fatalf("did not expect initial flow error, got %v", req.FlowErr)
				}
				return authMethodPickerResult{Choice: authMethodChoiceDevice}, nil
			case 2:
				if !errors.Is(req.FlowErr, auth.ErrDeviceCodeUnsupported) {
					t.Fatalf("expected device unsupported error on retry, got %v", req.FlowErr)
				}
				return authMethodPickerResult{Choice: authMethodChoiceDevice}, nil
			default:
				t.Fatalf("did not expect additional picker call %d", pickCalls)
				return authMethodPickerResult{}, nil
			}
		},
		runDeviceFlow: func(context.Context, auth.OpenAIOAuthOptions, func(auth.DeviceCode)) (auth.Method, error) {
			deviceCalls++
			if deviceCalls == 1 {
				return auth.Method{}, auth.ErrDeviceCodeUnsupported
			}
			return auth.Method{
				Type: auth.MethodOAuth,
				OAuth: &auth.OAuthMethod{
					AccessToken:  "access-token",
					RefreshToken: "refresh-token",
					TokenType:    "Bearer",
					Expiry:       time.Now().Add(time.Hour),
				},
			}, nil
		},
		showSuccess: func(authSuccessScreenData) error {
			successCalls++
			return nil
		},
	}

	_, err := interactor.Interact(ctx, authInteraction{
		Manager: mgr,
		State:   auth.EmptyState(),
		Gate:    auth.StartupGate{Reason: auth.ErrAuthNotConfigured.Error()},
		Theme:   "dark",
	})
	if err != nil {
		t.Fatalf("interact: %v", err)
	}
	if pickCalls != 2 {
		t.Fatalf("expected two picker calls, got %d", pickCalls)
	}
	if deviceCalls != 2 {
		t.Fatalf("expected two device flow attempts, got %d", deviceCalls)
	}
	if successCalls != 1 {
		t.Fatalf("expected success screen only after successful retry, got %d", successCalls)
	}
	state, err := mgr.Load(ctx)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if state.Method.Type != auth.MethodOAuth {
		t.Fatalf("expected oauth auth, got %q", state.Method.Type)
	}
}

func TestRunOAuthBrowserAutoClosesListenerAfterSuccessfulCompletion(t *testing.T) {
	listener := &stubOAuthCallbackListener{callback: auth.BrowserCallback{Code: "code-1"}}
	const clientID = "client-1"
	callbackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			if got := r.Form.Get("grant_type"); got != "authorization_code" {
				t.Fatalf("unexpected grant_type %q", got)
			}
			if got := r.Form.Get("code"); got != "code-1" {
				t.Fatalf("unexpected code %q", got)
			}
			if got := r.Form.Get("state"); got != "" {
				t.Fatalf("unexpected state form value %q", got)
			}
			if got := r.Form.Get("client_id"); got != clientID {
				t.Fatalf("unexpected client_id %q", got)
			}
			if got := r.Form.Get("redirect_uri"); got != listener.RedirectURI() {
				t.Fatalf("unexpected redirect_uri %q", got)
			}
			_, _ = w.Write([]byte(`{"access_token":"browser-access","refresh_token":"browser-refresh","token_type":"Bearer","expires_in":1800}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer callbackServer.Close()

	interactor := &interactiveAuthInteractor{
		startCallbackListener: func() (oauthCallbackListener, error) {
			return listener, nil
		},
		openBrowser: func(rawURL string) error {
			parsed, err := url.Parse(rawURL)
			if err != nil {
				return err
			}
			listener.callback.State = parsed.Query().Get("state")
			return nil
		},
		stderr: io.Discard,
	}

	method, err := interactor.authOAuthRunner("dark").BrowserAuto(context.Background(), auth.OpenAIOAuthOptions{
		ClientID:   clientID,
		HTTPClient: rewriteOAuthIssuerClient(callbackServer),
	})
	if err != nil {
		t.Fatalf("expected successful browser auth, got %v", err)
	}
	if method.Type != auth.MethodOAuth || method.OAuth == nil {
		t.Fatalf("unexpected method %+v", method)
	}
	if listener.closed != 1 {
		t.Fatalf("expected listener to be closed once, got %d", listener.closed)
	}
}
