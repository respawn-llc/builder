package runner

import (
	"context"
	"errors"
	"io"
	"testing"

	"core/shared/config"
	"core/shared/serverapi"
)

type fakeServer struct {
	closed bool
}

func runnerStringPtr(value string) *string { return &value }

func (s *fakeServer) Close() error {
	s.closed = true
	return nil
}

func TestRunInteractiveUsesInjectedStarterAndLifecycle(t *testing.T) {
	ctx := context.Background()
	server := &fakeServer{}
	startCalls := 0
	lifecycleCalls := 0

	err := RunInteractive(ctx, Request{
		SessionID: "selected-session",
		AgentRole: runnerStringPtr("reviewer"),
	}, Dependencies{
		StartSessionServer: func(context.Context) (io.Closer, error) {
			startCalls++
			return server, nil
		},
		RunSessionLifecycle: func(_ context.Context, gotServer io.Closer, intent *serverapi.SessionLaunchIntent, overrides serverapi.RunPromptOverrides) error {
			lifecycleCalls++
			if gotServer != server {
				t.Fatal("lifecycle did not receive started server")
			}
			if intent == nil || intent.Kind() != serverapi.SessionLaunchIntentOpenExisting {
				t.Fatalf("initial intent = %+v, want open existing", intent)
			}
			if overrides.AgentRole == nil || *overrides.AgentRole != "reviewer" {
				t.Fatalf("agent role override = %v, want reviewer", overrides.AgentRole)
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
	err := RunInteractive(context.Background(), Request{AgentRole: runnerStringPtr("reviewer")}, Dependencies{
		StartSessionServer: func(context.Context) (io.Closer, error) {
			return server, nil
		},
		RunSessionLifecycle: func(_ context.Context, _ io.Closer, intent *serverapi.SessionLaunchIntent, overrides serverapi.RunPromptOverrides) error {
			if intent == nil || intent.Kind() != serverapi.SessionLaunchIntentCreateNew {
				t.Fatalf("initial intent = %+v, want create new", intent)
			}
			if overrides.AgentRole == nil || *overrides.AgentRole != "reviewer" {
				t.Fatalf("agent role override = %v, want reviewer", overrides.AgentRole)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RunInteractive: %v", err)
	}
}

func TestRunInteractiveRejectsMissingDependencies(t *testing.T) {
	err := RunInteractive(context.Background(), Request{}, Dependencies{})
	if err == nil {
		t.Fatal("expected missing dependency error")
	}
}

func TestRunInteractiveClosesServerAfterLifecycleError(t *testing.T) {
	expected := errors.New("stop")
	server := &fakeServer{}
	err := RunInteractive(context.Background(), Request{}, Dependencies{
		StartSessionServer: func(context.Context) (io.Closer, error) {
			return server, nil
		},
		RunSessionLifecycle: func(context.Context, io.Closer, *serverapi.SessionLaunchIntent, serverapi.RunPromptOverrides) error {
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
	intent, overrides, err := SessionLifecycleOptionsFor(Request{AgentRole: runnerStringPtr(config.DefaultSubagentRole)})
	if err != nil {
		t.Fatalf("SessionLifecycleOptionsFor: %v", err)
	}
	if intent != nil {
		t.Fatal("default subagent role must not force a new session")
	}
	if overrides.AgentRole == nil || *overrides.AgentRole != config.DefaultSubagentRole {
		t.Fatalf("unexpected overrides: %+v", overrides)
	}
}

func TestSessionLifecycleOptionsRejectsBlankAgentRole(t *testing.T) {
	if _, _, err := SessionLifecycleOptionsFor(Request{AgentRole: runnerStringPtr(" \t ")}); err == nil {
		t.Fatal("SessionLifecycleOptionsFor accepted a blank agent role")
	}
}
