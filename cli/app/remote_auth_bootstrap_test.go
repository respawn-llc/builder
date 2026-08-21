package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"core/cli/app/internal/authui"
	"core/shared/config"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	"core/shared/serverapi"

	"google.golang.org/protobuf/types/known/emptypb"
)

type stubAuthBootstrapClient struct {
	status          *authpb.BootstrapStatus
	completeReq     *authpb.CompleteBootstrapRequest
	completeCalls   int
	completeErr     error
	completeResp    *authpb.BootstrapCompletion
	acknowledgeReq  *emptypb.Empty
	acknowledge     int
	acknowledgeResp *authpb.NoAuthAcknowledgement
	acknowledgeErr  error
}

func (c *stubAuthBootstrapClient) GetBootstrapStatus(context.Context, *emptypb.Empty) (*authpb.BootstrapStatus, error) {
	return c.status, nil
}

func (c *stubAuthBootstrapClient) CompleteBootstrap(_ context.Context, req *authpb.CompleteBootstrapRequest) (*authpb.BootstrapCompletion, error) {
	c.completeCalls++
	c.completeReq = req
	if c.completeErr != nil {
		err := c.completeErr
		c.completeErr = nil
		return nil, err
	}
	if c.completeResp != nil {
		return c.completeResp, nil
	}
	method := "oauth"
	return &authpb.BootstrapCompletion{AuthReady: true, MethodType: &method}, nil
}

func (c *stubAuthBootstrapClient) AcknowledgeNoAuth(_ context.Context, req *emptypb.Empty) (*authpb.NoAuthAcknowledgement, error) {
	c.acknowledge++
	c.acknowledgeReq = req
	if c.acknowledgeErr != nil {
		return nil, c.acknowledgeErr
	}
	if c.acknowledgeResp != nil {
		return c.acknowledgeResp, nil
	}
	return &authpb.NoAuthAcknowledgement{AuthReady: true}, nil
}

type stubOAuthCallbackListener struct {
	callback authui.OAuthBrowserCallback
	waitErr  error
	closed   int
}

func (l *stubOAuthCallbackListener) RedirectURI() string {
	return "http://127.0.0.1:0/callback"
}

func (l *stubOAuthCallbackListener) Wait(context.Context, time.Duration) (authui.OAuthBrowserCallback, error) {
	if l.waitErr != nil {
		return authui.OAuthBrowserCallback{}, l.waitErr
	}
	return l.callback, nil
}

func (l *stubOAuthCallbackListener) Close() error {
	l.closed++
	return nil
}

func TestRemoteAuthBootstrapRetriesUnsupportedSelectedMode(t *testing.T) {
	remote := &stubAuthBootstrapClient{status: &authpb.BootstrapStatus{
		AuthRequired:   true,
		SupportedModes: []authpb.BootstrapMode{authpb.BootstrapMode_BOOTSTRAP_MODE_API_KEY},
	}}
	pickerCalls := 0
	interactor := &interactiveAuthInteractor{
		lookupEnv: func(string) string { return "api-key" },
		pickMethod: func(req authInteraction) (authMethodPickerResult, error) {
			pickerCalls++
			if pickerCalls == 1 {
				return authMethodPickerResult{Choice: authMethodChoiceBrowserAuto}, nil
			}
			if req.FlowErr == nil {
				t.Fatal("unsupported mode must be surfaced on retry")
			}
			return authMethodPickerResult{Choice: authMethodChoiceEnvAPIKey}, nil
		},
	}

	if err := ensureRemoteAuthReady(context.Background(), remote, config.Settings{}, interactor, true); err != nil {
		t.Fatalf("ensureRemoteAuthReady: %v", err)
	}
	if pickerCalls != 2 || remote.completeCalls != 1 {
		t.Fatalf("picker calls=%d complete calls=%d, want 2 and 1", pickerCalls, remote.completeCalls)
	}
}

func TestRemoteAuthBootstrapSurfacesCompletionFailureThenRetries(t *testing.T) {
	completeErr := errors.New("remote completion failed")
	remote := &stubAuthBootstrapClient{
		status: &authpb.BootstrapStatus{
			AuthRequired:   true,
			SupportedModes: []authpb.BootstrapMode{authpb.BootstrapMode_BOOTSTRAP_MODE_API_KEY},
		},
		completeErr: completeErr,
	}
	pickerCalls := 0
	interactor := &interactiveAuthInteractor{
		lookupEnv: func(string) string { return "api-key" },
		pickMethod: func(req authInteraction) (authMethodPickerResult, error) {
			pickerCalls++
			if pickerCalls == 1 && req.FlowErr != nil {
				t.Fatalf("initial flow error = %v", req.FlowErr)
			}
			if pickerCalls == 2 && !errors.Is(req.FlowErr, completeErr) {
				t.Fatalf("retry flow error = %v, want completion failure", req.FlowErr)
			}
			return authMethodPickerResult{Choice: authMethodChoiceEnvAPIKey}, nil
		},
	}

	if err := ensureRemoteAuthReady(context.Background(), remote, config.Settings{}, interactor, true); err != nil {
		t.Fatalf("ensureRemoteAuthReady: %v", err)
	}
	if pickerCalls != 2 || remote.completeCalls != 2 {
		t.Fatalf("picker calls=%d complete calls=%d, want 2 and 2", pickerCalls, remote.completeCalls)
	}
}

func TestRemoteAuthBootstrapMapsProviderDeviceGrantToCompletion(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			_, _ = w.Write([]byte(`{"device_auth_id":"device-1","user_code":"CODE-1","interval":1}`))
		case "/api/accounts/deviceauth/token":
			_, _ = w.Write([]byte(`{"authorization_code":"authorization-1","code_verifier":"verifier-1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer provider.Close()

	remote := &stubAuthBootstrapClient{status: &authpb.BootstrapStatus{
		AuthRequired:   true,
		SupportedModes: []authpb.BootstrapMode{authpb.BootstrapMode_BOOTSTRAP_MODE_DEVICE_CODE},
		Oauth: &authpb.BootstrapOAuthConfig{
			Issuer:   &provider.URL,
			ClientId: ptrString("client-1"),
		},
	}}
	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			return authMethodPickerResult{Choice: authMethodChoiceDevice}, nil
		},
	}

	if err := ensureRemoteAuthReady(context.Background(), remote, config.Settings{}, interactor, true); err != nil {
		t.Fatalf("ensureRemoteAuthReady: %v", err)
	}
	if remote.completeReq.Mode != authpb.BootstrapMode_BOOTSTRAP_MODE_DEVICE_CODE ||
		remote.completeReq.GetDeviceAuthorizationCode() != "authorization-1" ||
		remote.completeReq.GetDeviceCodeVerifier() != "verifier-1" {
		t.Fatalf("unexpected completion request: %+v", remote.completeReq)
	}
}

func TestRemoteAuthBootstrapHybridBrowserAcceptsCallbackOrPaste(t *testing.T) {
	tests := []struct {
		name      string
		runPage   func(context.Context, authCallbackPageData, func(context.Context) (authui.OAuthBrowserCallback, error), func(context.Context, string) (authui.AuthMethod, error)) (authCallbackPageResult, error)
		wantInput string
	}{
		{
			name: "listener callback",
			runPage: func(ctx context.Context, _ authCallbackPageData, waitCallback func(context.Context) (authui.OAuthBrowserCallback, error), complete func(context.Context, string) (authui.AuthMethod, error)) (authCallbackPageResult, error) {
				callback, err := waitCallback(ctx)
				if err != nil {
					return authCallbackPageResult{}, err
				}
				input := browserCallbackInput(callback)
				method, err := complete(ctx, input)
				return authCallbackPageResult{Method: method, CallbackInput: input}, err
			},
			wantInput: "code=code-1&state=",
		},
		{
			name: "pasted callback",
			runPage: func(ctx context.Context, _ authCallbackPageData, _ func(context.Context) (authui.OAuthBrowserCallback, error), complete func(context.Context, string) (authui.AuthMethod, error)) (authCallbackPageResult, error) {
				input := "http://localhost/callback?code=pasted"
				method, err := complete(ctx, input)
				return authCallbackPageResult{Method: method, CallbackInput: input}, err
			},
			wantInput: "http://localhost/callback?code=pasted",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			listener := &stubOAuthCallbackListener{callback: authui.OAuthBrowserCallback{Code: "code-1"}}
			remote := &stubAuthBootstrapClient{status: &authpb.BootstrapStatus{
				AuthReady:    false,
				AuthRequired: true,
				SupportedModes: []authpb.BootstrapMode{
					authpb.BootstrapMode_BOOTSTRAP_MODE_BROWSER_CALLBACK_URL,
				},
			}}
			interactor := &interactiveAuthInteractor{
				pickMethod: func(authInteraction) (authMethodPickerResult, error) {
					return authMethodPickerResult{Choice: authMethodChoiceBrowserAuto}, nil
				},
				startCallbackListener: func() (oauthCallbackListener, error) { return listener, nil },
				openBrowser:           func(string) error { return nil },
				runCallbackPage:       tt.runPage,
			}

			if err := ensureRemoteAuthReady(context.Background(), remote, config.Settings{}, interactor, true); err != nil {
				t.Fatalf("ensureRemoteAuthReady: %v", err)
			}
			if remote.completeReq.GetCallbackInput() != tt.wantInput {
				t.Fatalf("callback input=%q, want %q", remote.completeReq.GetCallbackInput(), tt.wantInput)
			}
			if listener.closed == 0 {
				t.Fatal("expected listener to close")
			}
		})
	}
}

func TestRemoteAuthBootstrapHybridBrowserCancelClosesListener(t *testing.T) {
	listener := &stubOAuthCallbackListener{}
	remote := &stubAuthBootstrapClient{status: &authpb.BootstrapStatus{
		AuthReady:    false,
		AuthRequired: true,
		SupportedModes: []authpb.BootstrapMode{
			authpb.BootstrapMode_BOOTSTRAP_MODE_BROWSER_CALLBACK_URL,
		},
	}}
	pickCalls := 0
	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			pickCalls++
			if pickCalls > 1 {
				return authMethodPickerResult{Canceled: true}, nil
			}
			return authMethodPickerResult{Choice: authMethodChoiceBrowserAuto}, nil
		},
		startCallbackListener: func() (oauthCallbackListener, error) { return listener, nil },
		openBrowser:           func(string) error { return nil },
		runCallbackPage: func(context.Context, authCallbackPageData, func(context.Context) (authui.OAuthBrowserCallback, error), func(context.Context, string) (authui.AuthMethod, error)) (authCallbackPageResult, error) {
			return authCallbackPageResult{Canceled: true}, nil
		},
	}

	err := ensureRemoteAuthReady(context.Background(), remote, config.Settings{}, interactor, true)
	if err == nil || !errors.Is(err, ErrAuthCanceledByUser) {
		t.Fatalf("expected auth cancel, got %v", err)
	}
	if listener.closed == 0 {
		t.Fatal("expected listener to close")
	}
}

func TestRemoteAuthBootstrapRejectsMismatchedOAuthState(t *testing.T) {
	listener := &stubOAuthCallbackListener{}
	remote := &stubAuthBootstrapClient{status: &authpb.BootstrapStatus{
		AuthReady:    false,
		AuthRequired: true,
		SupportedModes: []authpb.BootstrapMode{
			authpb.BootstrapMode_BOOTSTRAP_MODE_BROWSER_CALLBACK_URL,
		},
	}}
	pickCalls := 0
	var flowErr error
	interactor := &interactiveAuthInteractor{
		pickMethod: func(req authInteraction) (authMethodPickerResult, error) {
			pickCalls++
			if pickCalls > 1 {
				flowErr = req.FlowErr
				return authMethodPickerResult{Canceled: true}, nil
			}
			return authMethodPickerResult{Choice: authMethodChoiceBrowserAuto}, nil
		},
		startCallbackListener: func() (oauthCallbackListener, error) { return listener, nil },
		openBrowser:           func(string) error { return nil },
		runCallbackPage: func(ctx context.Context, _ authCallbackPageData, _ func(context.Context) (authui.OAuthBrowserCallback, error), complete func(context.Context, string) (authui.AuthMethod, error)) (authCallbackPageResult, error) {
			input := "http://localhost/callback?code=pasted&state=wrong"
			method, err := complete(ctx, input)
			return authCallbackPageResult{Method: method, CallbackInput: input}, err
		},
	}

	err := ensureRemoteAuthReady(context.Background(), remote, config.Settings{}, interactor, true)
	if err == nil || !errors.Is(err, ErrAuthCanceledByUser) {
		t.Fatalf("expected auth cancel, got %v", err)
	}
	if flowErr == nil || !errors.Is(flowErr, ErrOAuthStateMismatch) {
		t.Fatalf("flow error = %v, want oauth state mismatch", flowErr)
	}
}

func TestRemoteAuthBootstrapNoAuthSelectionCompletesWithoutRePrompt(t *testing.T) {
	remote := &stubAuthBootstrapClient{
		status: &authpb.BootstrapStatus{
			AuthReady:    false,
			AuthRequired: true,
			SupportedModes: []authpb.BootstrapMode{
				authpb.BootstrapMode_BOOTSTRAP_MODE_NONE,
			},
		},
		completeResp:    &authpb.BootstrapCompletion{AuthReady: false, NoAuthSelected: true},
		acknowledgeResp: &authpb.NoAuthAcknowledgement{AuthReady: false, NoAuthSelected: true},
	}
	pickerCalls := 0
	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			pickerCalls++
			if pickerCalls > 1 {
				t.Fatal("no-auth completion must not re-enter the auth picker")
			}
			return authMethodPickerResult{Choice: authMethodChoiceSkip}, nil
		},
	}

	if err := ensureRemoteAuthReady(context.Background(), remote, config.Settings{}, interactor, true); err != nil {
		t.Fatalf("ensureRemoteAuthReady: %v", err)
	}
	if remote.completeReq.Mode != authpb.BootstrapMode_BOOTSTRAP_MODE_NONE {
		t.Fatalf("complete mode = %q, want none", remote.completeReq.Mode)
	}
	if remote.acknowledge != 1 {
		t.Fatalf("acknowledge calls = %d, want 1", remote.acknowledge)
	}
}

func TestRemoteAuthBootstrapNoAuthSelectionPropagatesAcknowledgementError(t *testing.T) {
	ackErr := errors.New("ack failed")
	remote := &stubAuthBootstrapClient{
		status: &authpb.BootstrapStatus{
			AuthReady:      false,
			AuthRequired:   true,
			SupportedModes: []authpb.BootstrapMode{authpb.BootstrapMode_BOOTSTRAP_MODE_NONE},
		},
		completeResp:   &authpb.BootstrapCompletion{AuthReady: false, NoAuthSelected: true},
		acknowledgeErr: ackErr,
	}
	pickerCalls := 0
	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			pickerCalls++
			if pickerCalls > 1 {
				t.Fatal("acknowledgement failure after persisted no-auth must not re-open the auth picker")
			}
			return authMethodPickerResult{Choice: authMethodChoiceSkip}, nil
		},
	}

	err := ensureRemoteAuthReady(context.Background(), remote, config.Settings{}, interactor, true)
	if !errors.Is(err, ackErr) {
		t.Fatalf("ensureRemoteAuthReady error = %v, want ackErr", err)
	}
	if pickerCalls != 1 {
		t.Fatalf("picker calls = %d, want 1", pickerCalls)
	}
}

func TestRemoteAuthBootstrapPersistedNoAuthAcknowledgesWithoutPicker(t *testing.T) {
	remote := &stubAuthBootstrapClient{
		status: &authpb.BootstrapStatus{
			AuthReady:      false,
			AuthRequired:   true,
			NoAuthSelected: true,
			SupportedModes: []authpb.BootstrapMode{
				authpb.BootstrapMode_BOOTSTRAP_MODE_NONE,
			},
		},
		acknowledgeResp: &authpb.NoAuthAcknowledgement{AuthReady: false, NoAuthSelected: true},
	}
	interactor := &interactiveAuthInteractor{
		pickMethod: func(authInteraction) (authMethodPickerResult, error) {
			t.Fatal("persisted no-auth should not open auth picker")
			return authMethodPickerResult{}, nil
		},
	}

	if err := ensureRemoteAuthReady(context.Background(), remote, config.Settings{}, interactor, true); err != nil {
		t.Fatalf("ensureRemoteAuthReady: %v", err)
	}
	if remote.acknowledge != 1 {
		t.Fatalf("acknowledge calls = %d, want 1", remote.acknowledge)
	}
	if remote.completeCalls != 0 {
		t.Fatalf("complete calls = %d, want 0", remote.completeCalls)
	}
}

func TestRemoteAuthBootstrapHeadlessDoesNotAcknowledgePersistedNoAuth(t *testing.T) {
	remote := &stubAuthBootstrapClient{
		status: &authpb.BootstrapStatus{
			AuthReady:      false,
			AuthRequired:   true,
			NoAuthSelected: true,
			SupportedModes: []authpb.BootstrapMode{authpb.BootstrapMode_BOOTSTRAP_MODE_NONE},
		},
		acknowledgeResp: &authpb.NoAuthAcknowledgement{AuthReady: false, NoAuthSelected: true},
	}

	err := ensureRemoteAuthReady(context.Background(), remote, config.Settings{}, newHeadlessAuthInteractor(), false)
	if !errors.Is(err, serverapi.ErrServerAuthRequired) {
		t.Fatalf("ensureRemoteAuthReady error = %v, want ErrServerAuthRequired", err)
	}
	if remote.acknowledge != 0 {
		t.Fatalf("acknowledge calls = %d, want 0", remote.acknowledge)
	}
	if remote.completeCalls != 0 {
		t.Fatalf("complete calls = %d, want 0", remote.completeCalls)
	}
}
