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

	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5"
	settings.ProviderOverride = "openai"
	reader := chatSettingsPersistedReader{Persistence: persistence}
	service := NewService(launch.Planner{
		Config:            config.App{WorkspaceRoot: workspace, Settings: settings},
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
