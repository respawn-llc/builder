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

func TestServiceMapsTypedLaunchIntents(t *testing.T) {
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

	createRequest := PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.CreateNewSessionLaunchIntent(serverapi.IndependentSessionCreateOrigin()),
	}
	created, err := service.PlanLaunchSession(context.Background(), createRequest)
	if err != nil {
		t.Fatalf("plan create-new session: %v", err)
	}

	secondCreated, err := service.PlanLaunchSession(context.Background(), createRequest)
	if err != nil {
		t.Fatalf("plan second create-new session: %v", err)
	}
	if secondCreated.Plan.Descriptor.SessionID() == created.Plan.Descriptor.SessionID() {
		t.Fatalf("second create-new session reused session ID %q", created.Plan.Descriptor.SessionID())
	}

	openRequest := PlanRequest{
		Mode:   launch.ModeInteractive,
		Intent: serverapi.OpenExistingSessionLaunchIntent(targetID),
	}
	opened, err := service.PlanLaunchSession(context.Background(), openRequest)
	if err != nil {
		t.Fatalf("plan open-existing session: %v", err)
	}
	if opened.Plan.Descriptor.SessionID() != targetID {
		t.Fatalf("open-existing session ID = %q, want %q", opened.Plan.Descriptor.SessionID(), targetID)
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
