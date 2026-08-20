package authservice

import (
	"context"
	"errors"
	"strings"

	"core/server/auth"

	"core/shared/config"
	authpb "core/shared/protoapi/gen/kent/api/auth"
	"core/shared/serverapi"

	"google.golang.org/protobuf/types/known/emptypb"
)

type BootstrapService struct {
	manager        *auth.Manager
	oauthOptions   auth.OpenAIOAuthOptions
	authRequired   bool
	supportedModes []authpb.BootstrapMode
}

func NewBootstrapService(manager *auth.Manager, oauthOptions auth.OpenAIOAuthOptions, settings config.Settings) *BootstrapService {
	return &BootstrapService{
		manager:      manager,
		oauthOptions: oauthOptions,
		authRequired: StartupAuthRequired(settings),
		supportedModes: []authpb.BootstrapMode{
			authpb.BootstrapMode_BOOTSTRAP_MODE_NONE,
			authpb.BootstrapMode_BOOTSTRAP_MODE_BROWSER_CALLBACK_URL,
			authpb.BootstrapMode_BOOTSTRAP_MODE_BROWSER_CALLBACK_CODE,
			authpb.BootstrapMode_BOOTSTRAP_MODE_DEVICE_CODE,
			authpb.BootstrapMode_BOOTSTRAP_MODE_API_KEY,
		},
	}
}

func (s *BootstrapService) GetBootstrapStatus(ctx context.Context, _ *emptypb.Empty) (*authpb.BootstrapStatus, error) {
	ready, err := s.authReady(ctx)
	if err != nil {
		return nil, err
	}
	stored, err := s.storedState(ctx)
	if err != nil {
		return nil, err
	}
	issuer := strings.TrimSpace(s.oauthOptions.Issuer)
	clientID := strings.TrimSpace(s.oauthOptions.ClientID)
	status := &authpb.BootstrapStatus{
		AuthReady:              ready,
		AuthRequired:           s.authRequired,
		NoAuthSelected:         stored.IsNoAuthSelected(),
		AuthBootstrapSupported: true,
		SupportedModes:         append([]authpb.BootstrapMode(nil), s.supportedModes...),
	}
	if issuer != "" || clientID != "" {
		status.Oauth = &authpb.BootstrapOAuthConfig{}
		if issuer != "" {
			status.Oauth.Issuer = &issuer
		}
		if clientID != "" {
			status.Oauth.ClientId = &clientID
		}
	}
	return status, nil
}

func (s *BootstrapService) AcknowledgeNoAuth(ctx context.Context, _ *emptypb.Empty) (*authpb.NoAuthAcknowledgement, error) {
	if s == nil || s.manager == nil {
		return nil, serverapi.ErrServerAuthRequired
	}
	stored, err := s.manager.StoredState(ctx)
	if err != nil {
		return nil, err
	}
	current, err := s.manager.Load(ctx)
	if err != nil {
		return nil, err
	}
	ready := !s.authRequired || auth.EvaluateStartupGate(current).Ready
	if stored.IsNoAuthSelected() {
		return &authpb.NoAuthAcknowledgement{AuthReady: ready, NoAuthSelected: true}, nil
	}
	if ready {
		return &authpb.NoAuthAcknowledgement{AuthReady: true}, nil
	}
	return nil, serverapi.ErrServerAuthRequired
}

func (s *BootstrapService) CompleteBootstrap(ctx context.Context, req *authpb.CompleteBootstrapRequest) (*authpb.BootstrapCompletion, error) {
	if req == nil {
		return nil, errors.New("complete bootstrap request is required")
	}
	if s == nil || s.manager == nil {
		return nil, serverapi.ErrServerAuthRequired
	}
	state, err := s.manager.Load(ctx)
	if err != nil {
		return nil, err
	}
	if req.Mode == authpb.BootstrapMode_BOOTSTRAP_MODE_NONE {
		state, err = s.manager.SwitchMethodAndSetEnvAPIKeyPreference(ctx, auth.Method{Type: auth.MethodNone}, auth.EnvAPIKeyPreferencePreferSaved, true, true)
		if err != nil {
			return nil, err
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
	case authpb.BootstrapMode_BOOTSTRAP_MODE_API_KEY:
		method = auth.Method{Type: auth.MethodAPIKey, APIKey: &auth.APIKeyMethod{Key: strings.TrimSpace(req.GetApiKey())}}
	case authpb.BootstrapMode_BOOTSTRAP_MODE_BROWSER_CALLBACK_URL, authpb.BootstrapMode_BOOTSTRAP_MODE_BROWSER_CALLBACK_CODE:
		method, completeErr = auth.CompleteOpenAIBrowserFlow(ctx, s.oauthOptions, auth.BrowserAuthSession{
			RedirectURI:  strings.TrimSpace(req.GetRedirectUri()),
			State:        strings.TrimSpace(req.GetOauthState()),
			CodeVerifier: strings.TrimSpace(req.GetOauthCodeVerifier()),
		}, req.GetCallbackInput())
	case authpb.BootstrapMode_BOOTSTRAP_MODE_DEVICE_CODE:
		method, completeErr = auth.CompleteOpenAIDeviceAuthorizationGrant(ctx, s.oauthOptions, strings.TrimSpace(req.GetDeviceAuthorizationCode()), strings.TrimSpace(req.GetDeviceCodeVerifier()))
	default:
		return nil, errors.New("validated auth bootstrap request has unsupported mode")
	}
	if completeErr != nil {
		return nil, completeErr
	}
	state, err = s.manager.SwitchMethodAndSetEnvAPIKeyPreference(ctx, method, auth.EnvAPIKeyPreferencePreferSaved, true, true)
	if err != nil {
		return nil, err
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

func (s *BootstrapService) bootstrapResponseFromState(state auth.State) *authpb.BootstrapCompletion {
	var accountID *string
	var email *string
	if state.Method.Type == auth.MethodOAuth && state.Method.OAuth != nil {
		if value := strings.TrimSpace(state.Method.OAuth.AccountID); value != "" {
			accountID = &value
		}
		if value := strings.TrimSpace(state.Method.OAuth.Email); value != "" {
			email = &value
		}
	}
	methodType := strings.TrimSpace(string(state.Method.Type))
	return &authpb.BootstrapCompletion{
		AuthReady:      !s.authRequired || auth.EvaluateStartupGate(state).Ready,
		NoAuthSelected: state.IsNoAuthSelected(),
		MethodType:     &methodType,
		AccountId:      accountID,
		Email:          email,
	}
}
