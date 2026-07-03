package runner

import (
	"context"
	"errors"
	"strings"

	"core/shared/config"
	"core/shared/serverapi"
)

func RunInteractive[S SessionServer, A any, SO any](ctx context.Context, req Request[SO], deps Dependencies[S, A, SO]) error {
	if deps.NewAuthInteractor == nil {
		return errors.New("auth interactor factory is required")
	}
	if deps.StartSessionServer == nil {
		return errors.New("session server starter is required")
	}
	if deps.RunSessionLifecycle == nil {
		return errors.New("session lifecycle runner is required")
	}
	authInteractor := deps.NewAuthInteractor()
	server, err := deps.StartSessionServer(ctx, req, authInteractor, true)
	if err != nil {
		return err
	}
	defer func() { _ = server.Close() }()
	return deps.RunSessionLifecycle(ctx, server, authInteractor, strings.TrimSpace(req.SessionID), SessionLifecycleOptionsFor(req))
}

func SessionLifecycleOptionsFor[SO any](req Request[SO]) SessionLifecycleOptions {
	agentRole := strings.TrimSpace(req.AgentRole)
	return SessionLifecycleOptions{
		ForceNewSession: agentRole != "" && agentRole != config.DefaultSubagentRole && strings.TrimSpace(req.SessionID) == "",
		Overrides: serverapi.RunPromptOverrides{
			AgentRole: agentRole,
		},
		TerminalPhaseMarkerEncoder:      req.TerminalPhaseMarkerEncoder,
		TerminalPhaseMarkerSinkObserver: req.TerminalPhaseMarkerSinkObserver,
	}
}
