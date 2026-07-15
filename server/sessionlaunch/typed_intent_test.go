package sessionlaunch

import (
	"context"
	"testing"

	"core/server/launch"
	"core/server/registry"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestServiceMapsTypedLaunchIntentsAndMemoizesByTypedIntent(t *testing.T) {
	containerDir := t.TempDir()
	persistence := sessiontest.NewPersistence()
	target, err := session.Create(
		containerDir,
		"workspace-a",
		"/tmp/workspace-a",
		sessioncontract.SessionCategoryMain,
		persistence.Options()...,
	)
	if err != nil {
		t.Fatalf("create target session: %v", err)
	}
	targetID, err := runtimeids.ParseSessionID(target.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse target session ID: %v", err)
	}

	service := NewService(launch.Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: t.TempDir(),
			Settings:        config.Settings{Model: "gpt-5"},
		},
		ContainerDir: containerDir,
		StoreOptions: persistence.Options(),
	}, registry.NewSessionStoreRegistry())

	createRequest := serverapi.SessionPlanRequest{
		ClientRequestID: "same-request-id",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.CreateNewSessionLaunchIntent(nil),
	}
	created, err := service.PlanSession(context.Background(), createRequest)
	if err != nil {
		t.Fatalf("plan create-new session: %v", err)
	}

	replayed, err := service.PlanSession(context.Background(), createRequest)
	if err != nil {
		t.Fatalf("replay create-new session: %v", err)
	}
	if replayed.Plan.SessionID != created.Plan.SessionID {
		t.Fatalf("replayed session ID = %q, want %q", replayed.Plan.SessionID, created.Plan.SessionID)
	}

	openRequest := serverapi.SessionPlanRequest{
		ClientRequestID: "same-request-id",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(targetID),
	}
	if _, err := service.PlanSession(context.Background(), openRequest); err == nil {
		t.Fatal("different typed intent reused the same client request ID")
	}
}

func mustSessionLaunchIntentID(t *testing.T, raw string) runtimeids.SessionID {
	t.Helper()
	id, err := runtimeids.ParseSessionID(raw)
	if err != nil {
		t.Fatalf("ParseSessionID(%q): %v", raw, err)
	}
	return id
}

func createNewSessionLaunchIntentWithParent(t *testing.T, raw string) serverapi.SessionLaunchIntent {
	t.Helper()
	parentID := mustSessionLaunchIntentID(t, raw)
	return serverapi.CreateNewSessionLaunchIntent(&parentID)
}
