package authui

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeListener struct {
	redirectURI string
	callback    OAuthBrowserCallback
	waitTimeout time.Duration
	closed      bool
}

func (l *fakeListener) RedirectURI() string { return l.redirectURI }

func (l *fakeListener) Wait(ctx context.Context, timeout time.Duration) (OAuthBrowserCallback, error) {
	l.waitTimeout = timeout
	return l.callback, nil
}

func (l *fakeListener) Close() error {
	l.closed = true
	return nil
}

type recordingPresenter struct {
	autoSession  OAuthBrowserSession
	autoOpenErr  error
	pasteSession OAuthBrowserSession
	pasteOpenErr error
	deviceCode   OAuthDeviceCode
}

func (p *recordingPresenter) ShowBrowserAuto(session OAuthBrowserSession, openErr error) {
	p.autoSession = session
	p.autoOpenErr = openErr
}

func (p *recordingPresenter) ShowBrowserPaste(session OAuthBrowserSession, openErr error) {
	p.pasteSession = session
	p.pasteOpenErr = openErr
}

func (p *recordingPresenter) ShowDeviceCode(code OAuthDeviceCode) {
	p.deviceCode = code
}

func TestRunnerBrowserAutoUsesListenerRedirectAndCallbackQuery(t *testing.T) {
	listener := &fakeListener{
		redirectURI: "http://127.0.0.1/callback",
		callback:    OAuthBrowserCallback{Code: "code-1", State: "state-1"},
	}
	presenter := &recordingPresenter{}
	var beginRedirect string
	var openedURL string
	var completedInput string
	_, err := (OAuthRunner{
		StartCallbackListener: func() (OAuthCallbackListener, error) { return listener, nil },
		BeginBrowserFlow: func(_ OAuthOptions, redirectURI string) (OAuthBrowserSession, error) {
			beginRedirect = redirectURI
			return OAuthBrowserSession{AuthorizeURL: "https://auth.example/authorize", State: "state-1"}, nil
		},
		OpenBrowser: func(url string) error {
			openedURL = url
			return errors.New("open failed")
		},
		CompleteBrowserFlow: func(_ context.Context, _ OAuthOptions, _ OAuthBrowserSession, callbackInput string) (AuthMethod, error) {
			completedInput = callbackInput
			return AuthMethod{}, nil
		},
		OAuthPresenter: presenter,
	}).BrowserAuto(context.Background(), OAuthOptions{PollTimeout: 7 * time.Second})
	if err != nil {
		t.Fatalf("browser auto: %v", err)
	}
	if beginRedirect != listener.redirectURI {
		t.Fatalf("begin redirect = %q, want %q", beginRedirect, listener.redirectURI)
	}
	if openedURL != "https://auth.example/authorize" {
		t.Fatalf("opened URL = %q", openedURL)
	}
	if completedInput != "code=code-1&state=state-1" {
		t.Fatalf("completed input = %q", completedInput)
	}
	if presenter.autoSession.AuthorizeURL != openedURL || presenter.autoOpenErr == nil {
		t.Fatalf("presenter did not receive browser auto event: %+v err=%v", presenter.autoSession, presenter.autoOpenErr)
	}
	if listener.waitTimeout != 7*time.Second || !listener.closed {
		t.Fatalf("listener wait/close mismatch: timeout=%v closed=%v", listener.waitTimeout, listener.closed)
	}
}

func TestRunnerBrowserAutoDelegatesToHybridPageAndClosesListener(t *testing.T) {
	tests := []struct {
		name      string
		page      OAuthBrowserCallbackPageFunc
		wantInput string
		wantErr   string
	}{
		{
			name: "listener callback succeeds",
			page: func(ctx context.Context, opts OAuthOptions, session OAuthBrowserSession, _ error, listener OAuthCallbackListener, complete OAuthCompleteBrowserFlowFunc) (AuthMethod, error) {
				callback, err := listener.Wait(ctx, opts.PollTimeout)
				if err != nil {
					return AuthMethod{}, err
				}
				return complete(ctx, opts, session, callbackQuery(callback).Encode())
			},
			wantInput: "code=code-1&state=state-1",
		},
		{
			name: "pasted callback succeeds",
			page: func(ctx context.Context, opts OAuthOptions, session OAuthBrowserSession, _ error, _ OAuthCallbackListener, complete OAuthCompleteBrowserFlowFunc) (AuthMethod, error) {
				return complete(ctx, opts, session, "http://localhost/callback?code=pasted&state=state-1")
			},
			wantInput: "http://localhost/callback?code=pasted&state=state-1",
		},
		{
			name: "esc cancel returns error",
			page: func(context.Context, OAuthOptions, OAuthBrowserSession, error, OAuthCallbackListener, OAuthCompleteBrowserFlowFunc) (AuthMethod, error) {
				return AuthMethod{}, errors.New("auth canceled by user")
			},
			wantErr: "auth canceled by user",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listener := &fakeListener{
				redirectURI: "http://127.0.0.1/callback",
				callback:    OAuthBrowserCallback{Code: "code-1", State: "state-1"},
			}
			var completedInput string
			_, err := (OAuthRunner{
				StartCallbackListener: func() (OAuthCallbackListener, error) { return listener, nil },
				BeginBrowserFlow: func(_ OAuthOptions, redirectURI string) (OAuthBrowserSession, error) {
					return OAuthBrowserSession{AuthorizeURL: "https://auth.example/authorize", RedirectURI: redirectURI, State: "state-1"}, nil
				},
				OpenBrowser: func(string) error { return nil },
				CompleteBrowserFlow: func(_ context.Context, _ OAuthOptions, _ OAuthBrowserSession, callbackInput string) (AuthMethod, error) {
					completedInput = callbackInput
					return AuthMethod{}, nil
				},
				BrowserCallbackPage: tt.page,
			}).BrowserAuto(context.Background(), OAuthOptions{PollTimeout: 7 * time.Second})
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("BrowserAuto error=%v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("BrowserAuto: %v", err)
			}
			if completedInput != tt.wantInput {
				t.Fatalf("completed input=%q, want %q", completedInput, tt.wantInput)
			}
			if !listener.closed {
				t.Fatal("expected listener to close")
			}
		})
	}
}

func TestRunnerBrowserPastePromptsForCallbackInput(t *testing.T) {
	presenter := &recordingPresenter{}
	var beginRedirect string
	var promptLabel string
	var completedInput string
	_, err := (OAuthRunner{
		BeginBrowserFlow: func(_ OAuthOptions, redirectURI string) (OAuthBrowserSession, error) {
			beginRedirect = redirectURI
			return OAuthBrowserSession{AuthorizeURL: "https://auth.example/paste"}, nil
		},
		OpenBrowser: func(string) error { return nil },
		Prompt: func(label string) (string, error) {
			promptLabel = label
			return "http://localhost/callback?code=manual", nil
		},
		CompleteBrowserFlow: func(_ context.Context, _ OAuthOptions, _ OAuthBrowserSession, callbackInput string) (AuthMethod, error) {
			completedInput = callbackInput
			return AuthMethod{}, nil
		},
		OAuthPresenter: presenter,
	}).BrowserPaste(context.Background(), OAuthOptions{})
	if err != nil {
		t.Fatalf("browser paste: %v", err)
	}
	if beginRedirect != "" {
		t.Fatalf("manual browser flow redirect = %q, want empty fallback", beginRedirect)
	}
	if promptLabel != "Paste callback URL or code: " {
		t.Fatalf("prompt label = %q", promptLabel)
	}
	if completedInput != "http://localhost/callback?code=manual" {
		t.Fatalf("completed input = %q", completedInput)
	}
	if presenter.pasteSession.AuthorizeURL != "https://auth.example/paste" {
		t.Fatalf("presenter did not receive paste session: %+v", presenter.pasteSession)
	}
}

func TestRunnerDevicePresentsCode(t *testing.T) {
	presenter := &recordingPresenter{}
	_, err := (OAuthRunner{
		RunDeviceFlow: func(_ context.Context, _ OAuthOptions, onCode func(OAuthDeviceCode)) (AuthMethod, error) {
			onCode(OAuthDeviceCode{VerificationURL: "https://verify.example", UserCode: "ABCD"})
			return AuthMethod{}, nil
		},
		OAuthPresenter: presenter,
	}).Device(context.Background(), OAuthOptions{})
	if err != nil {
		t.Fatalf("device: %v", err)
	}
	if presenter.deviceCode.VerificationURL != "https://verify.example" || presenter.deviceCode.UserCode != "ABCD" {
		t.Fatalf("presenter device code = %+v", presenter.deviceCode)
	}
}
