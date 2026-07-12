package app

import (
	"context"
	"errors"
	"fmt"

	"core/cli/app/internal/embeddedattach"
	"core/shared/config"
)

func startEmbeddedServer(ctx context.Context, opts Options, interactor authInteractor, interactive bool) (*embeddedAppServer, error) {
	if interactor == nil {
		return nil, errors.New("auth interactor is required")
	}
	server, err := embeddedattach.Start(ctx, embeddedattach.StartupRequest{
		WorkspaceRoot:         opts.WorkspaceRoot,
		WorkspaceRootExplicit: opts.WorkspaceRootExplicit,
		SessionID:             opts.SessionID,
		OpenAIBaseURL:         opts.OpenAIBaseURL,
		OpenAIBaseURLExplicit: opts.OpenAIBaseURLExplicit,
		LoadOptions: config.LoadOptions{
			Model:               opts.Model,
			ProviderOverride:    opts.ProviderOverride,
			ThinkingLevel:       opts.ThinkingLevel,
			Theme:               opts.Theme,
			ModelTimeoutSeconds: opts.ModelTimeoutSeconds,
			Tools:               opts.Tools,
			ConfigRoot:          opts.ConfigRoot,
		},
		StartupOptions: opts.startupOptions,
	}, interactor, nil)
	if err != nil {
		return nil, err
	}
	// Interactive sessions expose the in-process server over the loopback control
	// endpoints so `kent run` subagents launched from the TUI can attach (kent run
	// is a pure client and never starts its own server). The listeners are torn
	// down when the session's server closes, so the subagents stop with it — which
	// is intended and surfaced in the UI.
	if interactive {
		if err := server.ServeBackground(); err != nil {
			_ = server.Close()
			return nil, fmt.Errorf("expose embedded interactive server for client attach: %w", err)
		}
	}
	return newEmbeddedAppServer(server), nil
}
