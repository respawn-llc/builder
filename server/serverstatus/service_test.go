package serverstatus

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/auth"
	"core/shared/apicontract"
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
	service := NewServerStatusService(manager, config.App{Settings: config.Settings{ProviderOverride: "openai"}}, nil)

	if _, err := service.GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{}); err == nil {
		t.Fatal("expected auth store error to surface when startup auth is required")
	}
}

func TestServerStatusSeparatesReadinessFromLazyUpdateStatus(t *testing.T) {
	source := &countingReleaseSource{metadata: releaseMetadata{Version: updateVersion{components: [3]uint64{1, 2, 0}}}}
	updates := newUpdateStatusService("1.1.0", false, source, time.Now)
	t.Cleanup(func() {
		if err := updates.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	service := NewServerStatusService(nil, config.App{Settings: config.Settings{ProviderOverride: "anthropic"}}, updates)

	if _, err := service.GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{}); err != nil {
		t.Fatalf("GetServerReadiness: %v", err)
	}
	if calls := source.calls.Load(); calls != 0 {
		t.Fatalf("release checks after readiness = %d, want 0", calls)
	}

	response, err := service.GetUpdateStatus(context.Background(), serverapi.UpdateStatusRequest{})
	if err != nil {
		t.Fatalf("GetUpdateStatus: %v", err)
	}
	if response.Result.Kind() != serverapi.UpdateStatusAvailable {
		t.Fatalf("update result kind = %q, want available", response.Result.Kind())
	}
	if calls := source.calls.Load(); calls != 1 {
		t.Fatalf("release checks after update request = %d, want 1", calls)
	}
}

func TestServerStatusValidatedMethodsPreserveStatusBehavior(t *testing.T) {
	source := &countingReleaseSource{metadata: releaseMetadata{Version: updateVersion{components: [3]uint64{1, 2, 0}}}}
	updates := newUpdateStatusService("1.1.0", false, source, time.Now)
	t.Cleanup(func() {
		if err := updates.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	service := NewServerStatusService(nil, config.App{Settings: config.Settings{ProviderOverride: "anthropic"}}, updates)

	readiness, err := apicontract.WithValidated(
		serverapi.ServerReadinessRequest{},
		apicontract.NoSemanticValidation,
		func(request apicontract.Validated[serverapi.ServerReadinessRequest]) (serverapi.ServerReadinessResponse, error) {
			return service.GetServerReadinessValidated(context.Background(), request)
		},
	)
	if err != nil {
		t.Fatalf("GetServerReadinessValidated: %v", err)
	}
	if !readiness.Ready {
		t.Fatalf("readiness = %+v, want ready", readiness)
	}

	update, err := apicontract.WithValidated(
		serverapi.UpdateStatusRequest{},
		apicontract.SemanticValidationRequired,
		func(request apicontract.Validated[serverapi.UpdateStatusRequest]) (serverapi.UpdateStatusResponse, error) {
			return service.GetUpdateStatusValidated(context.Background(), request)
		},
	)
	if err != nil {
		t.Fatalf("GetUpdateStatusValidated: %v", err)
	}
	if update.Result.Kind() != serverapi.UpdateStatusAvailable {
		t.Fatalf("update kind = %q, want available", update.Result.Kind())
	}
}

func requireServerReadiness(t *testing.T, manager *auth.Manager, app config.App) serverapi.ServerReadinessResponse {
	t.Helper()
	readiness, err := NewServerStatusService(manager, app, nil).GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{})
	if err != nil {
		t.Fatalf("GetServerReadiness: %v", err)
	}
	return readiness
}
