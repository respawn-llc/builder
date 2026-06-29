package app

import (
	"context"
	"errors"
	"testing"

	"core/cli/app/internal/authui"
	"core/shared/config"
	"core/shared/serverapi"
)

type stubAuthBootstrapClient struct {
	status          serverapi.AuthGetBootstrapStatusResponse
	completeReq     serverapi.AuthCompleteBootstrapRequest
	completeCalls   int
	completeResp    serverapi.AuthCompleteBootstrapResponse
	acknowledgeReq  serverapi.AuthAcknowledgeNoAuthRequest
	acknowledge     int
	acknowledgeResp serverapi.AuthAcknowledgeNoAuthResponse
	acknowledgeErr  error
}

func (c *stubAuthBootstrapClient) GetAuthBootstrapStatus(context.Context, serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error) {
	return c.status, nil
}

func (c *stubAuthBootstrapClient) CompleteAuthBootstrap(_ context.Context, req serverapi.AuthCompleteBootstrapRequest) (serverapi.AuthCompleteBootstrapResponse, error) {
	c.completeCalls++
	c.completeReq = req
	if c.completeResp != (serverapi.AuthCompleteBootstrapResponse{}) {
		return c.completeResp, nil
	}
	return serverapi.AuthCompleteBootstrapResponse{AuthReady: true, MethodType: "oauth"}, nil
}

func (c *stubAuthBootstrapClient) AcknowledgeNoAuth(_ context.Context, req serverapi.AuthAcknowledgeNoAuthRequest) (serverapi.AuthAcknowledgeNoAuthResponse, error) {
	c.acknowledge++
	c.acknowledgeReq = req
	if c.acknowledgeErr != nil {
		return serverapi.AuthAcknowledgeNoAuthResponse{}, c.acknowledgeErr
	}
	if c.acknowledgeResp != (serverapi.AuthAcknowledgeNoAuthResponse{}) {
		return c.acknowledgeResp, nil
	}
	return serverapi.AuthAcknowledgeNoAuthResponse{AuthReady: true}, nil
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
			remote := &stubAuthBootstrapClient{status: serverapi.AuthGetBootstrapStatusResponse{
				AuthReady:    false,
				AuthRequired: true,
				SupportedModes: []serverapi.AuthBootstrapMode{
					serverapi.AuthBootstrapModeBrowserCallbackURL,
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
			if remote.completeReq.CallbackInput != tt.wantInput {
				t.Fatalf("callback input=%q, want %q", remote.completeReq.CallbackInput, tt.wantInput)
			}
			if listener.closed == 0 {
				t.Fatal("expected listener to close")
			}
		})
	}
}

func TestRemoteAuthBootstrapHybridBrowserCancelClosesListener(t *testing.T) {
	listener := &stubOAuthCallbackListener{}
	remote := &stubAuthBootstrapClient{status: serverapi.AuthGetBootstrapStatusResponse{
		AuthReady:    false,
		AuthRequired: true,
		SupportedModes: []serverapi.AuthBootstrapMode{
			serverapi.AuthBootstrapModeBrowserCallbackURL,
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
	remote := &stubAuthBootstrapClient{status: serverapi.AuthGetBootstrapStatusResponse{
		AuthReady:    false,
		AuthRequired: true,
		SupportedModes: []serverapi.AuthBootstrapMode{
			serverapi.AuthBootstrapModeBrowserCallbackURL,
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
		status: serverapi.AuthGetBootstrapStatusResponse{
			AuthReady:    false,
			AuthRequired: true,
			SupportedModes: []serverapi.AuthBootstrapMode{
				serverapi.AuthBootstrapModeNone,
			},
		},
		completeResp:    serverapi.AuthCompleteBootstrapResponse{AuthReady: false, NoAuthSelected: true},
		acknowledgeResp: serverapi.AuthAcknowledgeNoAuthResponse{AuthReady: false, NoAuthSelected: true},
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
	if remote.completeReq.Mode != serverapi.AuthBootstrapModeNone {
		t.Fatalf("complete mode = %q, want none", remote.completeReq.Mode)
	}
	if remote.acknowledge != 1 {
		t.Fatalf("acknowledge calls = %d, want 1", remote.acknowledge)
	}
}

func TestRemoteAuthBootstrapNoAuthSelectionPropagatesAcknowledgementError(t *testing.T) {
	ackErr := errors.New("ack failed")
	remote := &stubAuthBootstrapClient{
		status: serverapi.AuthGetBootstrapStatusResponse{
			AuthReady:      false,
			AuthRequired:   true,
			SupportedModes: []serverapi.AuthBootstrapMode{serverapi.AuthBootstrapModeNone},
		},
		completeResp:   serverapi.AuthCompleteBootstrapResponse{AuthReady: false, NoAuthSelected: true},
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
		status: serverapi.AuthGetBootstrapStatusResponse{
			AuthReady:      false,
			AuthRequired:   true,
			NoAuthSelected: true,
			SupportedModes: []serverapi.AuthBootstrapMode{
				serverapi.AuthBootstrapModeNone,
			},
		},
		acknowledgeResp: serverapi.AuthAcknowledgeNoAuthResponse{AuthReady: false, NoAuthSelected: true},
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
		status: serverapi.AuthGetBootstrapStatusResponse{
			AuthReady:      false,
			AuthRequired:   true,
			NoAuthSelected: true,
			SupportedModes: []serverapi.AuthBootstrapMode{serverapi.AuthBootstrapModeNone},
		},
		acknowledgeResp: serverapi.AuthAcknowledgeNoAuthResponse{AuthReady: false, NoAuthSelected: true},
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
