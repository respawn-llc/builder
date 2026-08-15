package sessionlaunch

import (
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

func TestHeadlessExistingSessionPlanDoesNotWaitForSessionMetadataMutation(t *testing.T) {
	persistence := sessiontest.NewPersistence()
	gate := sessiontest.NewPersistenceGate(persistence)
	workspace := t.TempDir()
	containerDir := t.TempDir()
	store := sessiontest.Must(session.Create(
		containerDir,
		"workspace",
		workspace,
		sessioncontract.SessionCategoryMain,
		session.WithPersistenceObserver(gate),
		session.WithPersistedSessionResolver(persistence),
	))
	blocked, release := gate.BlockNextAfter()
	t.Cleanup(release)
	mutationDone := make(chan error, 1)
	go func() { mutationDone <- store.SetName("committed name") }()
	<-blocked

	cfg := config.App{PersistenceRoot: t.TempDir(), WorkspaceRoot: workspace}
	cfg.Settings.ProviderCapabilities.ProviderID = "anthropic"
	service := NewService(launch.Planner{
		Config:                   cfg,
		ContainerDir:             containerDir,
		PersistedSessions:        persistence,
		ProjectWorkspaceBoundary: sessionLaunchBoundaryResolver{root: workspace},
	})
	sessionID := sessiontest.Must(runtimeids.ParseSessionID(store.Meta().SessionID))
	result := make(chan bool, 1)
	go func() {
		response, planErr := service.PlanSession(t.Context(), serverapi.SessionPlanRequest{
			ClientRequestID: "persisted-headless-plan",
			Mode:            serverapi.SessionLaunchModeHeadless,
			Intent:          serverapi.OpenExistingSessionLaunchIntent(sessionID),
		})
		result <- planErr == nil && response.Plan.SessionName != nil && *response.Plan.SessionName == "committed name"
	}()
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("headless plan did not use committed persisted Session facts")
		}
	case <-time.After(time.Second):
		t.Fatal("headless existing-Session planning waited for the Session mutation owner")
	}
	release()
	sessiontest.MustNoError(<-mutationDone)
}
