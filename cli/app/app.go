package app

import (
	"context"
	"strings"
	"time"

	"core/cli/app/internal/embeddedattach"
	"core/cli/app/internal/runner"
	"core/cli/app/internal/startupconfig"
	"core/shared/config"
	"core/shared/serverapi"
)

type Options struct {
	WorkspaceRoot             string
	WorkspaceRootExplicit     bool
	SessionID                 string
	WorkspaceContextSessionID string
	AgentRole                 *string
	Model                     string
	ProviderOverride          string
	ThinkingLevel             string
	Theme                     string
	ModelTimeoutSeconds       int
	Tools                     string
	OpenAIBaseURL             string
	OpenAIBaseURLExplicit     bool
	ConfigRoot                string
	startupOptions            embeddedattach.StartupOptions
}

func Run(ctx context.Context, opts Options) error {
	return runner.RunInteractive(ctx, runnerRequestFromOptions(opts), runner.Dependencies[interactiveSessionServer, authInteractor, embeddedattach.StartupOptions]{
		ResolveInteractiveConfig: func(req runner.Request[embeddedattach.StartupOptions]) (runner.InteractiveConfig, error) {
			resolved, err := startupconfig.ResolveInteractiveConfig(startupConfigRequest(optionsFromRunnerRequest(req)))
			if err != nil {
				return runner.InteractiveConfig{}, err
			}
			return runner.InteractiveConfig{Server: resolved.Server, Client: resolved.Client}, nil
		},
		NewAuthInteractor: newInteractiveAuthInteractor,
		StartSessionServer: func(ctx context.Context, req runner.Request[embeddedattach.StartupOptions], interactor authInteractor, interactive bool, cfg config.App) (interactiveSessionServer, error) {
			return startSessionServer(ctx, optionsFromRunnerRequest(req), interactor, interactive, cfg)
		},
		RunSessionLifecycle: func(ctx context.Context, server interactiveSessionServer, interactor authInteractor, clientSettings config.ClientSettings, opts runner.SessionLifecycleOptions) error {
			return runSessionLifecycleWithOptions(ctx, server, interactor, clientSettings, sessionLifecycleOptions{
				Intent:    opts.Intent,
				Overrides: opts.Overrides,
			})
		},
	})
}

func RunPrompt(ctx context.Context, opts Options, prompt string, timeout time.Duration, progress serverapi.RunPromptProgressSink) (RunPromptResult, error) {
	workspaceConfig, err := resolveRunPromptWorkspaceConfig(opts)
	if err != nil {
		return RunPromptResult{}, err
	}
	runClient, closeFn, err := startRunPromptClientWithWorkspaceConfig(ctx, workspaceConfig)
	if err != nil {
		return RunPromptResult{}, err
	}
	defer func() {
		if closeFn != nil {
			_ = closeFn()
		}
	}()
	return runPrompt(ctx, runClient, workspaceConfig.Options, workspaceConfig.CallerContext, strings.TrimSpace(opts.SessionID), prompt, timeout, progress)
}

func runnerRequestFromOptions(opts Options) runner.Request[embeddedattach.StartupOptions] {
	return runner.Request[embeddedattach.StartupOptions]{
		WorkspaceRoot:             opts.WorkspaceRoot,
		WorkspaceRootExplicit:     opts.WorkspaceRootExplicit,
		SessionID:                 opts.SessionID,
		WorkspaceContextSessionID: opts.WorkspaceContextSessionID,
		AgentRole:                 opts.AgentRole,
		Model:                     opts.Model,
		ProviderOverride:          opts.ProviderOverride,
		ThinkingLevel:             opts.ThinkingLevel,
		Theme:                     opts.Theme,
		ModelTimeoutSeconds:       opts.ModelTimeoutSeconds,
		Tools:                     opts.Tools,
		OpenAIBaseURL:             opts.OpenAIBaseURL,
		OpenAIBaseURLExplicit:     opts.OpenAIBaseURLExplicit,
		ConfigRoot:                opts.ConfigRoot,
		StartupOptions:            opts.startupOptions,
	}
}

func optionsFromRunnerRequest(req runner.Request[embeddedattach.StartupOptions]) Options {
	return Options{
		WorkspaceRoot:             req.WorkspaceRoot,
		WorkspaceRootExplicit:     req.WorkspaceRootExplicit,
		SessionID:                 req.SessionID,
		WorkspaceContextSessionID: req.WorkspaceContextSessionID,
		AgentRole:                 req.AgentRole,
		Model:                     req.Model,
		ProviderOverride:          req.ProviderOverride,
		ThinkingLevel:             req.ThinkingLevel,
		Theme:                     req.Theme,
		ModelTimeoutSeconds:       req.ModelTimeoutSeconds,
		Tools:                     req.Tools,
		OpenAIBaseURL:             req.OpenAIBaseURL,
		OpenAIBaseURLExplicit:     req.OpenAIBaseURLExplicit,
		ConfigRoot:                req.ConfigRoot,
		startupOptions:            req.StartupOptions,
	}
}
