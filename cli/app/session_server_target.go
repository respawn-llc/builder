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
	"core/shared/protocol"
	"core/shared/serverapi"
)

func startSessionServer(ctx context.Context, opts Options, interactor authInteractor, interactive bool) (server interactiveSessionServer, returnErr error) {
	promptRoots, err := commands.NewClientPromptRoots()
	if err != nil {
		return nil, err
	}
	cfg, err := startupconfig.ResolveSessionConfig(startupConfigRequest(opts))
	if err != nil {
		return nil, err
	}
	remote, err := attachConfiguredStartupRemote(ctx, cfg)
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
		return nil, err
	}
	readiness, err := remote.GetServerReadiness(ctx, serverapi.ServerReadinessRequest{})
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

func attachConfiguredStartupRemote(ctx context.Context, cfg config.App) (remote *client.Remote, returnErr error) {
	remote, err := client.DialConfiguredRemote(ctx, cfg)
	if err != nil {
		return nil, newConfiguredServerPreflightError(cfg, "attach", err)
	}
	closeRemote := true
	defer func() {
		if closeRemote {
			returnErr = errors.Join(returnErr, remote.Close())
		}
	}()
	if err := remote.RequireRoot(config.ExplicitPersistenceRootID(cfg)); err != nil {
		return nil, newConfiguredServerPreflightError(cfg, "validate persistence root", err)
	}
	if err := validateStartupRemoteIdentity(remote.Identity()); err != nil {
		return nil, newConfiguredServerPreflightError(cfg, "validate compatibility", err)
	}
	if _, err := remote.GetServerReadiness(ctx, serverapi.ServerReadinessRequest{}); err != nil {
		return nil, newConfiguredServerPreflightError(cfg, "probe server readiness", err)
	}
	if _, err := remote.GetAuthBootstrapStatus(ctx, serverapi.AuthGetBootstrapStatusRequest{}); err != nil {
		return nil, newConfiguredServerPreflightError(cfg, "probe auth bootstrap", err)
	}
	var workspaceRoot *string
	if root := strings.TrimSpace(cfg.WorkspaceRoot); root != "" {
		workspaceRoot = &root
	}
	if _, err := remote.GetCapabilityFacts(ctx, serverapi.CapabilityFactsRequest{WorkspaceRoot: workspaceRoot}); err != nil {
		return nil, newConfiguredServerPreflightError(cfg, "probe onboarding capability facts", err)
	}
	closeRemote = false
	return remote, nil
}

func validateStartupRemoteIdentity(identity protocol.ServerIdentity) error {
	if identity.ProtocolVersion != protocol.Version {
		return fmt.Errorf("server protocol version %q is incompatible with client protocol version %q", identity.ProtocolVersion, protocol.Version)
	}
	if !identity.Capabilities.AuthBootstrap {
		return errors.New("server does not advertise auth bootstrap support")
	}
	if !identity.Capabilities.OnboardingFinalize {
		return errors.New("server does not advertise onboarding finalization support")
	}
	return nil
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
	for _, cause := range readiness.Causes {
		if cause.Code == string(serverapi.ServerNotReadyOnboardingRequired) {
			return true
		}
	}
	return false
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
