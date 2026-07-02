package app

import (
	"context"
	"io"
	"strings"
	"time"

	"core/shared/config"
	"core/shared/serverapi"
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
}

func Run(ctx context.Context, opts Options) error {
	interactor := newInteractiveAuthInteractor()
	server, err := startSessionServer(ctx, opts, interactor, true)
	if err != nil {
		return err
	}
	defer func() { _ = server.Close() }()
	agentRole := strings.TrimSpace(opts.AgentRole)
	return runSessionLifecycleWithOptions(ctx, server, interactor, strings.TrimSpace(opts.SessionID), sessionLifecycleOptions{
		ForceNewSession: agentRole != "" && agentRole != config.DefaultSubagentRole && strings.TrimSpace(opts.SessionID) == "",
		Overrides: serverapi.RunPromptOverrides{
			AgentRole: agentRole,
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
