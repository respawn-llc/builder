package workflowrunner

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/workflowstore"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

type workflowContinuationTestClient struct{}

func (workflowContinuationTestClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("unexpected model request")
}

func TestPlanExistingWorkflowSessionSerializesReplacementOnCurrentResource(t *testing.T) {
	ctx := context.Background()
	persistenceRoot := t.TempDir()
	workspace := t.TempDir()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	binding, err := metadataStore.RegisterWorkspaceBinding(ctx, workspace)
	if err != nil {
		t.Fatalf("register workspace: %v", err)
	}
	persistence := sessiontest.NewPersistence()
	persistenceGate := sessiontest.NewPersistenceGate(persistence)
	storeOptions := []session.StoreOption{
		session.WithPersistenceObserver(persistenceGate),
		session.WithPersistedSessionResolver(persistence),
	}
	containerDir := filepath.Join(persistenceRoot, "projects", binding.ProjectID, "sessions")
	store, err := session.Create(
		containerDir,
		"sessions",
		workspace,
		sessioncontract.SessionCategoryMain,
		storeOptions...,
	)
	if err != nil {
		t.Fatalf("create workflow session: %v", err)
	}
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	settings := config.Settings{
		Model:              "gpt-5",
		ModelContextWindow: 200_000,
		OpenAIBaseURL:      "http://workflow-planning.example/v1",
		Reviewer:           config.ReviewerSettings{Frequency: "off"},
		Shell: config.ShellSettings{
			PostprocessingMode: config.ShellPostprocessingModeNone,
		},
	}
	runtimePlan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: settings,
		Workdir:  workspace,
		Client:   workflowContinuationTestClient{},
	})
	if err != nil {
		t.Fatalf("create runtime plan: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: persistenceRoot,
		StoreOptions:    storeOptions,
	})
	t.Cleanup(func() {
		if closeErr := authority.Close(context.Background()); closeErr != nil {
			t.Errorf("close authority: %v", closeErr)
		}
	})
	initial, err := authority.OpenRuntime(ctx, sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "initial-resource",
		Runtime:   &runtimePlan,
	})
	if err != nil {
		t.Fatalf("open initial runtime: %v", err)
	}
	starter := &Starter{
		cfg: config.App{
			PersistenceRoot: persistenceRoot,
			WorkspaceRoot:   workspace,
			Settings:        settings,
		},
		metadata:         metadataStore,
		runtimeAuthority: authority,
		storeOptions:     storeOptions,
	}
	input := workflowstore.RunStartContext{
		Run: workflowstore.RunRecord{
			ID:        "workflow-run",
			SessionID: sessionID.String(),
		},
		Task: workflowstore.TaskRecord{ProjectID: binding.ProjectID},
		ExecutionRoot: &workflowstore.ExecutionRoot{
			SourceWorkspaceID:   binding.WorkspaceID,
			SourceWorkspaceRoot: workspace,
		},
	}

	planningPersisted, releasePlanningPersistence := persistenceGate.BlockNext()
	t.Cleanup(releasePlanningPersistence)
	planned := make(chan error, 1)
	go func() {
		_, _, planErr := starter.planSession(ctx, input)
		planned <- planErr
	}()
	select {
	case <-planningPersisted:
	case <-time.After(3 * time.Second):
		t.Fatal("workflow planning mutation did not reach persistence gate")
	}

	replacementRelease := make(chan struct{})
	var releaseReplacement sync.Once
	t.Cleanup(func() { releaseReplacement.Do(func() { close(replacementRelease) }) })
	type replacementResult struct {
		handle sessionruntime.ExecutionHandle
		err    error
	}
	replaced := make(chan replacementResult, 1)
	go func() {
		handle, startErr := authority.StartAgentExecution(ctx, sessionruntime.AgentExecutionRequest{
			Descriptor: mustWorkflowRecoveryOpenDescriptor(t, sessionID),
			Runtime:    &runtimePlan,
			Resource:   sessionruntime.ReplaceAgentResource{},
			Runner: func(runCtx context.Context, _ sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
				select {
				case <-replacementRelease:
					return nil
				case <-runCtx.Done():
					return context.Cause(runCtx)
				}
			},
		})
		replaced <- replacementResult{handle: handle, err: startErr}
	}()
	select {
	case result := <-replaced:
		t.Fatalf("replacement passed workflow planning ownership: handle=%v error=%v", result.handle, result.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := authority.WithRuntime(ctx, initial.Resource(), func(context.Context, *runtime.Engine) error {
		return nil
	}); err != nil {
		t.Fatalf("current resource was unavailable while workflow planning owned the session: %v", err)
	}

	releasePlanningPersistence()
	select {
	case err := <-planned:
		if err != nil {
			t.Fatalf("plan existing workflow session: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("workflow planning did not complete after persistence release")
	}
	var replacement replacementResult
	select {
	case replacement = <-replaced:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement did not complete after workflow planning")
	}
	if replacement.err != nil {
		t.Fatalf("replace runtime: %v", replacement.err)
	}
	if replacement.handle == nil {
		t.Fatal("replacement returned nil execution handle")
	}
	if replacement.handle.Scope().ResourceGeneration() <= initial.Resource().Generation() {
		t.Fatalf(
			"replacement resource generation = %d, want successor of %d",
			replacement.handle.Scope().ResourceGeneration(),
			initial.Resource().Generation(),
		)
	}
	releaseReplacement.Do(func() { close(replacementRelease) })
	if _, err := replacement.handle.Wait(ctx); err != nil {
		t.Fatalf("wait replacement execution: %v", err)
	}
	if err := replacement.handle.Close(ctx); err != nil {
		t.Fatalf("close replacement execution: %v", err)
	}
}

func mustWorkflowRecoveryOpenDescriptor(t *testing.T, sessionID runtimeids.SessionID) session.SessionDescriptor {
	t.Helper()
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new open session descriptor: %v", err)
	}
	return descriptor
}
