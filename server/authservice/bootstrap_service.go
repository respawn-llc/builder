package authservice

import (
	"context"
	"strings"

	"core/server/auth"

	servicecontract "core/shared/apicontract"
	"core/shared/config"
	"core/shared/serverapi"
)

type BootstrapService struct {
	manager        *auth.Manager
	oauthOptions   auth.OpenAIOAuthOptions
	authRequired   bool
	allowedPreAuth []string
	supportedModes []serverapi.AuthBootstrapMode
}

func NewBootstrapService(manager *auth.Manager, oauthOptions auth.OpenAIOAuthOptions, settings config.Settings, allowedPreAuthMethods []string) *BootstrapService {
	return &BootstrapService{
		manager:        manager,
		oauthOptions:   oauthOptions,
		authRequired:   StartupAuthRequired(settings),
		allowedPreAuth: append([]string(nil), allowedPreAuthMethods...),
		supportedModes: []serverapi.AuthBootstrapMode{
			serverapi.AuthBootstrapModeNone,
			serverapi.AuthBootstrapModeBrowserCallbackURL,
			serverapi.AuthBootstrapModeBrowserCallbackCode,
			serverapi.AuthBootstrapModeDeviceCode,
			serverapi.AuthBootstrapModeAPIKey,
		},
	}
}

func (s *BootstrapService) GetAuthBootstrapStatus(ctx context.Context, _ serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error) {
	ready, err := s.authReady(ctx)
	if err != nil {
		return serverapi.AuthGetBootstrapStatusResponse{}, err
	}
	stored, err := s.storedState(ctx)
	if err != nil {
		return serverapi.AuthGetBootstrapStatusResponse{}, err
	}
	return serverapi.AuthGetBootstrapStatusResponse{
		AuthReady:              ready,
		AuthRequired:           s.authRequired,
		NoAuthSelected:         stored.IsNoAuthSelected(),
		AuthBootstrapSupported: true,
		AllowedPreAuthMethods:  append([]string(nil), s.allowedPreAuth...),
		SupportedModes:         append([]serverapi.AuthBootstrapMode(nil), s.supportedModes...),
		OAuth: serverapi.AuthBootstrapOAuthConfig{
			Issuer:   strings.TrimSpace(s.oauthOptions.Issuer),
			ClientID: strings.TrimSpace(s.oauthOptions.ClientID),
		},
	}, nil
}

func (s *BootstrapService) AcknowledgeNoAuth(ctx context.Context, _ serverapi.AuthAcknowledgeNoAuthRequest) (serverapi.AuthAcknowledgeNoAuthResponse, error) {
	if s == nil || s.manager == nil {
		return serverapi.AuthAcknowledgeNoAuthResponse{}, serverapi.ErrServerAuthRequired
	}
	stored, err := s.manager.StoredState(ctx)
	if err != nil {
		return serverapi.AuthAcknowledgeNoAuthResponse{}, err
	}
	current, err := s.manager.Load(ctx)
	if err != nil {
		return serverapi.AuthAcknowledgeNoAuthResponse{}, err
	}
	ready := !s.authRequired || auth.EvaluateStartupGate(current).Ready
	if stored.IsNoAuthSelected() {
		return serverapi.AuthAcknowledgeNoAuthResponse{AuthReady: ready, NoAuthSelected: true}, nil
	}
	if ready {
		return serverapi.AuthAcknowledgeNoAuthResponse{AuthReady: true}, nil
	}
	return serverapi.AuthAcknowledgeNoAuthResponse{}, serverapi.ErrServerAuthRequired
}

func (s *BootstrapService) CompleteAuthBootstrap(ctx context.Context, req serverapi.AuthCompleteBootstrapRequest) (serverapi.AuthCompleteBootstrapResponse, error) {
	if err := req.Validate(); err != nil {
		return serverapi.AuthCompleteBootstrapResponse{}, err
	}
	if s == nil || s.manager == nil {
		return serverapi.AuthCompleteBootstrapResponse{}, serverapi.ErrServerAuthRequired
	}
	state, err := s.manager.Load(ctx)
	if err != nil {
		return serverapi.AuthCompleteBootstrapResponse{}, err
	}
	if req.Mode == serverapi.AuthBootstrapModeNone {
		state, err = s.manager.SwitchMethodAndSetEnvAPIKeyPreference(ctx, auth.Method{Type: auth.MethodNone}, auth.EnvAPIKeyPreferencePreferSaved, true, true)
		if err != nil {
			return serverapi.AuthCompleteBootstrapResponse{}, err
		}
		return s.bootstrapResponseFromState(state), nil
	}
	if auth.EvaluateStartupGate(state).Ready && !req.Force {
		return s.bootstrapResponseFromState(state), nil
	}
	var (
		method      auth.Method
		completeErr error
	)
	switch req.Mode {
	case serverapi.AuthBootstrapModeAPIKey:
		method = auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: strings.TrimSpace(req.APIKey)}}
	case serverapi.AuthBootstrapModeBrowserCallbackURL, serverapi.AuthBootstrapModeBrowserCallbackCode:
		method, completeErr = auth.CompleteOpenAIBrowserFlow(ctx, s.oauthOptions, auth.BrowserAuthSession{
			RedirectURI:  strings.TrimSpace(req.RedirectURI),
			State:        strings.TrimSpace(req.OAuthState),
			CodeVerifier: strings.TrimSpace(req.OAuthCodeVerifier),
		}, req.CallbackInput)
	case serverapi.AuthBootstrapModeDeviceCode:
		method, completeErr = auth.CompleteOpenAIDeviceAuthorizationGrant(ctx, s.oauthOptions, strings.TrimSpace(req.DeviceAuthorizationCode), strings.TrimSpace(req.DeviceCodeVerifier))
	default:
		return serverapi.AuthCompleteBootstrapResponse{}, req.Validate()
	}
	if completeErr != nil {
		return serverapi.AuthCompleteBootstrapResponse{}, completeErr
	}
	state, err = s.manager.SwitchMethodAndSetEnvAPIKeyPreference(ctx, method, auth.EnvAPIKeyPreferencePreferSaved, true, true)
	if err != nil {
		return serverapi.AuthCompleteBootstrapResponse{}, err
	}
	return s.bootstrapResponseFromState(state), nil
}

func (s *BootstrapService) authReady(ctx context.Context) (bool, error) {
	if s == nil || s.manager == nil {
		return false, nil
	}
	if !s.authRequired {
		return true, nil
	}
	state, err := s.manager.Load(ctx)
	if err != nil {
		return false, err
	}
	return auth.EvaluateStartupGate(state).Ready, nil
}

func (s *BootstrapService) storedState(ctx context.Context) (auth.State, error) {
	if s == nil || s.manager == nil {
		return auth.EmptyState(), nil
	}
	return s.manager.StoredState(ctx)
}

func (s *BootstrapService) bootstrapResponseFromState(state auth.State) serverapi.AuthCompleteBootstrapResponse {
	accountID := ""
	email := ""
	if state.Method.Type == auth.MethodOAuth && state.Method.OAuth != nil {
		accountID = strings.TrimSpace(state.Method.OAuth.AccountID)
		email = strings.TrimSpace(state.Method.OAuth.Email)
	}
	return serverapi.AuthCompleteBootstrapResponse{
		AuthReady:      !s.authRequired || auth.EvaluateStartupGate(state).Ready,
		NoAuthSelected: state.IsNoAuthSelected(),
		MethodType:     strings.TrimSpace(string(state.Method.Type)),
		AccountID:      accountID,
		Email:          email,
	}
}

var _ servicecontract.AuthBootstrapService = (*BootstrapService)(nil)
