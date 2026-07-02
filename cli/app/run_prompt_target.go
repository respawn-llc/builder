package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"core/cli/app/internal/remoteattach"
	"core/cli/app/internal/serverattach"
	"core/cli/app/internal/startupconfig"
	"core/shared/client"
	"core/shared/config"
	"core/shared/protocol"
	"core/shared/serverapi"
)

var dialConfiguredRemote = client.DialConfiguredRemoteForProjectWorkspaceID
var dialConfiguredProjectViewRemote = func(ctx context.Context, cfg config.App) (remoteattach.ProjectViewRemote, error) {
	return client.DialConfiguredRemote(ctx, cfg)
}

var configuredRemoteAttachTimeout = 500 * time.Millisecond
var configuredRemoteWorkspaceDiscoveryTimeout = 5 * time.Second

type configuredProjectViewRemote = remoteattach.ProjectViewRemote

type runPromptWorkspaceConfig struct {
	Options          Options
	Config           config.App
	ContextAgentRole string
}

func startRunPromptClient(ctx context.Context, opts Options) (client.RunPromptClient, func() error, error) {
	workspaceConfig, err := resolveRunPromptWorkspaceConfig(opts)
	if err != nil {
		return nil, nil, err
	}
	opts = workspaceConfig.Options
	cfg := workspaceConfig.Config
	kentSessionCaller := strings.TrimSpace(opts.WorkspaceContextSessionID) != ""
	contextAgentRole := strings.TrimSpace(workspaceConfig.ContextAgentRole)
	if err := validateRunPromptAgentRole(cfg.Settings, opts.AgentRole, kentSessionCaller, contextAgentRole); err != nil {
		return nil, nil, err
	}
	// Omitting LaunchDaemon and StartEmbedded keeps kent run a pure client (see
	// docs/dev/specs/core-runtime-tools.md): Resolve returns ErrNoServerAvailable
	// when nothing is reachable, translated into errRunRequiresServer below.
	target, err := serverattach.Resolve[serverattach.RunPromptTarget](ctx, serverattach.Request[serverattach.RunPromptTarget]{
		Mode:   serverattach.ModeHeadless,
		Remote: serverAttachRemotePolicy(cfg, remoteattach.SupportsRunPrompt, true),
		WrapRemote: func(remote *client.Remote, cfg config.App, closeFn func() error, _ serverattach.OwnershipState) (serverattach.Target[serverattach.RunPromptTarget], error) {
			target := serverattach.RunPromptRemoteWithClose(remote, cfg, closeFn)
			return serverattach.Target[serverattach.RunPromptTarget]{Value: target.Value, Close: target.Close}, nil
		},
		Validate: func(ctx context.Context, resolution serverattach.Resolution[serverattach.RunPromptTarget]) (serverattach.AuthReadiness, error) {
			if err := serverattach.ValidateRunPromptTarget(ctx, serverattach.RunPromptValidateRequest{
				Target: resolution.Value,
				Config: cfg,
				EnsureAuthReady: func(ctx context.Context, auth client.AuthBootstrapClient) error {
					return ensureRemoteAuthReady(ctx, auth, cfg.Settings, newHeadlessAuthInteractor(), false)
				},
			}); err != nil {
				return serverattach.AuthReadinessUnchecked, err
			}
			if resolution.Value.Auth == nil {
				return serverattach.AuthReadinessUnchecked, nil
			}
			return serverattach.AuthReadinessValidated, nil
		},
	})
	if err != nil {
		var incompatible *serverattach.IncompatibleServerError
		if errors.As(err, &incompatible) {
			if reason := strings.TrimSpace(incompatible.Reason); reason != "" {
				return nil, nil, fmt.Errorf("%w (%s)", errRunServerIncompatible, reason)
			}
			return nil, nil, errRunServerIncompatible
		}
		var rootMismatch *serverattach.RootMismatchServerError
		if errors.As(err, &rootMismatch) {
			if reason := strings.TrimSpace(rootMismatch.Reason); reason != "" {
				return nil, nil, fmt.Errorf("%w (%s)", errRunServerRootMismatch, reason)
			}
			return nil, nil, errRunServerRootMismatch
		}
		if errors.Is(err, serverattach.ErrNoServerAvailable) {
			return nil, nil, errRunRequiresServer
		}
		return nil, nil, err
	}
	return target.Value.Client, target.Close, nil
}

// errRunRequiresServer is returned when no server is reachable for `kent run`.
var errRunRequiresServer = errors.New("`kent run` can only be used when a server is already running. Start a server with `kent serve` or install a service with `kent service install` to prevent subagents and scripted runs from exiting abruptly if running concurrently with each other")

// errRunServerIncompatible is returned when a reachable server fails the
// capability check.
var errRunServerIncompatible = errors.New("a Kent server is running on the configured endpoint but is not compatible with this client. Restart or upgrade the running server (for example `kent service restart`) instead of starting another, which would conflict on the same address")

// errRunServerRootMismatch is returned when a reachable server on the configured
// endpoint serves a different persistence root than the one selected. Starting
// another server on the same address would only hit a bind conflict, so the
// operator must stop/reconfigure the other-root server or target a matching
// endpoint.
var errRunServerRootMismatch = errors.New("a Kent server is running on the configured endpoint but serves a different persistence root than the selected one. Stop or reconfigure that server, or point `--persistence-root`/the configured endpoint at the matching instance, instead of starting another server which would conflict on the same address")

const nonCallableSubagentRoleMessage = "User has disallowed calling this agent by other agents like you. Do not try to circumvent this, pick another suitable agent or do the work manually and let the user know your desire to use the subagent at the end of the task"

// errNonCallableSubagentRole and errUnrecognizedSubagentRole classify
// run-prompt agent-role validation failures. Callers and tests match these with
// errors.Is rather than comparing rendered message text.
var (
	errNonCallableSubagentRole  = errors.New(nonCallableSubagentRoleMessage)
	errUnrecognizedSubagentRole = errors.New("unrecognized subagent role")
)

func validateRunPromptAgentRole(settings config.Settings, rawRole string, kentSessionCaller bool, contextAgentRole string) error {
	override, err := serverapi.RunPromptOverrides{AgentRole: rawRole}.AgentRoleOverride()
	if err != nil {
		return err
	}
	if !override.Present || override.Default {
		if kentSessionCaller {
			if err := validateContextAgentRoleCallable(settings, contextAgentRole); err != nil {
				return err
			}
		}
		return nil
	}
	roleName := override.Role
	lookup := config.LookupSubagentRole(settings, roleName)
	if lookup.Status != config.SubagentRoleLookupPresent {
		return fmt.Errorf("%w: %s. It may have been removed by the user during the session. Available roles: [%s]", errUnrecognizedSubagentRole, strconv.Quote(roleName), strings.Join(config.AvailableSubagentRoleNames(settings, kentSessionCaller), ", "))
	}
	if kentSessionCaller && !config.SubagentRoleCallable(lookup.Role) {
		return errNonCallableSubagentRole
	}
	return nil
}

func validateContextAgentRoleCallable(settings config.Settings, rawRole string) error {
	lookup := config.LookupSubagentRole(settings, rawRole)
	if lookup.Status == config.SubagentRoleLookupInvalid {
		return nil
	}
	if lookup.Status == config.SubagentRoleLookupMissing {
		return fmt.Errorf("%w: %s. It may have been removed by the user during the session. Available roles: [%s]", errUnrecognizedSubagentRole, strconv.Quote(*lookup.NormalizedSelector), strings.Join(config.AvailableSubagentRoleNames(settings, true), ", "))
	}
	if !config.SubagentRoleCallable(lookup.Role) {
		return errNonCallableSubagentRole
	}
	return nil
}

func tryDialMatchingConfiguredRunPromptRemote(ctx context.Context, opts Options, accept func(protocol.ServerIdentity) bool) (*client.Remote, bool, error) {
	workspaceConfig, err := resolveRunPromptWorkspaceConfig(opts)
	if err != nil {
		return nil, false, err
	}
	return serverattach.DialRemote(ctx, serverattach.ModeHeadless, serverAttachRemotePolicy(workspaceConfig.Config, remoteattach.SupportsRunPrompt, true), accept)
}

func tryDialMatchingConfiguredRemoteWithRequirement(ctx context.Context, opts Options, supports func(protocol.CapabilityFlags) bool, accept func(protocol.ServerIdentity) bool, requireRegistered bool) (*client.Remote, bool) {
	cfg, err := loadRemoteAttachConfig(opts)
	if err != nil {
		return nil, false
	}
	remote, ok, err := serverattach.DialRemote(ctx, serverattach.ModeInteractive, serverAttachRemotePolicy(cfg, supports, requireRegistered), accept)
	if err != nil {
		return nil, false
	}
	return remote, ok
}

func loadRemoteAttachConfig(opts Options) (config.App, error) {
	workspaceConfig, err := resolveRunPromptWorkspaceConfig(opts)
	return workspaceConfig.Config, err
}

func resolveRunPromptWorkspaceConfig(opts Options) (runPromptWorkspaceConfig, error) {
	result, err := startupconfig.ResolveRunPromptConfig(startupConfigRequest(opts))
	if err != nil {
		return runPromptWorkspaceConfig{}, err
	}
	resolvedOpts := opts
	if strings.TrimSpace(result.ResolvedWorkspaceRoot) != "" && result.ResolvedWorkspaceRoot != opts.WorkspaceRoot {
		resolvedOpts.WorkspaceRoot = result.ResolvedWorkspaceRoot
	}
	return runPromptWorkspaceConfig{Options: resolvedOpts, Config: result.Config, ContextAgentRole: result.ContextAgentRole}, nil
}

func startupConfigRequest(opts Options) startupconfig.Request {
	return startupconfig.Request{
		WorkspaceRoot:             opts.WorkspaceRoot,
		WorkspaceRootExplicit:     opts.WorkspaceRootExplicit,
		SessionID:                 opts.SessionID,
		WorkspaceContextSessionID: opts.WorkspaceContextSessionID,
		OpenAIBaseURL:             opts.OpenAIBaseURL,
		OpenAIBaseURLExplicit:     opts.OpenAIBaseURLExplicit,
		LoadOptions: config.LoadOptions{
			Model:               opts.Model,
			ProviderOverride:    opts.ProviderOverride,
			ThinkingLevel:       opts.ThinkingLevel,
			Theme:               opts.Theme,
			ModelTimeoutSeconds: opts.ModelTimeoutSeconds,
			Tools:               opts.Tools,
			ConfigRoot:          opts.ConfigRoot,
		},
	}
}

func serverAttachRemotePolicy(cfg config.App, supports remoteattach.Supports, requireBound bool) serverattach.RemotePolicy {
	return serverattach.RemotePolicy{
		Config:           cfg,
		AttachTimeout:    configuredRemoteAttachTimeout,
		DiscoveryTimeout: configuredRemoteWorkspaceDiscoveryTimeout,
		DialProjectView:  dialConfiguredProjectViewRemote,
		DialWorkspace:    dialConfiguredRemote,
		Supports:         supports,
		RequireBound:     requireBound,
		RootID:           config.ExplicitPersistenceRootID(cfg),
	}
}
