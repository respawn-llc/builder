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
	service := NewServerStatusService(manager, config.App{Settings: config.Settings{ProviderOverride: "openai"}}, nil)

	if _, err := service.GetServerReadiness(context.Background(), serverapi.ServerReadinessRequest{}); err == nil {
		t.Fatal("expected auth store error to surface when startup auth is required")
	}
}

func TestServerStatusSeparatesReadinessFromLazyUpdateStatus(t *testing.T) {
	source := &countingReleaseSource{metadata: ReleaseMetadata{Version: "1.2.0"}}
	updates := NewUpdateStatusService("1.1.0", Dependencies{ReleaseSource: source})
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

func TestServerStatusDelegatesEveryUpdateResultKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want serverapi.UpdateStatusResultKind
	}{
		{name: "current", want: serverapi.UpdateStatusCurrent},
		{name: "available", want: serverapi.UpdateStatusAvailable},
		{name: "check unavailable", err: &ReleaseTransportError{Cause: errors.New("offline")}, want: serverapi.UpdateStatusCheckUnavailable},
		{name: "check failed", err: &ReleaseHTTPStatusError{StatusCode: 403, Status: "403 Forbidden"}, want: serverapi.UpdateStatusCheckFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			version := "1.1.0"
			if test.want == serverapi.UpdateStatusCurrent {
				version = "1.2.0"
			}
			updates := NewUpdateStatusService(version, Dependencies{
				ReleaseSource: &countingReleaseSource{
					metadata: ReleaseMetadata{Version: "1.2.0"},
					err:      test.err,
				},
			})
			t.Cleanup(func() {
				if err := updates.Close(); err != nil {
					t.Fatalf("Close: %v", err)
				}
			})
			response, err := NewServerStatusService(nil, config.App{}, updates).
				GetUpdateStatus(context.Background(), serverapi.UpdateStatusRequest{})
			if err != nil {
				t.Fatalf("GetUpdateStatus: %v", err)
			}
			if response.Result.Kind() != test.want {
				t.Fatalf("result kind = %q, want %q", response.Result.Kind(), test.want)
			}
		})
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
