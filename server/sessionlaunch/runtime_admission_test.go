package sessionlaunch

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"core/server/launch"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/shared/config"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

type sessionLaunchRuntimeClient struct{}

func (sessionLaunchRuntimeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (sessionLaunchRuntimeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func TestServiceOpenExistingSessionDoesNotWaitForActiveRuntime(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	containerDir := filepath.Join(root, "sessions")
	persistence := sessiontest.NewPersistence()
	store, err := session.Create(
		containerDir,
		"sessions",
		workspace,
		sessioncontract.SessionCategorySubagent,
		persistence.Options()...,
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionID := mustSessionLaunchIntentID(t, store.Meta().SessionID)
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: root,
		StoreOptions:    persistence.Options(),
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	service := NewService(launchPlannerForRuntimeAdmissionTest(
		root,
		workspace,
		containerDir,
		persistence,
	))
	filesystemContext, err := runtimewire.NewFilesystemContext(
		workspace,
		workspace,
		metadata.ProjectWorkspaceBoundary{ProjectID: "test"},
	)
	if err != nil {
		t.Fatalf("NewFilesystemContext: %v", err)
	}
	runtimePlan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: config.Settings{
			Model:              "gpt-5",
			ModelContextWindow: 200_000,
			Reviewer:           config.ReviewerSettings{Frequency: "off"},
			Shell: config.ShellSettings{
				PostprocessingMode: config.ShellPostprocessingModeNone,
			},
		},
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		FilesystemContext:     filesystemContext,
		Client:                sessionLaunchRuntimeClient{},
	})
	if err != nil {
		t.Fatalf("create runtime plan: %v", err)
	}
	attachment, err := authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "active-runtime-owner",
		Runtime:   &runtimePlan,
	})
	if err != nil {
		t.Fatalf("open runtime: %v", err)
	}
	t.Cleanup(func() {
		_, _ = attachment.Release(context.Background(), sessionruntime.RuntimeReleaseDetach)
	})

	started := make(chan struct{})
	release := make(chan struct{})
	runtimeDone := make(chan error, 1)
	go func() {
		runtimeDone <- authority.WithRuntime(context.Background(), attachment.Resource(), func(context.Context, *runtime.Engine) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	planned := make(chan error, 1)
	go func() {
		_, planErr := service.PlanSession(context.Background(), serverapi.SessionPlanRequest{
			ClientRequestID: "plan-during-active-runtime",
			Mode:            serverapi.SessionLaunchModeInteractive,
			Intent:          serverapi.OpenExistingSessionLaunchIntent(sessionID),
		})
		planned <- planErr
	}()
	select {
	case planErr := <-planned:
		if planErr != nil {
			t.Fatalf("plan existing Session: %v", planErr)
		}
	case <-time.After(time.Second):
		t.Fatal("existing-Session planning waited for the active Runtime")
	}
	close(release)
	if err := <-runtimeDone; err != nil {
		t.Fatalf("release active Runtime: %v", err)
	}

	persisted, err := persistence.ResolvePersistedSession(t.Context(), sessionID.String())
	if err != nil {
		t.Fatalf("resolve persisted Session: %v", err)
	}
	if persisted.Meta.Category == nil || *persisted.Meta.Category != sessioncontract.SessionCategorySubagent {
		t.Fatalf("persisted category = %v, want unchanged subagent", persisted.Meta.Category)
	}
}

func launchPlannerForRuntimeAdmissionTest(
	persistenceRoot string,
	workspace string,
	containerDir string,
	persistence *sessiontest.Persistence,
) launch.Planner {
	return launch.Planner{
		Config: config.App{
			WorkspaceRoot:   workspace,
			PersistenceRoot: persistenceRoot,
			Settings:        config.Settings{Model: "gpt-5"},
		},
		ContainerDir:             containerDir,
		StoreOptions:             persistence.Options(),
		PersistedSessions:        persistence,
		ProjectWorkspaceBoundary: sessionLaunchBoundaryResolver{root: workspace},
	}
}
