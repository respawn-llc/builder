package serverstatus

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/auth"
	"core/shared/config"

	serverpb "core/shared/protoapi/gen/kent/api/server"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestGetServerReadinessIncludesWorkflowAssigneeRoles(t *testing.T) {
	readiness := requireServerReadiness(t, nil, config.App{
		Settings: config.Settings{
			Model: "base",
			Workflow: config.WorkflowSettings{
				Subagents: false,
			},
			Subagents: map[string]config.SubagentRole{
				"coder": {
					Settings: config.Settings{Model: "coder-model"},
					Sources:  map[string]string{"model": "test"},
				},
				"blocked": {
					AgentCallable:    false,
					AgentCallableSet: true,
					Settings:         config.Settings{Model: "blocked-model"},
					Sources:          map[string]string{"model": "test"},
				},
				"workflow_hidden": {
					Settings:            config.Settings{Model: "workflow-hidden-model"},
					Sources:             map[string]string{"model": "test"},
					WorkflowSubagent:    false,
					WorkflowSubagentSet: true,
				},
			},
		},
	})

	got := make([]string, 0, len(readiness.SubagentRoles))
	for _, role := range readiness.SubagentRoles {
		got = append(got, role.Name)
	}
	want := []string{"default", "fast", "blocked", "coder", "workflow_hidden"}
	if len(got) != len(want) {
		t.Fatalf("roles = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roles = %+v, want %+v", got, want)
		}
	}
}

func TestGetServerReadinessReadyWhenStartupAuthNotRequired(t *testing.T) {
	readiness := requireServerReadiness(t, nil, config.App{
		Settings: config.Settings{ProviderOverride: "anthropic"},
	})

	if readiness.AuthRequired {
		t.Fatalf("AuthRequired = true, want false for non-OpenAI provider")
	}
	if !readiness.Ready {
		t.Fatalf("Ready = false, want true when startup auth is not required")
	}
	if len(readiness.Causes) != 0 {
		t.Fatalf("Causes = %+v, want none when ready", readiness.Causes)
	}
}

func TestGetServerReadinessBlockedWhenStartupAuthRequiredButMissing(t *testing.T) {
	readiness := requireServerReadiness(t, nil, config.App{
		Settings: config.Settings{ProviderOverride: "openai"},
	})

	if !readiness.AuthRequired {
		t.Fatalf("AuthRequired = false, want true for OpenAI provider")
	}
	if readiness.Ready {
		t.Fatalf("Ready = true, want false when required auth is missing")
	}
	if len(readiness.Causes) == 0 {
		t.Fatalf("Causes empty, want a startup blocker cause")
	}
}

type failingAuthStore struct{}

func (failingAuthStore) Load(context.Context) (auth.State, error) {
	return auth.State{}, errors.New("auth store unavailable")
}

func (failingAuthStore) Save(context.Context, auth.State) error { return nil }

func TestGetServerReadinessLoadsAuthStateWhenStartupAuthNotRequired(t *testing.T) {
	manager := auth.NewManager(failingAuthStore{}, nil, nil)
	service := NewServerStatusService(manager, config.App{Settings: config.Settings{ProviderOverride: "anthropic"}}, nil)

	if _, err := service.GetReadiness(context.Background(), &emptypb.Empty{}); err == nil {
		t.Fatal("expected request-owned auth store error")
	}
}

func TestServerStatusSeparatesReadinessFromLazyUpdateStatus(t *testing.T) {
	source := &countingReleaseSource{metadata: releaseMetadata{Version: "1.2.0"}}
	updates := newUpdateStatusService("1.1.0", false, source, time.Now)
	t.Cleanup(func() {
		if err := updates.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	service := NewServerStatusService(nil, config.App{Settings: config.Settings{ProviderOverride: "anthropic"}}, updates)

	if _, err := service.GetReadiness(context.Background(), &emptypb.Empty{}); err != nil {
		t.Fatalf("GetReadiness: %v", err)
	}
	if calls := source.calls.Load(); calls != 0 {
		t.Fatalf("release checks after readiness = %d, want 0", calls)
	}

	response, err := service.GetUpdateStatus(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("GetUpdateStatus: %v", err)
	}
	if response.Status.GetAvailable() == nil {
		t.Fatalf("update status = %T, want available", response.Status.GetStatus())
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("release checks after update request = %d, want 1", calls)
	}
}

func requireServerReadiness(t *testing.T, manager *auth.Manager, app config.App) *serverpb.Readiness {
	t.Helper()
	response, err := NewServerStatusService(manager, app, nil).GetReadiness(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("GetReadiness: %v", err)
	}
	return response.Readiness
}
