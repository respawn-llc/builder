package startup

import (
	"context"
	"errors"
	"os"

	"core/server/auth"
	"core/server/authservice"
	serverbootstrap "core/server/bootstrap"
	"core/server/capabilityfacts"
	"core/server/core"
	"core/shared/client"
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

type Options struct {
	Core core.Options
}

type AuthHandler interface {
	WrapStore(base auth.Store) auth.Store
	NeedsInteraction(req authservice.FlowInteractionRequest) bool
	Interact(ctx context.Context, req authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error)
	LookupEnv(key string) string
}

type AuthState interface {
	Config() config.App
	OAuthOptions() auth.OpenAIOAuthOptions
	AuthManager() *auth.Manager
}

type OnboardingHandler func(ctx context.Context, req OnboardingRequest) (config.App, error)

type OnboardingRequest struct {
	Config                config.App
	AuthManager           *auth.Manager
	CapabilityFactsClient client.CapabilityFactsClient
	ReloadConfig          func() (config.App, error)
}

func Start(ctx context.Context, req Request, authHandler AuthHandler, onboardingHandler OnboardingHandler) (*EmbeddedServer, error) {
	return StartWithOptions(ctx, req, authHandler, onboardingHandler, Options{})
}

func StartWithOptions(ctx context.Context, req Request, authHandler AuthHandler, onboardingHandler OnboardingHandler, opts Options) (*EmbeddedServer, error) {
	if authHandler == nil {
		return nil, errors.New("auth handler is required")
	}
	bootstrapReq := buildRequest(req, authHandler)
	resolved, err := serverbootstrap.ResolveConfig(bootstrapReq)
	if err != nil {
		return nil, err
	}
	if !resolved.Config.Source.SettingsFileExists && onboardingHandler == nil {
		cfg, deps, surfaceErr := buildStartupControlSurface(ctx, bootstrapReq, !req.AllowUnauthenticated, authHandler, opts)
		if surfaceErr != nil {
			if errors.Is(surfaceErr, errStartupControlSurfaceNotRequired) {
				return startConfiguredEmbeddedServer(ctx, bootstrapReq, !req.AllowUnauthenticated, authHandler, onboardingHandler, opts)
			}
			return nil, surfaceErr
		}
		return &EmbeddedServer{deps: deps, cfg: cfg}, nil
	}
	appCore, err := startCoreWithBootstrap(ctx, bootstrapReq, !req.AllowUnauthenticated, authHandler, onboardingHandler, opts)
	if err != nil {
		if errors.Is(err, ErrOnboardingRequired) {
			return nil, err
		}
		return nil, err
	}
	return &EmbeddedServer{Core: appCore}, nil
}

func startConfiguredEmbeddedServer(ctx context.Context, bootstrapReq serverbootstrap.Request, requireAuth bool, authHandler startupAuthHandler, onboardingHandler OnboardingHandler, opts Options) (*EmbeddedServer, error) {
	appCore, err := startCoreWithBootstrap(ctx, bootstrapReq, requireAuth, authHandler, onboardingHandler, opts)
	if err != nil {
		return nil, err
	}
	return &EmbeddedServer{Core: appCore}, nil
}

func StartCore(ctx context.Context, req Request, authHandler AuthHandler, onboardingHandler OnboardingHandler) (*core.Core, error) {
	return StartCoreWithOptions(ctx, req, authHandler, onboardingHandler, Options{})
}

func StartCoreWithOptions(ctx context.Context, req Request, authHandler AuthHandler, onboardingHandler OnboardingHandler, opts Options) (*core.Core, error) {
	if authHandler == nil {
		return nil, errors.New("auth handler is required")
	}
	bootstrapReq := buildRequest(req, authHandler)
	return startCoreWithBootstrap(ctx, bootstrapReq, !req.AllowUnauthenticated, authHandler, onboardingHandler, opts)
}

type startupAuthHandler interface {
	WrapStore(base auth.Store) auth.Store
	NeedsInteraction(req authservice.FlowInteractionRequest) bool
	Interact(ctx context.Context, req authservice.FlowInteractionRequest) (authservice.FlowInteractionOutcome, error)
}

func startCoreWithBootstrap(ctx context.Context, bootstrapReq serverbootstrap.Request, requireAuth bool, authHandler startupAuthHandler, onboardingHandler OnboardingHandler, opts Options) (*core.Core, error) {
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
			CapabilityFactsClient: client.NewLoopbackCapabilityFactsClient(factsService),
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
	appCore, err := core.NewWithContextOptions(ctx, cfg, authSupport, runtimeSupport, opts.Core)
	if err != nil {
		if _, retained := core.RetainedStartupCleanupCore(err); !retained {
			_ = runtimeSupport.Background.Close()
		}
		return nil, err
	}
	return appCore, nil
}

func EnsureReady(ctx context.Context, state AuthState, authHandler AuthHandler) error {
	if state == nil {
		return errors.New("auth state is required")
	}
	if state.AuthManager() == nil {
		return errAuthManagerRequired
	}
	if authHandler == nil {
		return errors.New("auth handler is required")
	}
	cfg := state.Config()
	return authservice.EnsureFlowReady(
		ctx,
		state.AuthManager(),
		state.OAuthOptions(),
		cfg.Settings.Theme,
		authHandler.LookupEnv,
		authservice.StartupAuthRequired(cfg.Settings),
		true,
		authHandler,
	)
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
	lookupEnv := os.Getenv
	if authHandler != nil {
		lookupEnv = authHandler.LookupEnv
	}
	return serverbootstrap.Request{
		WorkspaceRoot:         req.WorkspaceRoot,
		WorkspaceRootExplicit: req.WorkspaceRootExplicit,
		SessionID:             req.SessionID,
		OpenAIBaseURL:         req.OpenAIBaseURL,
		OpenAIBaseURLExplicit: req.OpenAIBaseURLExplicit,
		LookupEnv:             lookupEnv,
		LoadOptions:           loadOptions,
	}
}
