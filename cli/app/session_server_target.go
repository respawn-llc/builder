package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/cli/app/commands"
	"core/cli/app/internal/startupconfig"
	"core/shared/client"
	"core/shared/config"
	"core/shared/serverapi"
)

func startSessionServer(ctx context.Context, opts Options, interactor authInteractor, interactive bool) (server interactiveSessionServer, returnErr error) {
	return startSessionServerWithConnection(ctx, opts, interactor, interactive, nil)
}

func startSessionServerWithConnection(ctx context.Context, opts Options, interactor authInteractor, interactive bool, connection *interactiveConnectionOwner) (server interactiveSessionServer, returnErr error) {
	promptRoots, err := commands.NewClientPromptRoots()
	if err != nil {
		return nil, err
	}
	cfg, err := startupconfig.ResolveSessionConfig(startupConfigRequest(opts))
	if err != nil {
		return nil, err
	}
	remote, err := attachConfiguredStartupRemoteWithConnection(ctx, cfg, connection)
	if err != nil {
		return nil, err
	}
	closeRemote := true
	defer func() {
		if closeRemote {
			returnErr = errors.Join(returnErr, remote.Close())
		}
	}()
	remoteServer := newRemoteAppServerWithAuthAndPromptRoots(remote, cfg, remote.Close, false, promptRoots)
	server = remoteServer
	if err := server.EnsureAuthReady(ctx, interactor, interactive); err != nil {
		if connection != nil {
			connection.ObserveUnary(err)
		}
		return nil, err
	}
	readiness, err := remote.GetServerReadiness(ctx, serverapi.ServerReadinessRequest{})
	if connection != nil {
		connection.ObserveUnary(err)
	}
	if err != nil {
		return nil, newConfiguredServerPreflightError(cfg, "probe server readiness", err)
	}
	if !startupReadinessAllowsSession(remote, readiness) {
		if !serverRequiresOnboarding(readiness) {
			return nil, newConfiguredServerPreflightError(cfg, "server is not ready", errors.New(readinessReason(readiness)))
		}
		result, err := runOnboardingFlow(ctx, cfg, remote, remote)
		if err != nil {
			return nil, err
		}
		remoteServer.presentation = startupPresentation{Theme: result.EffectiveTheme}
		readiness, err = remote.GetServerReadiness(ctx, serverapi.ServerReadinessRequest{})
		if connection != nil {
			connection.ObserveUnary(err)
		}
		if err != nil {
			return nil, newConfiguredServerPreflightError(cfg, "confirm onboarding completion", err)
		}
		if !startupReadinessAllowsSession(remote, readiness) {
			return nil, newConfiguredServerPreflightError(cfg, "activate completed onboarding", errors.New(readinessReason(readiness)))
		}
	}
	closeRemote = false
	return server, nil
}

func attachConfiguredStartupRemote(ctx context.Context, cfg config.App) (attached *client.Remote, returnErr error) {
	return attachConfiguredStartupRemoteWithConnection(ctx, cfg, nil)
}

func attachConfiguredStartupRemoteWithConnection(ctx context.Context, cfg config.App, connection *interactiveConnectionOwner) (attached *client.Remote, returnErr error) {
	remote, err := client.DialConfiguredRemote(ctx, cfg)
	if connection != nil {
		connection.ObserveUnary(err)
	}
	if err != nil {
		return nil, newConfiguredServerPreflightError(cfg, "attach", err)
	}
	closeRemote := true
	defer func() {
		if closeRemote {
			returnErr = errors.Join(returnErr, remote.Close())
		}
	}()
	var workspaceRoot *string
	if root := strings.TrimSpace(cfg.WorkspaceRoot); root != "" {
		workspaceRoot = &root
	}
	if err := validateConfiguredRemoteWithWorkspace(ctx, remote, config.ExplicitPersistenceRootID(cfg), workspaceRoot); err != nil {
		if connection != nil {
			connection.ObserveUnary(err)
		}
		return nil, newConfiguredServerPreflightError(cfg, "validate configured remote", err)
	}
	if connection != nil {
		connection.ObserveUnary(nil)
	}
	closeRemote = false
	return remote, nil
}

type configuredServerPreflightError struct {
	endpoint  string
	operation string
	cause     error
}

func (e *configuredServerPreflightError) Error() string {
	return fmt.Sprintf("configured server %s: %s: %v", e.endpoint, e.operation, e.cause)
}

func (e *configuredServerPreflightError) Unwrap() error {
	return e.cause
}

func newConfiguredServerPreflightError(cfg config.App, operation string, cause error) error {
	return &configuredServerPreflightError{
		endpoint:  config.ServerRPCURL(cfg),
		operation: operation,
		cause:     cause,
	}
}

func serverRequiresOnboarding(readiness serverapi.ServerReadinessResponse) bool {
	return serverReadinessHasCause(readiness, serverapi.ServerNotReadyOnboardingRequired)
}

func startupReadinessAllowsSession(remote *client.Remote, readiness serverapi.ServerReadinessResponse) bool {
	if readiness.Ready {
		return true
	}
	return remote != nil &&
		remote.NoAuthBootstrapAcknowledgementEnabled() &&
		readiness.AuthRequired &&
		!readiness.AuthReady &&
		!serverRequiresOnboarding(readiness) &&
		!serverReadinessHasCause(readiness, serverapi.ServerNotReadyActivationFailed)
}

func serverReadinessHasCause(readiness serverapi.ServerReadinessResponse, reason serverapi.ServerNotReadyReason) bool {
	for _, cause := range readiness.Causes {
		if cause.Code == string(reason) {
			return true
		}
	}
	return false
}

func readinessReason(readiness serverapi.ServerReadinessResponse) string {
	if len(readiness.Causes) == 0 {
		return "server reported not ready without a reason"
	}
	return readiness.Causes[0].Code
}
