package sessionlaunch

import (
	"context"
	"testing"

	"core/server/launch"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestServiceMapsEveryTypedLaunchIntentAsNewOperation(t *testing.T) {
	containerDir := t.TempDir()
	persistenceRoot := t.TempDir()
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

	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: persistenceRoot,
		StoreOptions:    persistence.Options(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	service := NewService(launch.Planner{
		Config: config.App{
			WorkspaceRoot:   "/tmp/workspace-a",
			PersistenceRoot: persistenceRoot,
			Settings:        config.Settings{Model: "gpt-5"},
		},
		ContainerDir:             containerDir,
		StoreOptions:             persistence.Options(),
		PersistedSessions:        persistence,
		ProjectWorkspaceBoundary: sessionLaunchBoundaryResolver{root: "/tmp/workspace-a"},
	}).WithRuntimeAuthority(authority)

	createRequest := serverapi.SessionPlanRequest{
		ClientRequestID: "same-request-id",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	}
	created, err := service.PlanSession(context.Background(), createRequest)
	if err != nil {
		t.Fatalf("plan create-new session: %v", err)
	}

	repeated, err := service.PlanSession(context.Background(), createRequest)
	if err != nil {
		t.Fatalf("repeat create-new session: %v", err)
	}
	if repeated.Plan.SessionID == created.Plan.SessionID {
		t.Fatalf("repeated create reused session %q", created.Plan.SessionID)
	}

	openRequest := serverapi.SessionPlanRequest{
		ClientRequestID: "same-request-id",
		Mode:            serverapi.SessionLaunchModeInteractive,
		Intent:          serverapi.OpenExistingSessionLaunchIntent(targetID),
	}
	if _, err := service.PlanSession(context.Background(), openRequest); err != nil {
		t.Fatalf("open existing after create with same request ID: %v", err)
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
