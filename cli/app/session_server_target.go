package app

import (
	"context"

	"core/cli/app/internal/daemonlaunch"
	"core/cli/app/internal/remoteattach"
	"core/cli/app/internal/serverattach"
	"core/cli/app/internal/startupconfig"
	"core/shared/client"
	"core/shared/config"
	"core/shared/protocol"
)

var launchSessionServerDaemon = startLocalInteractiveSessionDaemon
var startInteractiveEmbeddedSessionServer = startEmbeddedServer

// resolveDaemonExecutablePath, buildServeArgsFunc, and buildServeEnvFunc support
// launching a local `kent serve` daemon for the interactive TUI. The headless
// run path no longer starts servers, so these live with the interactive session
// server that still uses them.
var resolveDaemonExecutablePath = startupconfig.ServeExecutablePath
var buildServeArgsFunc = func(_ string, _ Options) []string { return startupconfig.ServeArgs() }
var buildServeEnvFunc = startupconfig.ServeEnv

func startSessionServer(ctx context.Context, opts Options, interactor authInteractor, interactive bool) (interactiveSessionServer, error) {
	cfg, err := startupconfig.ResolveSessionConfig(startupConfigRequest(opts))
	if err != nil {
		return nil, err
	}
	target, err := serverattach.Resolve[interactiveSessionServer](ctx, serverattach.Request[interactiveSessionServer]{
		Mode:   serverattach.ModeInteractive,
		Remote: serverAttachRemotePolicy(cfg, remoteattach.SupportsInteractiveSession, false),
		BypassRemote: func(context.Context) (bool, error) {
			return shouldBypassRemoteStartupForInteractiveOnboardingWithConfig(cfg, interactive), nil
		},
		LaunchDaemon: func(ctx context.Context, _ serverattach.LaunchedRemoteDialer) (serverattach.DaemonTarget[*client.Remote], bool, error) {
			remote, closeFn, ok, err := launchSessionServerDaemon(ctx, opts)
			if err != nil || !ok {
				return serverattach.DaemonTarget[*client.Remote]{}, ok, err
			}
			return serverattach.DaemonTarget[*client.Remote]{Value: remote, Close: closeFn}, true, nil
		},
		WrapRemote: func(remote *client.Remote, cfg config.App, closeFn func() error, ownership serverattach.OwnershipState) (serverattach.Target[interactiveSessionServer], error) {
			server := newRemoteAppServerWithAuth(remote, cfg, closeFn, ownership == serverattach.OwnershipLaunchedDaemon)
			return serverattach.Target[interactiveSessionServer]{Value: server, Close: server.Close}, nil
		},
		StartEmbedded: func(ctx context.Context) (serverattach.Target[interactiveSessionServer], error) {
			server, err := startInteractiveEmbeddedSessionServer(ctx, opts, interactor, interactive)
			if err != nil {
				return serverattach.Target[interactiveSessionServer]{}, err
			}
			return serverattach.Target[interactiveSessionServer]{Value: server, Close: server.Close}, nil
		},
		Validate: func(ctx context.Context, resolution serverattach.Resolution[interactiveSessionServer]) (serverattach.AuthReadiness, error) {
			if resolution.Source == serverattach.SourceEmbeddedFallback {
				return serverattach.AuthReadinessUnchecked, nil
			}
			if err := resolution.Value.EnsureAuthReady(ctx, interactor, interactive); err != nil {
				return serverattach.AuthReadinessUnchecked, err
			}
			return serverattach.AuthReadinessValidated, nil
		},
	})
	if err != nil {
		return nil, err
	}
	return target.Value, nil
}

func shouldBypassRemoteStartupForInteractiveOnboardingWithConfig(cfg config.App, interactive bool) bool {
	if !interactive {
		return false
	}
	return !cfg.Source.SettingsFileExists
}

func startLocalInteractiveSessionDaemon(ctx context.Context, opts Options) (*client.Remote, func() error, bool, error) {
	cfg, err := loadRemoteAttachConfig(opts)
	if err != nil {
		return nil, nil, false, err
	}
	execPath, ok := resolveDaemonExecutablePath()
	if !ok {
		return nil, nil, false, nil
	}
	return daemonlaunch.Launch[*client.Remote](ctx, daemonlaunch.Request[*client.Remote]{
		ExecutablePath: execPath,
		Args:           buildServeArgsFunc("", opts),
		Env:            buildServeEnvFunc(cfg),
		Dial: func(ctx context.Context, childPID int) (*client.Remote, bool, error) {
			remote, ok := tryDialMatchingConfiguredRemoteWithRequirement(ctx, opts, remoteattach.SupportsInteractiveSession, func(identity protocol.ServerIdentity) bool {
				return identity.PID == childPID
			}, false)
			return remote, ok, nil
		},
		CloseTarget: func(remote *client.Remote) error {
			if remote == nil {
				return nil
			}
			return remote.Close()
		},
	})
}
