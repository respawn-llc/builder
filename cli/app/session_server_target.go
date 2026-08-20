package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"core/cli/app/internal/startupconfig"
	"core/shared/client"
	"core/shared/config"
	capabilitypb "core/shared/protoapi/gen/kent/api/capability"
	serverpb "core/shared/protoapi/gen/kent/api/server"
	"core/shared/protocol"

	"google.golang.org/protobuf/types/known/emptypb"
)

func startSessionServer(ctx context.Context, opts Options, interactor authInteractor, interactive bool) (server interactiveSessionServer, returnErr error) {
	resolved, err := startupconfig.ResolveSessionConfig(startupConfigRequest(opts))
	if err != nil {
		return nil, err
	}
	cfg := resolved.Config
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
	remoteServer := newRemoteAppServerWithAuth(remote, cfg)
	server = remoteServer
	if err := server.EnsureAuthReady(ctx, interactor, interactive); err != nil {
		return nil, err
	}
	readinessResponse, err := remote.GetReadiness(ctx, &emptypb.Empty{})
	if err != nil {
		return nil, newConfiguredServerPreflightError(cfg, "probe server readiness", err)
	}
	readiness := readinessResponse.GetReadiness()
	if !startupReadinessAllowsSession(remote, readiness) {
		if !serverRequiresOnboarding(readiness) {
			return nil, newConfiguredServerPreflightError(cfg, "server is not ready", errors.New(readinessReason(readiness)))
		}
		result, err := runOnboardingFlow(ctx, cfg, remote, remote)
		if err != nil {
			return nil, err
		}
		remoteServer.presentation = startupPresentation{Theme: result.EffectiveTheme}
		readinessResponse, err = remote.GetReadiness(ctx, &emptypb.Empty{})
		if err != nil {
			return nil, newConfiguredServerPreflightError(cfg, "confirm onboarding completion", err)
		}
		readiness = readinessResponse.GetReadiness()
		if !startupReadinessAllowsSession(remote, readiness) {
			return nil, newConfiguredServerPreflightError(cfg, "activate completed onboarding", errors.New(readinessReason(readiness)))
		}
	}
	closeRemote = false
	remoteServer.clientSettings = resolved.Client
	return server, nil
}

func attachConfiguredStartupRemote(ctx context.Context, cfg config.App) (attached *client.Remote, returnErr error) {
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
	if _, err := remote.GetReadiness(ctx, &emptypb.Empty{}); err != nil {
		return nil, newConfiguredServerPreflightError(cfg, "probe server readiness", err)
	}
	if _, err := remote.GetBootstrapStatus(ctx, &emptypb.Empty{}); err != nil {
		return nil, newConfiguredServerPreflightError(cfg, "probe auth bootstrap", err)
	}
	var workspaceRoot *string
	if root := strings.TrimSpace(cfg.WorkspaceRoot); root != "" {
		workspaceRoot = &root
	}
	if _, err := remote.GetFacts(ctx, &capabilitypb.GetFactsRequest{WorkspaceRoot: workspaceRoot}); err != nil {
		return nil, newConfiguredServerPreflightError(cfg, "probe onboarding capability facts", err)
	}
	closeRemote = false
	return remote, nil
}

func validateStartupRemoteIdentity(identity protocol.ServerIdentity) error {
	if identity.ProtocolVersion != protocol.Version {
		return fmt.Errorf("server protocol version %q is incompatible with client protocol version %q", identity.ProtocolVersion, protocol.Version)
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

func serverRequiresOnboarding(readiness *serverpb.Readiness) bool {
	return serverReadinessHasCause(readiness, "onboarding_required")
}

func startupReadinessAllowsSession(remote *client.Remote, readiness *serverpb.Readiness) bool {
	if readiness.GetReady() {
		return true
	}
	return remote != nil &&
		remote.NoAuthBootstrapAcknowledgementEnabled() &&
		readiness.GetAuthRequired() &&
		!readiness.GetAuthReady() &&
		!serverRequiresOnboarding(readiness) &&
		!serverReadinessHasCause(readiness, "activation_failed")
}

func serverReadinessHasCause(readiness *serverpb.Readiness, reason string) bool {
	for _, cause := range readiness.GetCauses() {
		if cause.GetCode() == reason {
			return true
		}
	}
	return false
}

func readinessReason(readiness *serverpb.Readiness) string {
	if len(readiness.GetCauses()) == 0 {
		return "server reported not ready without a reason"
	}
	return readiness.GetCauses()[0].GetCode()
}
