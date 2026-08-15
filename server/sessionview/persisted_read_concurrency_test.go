package sessionview

import (
	"errors"
	"testing"
	"time"

	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

func TestPersistedSessionReadsDoNotWaitForCommittedAppendOwner(t *testing.T) {
	persistence := sessiontest.NewPersistence()
	gate := sessiontest.NewPersistenceGate(persistence)
	workspaceRoot := t.TempDir()
	store := sessiontest.Must(session.Create(
		t.TempDir(),
		"workspace",
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		session.WithPersistenceObserver(gate),
		session.WithPersistedSessionResolver(persistence),
		session.WithSessionContextFactWriter(persistence),
	))
	if _, _, err := store.SetGoal("persisted goal", session.GoalActorUser); err != nil {
		t.Fatalf("set Goal: %v", err)
	}
	eventLog := sessiontest.Must(store.MaterializeEventLog())
	blocked, release := gate.BlockNextAfter()
	appendDone := make(chan error, 1)
	go func() {
		stepID := "11111111-1111-4111-8111-111111111111"
		phase := session.MessagePhaseFinal
		content := "committed final answer"
		_, _, appendErr := eventLog.AppendRecord(&stepID, session.MessageRecord{
			Role:    session.MessageRoleAssistant,
			Content: &content,
			Phase:   &phase,
		})
		appendDone <- appendErr
	}()
	t.Cleanup(release)
	<-blocked

	service := NewService(persistence, nil, staticExecutionTargetResolver{target: availableSessionExecutionTarget(workspaceRoot)})
	result := make(chan error, 1)
	go func() {
		response, err := service.GetSessionMainView(t.Context(), serverapi.SessionMainViewRequest{SessionID: store.Meta().SessionID})
		if err == nil && (response.MainView.Status.Goal == nil ||
			response.MainView.Status.LastCommittedAssistantFinalAnswer == nil ||
			*response.MainView.Status.LastCommittedAssistantFinalAnswer != "committed final answer") {
			err = errors.New("dormant Main View did not use one persisted Session view")
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("persisted read while append owner was blocked: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("persisted Session read waited for the append owner")
	}
	release()
	sessiontest.MustNoError(<-appendDone)
}
