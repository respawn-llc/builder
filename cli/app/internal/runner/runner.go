package runner

import (
	"context"
	"errors"
	"strings"

	"core/shared/config"
	"core/shared/runtimeids"
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
	options, err := SessionLifecycleOptionsFor(req)
	if err != nil {
		return err
	}
	return deps.RunSessionLifecycle(ctx, server, authInteractor, options)
}

func SessionLifecycleOptionsFor[SO any](req Request[SO]) (SessionLifecycleOptions, error) {
	agentRole := strings.TrimSpace(req.AgentRole)
	options := SessionLifecycleOptions{
		Overrides: serverapi.RunPromptOverrides{
			AgentRole: agentRole,
		},
	}
	if req.SessionID != "" && req.WorkspaceContextSessionID != "" {
		return SessionLifecycleOptions{}, errors.New("session ID and workspace context session ID cannot both be set")
	}
	if req.SessionID != "" {
		sessionID, err := runtimeids.ParseSessionID(req.SessionID)
		if err != nil {
			return SessionLifecycleOptions{}, err
		}
		intent := serverapi.OpenExistingSessionLaunchIntent(sessionID)
		options.Intent = &intent
		return options, nil
	}
	if req.WorkspaceContextSessionID != "" {
		parentID, err := runtimeids.ParseSessionID(req.WorkspaceContextSessionID)
		if err != nil {
			return SessionLifecycleOptions{}, err
		}
		intent := serverapi.CreateNewSessionLaunchIntent(&parentID)
		options.Intent = &intent
		return options, nil
	}
	if agentRole != "" && agentRole != config.DefaultSubagentRole {
		intent := serverapi.CreateNewSessionLaunchIntent(nil)
		options.Intent = &intent
	}
	return options, nil
}
