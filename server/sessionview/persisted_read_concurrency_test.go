package sessionview

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
)

type blockedCommittedSessionResolver struct {
	store       *session.Store
	persistence *sessiontest.Persistence
}

func (r blockedCommittedSessionResolver) ResolveSessionStore(context.Context, string) (*session.Store, error) {
	return r.store, nil
}

func (r blockedCommittedSessionResolver) ResolvePersistedSession(
	ctx context.Context,
	sessionID string,
) (session.PersistedSessionRecord, error) {
	return r.persistence.ResolvePersistedSession(ctx, sessionID)
}

func TestPersistedSessionReadsDoNotWaitForCommittedAppendOwner(t *testing.T) {
	persistence := sessiontest.NewPersistence()
	gate := sessiontest.NewPersistenceGate(persistence)
	workspaceRoot := t.TempDir()
	store, err := session.Create(
		t.TempDir(),
		"workspace",
		workspaceRoot,
		sessioncontract.SessionCategoryMain,
		session.WithPersistenceObserver(gate),
		session.WithPersistedSessionResolver(persistence),
		session.WithSessionContextFactWriter(persistence),
	)
	if err != nil {
		t.Fatalf("create Session: %v", err)
	}
	if _, _, err := store.SetGoal("persisted goal", session.GoalActorUser); err != nil {
		t.Fatalf("set Goal: %v", err)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	blocked, release := gate.BlockNextAfter()
	appendDone := make(chan error, 1)
	go func() {
		phase := session.MessagePhaseFinal
		content := "committed final answer"
		_, _, appendErr := eventLog.AppendRecord(nil, session.MessageRecord{
			Role:    session.MessageRoleAssistant,
			Content: &content,
			Phase:   &phase,
		})
		appendDone <- appendErr
	}()
	t.Cleanup(release)
	<-blocked

	settings := config.DefaultOnboardingSettings()
	settings.Model = "gpt-5"
	settings.ProviderOverride = "openai"
	target := availableSessionExecutionTarget(workspaceRoot)
	resolver := blockedCommittedSessionResolver{store: store, persistence: persistence}
	service := NewService(resolver, nil, nil, staticExecutionTargetResolver{target: target}).
		WithExecutionEnvironmentConfig(config.App{Settings: settings}).
		WithChatContextWorkspaceResolver(&sessionChatContextWorkspaceResolver{
			app: config.App{Settings: settings},
		}).
		WithChatContextAuthReader(&sessionChatContextAuthReader{})
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse Session ID: %v", err)
	}

	type readResult struct {
		name string
		err  error
	}
	results := make(chan readResult, 6)
	run := func(name string, read func(context.Context) error) {
		go func() {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			results <- readResult{name: name, err: read(ctx)}
		}()
	}
	run("Chat Context", func(ctx context.Context) error {
		_, err := service.ReadSessionChatContext(ctx, sessionID)
		return err
	})
	run("transcript page", func(ctx context.Context) error {
		_, err := service.GetSessionTranscriptPage(ctx, serverapi.SessionTranscriptPageRequest{SessionID: sessionID.String()})
		return err
	})
	run("latest committed answer", func(ctx context.Context) error {
		response, err := service.GetLatestCommittedAssistantFinalAnswer(ctx, serverapi.SessionLatestCommittedAssistantFinalAnswerRequest{SessionID: sessionID.String()})
		if err == nil && (response.Answer == nil || *response.Answer != "committed final answer") {
			return errors.New("latest committed answer did not use the committed persisted boundary")
		}
		return err
	})
	run("execution environment", func(ctx context.Context) error {
		_, err := service.GetSessionExecutionEnvironment(ctx, serverapi.SessionExecutionEnvironmentRequest{SessionID: sessionID})
		return err
	})
	run("dormant Main View", func(ctx context.Context) error {
		response, err := service.GetSessionMainView(ctx, serverapi.SessionMainViewRequest{SessionID: sessionID.String()})
		if err == nil && (response.MainView.Status.Goal == nil ||
			response.MainView.Status.LastCommittedAssistantFinalAnswer == nil ||
			*response.MainView.Status.LastCommittedAssistantFinalAnswer != "committed final answer") {
			return errors.New("dormant Main View did not use one persisted Session view")
		}
		return err
	})
	run("transcript tail", func(ctx context.Context) error {
		_, err := service.SessionTranscriptTailEntries(ctx, sessionID.String())
		return err
	})

	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for remaining := 6; remaining > 0; remaining-- {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatalf("%s read while append owner was blocked: %v", result.name, result.err)
			}
		case <-timer.C:
			t.Fatal("persisted Session reads waited for the append owner")
		}
	}
	release()
	if err := <-appendDone; err != nil {
		t.Fatalf("complete append: %v", err)
	}
}
