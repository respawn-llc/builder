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

func (s *BootstrapService) GetAuthBootstrapStatus(ctx context.Context, req serverapi.AuthGetBootstrapStatusRequest) (serverapi.AuthGetBootstrapStatusResponse, error) {
	return servicecontract.WithValidated(
		req,
		servicecontract.NoSemanticValidation,
		func(request servicecontract.Validated[serverapi.AuthGetBootstrapStatusRequest]) (serverapi.AuthGetBootstrapStatusResponse, error) {
			return s.GetAuthBootstrapStatusValidated(ctx, request)
		},
	)
}

func (s *BootstrapService) GetAuthBootstrapStatusValidated(ctx context.Context, _ servicecontract.Validated[serverapi.AuthGetBootstrapStatusRequest]) (serverapi.AuthGetBootstrapStatusResponse, error) {
	if s == nil {
		return serverapi.AuthGetBootstrapStatusResponse{}, nil
	}
	var (
		manager      *auth.Manager
		authRequired bool
	)
	authRequired = s.authRequired
	if authRequired {
		manager = s.manager
	}
	readiness, err := EvaluateReadiness(ctx, manager, authRequired)
	if err != nil {
		return serverapi.AuthGetBootstrapStatusResponse{}, err
	}
	stored, err := s.storedState(ctx)
	if err != nil {
		return serverapi.AuthGetBootstrapStatusResponse{}, err
	}
	return serverapi.AuthGetBootstrapStatusResponse{
		AuthReady:              readiness.Ready,
		AuthRequired:           readiness.Required,
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

func (s *BootstrapService) AcknowledgeNoAuth(ctx context.Context, req serverapi.AuthAcknowledgeNoAuthRequest) (serverapi.AuthAcknowledgeNoAuthResponse, error) {
	return servicecontract.WithValidated(
		req,
		servicecontract.NoSemanticValidation,
		func(request servicecontract.Validated[serverapi.AuthAcknowledgeNoAuthRequest]) (serverapi.AuthAcknowledgeNoAuthResponse, error) {
			return s.AcknowledgeNoAuthValidated(ctx, request)
		},
	)
}

func (s *BootstrapService) AcknowledgeNoAuthValidated(ctx context.Context, _ servicecontract.Validated[serverapi.AuthAcknowledgeNoAuthRequest]) (serverapi.AuthAcknowledgeNoAuthResponse, error) {
	if s == nil || s.manager == nil {
		return serverapi.AuthAcknowledgeNoAuthResponse{}, serverapi.ErrServerAuthRequired
	}
	stored, err := s.manager.StoredState(ctx)
	if err != nil {
		return serverapi.AuthAcknowledgeNoAuthResponse{}, err
	}
	readiness, err := EvaluateReadiness(ctx, s.manager, s.authRequired)
	if err != nil {
		return serverapi.AuthAcknowledgeNoAuthResponse{}, err
	}
	if stored.IsNoAuthSelected() {
		return serverapi.AuthAcknowledgeNoAuthResponse{AuthReady: readiness.Ready, NoAuthSelected: true}, nil
	}
	if readiness.Ready {
		return serverapi.AuthAcknowledgeNoAuthResponse{AuthReady: true}, nil
	}
	return serverapi.AuthAcknowledgeNoAuthResponse{}, serverapi.ErrServerAuthRequired
}

func (s *BootstrapService) CompleteAuthBootstrap(ctx context.Context, req serverapi.AuthCompleteBootstrapRequest) (serverapi.AuthCompleteBootstrapResponse, error) {
	return servicecontract.WithValidated(
		req,
		servicecontract.SemanticValidationRequired,
		func(request servicecontract.Validated[serverapi.AuthCompleteBootstrapRequest]) (serverapi.AuthCompleteBootstrapResponse, error) {
			return s.CompleteAuthBootstrapValidated(ctx, request)
		},
	)
}

func (s *BootstrapService) CompleteAuthBootstrapValidated(ctx context.Context, validated servicecontract.Validated[serverapi.AuthCompleteBootstrapRequest]) (serverapi.AuthCompleteBootstrapResponse, error) {
	req := validated.Value()
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
var _ servicecontract.AuthBootstrapTrustedService = (*BootstrapService)(nil)
