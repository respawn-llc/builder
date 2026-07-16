package embeddedattach

import (
	"context"

	serverauth "core/server/auth"
	"core/server/authservice"
	serverstartup "core/server/startup"
	"core/shared/apicontract"
	"core/shared/config"
)

type Server = serverstartup.EmbeddedServer
type AuthManager = serverauth.Manager
type StartupOptions = serverstartup.Options

type AuthHandler interface {
	WrapStore(base serverauth.Store) serverauth.Store
	NeedsInteraction(req authservice.FlowInteractionRequest) bool
	Interact(ctx context.Context, req authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error)
	LookupEnv(key string) string
}

type StartupRequest struct {
	WorkspaceRoot         string
	WorkspaceRootExplicit bool
	SessionID             string
	OpenAIBaseURL         string
	OpenAIBaseURLExplicit bool
	LoadOptions           config.LoadOptions
	StartupOptions        serverstartup.Options
}

type OnboardingRequest struct {
	Config                config.App
	AuthManager           *AuthManager
	CapabilityFactsClient apicontract.CapabilityFactsService
	ReloadConfig          func() (config.App, error)
}

type OnboardingHandler func(ctx context.Context, req OnboardingRequest) (config.App, error)

func Start(ctx context.Context, req StartupRequest, authHandler AuthHandler, onboardingHandler OnboardingHandler) (*Server, error) {
	return serverstartup.StartWithOptions(ctx, buildStartupRequest(req), authHandler, adaptOnboardingHandler(onboardingHandler), req.StartupOptions)
}

func buildStartupRequest(req StartupRequest) serverstartup.Request {
	return serverstartup.Request{
		WorkspaceRoot:         req.WorkspaceRoot,
		WorkspaceRootExplicit: req.WorkspaceRootExplicit,
		SessionID:             req.SessionID,
		OpenAIBaseURL:         req.OpenAIBaseURL,
		OpenAIBaseURLExplicit: req.OpenAIBaseURLExplicit,
		LoadOptions:           req.LoadOptions,
	}
}

func adaptOnboardingHandler(handler OnboardingHandler) serverstartup.OnboardingHandler {
	if handler == nil {
		return nil
	}
	return func(ctx context.Context, req serverstartup.OnboardingRequest) (config.App, error) {
		return handler(ctx, OnboardingRequest{
			Config:                req.Config,
			AuthManager:           req.AuthManager,
			CapabilityFactsClient: req.CapabilityFactsClient,
			ReloadConfig:          req.ReloadConfig,
		})
	}
}
