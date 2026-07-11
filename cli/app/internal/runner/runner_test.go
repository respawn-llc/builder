package runner

import (
	"context"
	"errors"
	"testing"

	"core/server/core"
	serverstartup "core/server/startup"
	"core/shared/config"
	"core/shared/serverapi"
)

type fakeServer struct {
	closed bool
}

func (s *fakeServer) Close() error {
	s.closed = true
	return nil
}

func TestRunInteractiveUsesInjectedStarterAndLifecycle(t *testing.T) {
	ctx := context.Background()
	auth := &struct{}{}
	server := &fakeServer{}
	factory := core.Options{}.RuntimeClientFactory
	startCalls := 0
	lifecycleCalls := 0

	err := RunInteractive(ctx, Request[serverstartup.Options]{
		SessionID:      "selected-session",
		AgentRole:      "reviewer",
		StartupOptions: serverstartup.Options{Core: core.Options{RuntimeClientFactory: factory}},
	}, Dependencies[*fakeServer, *struct{}, serverstartup.Options]{
		NewAuthInteractor: func() *struct{} {
			return auth
		},
		StartSessionServer: func(ctx context.Context, req Request[serverstartup.Options], gotAuth *struct{}, interactive bool) (*fakeServer, error) {
			startCalls++
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
		RunSessionLifecycle: func(ctx context.Context, gotServer *fakeServer, gotAuth *struct{}, initialSessionID string, opts SessionLifecycleOptions) error {
			lifecycleCalls++
			if gotServer != server {
				t.Fatal("lifecycle did not receive started server")
			}
			if gotAuth != auth {
				t.Fatal("lifecycle did not receive auth interactor")
			}
			if initialSessionID != "selected-session" {
				t.Fatalf("initial session id = %q, want selected-session", initialSessionID)
			}
			if opts.ForceNewSession {
				t.Fatal("selected session must not force new session")
			}
			if opts.Overrides.AgentRole != "reviewer" {
				t.Fatalf("agent role override = %q, want reviewer", opts.Overrides.AgentRole)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunInteractive: %v", err)
	}
	if startCalls != 1 || lifecycleCalls != 1 {
		t.Fatalf("calls start=%d lifecycle=%d, want 1/1", startCalls, lifecycleCalls)
	}
	if !server.closed {
		t.Fatal("started server was not closed")
	}
}

func TestRunInteractiveComputesForceNewForNonDefaultAgentRole(t *testing.T) {
	server := &fakeServer{}
	err := RunInteractive(context.Background(), Request[NoStartupOptions]{AgentRole: "reviewer"}, Dependencies[*fakeServer, struct{}, NoStartupOptions]{
		NewAuthInteractor: func() struct{} { return struct{}{} },
		StartSessionServer: func(ctx context.Context, req Request[NoStartupOptions], auth struct{}, interactive bool) (*fakeServer, error) {
			return server, nil
		},
		RunSessionLifecycle: func(ctx context.Context, server *fakeServer, auth struct{}, initialSessionID string, opts SessionLifecycleOptions) error {
			if !opts.ForceNewSession {
				t.Fatal("non-default role without selected session should force a new session")
			}
			if opts.Overrides.AgentRole != "reviewer" {
				t.Fatalf("agent role override = %q, want reviewer", opts.Overrides.AgentRole)
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

func TestRunInteractiveClosesServerAfterLifecycleError(t *testing.T) {
	expected := errors.New("stop")
	server := &fakeServer{}
	err := RunInteractive(context.Background(), Request[NoStartupOptions]{}, Dependencies[*fakeServer, struct{}, NoStartupOptions]{
		NewAuthInteractor: func() struct{} { return struct{}{} },
		StartSessionServer: func(ctx context.Context, req Request[NoStartupOptions], auth struct{}, interactive bool) (*fakeServer, error) {
			return server, nil
		},
		RunSessionLifecycle: func(context.Context, *fakeServer, struct{}, string, SessionLifecycleOptions) error {
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
	opts := SessionLifecycleOptionsFor(Request[NoStartupOptions]{AgentRole: config.DefaultSubagentRole})
	if opts.ForceNewSession {
		t.Fatal("default subagent role must not force a new session")
	}
	if opts.Overrides != (serverapi.RunPromptOverrides{AgentRole: config.DefaultSubagentRole}) {
		t.Fatalf("unexpected overrides: %+v", opts.Overrides)
	}
}
