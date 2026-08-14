package sessionlaunch

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/launch"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

type chatSettingsPersistedReader struct {
	*sessiontest.Persistence
}

func (chatSettingsPersistedReader) WorkflowTaskIDForSession(context.Context, string) (*string, error) {
	return nil, nil
}

func TestMaterializedChatSettingsDoesNotWaitForSessionMetadataMutation(t *testing.T) {
	persistence := sessiontest.NewPersistence()
	gate := sessiontest.NewPersistenceGate(persistence)
	options := []session.StoreOption{
		session.WithPersistenceObserver(gate),
		session.WithPersistedSessionResolver(persistence),
		session.WithSessionContextFactWriter(persistence),
	}
	workspace := t.TempDir()
	store, err := session.Create(
		t.TempDir(),
		"workspace",
		workspace,
		sessioncontract.SessionCategoryMain,
		options...,
	)
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	blocked, release := gate.BlockNextAfter()
	t.Cleanup(release)
	mutationDone := make(chan error, 1)
	go func() { mutationDone <- store.SetName("committed name") }()
	<-blocked

	cfg, err := config.Load(workspace, config.LoadOptions{ConfigRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Settings.ProviderCapabilities.ProviderID = "anthropic"
	reader := chatSettingsPersistedReader{Persistence: persistence}
	service := NewService(launch.Planner{
		Config:            cfg,
		PersistedSessions: reader,
	})
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse Session ID: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		response, readErr := service.MaterializedChatSettings(t.Context(), sessionID)
		if readErr == nil && (response.Session == nil || response.Session.SessionID != sessionID) {
			readErr = errors.New("Chat settings did not return the persisted Session facts")
		}
		result <- readErr
	}()
	select {
	case readErr := <-result:
		if readErr != nil {
			t.Fatalf("MaterializedChatSettings during metadata mutation: %v", readErr)
		}
	case <-time.After(time.Second):
		t.Fatal("MaterializedChatSettings waited for the Session mutation owner")
	}
	release()
	if err := <-mutationDone; err != nil {
		t.Fatalf("complete metadata mutation: %v", err)
	}
}

func TestHeadlessExistingSessionPlanDoesNotWaitForSessionMetadataMutation(t *testing.T) {
	persistence := sessiontest.NewPersistence()
	gate := sessiontest.NewPersistenceGate(persistence)
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := t.TempDir()
	store, err := session.Create(
		containerDir,
		"workspace",
		workspace,
		sessioncontract.SessionCategoryMain,
		session.WithPersistenceObserver(gate),
		session.WithPersistedSessionResolver(persistence),
	)
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	blocked, release := gate.BlockNextAfter()
	t.Cleanup(release)
	mutationDone := make(chan error, 1)
	go func() { mutationDone <- store.SetName("committed name") }()
	<-blocked

	cfg, err := config.Load(workspace, config.LoadOptions{ConfigRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.PersistenceRoot = root
	cfg.Settings.ProviderCapabilities.ProviderID = "anthropic"
	service := NewService(launch.Planner{
		Config:                   cfg,
		ContainerDir:             containerDir,
		PersistedSessions:        persistence,
		ProjectWorkspaceBoundary: sessionLaunchBoundaryResolver{root: workspace},
	})
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse Session ID: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		response, planErr := service.PlanSession(t.Context(), serverapi.SessionPlanRequest{
			ClientRequestID: "persisted-headless-plan",
			Mode:            serverapi.SessionLaunchModeHeadless,
			Intent:          serverapi.OpenExistingSessionLaunchIntent(sessionID),
		})
		if planErr == nil && (response.Plan.SessionName == nil || *response.Plan.SessionName != "committed name") {
			planErr = errors.New("headless plan did not use committed persisted Session facts")
		}
		result <- planErr
	}()
	select {
	case planErr := <-result:
		if planErr != nil {
			t.Fatalf("PlanSession during metadata mutation: %v", planErr)
		}
	case <-time.After(time.Second):
		t.Fatal("headless existing-Session planning waited for the Session mutation owner")
	}
	release()
	if err := <-mutationDone; err != nil {
		t.Fatalf("complete metadata mutation: %v", err)
	}
}
