package startup

import (
	"context"

	"core/server/auth"
	"core/server/authservice"
	serverbootstrap "core/server/bootstrap"
	"core/server/capabilityfacts"
	"core/server/core"
	"core/shared/apicontract"
	"core/shared/config"
)

type Request struct {
	WorkspaceRoot         string
	WorkspaceRootExplicit bool
	AllowUnauthenticated  bool
	SessionID             string
	Model                 string
	ProviderOverride      string
	ThinkingLevel         string
	Theme                 string
	ModelTimeoutSeconds   int
	Tools                 string
	OpenAIBaseURL         string
	OpenAIBaseURLExplicit bool
	LoadOptions           config.LoadOptions
}

type AuthHandler interface {
	WrapStore(base auth.Store) auth.Store
	NeedsInteraction(req authservice.FlowInteractionRequest) bool
	Interact(ctx context.Context, req authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error)
	LookupEnv(key string) string
}

type OnboardingHandler func(ctx context.Context, req OnboardingRequest) (config.App, error)

type OnboardingRequest struct {
	Config                config.App
	AuthManager           *auth.Manager
	CapabilityFactsClient apicontract.CapabilityFactsService
	ReloadConfig          func() (config.App, error)
}

func startCoreWithBootstrap(ctx context.Context, bootstrapReq serverbootstrap.Request, requireAuth bool, authHandler AuthHandler, onboardingHandler OnboardingHandler) (*core.Core, error) {
	resolved, err := serverbootstrap.ResolveConfig(bootstrapReq)
	if err != nil {
		return nil, err
	}
	cfg := resolved.Config
	store := authHandler.WrapStore(auth.NewFileStore(config.GlobalAuthConfigPath(cfg)))
	authSupport, err := serverbootstrap.BuildAuthSupport(store, bootstrapReq.LookupEnv, bootstrapReq.Now)
	if err != nil {
		return nil, err
	}
	if requireAuth {
		if err := authservice.EnsureFlowReady(ctx, authSupport.AuthManager, authSupport.OAuthOptions, cfg.Settings.Theme, bootstrapReq.LookupEnv, authservice.StartupAuthRequired(cfg.Settings), false, authHandler); err != nil {
			return nil, err
		}
	}
	if onboardingHandler != nil {
		factsService := capabilityfacts.NewService(capabilityfacts.Options{Config: cfg, AuthManager: authSupport.AuthManager})
		cfg, err = onboardingHandler(ctx, OnboardingRequest{
			Config:                cfg,
			AuthManager:           authSupport.AuthManager,
			CapabilityFactsClient: factsService,
			ReloadConfig: func() (config.App, error) {
				refreshed, err := serverbootstrap.ResolveConfig(bootstrapReq)
				if err != nil {
					return config.App{}, err
				}
				return refreshed.Config, nil
			},
		})
		if err != nil {
			return nil, err
		}
	}
	if !cfg.Source.SettingsFileExists {
		return nil, ErrOnboardingRequired
	}
	runtimeSupport, err := serverbootstrap.BuildRuntimeSupport(cfg)
	if err != nil {
		return nil, err
	}
	appCore, err := core.NewWithContext(ctx, cfg, authSupport, runtimeSupport)
	if err != nil {
		_ = runtimeSupport.Background.Close()
		return nil, err
	}
	return appCore, nil
}

func buildRequest(req Request, authHandler AuthHandler) serverbootstrap.Request {
	loadOptions := req.LoadOptions
	if loadOptions == (config.LoadOptions{}) {
		loadOptions = config.LoadOptions{
			Model:               req.Model,
			ProviderOverride:    req.ProviderOverride,
			ThinkingLevel:       req.ThinkingLevel,
			Theme:               req.Theme,
			ModelTimeoutSeconds: req.ModelTimeoutSeconds,
			Tools:               req.Tools,
		}
	}
	return serverbootstrap.Request{
		WorkspaceRoot:         req.WorkspaceRoot,
		WorkspaceRootExplicit: req.WorkspaceRootExplicit,
		SessionID:             req.SessionID,
		OpenAIBaseURL:         req.OpenAIBaseURL,
		OpenAIBaseURLExplicit: req.OpenAIBaseURLExplicit,
		LookupEnv:             authHandler.LookupEnv,
		LoadOptions:           loadOptions,
	}
}
