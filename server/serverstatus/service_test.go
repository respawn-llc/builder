package serverstatus

import (
	"context"
	"errors"
	"testing"

	"core/server/auth"
	"core/shared/config"
	"core/shared/serverapi"
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

func TestGetServerReadinessIgnoresAuthStoreWhenStartupAuthNotRequired(t *testing.T) {
	manager := auth.NewManager(failingAuthStore{}, nil, nil)
	readiness := requireServerReadiness(t, manager, config.App{Settings: config.Settings{ProviderOverride: "anthropic"}})
	if readiness.AuthRequired {
		t.Fatalf("AuthRequired = true, want false for non-OpenAI provider")
	}
	if !readiness.Ready {
		t.Fatalf("Ready = false, want true when startup auth is not required")
	}
}

func TestGetServerReadinessSurfacesAuthStoreErrorWhenStartupAuthRequired(t *testing.T) {
	manager := auth.NewManager(failingAuthStore{}, nil, nil)
	service := NewServerStatusService(manager, config.App{Settings: config.Settings{ProviderOverride: "openai"}})

	if _, err := service.GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{}); err == nil {
		t.Fatal("expected auth store error to surface when startup auth is required")
	}
}

func requireServerReadiness(t *testing.T, manager *auth.Manager, app config.App) serverapi.ServerReadinessResponse {
	t.Helper()
	readiness, err := NewServerStatusService(manager, app).GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if err != nil {
		t.Fatalf("GetServerReadiness: %v", err)
	}
	return readiness
}
