package app

import (
	"context"
	"io"
	"strings"
	"time"

	"core/cli/app/internal/embeddedattach"
	"core/cli/app/internal/runner"
)

type Options struct {
	WorkspaceRoot             string
	WorkspaceRootExplicit     bool
	SessionID                 string
	WorkspaceContextSessionID string
	AgentRole                 string
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
		NewAuthInteractor: newInteractiveAuthInteractor,
		StartSessionServer: func(ctx context.Context, req runner.Request[embeddedattach.StartupOptions], interactor authInteractor, interactive bool) (interactiveSessionServer, error) {
			return startSessionServer(ctx, optionsFromRunnerRequest(req), interactor, interactive)
		},
		RunSessionLifecycle: func(ctx context.Context, server interactiveSessionServer, interactor authInteractor, initialSessionID string, opts runner.SessionLifecycleOptions) error {
			return runSessionLifecycleWithOptions(ctx, server, interactor, initialSessionID, sessionLifecycleOptions{
				ForceNewSession: opts.ForceNewSession,
				Overrides:       opts.Overrides,
			})
		},
	})
}

func RunPrompt(ctx context.Context, opts Options, prompt string, timeout time.Duration, progress io.Writer) (RunPromptResult, error) {
	runClient, closeFn, err := startRunPromptClient(ctx, opts)
	if err != nil {
		return RunPromptResult{}, err
	}
	defer func() {
		if closeFn != nil {
			_ = closeFn()
		}
	}()
	return runPrompt(ctx, runClient, opts, strings.TrimSpace(opts.SessionID), prompt, timeout, progress)
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
