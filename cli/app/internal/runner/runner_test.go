package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"core/server/core"
	serverstartup "core/server/startup"
	"core/shared/config"
	"core/shared/serverapi"
)

type fakeServer struct {
	closed bool
}

func runnerStringPtr(value string) *string { return &value }

func resolveEmptyInteractiveConfig[SO any](Request[SO]) (InteractiveConfig, error) {
	return InteractiveConfig{}, nil
}

func loadRunnerClientSettings(t *testing.T) config.ClientSettings {
	t.Helper()
	root := t.TempDir()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte("[hooks.client]\nlifecycle = [\"notify\", \"fixed\"]\n"), 0o644); err != nil {
		t.Fatalf("write client config: %v", err)
	}
	_, client, err := config.LoadInteractive(workspace, config.LoadOptions{ConfigRoot: root})
	if err != nil {
		t.Fatalf("load client settings: %v", err)
	}
	return client
}

func (s *fakeServer) Close() error {
	s.closed = true
	return nil
}

func TestRunInteractiveUsesInjectedStarterAndLifecycle(t *testing.T) {
	ctx := context.Background()
	auth := &struct{}{}
	server := &fakeServer{}
	serverConfig := config.App{AppName: "server-config"}
	clientConfig := loadRunnerClientSettings(t)
	factory := core.Options{}.RuntimeClientFactory
	resolveCalls := 0
	startCalls := 0
	lifecycleCalls := 0

	request := Request[serverstartup.Options]{
		SessionID:      "selected-session",
		AgentRole:      runnerStringPtr("reviewer"),
		StartupOptions: serverstartup.Options{Core: core.Options{RuntimeClientFactory: factory}},
	}
	err := RunInteractive(ctx, request, Dependencies[*fakeServer, *struct{}, serverstartup.Options]{
		ResolveInteractiveConfig: func(got Request[serverstartup.Options]) (InteractiveConfig, error) {
			resolveCalls++
			if got.SessionID != request.SessionID || got.StartupOptions.Core.RuntimeClientFactory != factory {
				t.Fatalf("resolver request = %+v, want original request", got)
			}
			return InteractiveConfig{Server: serverConfig, Client: clientConfig}, nil
		},
		NewAuthInteractor: func() *struct{} {
			return auth
		},
		StartSessionServer: func(ctx context.Context, req Request[serverstartup.Options], gotAuth *struct{}, interactive bool, gotConfig config.App) (*fakeServer, error) {
			startCalls++
			if gotConfig.AppName != serverConfig.AppName {
				t.Fatalf("starter config = %+v, want %+v", gotConfig, serverConfig)
			}
			if gotAuth != auth {
				t.Fatal("starter did not receive auth interactor")
			}
			if !interactive {
				t.Fatal("starter must run interactive session startup")
			}
			if req.StartupOptions.Core.RuntimeClientFactory != factory {
				t.Fatal("startup options were not carried to starter")
			}
			if req.SessionID != "selected-session" {
				t.Fatalf("starter session id = %q, want selected-session", req.SessionID)
			}
			return server, nil
		},
		RunSessionLifecycle: func(ctx context.Context, gotServer *fakeServer, gotAuth *struct{}, client config.ClientSettings, opts SessionLifecycleOptions) error {
			lifecycleCalls++
			if got, want := client.Hooks.LifecycleCommand(), []string{"notify", "fixed"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("lifecycle client command = %#v, want %#v", got, want)
			}
			if gotServer != server {
				t.Fatal("lifecycle did not receive started server")
			}
			if gotAuth != auth {
				t.Fatal("lifecycle did not receive auth interactor")
			}
			if opts.Intent == nil || opts.Intent.Kind() != serverapi.SessionLaunchIntentOpenExisting {
				t.Fatalf("initial intent = %+v, want open existing", opts.Intent)
			}
			if opts.Overrides.AgentRole == nil || *opts.Overrides.AgentRole != "reviewer" {
				t.Fatalf("agent role override = %v, want reviewer", opts.Overrides.AgentRole)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunInteractive: %v", err)
	}
	if resolveCalls != 1 || startCalls != 1 || lifecycleCalls != 1 {
		t.Fatalf("calls resolve=%d start=%d lifecycle=%d, want 1/1/1", resolveCalls, startCalls, lifecycleCalls)
	}
	if !server.closed {
		t.Fatal("started server was not closed")
	}
}

func TestRunInteractiveComputesForceNewForNonDefaultAgentRole(t *testing.T) {
	server := &fakeServer{}
	err := RunInteractive(context.Background(), Request[NoStartupOptions]{AgentRole: runnerStringPtr("reviewer")}, Dependencies[*fakeServer, struct{}, NoStartupOptions]{
		ResolveInteractiveConfig: resolveEmptyInteractiveConfig[NoStartupOptions],
		NewAuthInteractor:        func() struct{} { return struct{}{} },
		StartSessionServer: func(ctx context.Context, req Request[NoStartupOptions], auth struct{}, interactive bool, _ config.App) (*fakeServer, error) {
			return server, nil
		},
		RunSessionLifecycle: func(ctx context.Context, server *fakeServer, auth struct{}, _ config.ClientSettings, opts SessionLifecycleOptions) error {
			if opts.Intent == nil || opts.Intent.Kind() != serverapi.SessionLaunchIntentCreateNew {
				t.Fatalf("initial intent = %+v, want create new", opts.Intent)
			}
			if opts.Overrides.AgentRole == nil || *opts.Overrides.AgentRole != "reviewer" {
				t.Fatalf("agent role override = %v, want reviewer", opts.Overrides.AgentRole)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunInteractive: %v", err)
	}
}

func TestRunInteractiveRejectsMissingDependencies(t *testing.T) {
	err := RunInteractive(context.Background(), Request[NoStartupOptions]{}, Dependencies[*fakeServer, struct{}, NoStartupOptions]{})
	if err == nil {
		t.Fatal("expected missing dependency error")
	}
}

func TestRunInteractiveConfigurationFailureStartsNothing(t *testing.T) {
	expected := errors.New("config failed")
	authCalls := 0
	startCalls := 0
	lifecycleCalls := 0
	err := RunInteractive(context.Background(), Request[NoStartupOptions]{}, Dependencies[*fakeServer, struct{}, NoStartupOptions]{
		ResolveInteractiveConfig: func(Request[NoStartupOptions]) (InteractiveConfig, error) {
			return InteractiveConfig{}, expected
		},
		NewAuthInteractor: func() struct{} {
			authCalls++
			return struct{}{}
		},
		StartSessionServer: func(context.Context, Request[NoStartupOptions], struct{}, bool, config.App) (*fakeServer, error) {
			startCalls++
			return &fakeServer{}, nil
		},
		RunSessionLifecycle: func(context.Context, *fakeServer, struct{}, config.ClientSettings, SessionLifecycleOptions) error {
			lifecycleCalls++
			return nil
		},
	})
	if !errors.Is(err, expected) {
		t.Fatalf("RunInteractive error = %v, want %v", err, expected)
	}
	if authCalls != 0 || startCalls != 0 || lifecycleCalls != 0 {
		t.Fatalf("calls auth=%d start=%d lifecycle=%d, want zero", authCalls, startCalls, lifecycleCalls)
	}
}

func TestRunInteractiveStartFailureLeavesPartialCleanupToStarter(t *testing.T) {
	expected := errors.New("start failed")
	partial := &fakeServer{}
	lifecycleCalls := 0
	err := RunInteractive(context.Background(), Request[NoStartupOptions]{}, Dependencies[*fakeServer, struct{}, NoStartupOptions]{
		ResolveInteractiveConfig: resolveEmptyInteractiveConfig[NoStartupOptions],
		NewAuthInteractor:        func() struct{} { return struct{}{} },
		StartSessionServer: func(context.Context, Request[NoStartupOptions], struct{}, bool, config.App) (*fakeServer, error) {
			return partial, expected
		},
		RunSessionLifecycle: func(context.Context, *fakeServer, struct{}, config.ClientSettings, SessionLifecycleOptions) error {
			lifecycleCalls++
			return nil
		},
	})
	if !errors.Is(err, expected) {
		t.Fatalf("RunInteractive error = %v, want %v", err, expected)
	}
	if partial.closed {
		t.Fatal("runner closed a server returned with a start failure")
	}
	if lifecycleCalls != 0 {
		t.Fatalf("lifecycle calls = %d, want zero", lifecycleCalls)
	}
}

func TestRunInteractiveLifecycleOptionFailureClosesStartedServer(t *testing.T) {
	server := &fakeServer{}
	lifecycleCalls := 0
	err := RunInteractive(context.Background(), Request[NoStartupOptions]{
		SessionID:                 "selected",
		WorkspaceContextSessionID: "context",
	}, Dependencies[*fakeServer, struct{}, NoStartupOptions]{
		ResolveInteractiveConfig: resolveEmptyInteractiveConfig[NoStartupOptions],
		NewAuthInteractor:        func() struct{} { return struct{}{} },
		StartSessionServer: func(context.Context, Request[NoStartupOptions], struct{}, bool, config.App) (*fakeServer, error) {
			return server, nil
		},
		RunSessionLifecycle: func(context.Context, *fakeServer, struct{}, config.ClientSettings, SessionLifecycleOptions) error {
			lifecycleCalls++
			return nil
		},
	})
	if err == nil {
		t.Fatal("RunInteractive accepted conflicting session identities")
	}
	if !server.closed {
		t.Fatal("server was not closed after lifecycle option failure")
	}
	if lifecycleCalls != 0 {
		t.Fatalf("lifecycle calls = %d, want zero", lifecycleCalls)
	}
}

func TestRunInteractiveClosesServerAfterLifecycleError(t *testing.T) {
	expected := errors.New("stop")
	server := &fakeServer{}
	err := RunInteractive(context.Background(), Request[NoStartupOptions]{}, Dependencies[*fakeServer, struct{}, NoStartupOptions]{
		ResolveInteractiveConfig: resolveEmptyInteractiveConfig[NoStartupOptions],
		NewAuthInteractor:        func() struct{} { return struct{}{} },
		StartSessionServer: func(ctx context.Context, req Request[NoStartupOptions], auth struct{}, interactive bool, _ config.App) (*fakeServer, error) {
			return server, nil
		},
		RunSessionLifecycle: func(context.Context, *fakeServer, struct{}, config.ClientSettings, SessionLifecycleOptions) error {
			return expected
		},
	})
	if !errors.Is(err, expected) {
		t.Fatalf("RunInteractive error = %v, want %v", err, expected)
	}
	if !server.closed {
		t.Fatal("server was not closed after lifecycle error")
	}
}

func TestRequestDefaultsDoNotForceDefaultSubagentRole(t *testing.T) {
	opts, err := SessionLifecycleOptionsFor(Request[NoStartupOptions]{AgentRole: runnerStringPtr(config.DefaultSubagentRole)})
	if err != nil {
		t.Fatalf("SessionLifecycleOptionsFor: %v", err)
	}
	if opts.Intent != nil {
		t.Fatal("default subagent role must not force a new session")
	}
	if opts.Overrides.AgentRole == nil || *opts.Overrides.AgentRole != config.DefaultSubagentRole {
		t.Fatalf("unexpected overrides: %+v", opts.Overrides)
	}
}

func TestSessionLifecycleOptionsRejectsBlankAgentRole(t *testing.T) {
	if _, err := SessionLifecycleOptionsFor(Request[NoStartupOptions]{AgentRole: runnerStringPtr(" \t ")}); err == nil {
		t.Fatal("SessionLifecycleOptionsFor accepted a blank agent role")
	}
}
