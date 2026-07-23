package runner

import (
	"context"
	"errors"
	"strings"

	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
)

type failureSource interface {
	Failures() <-chan error
}

func RunInteractive[S SessionServer, A any, SO any](ctx context.Context, req Request[SO], deps Dependencies[S, A, SO]) (runErr error) {
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
	defer func() {
		closeErr := server.Close()
		if closeErr == nil {
			return
		}
		if runErr == nil || !errors.Is(closeErr, runErr) {
			runErr = errors.Join(runErr, closeErr)
		}
	}()
	options, err := SessionLifecycleOptionsFor(req)
	if err != nil {
		return err
	}
	lifecycleCtx, cancelLifecycle := context.WithCancelCause(ctx)
	defer cancelLifecycle(nil)
	stopFailureWatch := make(chan struct{})
	failureWatchDone := make(chan struct{})
	var failureCh <-chan error
	if source, ok := any(server).(failureSource); ok {
		failureCh = source.Failures()
	}
	if failureCh != nil {
		go func() {
			defer close(failureWatchDone)
			select {
			case failure := <-failureCh:
				cancelLifecycle(failure)
			case <-stopFailureWatch:
			}
		}()
	} else {
		close(failureWatchDone)
	}
	runErr = deps.RunSessionLifecycle(lifecycleCtx, server, authInteractor, options)
	close(stopFailureWatch)
	<-failureWatchDone
	if cause := context.Cause(lifecycleCtx); cause != nil && !errors.Is(cause, context.Canceled) {
		if runErr == nil || errors.Is(runErr, context.Canceled) {
			runErr = cause
		} else if !errors.Is(runErr, cause) {
			runErr = errors.Join(runErr, cause)
		}
	}
	return runErr
}

func SessionLifecycleOptionsFor[SO any](req Request[SO]) (SessionLifecycleOptions, error) {
	var agentRole *string
	if req.AgentRole != nil {
		value := strings.TrimSpace(*req.AgentRole)
		if value == "" {
			return SessionLifecycleOptions{}, errors.New("agent role must not be blank")
		}
		agentRole = &value
	}
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
		intent := serverapi.CreateNewSessionLaunchIntent(serverapi.ParentAgentSessionCreateOrigin(parentID))
		options.Intent = &intent
		return options, nil
	}
	if agentRole != nil && *agentRole != config.DefaultSubagentRole {
		intent := serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin())
		options.Intent = &intent
	}
	return options, nil
}
