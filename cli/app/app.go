package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"time"

	"core/cli/app/internal/runner"
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
}

func Run(ctx context.Context, opts Options) error {
	interactor := newInteractiveAuthInteractor()
	return runner.RunInteractive(ctx, runnerRequestFromOptions(opts), runner.Dependencies{
		StartSessionServer: func(ctx context.Context) (io.Closer, error) {
			return startSessionServer(ctx, opts, interactor, true)
		},
		RunSessionLifecycle: func(ctx context.Context, server io.Closer, intent *serverapi.SessionLaunchIntent, overrides serverapi.RunPromptOverrides) error {
			interactive, ok := server.(interactiveSessionServer)
			if !ok {
				return errors.New("interactive session server is required")
			}
			return runSessionLifecycleWithOptions(ctx, interactive, interactor, sessionLifecycleOptions{
				Intent:    intent,
				Overrides: overrides,
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

func runnerRequestFromOptions(opts Options) runner.Request {
	return runner.Request{
		SessionID:                 opts.SessionID,
		WorkspaceContextSessionID: opts.WorkspaceContextSessionID,
		AgentRole:                 opts.AgentRole,
	}
}
