package sessionservice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimewire"
	"core/server/session"
	sessionruntime "core/server/sessionruntime"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
)

type retargetIdentityPublisher map[string]int

func (p retargetIdentityPublisher) PublishSessionIdentity(sessionID string) error {
	p[sessionID]++
	return nil
}

type retargetOutcomePublisher struct {
	outcomes chan serverapi.SessionRetargetOutcome
}

func (p retargetOutcomePublisher) PublishSessionRetargetOutcome(_ string, outcome serverapi.SessionRetargetOutcome) {
	p.outcomes <- outcome
}

type realSessionRetargetFixture struct {
	metadata            *metadata.Store
	sourceBinding       metadata.Binding
	targetProject       metadata.Binding
	parent              *session.Store
	child               *session.Store
	childID             runtimeids.SessionID
	managedBase         string
	targetWorkspaceRoot string
	authority           *sessionruntime.Authority
	publisher           retargetIdentityPublisher
}

func newRealSessionRetargetFixture(t *testing.T) realSessionRetargetFixture {
	t.Helper()
	ctx := context.Background()
	persistenceRoot := t.TempDir()
	metadataStore, err := metadata.Open(persistenceRoot)
	if err != nil {
		t.Fatalf("metadata.Open: %v", err)
	}
	t.Cleanup(func() { _ = metadataStore.Close() })
	managedBase := t.TempDir()
	sourceRoot := filepath.Join(managedBase, "source")
	targetRoot := filepath.Join(managedBase, "target")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll source: %v", err)
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll target: %v", err)
	}
	retargetRoot := filepath.Join(managedBase, "retarget")
	if err := os.MkdirAll(retargetRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll retarget: %v", err)
	}
	sourceBinding, err := metadataStore.RegisterWorkspaceBinding(ctx, sourceRoot)
	if err != nil {
		t.Fatalf("RegisterWorkspaceBinding source: %v", err)
	}
	targetProject, err := metadataStore.CreateProjectForWorkspace(ctx, targetRoot, "Target")
	if err != nil {
		t.Fatalf("CreateProjectForWorkspace target: %v", err)
	}
	storeOptions := metadataStore.AuthoritativeSessionStoreOptions()
	sourceContainer := filepath.Join(persistenceRoot, "projects", sourceBinding.ProjectID, "sessions")
	parent, err := session.Create(
		sourceContainer,
		sourceBinding.WorkspaceName,
		sourceBinding.CanonicalRoot,
		sessioncontract.SessionCategoryMain,
		storeOptions...,
	)
	if err != nil {
		t.Fatalf("session.Create parent: %v", err)
	}
	if err := parent.EnsureDurable(); err != nil {
		t.Fatalf("parent EnsureDurable: %v", err)
	}
	child, err := session.NewLazy(
		sourceContainer,
		sourceBinding.WorkspaceName,
		sourceBinding.CanonicalRoot,
		sessioncontract.SessionCategoryMain,
		storeOptions...,
	)
	if err != nil {
		t.Fatalf("session.NewLazy child: %v", err)
	}
	if err := session.InitializeCreationContext(child, parent, session.SessionCreationSourcePreviousSession, session.ChildContextOptions{}); err != nil {
		t.Fatalf("InitializeCreationContext: %v", err)
	}
	if err := child.EnsureDurable(); err != nil {
		t.Fatalf("child EnsureDurable: %v", err)
	}
	childID, err := runtimeids.ParseSessionID(child.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID child: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: persistenceRoot,
		StoreOptions:    storeOptions,
	})
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close runtime authority: %v", err)
		}
	})
	return realSessionRetargetFixture{
		metadata:            metadataStore,
		sourceBinding:       sourceBinding,
		targetProject:       targetProject,
		parent:              parent,
		child:               child,
		childID:             childID,
		managedBase:         managedBase,
		targetWorkspaceRoot: retargetRoot,
		authority:           authority,
		publisher:           make(retargetIdentityPublisher),
	}
}

func (f realSessionRetargetFixture) retargeter(metadataSource sessionRetargetMetadata) *SessionWorkspaceRetargeter {
	retargeter := NewSessionWorkspaceRetargeter(metadataSource, f.authority, f.publisher)
	return retargeter
}

func (f realSessionRetargetFixture) openRuntime(t *testing.T) {
	f.openRuntimeWithClient(t, retargetRuntimeClient{})
}

func (f realSessionRetargetFixture) openRuntimeWithClient(t *testing.T, client llm.Client) *runtime.Engine {
	t.Helper()
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings: config.Settings{
			Model:    "gpt-5",
			Reviewer: config.ReviewerSettings{Frequency: "off"},
			Shell:    config.ShellSettings{PostprocessingMode: config.ShellPostprocessingModeBuiltin},
		},
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		FilesystemContext: func() tools.FilesystemContext {
			context, err := runtimewire.NewFilesystemContext(f.sourceBinding.CanonicalRoot, f.sourceBinding.CanonicalRoot, metadata.ProjectWorkspaceBoundary{ProjectID: f.sourceBinding.ProjectID})
			if err != nil {
				t.Fatalf("NewFilesystemContext: %v", err)
			}
			context.ManagedWorktree, err = tools.NewManagedWorktreePathContext(f.managedBase, nil, nil)
			if err != nil {
				t.Fatalf("NewManagedWorktreePathContext: %v", err)
			}
			return context
		}(),
		Client: client,
	})
	if err != nil {
		t.Fatalf("NewAgentRuntimePlan: %v", err)
	}
	_, err = f.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: f.childID,
		OwnerID:   "retarget-test",
		Runtime:   &plan,
	})
	if err != nil {
		t.Fatalf("OpenRuntime: %v", err)
	}
	var engine *runtime.Engine
	if err := f.authority.WithCurrentRuntime(context.Background(), f.childID, func(_ context.Context, current *runtime.Engine) error {
		engine = current
		return nil
	}); err != nil {
		t.Fatalf("capture runtime: %v", err)
	}
	return engine
}

func (f realSessionRetargetFixture) runtimeWorkdir(t *testing.T) string {
	t.Helper()
	var workdir string
	if err := f.authority.WithCurrentRuntime(context.Background(), f.childID, func(_ context.Context, engine *runtime.Engine) error {
		workdir = engine.TranscriptWorkingDir()
		return nil
	}); err != nil {
		t.Fatalf("WithCurrentRuntime: %v", err)
	}
	return workdir
}

type retargetRuntimeClient struct{}

func (retargetRuntimeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, nil
}

func (retargetRuntimeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

type selfRetargetRuntimeClient struct {
	run            func() error
	ignoreRunError bool
	runErr         error
	requests       []llm.Request
}

func (c *selfRetargetRuntimeClient) Generate(_ context.Context, request llm.Request) (llm.Response, error) {
	c.requests = append(c.requests, request)
	if len(c.requests) == 1 {
		if err := c.run(); err != nil {
			c.runErr = err
			if !c.ignoreRunError {
				return llm.Response{}, err
			}
		}
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("continuing after the move"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		}, nil
	}
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("continued"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}, nil
}

func TestSessionWorkspaceRetargeterFailureSteersUnchangedContextIntoSameEngine(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	overlappingRoot := filepath.Join(fixture.managedBase, "overlapping")
	if err := os.MkdirAll(overlappingRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll overlapping target: %v", err)
	}
	targetProjectID := fixture.targetProject.ProjectID
	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	published := retargetOutcomePublisher{outcomes: make(chan serverapi.SessionRetargetOutcome, 1)}
	retargeter := fixture.retargeter(overlappingSessionRetargetCommit{
		Store: fixture.metadata,
		request: metadata.SessionWorkspaceRetargetRequest{
			SessionID:     fixture.child.Meta().SessionID,
			WorkspaceRoot: overlappingRoot,
		},
	}).
		WithOutcomePublisher(published)
	t.Cleanup(func() { _ = retargeter.Close() })
	client := &selfRetargetRuntimeClient{ignoreRunError: true}
	engine := fixture.openRuntimeWithClient(t, client)
	operationID := serverapi.NewWorktreeOperationID()
	client.run = func() error {
		active := engine.ActiveRun()
		if active == nil {
			return errors.New("active Agent Step is required")
		}
		_, err := retargeter.RetargetWorkspace(context.Background(), SessionWorkspaceRetargetInvocation{
			OperationID: operationID,
			Request:     req,
			Origin: &serverapi.RuntimeStepOrigin{
				RunID:  active.RunID,
				StepID: active.StepID,
			},
			CompletionMode: serverapi.SessionRetargetCompletionScheduled,
		})
		return err
	}

	if _, err := engine.SubmitUserMessage(context.Background(), "try moving this Session"); err != nil {
		t.Fatalf("originating Agent Step: %v", err)
	}
	if client.runErr != nil {
		t.Fatalf("scheduled rebind: %v", client.runErr)
	}
	outcome := <-published.outcomes
	if outcome.Kind != serverapi.SessionRetargetOutcomeFailed ||
		outcome.Failure == nil ||
		outcome.Failure.Diagnostic == "" ||
		outcome.Failure.UnchangedProject.ID != fixture.sourceBinding.ProjectID ||
		canonicalRetargetTestPath(t, outcome.Failure.UnchangedWorkingDirectory) != canonicalRetargetTestPath(t, overlappingRoot) {
		t.Fatalf("published failure outcome = %+v", *outcome.Failure)
	}
	if len(client.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(client.requests))
	}
	foundFailureNotice := false
	for _, item := range client.requests[1].Items {
		if item.MessageType != nil && *item.MessageType == llm.MessageTypeErrorFeedback {
			foundFailureNotice = true
			break
		}
	}
	if !foundFailureNotice {
		t.Fatalf("second provider request lacks typed failure notice: %+v", client.requests[1].Items)
	}
	if workdir := fixture.runtimeWorkdir(t); canonicalRetargetTestPath(t, workdir) != canonicalRetargetTestPath(t, fixture.sourceBinding.CanonicalRoot) {
		t.Fatalf("failed rebind changed runtime workdir to %q", workdir)
	}
}

func (*selfRetargetRuntimeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func projectContainsWorkspaceRoot(t *testing.T, store *metadata.Store, projectID string, workspaceRoot string) bool {
	t.Helper()
	workspaces, err := store.ListProjectWorkspaces(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListProjectWorkspaces: %v", err)
	}
	targetRoot := canonicalRetargetTestPath(t, workspaceRoot)
	for _, workspace := range workspaces {
		if canonicalRetargetTestPath(t, workspace.RootPath) == targetRoot {
			return true
		}
	}
	return false
}

func canonicalRetargetTestPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := config.CanonicalWorkspaceRoot(path)
	if err != nil {
		t.Fatalf("CanonicalWorkspaceRoot %q: %v", path, err)
	}
	return canonical
}

func TestSessionWorkspaceRetargeterDoesNotBlockOnBackgroundProcessState(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	targetProjectID := fixture.targetProject.ProjectID
	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	plan, err := fixture.metadata.PlanSessionWorkspaceRetarget(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanSessionWorkspaceRetarget: %v", err)
	}
	processes, err := shelltool.NewManager(
		shelltool.WithMinimumExecToBgTime(10*time.Millisecond),
		shelltool.WithCloseTimeouts(50*time.Millisecond, time.Second),
	)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = processes.Close() })
	process, err := processes.Start(context.Background(), shelltool.ExecRequest{
		Command:        []string{"/bin/sh", "-c", "sleep 30"},
		DisplayCommand: "sleep 30",
		OwnerSessionID: req.SessionID,
		Workdir:        fixture.sourceBinding.CanonicalRoot,
		YieldTime:      20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("start background process: %v", err)
	}
	if !process.Running || !process.Backgrounded {
		t.Fatalf("process = %+v, want running background command", process)
	}
	if _, retargetErr := fixture.retargeter(fixture.metadata).execute(context.Background(), req, nil); retargetErr != nil {
		t.Fatalf("RetargetWorkspace: %v", retargetErr)
	}
	if _, statErr := os.Stat(plan.SourceSessionDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("source artifact still exists: %v", statErr)
	}
	if info, statErr := os.Stat(plan.TargetSessionDir); statErr != nil || !info.IsDir() {
		t.Fatalf("target artifact = %v, %v", info, statErr)
	}
}

func TestSessionWorkspaceRetargeterSelfRebindAppliesInsideOriginatingStepWithoutDeadlock(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	targetProjectID := fixture.targetProject.ProjectID
	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	retargeter := fixture.retargeter(fixture.metadata)
	t.Cleanup(func() { _ = retargeter.Close() })
	client := &selfRetargetRuntimeClient{}
	engine := fixture.openRuntimeWithClient(t, client)
	client.run = func() error {
		active := engine.ActiveRun()
		if active == nil {
			return errors.New("active Agent Step is required")
		}
		_, err := retargeter.RetargetWorkspace(context.Background(), SessionWorkspaceRetargetInvocation{
			OperationID: serverapi.NewWorktreeOperationID(),
			Request:     req,
			Origin: &serverapi.RuntimeStepOrigin{
				RunID:  active.RunID,
				StepID: active.StepID,
			},
			CompletionMode: serverapi.SessionRetargetCompletionScheduled,
		})
		return err
	}

	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "rebind this Session")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("originating Agent Step: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("self-rebind deadlocked its originating Agent Step")
	}
	if workdir := fixture.runtimeWorkdir(t); canonicalRetargetTestPath(t, workdir) != canonicalRetargetTestPath(t, fixture.targetWorkspaceRoot) {
		t.Fatalf("continued runtime workdir = %q, want %q", workdir, fixture.targetWorkspaceRoot)
	}
	if len(client.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(client.requests))
	}
	foundReminder := false
	for _, item := range client.requests[1].Items {
		if item.MessageType != nil && *item.MessageType == llm.MessageTypeSessionRebind {
			foundReminder = true
			break
		}
	}
	if !foundReminder {
		t.Fatalf("second provider request lacks Session move reminder: %+v", client.requests[1].Items)
	}
	if fixture.child.Meta().RebindReminder != nil {
		t.Fatalf("consumed Session move reminder remains durable: %+v", fixture.child.Meta().RebindReminder)
	}
}

func TestSessionWorkspaceRetargeterNoOpPublishesSuccessWithoutReminder(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	published := retargetOutcomePublisher{outcomes: make(chan serverapi.SessionRetargetOutcome, 1)}
	retargeter := fixture.retargeter(fixture.metadata).WithOutcomePublisher(published)
	t.Cleanup(func() { _ = retargeter.Close() })

	response, err := retargeter.RetargetWorkspace(context.Background(), SessionWorkspaceRetargetInvocation{
		OperationID: serverapi.NewWorktreeOperationID(),
		Request: metadata.SessionWorkspaceRetargetRequest{
			SessionID:     fixture.child.Meta().SessionID,
			WorkspaceRoot: fixture.sourceBinding.CanonicalRoot,
		},
		CompletionMode: serverapi.SessionRetargetCompletionScheduled,
	})
	if err != nil {
		t.Fatalf("RetargetWorkspace: %v", err)
	}
	if response.Outcome != nil {
		t.Fatalf("scheduled no-op response = %+v", response)
	}
	select {
	case outcome := <-published.outcomes:
		if outcome.Kind != serverapi.SessionRetargetOutcomeSucceeded {
			t.Fatalf("no-op outcome = %+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("no-op outcome was not published")
	}
}

func TestSessionWorkspaceRetargeterSharedRootRemainsPersistable(t *testing.T) {
	tests := []struct {
		name         string
		crossProject bool
	}{
		{name: "default source project", crossProject: false},
		{name: "explicit target project", crossProject: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRealSessionRetargetFixture(t)
			sharedRoot := fixture.targetWorkspaceRoot
			if _, err := fixture.metadata.AttachWorkspaceToProject(context.Background(), fixture.sourceBinding.ProjectID, sharedRoot); err != nil {
				t.Fatalf("AttachWorkspaceToProject source: %v", err)
			}
			if _, err := fixture.metadata.AttachWorkspaceToProject(context.Background(), fixture.targetProject.ProjectID, sharedRoot); err != nil {
				t.Fatalf("AttachWorkspaceToProject target: %v", err)
			}

			req := metadata.SessionWorkspaceRetargetRequest{
				SessionID:     fixture.child.Meta().SessionID,
				WorkspaceRoot: sharedRoot,
			}
			wantProjectID := fixture.sourceBinding.ProjectID
			targetProjectID := fixture.targetProject.ProjectID
			if test.crossProject {
				req.ProjectID = &targetProjectID
				wantProjectID = targetProjectID
				fixture.openRuntime(t)
			}

			result, err := fixture.retargeter(fixture.metadata).execute(context.Background(), req, nil)
			if err != nil {
				t.Fatalf("RetargetWorkspace: %v", err)
			}
			if result.Binding.ProjectID != wantProjectID {
				t.Fatalf("binding project = %q, want %q", result.Binding.ProjectID, wantProjectID)
			}
			if test.crossProject {
				var rebound tools.FilesystemContext
				if err := fixture.authority.RunSessionMaintenance(context.Background(), fixture.childID.String(), func(_ context.Context, _ *session.Store, maintenance *sessionruntime.ActiveRuntimeMaintenance) error {
					rebound = maintenance.PreviousFilesystemContext.Clone()
					return nil
				}); err != nil {
					t.Fatalf("inspect cross-project filesystem context: %v", err)
				}
				if rebound.Access.ProjectWorkspace.ProjectID != targetProjectID {
					t.Fatalf("rebound Project ID = %q, want %q", rebound.Access.ProjectWorkspace.ProjectID, targetProjectID)
				}
				for _, root := range rebound.Access.ProjectWorkspace.Roots {
					if root.LexicalPath == fixture.sourceBinding.CanonicalRoot {
						t.Fatalf("rebound context retained source-only root %q", root.LexicalPath)
					}
				}
			}
			descriptor, err := session.NewOpenSessionDescriptor(fixture.childID)
			if err != nil {
				t.Fatalf("NewOpenSessionDescriptor: %v", err)
			}
			if err := fixture.authority.WithSessionStore(context.Background(), descriptor, func(_ context.Context, store *session.Store) error {
				if err := store.SetName("persisted after shared-root rebind"); err != nil {
					return err
				}
				appendSessionMessage(t, store, "step-after-rebind", session.MessageRoleUser, "after rebind")
				return nil
			}); err != nil {
				t.Fatalf("persist after rebind: %v", err)
			}

			reopened, err := session.OpenByID(
				fixture.metadata.PersistenceRoot(),
				fixture.child.Meta().SessionID,
				fixture.metadata.AuthoritativeSessionStoreOptions()...,
			)
			if err != nil {
				t.Fatalf("OpenByID after rebind: %v", err)
			}
			eventLog, err := reopened.MaterializeEventLog()
			if err != nil {
				t.Fatalf("materialize reopened event log: %v", err)
			}
			revision, err := eventLog.Revision()
			if err != nil {
				t.Fatalf("read reopened event-log revision: %v", err)
			}
			if reopened.Meta().Name != "persisted after shared-root rebind" || revision != 1 {
				t.Fatalf("reopened metadata=%+v event-log revision=%d", reopened.Meta(), revision)
			}
			belongs, err := fixture.metadata.SessionBelongsToProject(context.Background(), fixture.child.Meta().SessionID, wantProjectID)
			if err != nil {
				t.Fatalf("SessionBelongsToProject: %v", err)
			}
			if !belongs {
				t.Fatalf("session does not belong to selected project %q", wantProjectID)
			}
		})
	}
}

func TestSessionWorkspaceRetargeterMovesDormantSessionWithoutRuntimeRebind(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	targetProjectID := fixture.targetProject.ProjectID
	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	plan, err := fixture.metadata.PlanSessionWorkspaceRetarget(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanSessionWorkspaceRetarget: %v", err)
	}

	result, err := fixture.retargeter(fixture.metadata).execute(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("RetargetWorkspace: %v", err)
	}
	if info, err := os.Stat(plan.TargetSessionDir); err != nil || !info.IsDir() {
		t.Fatalf("target artifact = %v, %v", info, err)
	}
	if _, err := os.Stat(plan.SourceSessionDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source artifact still exists: %v", err)
	}
	if _, active := fixture.authority.SessionExecution(fixture.childID); active {
		t.Fatal("dormant retarget unexpectedly opened a runtime")
	}
	if result.Binding.ProjectID != targetProjectID {
		t.Fatalf("target project = %q, want %q", result.Binding.ProjectID, targetProjectID)
	}
}

type failingSessionRetargetCommit struct {
	*metadata.Store
	err error
}

func (s failingSessionRetargetCommit) CommitSessionWorkspaceRetarget(context.Context, metadata.SessionWorkspaceRetargetPlan, time.Time) (metadata.SessionWorkspaceRetargetResult, error) {
	return metadata.SessionWorkspaceRetargetResult{}, s.err
}

type blockingSessionRetargetCommit struct {
	*metadata.Store
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingSessionRetargetCommit) CommitSessionWorkspaceRetarget(
	ctx context.Context,
	plan metadata.SessionWorkspaceRetargetPlan,
	updatedAt time.Time,
) (metadata.SessionWorkspaceRetargetResult, error) {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return s.Store.CommitSessionWorkspaceRetarget(ctx, plan, updatedAt)
	case <-ctx.Done():
		return metadata.SessionWorkspaceRetargetResult{}, context.Cause(ctx)
	}
}

type overlappingSessionRetargetCommit struct {
	*metadata.Store
	request metadata.SessionWorkspaceRetargetRequest
}

func (s overlappingSessionRetargetCommit) CommitSessionWorkspaceRetarget(
	ctx context.Context,
	stalePlan metadata.SessionWorkspaceRetargetPlan,
	updatedAt time.Time,
) (metadata.SessionWorkspaceRetargetResult, error) {
	overlappingPlan, err := s.Store.PlanSessionWorkspaceRetarget(ctx, s.request)
	if err != nil {
		return metadata.SessionWorkspaceRetargetResult{}, err
	}
	if _, err := s.Store.CommitSessionWorkspaceRetarget(ctx, overlappingPlan, updatedAt); err != nil {
		return metadata.SessionWorkspaceRetargetResult{}, err
	}
	return s.Store.CommitSessionWorkspaceRetarget(ctx, stalePlan, updatedAt)
}

type failingSessionRetargetBoundary struct {
	*metadata.Store
	err error
}

func (s failingSessionRetargetBoundary) ResolveProjectWorkspaceBoundary(context.Context, string) (metadata.ProjectWorkspaceBoundary, error) {
	return metadata.ProjectWorkspaceBoundary{}, s.err
}

func TestSessionWorkspaceRetargeterPreservesSameProjectWorkspaceSnapshot(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	fixture.openRuntime(t)
	if _, err := fixture.metadata.AttachWorkspaceToProject(context.Background(), fixture.sourceBinding.ProjectID, fixture.targetWorkspaceRoot); err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}

	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
	}
	if _, err := fixture.retargeter(fixture.metadata).execute(context.Background(), req, nil); err != nil {
		t.Fatalf("RetargetWorkspace: %v", err)
	}

	var rebound tools.FilesystemContext
	if err := fixture.authority.RunSessionMaintenance(context.Background(), fixture.childID.String(), func(_ context.Context, _ *session.Store, maintenance *sessionruntime.ActiveRuntimeMaintenance) error {
		rebound = maintenance.PreviousFilesystemContext.Clone()
		return nil
	}); err != nil {
		t.Fatalf("inspect same-project filesystem context: %v", err)
	}
	if rebound.Access.ProjectWorkspace.ProjectID != fixture.sourceBinding.ProjectID {
		t.Fatalf("rebound Project ID = %q, want %q", rebound.Access.ProjectWorkspace.ProjectID, fixture.sourceBinding.ProjectID)
	}
	if len(rebound.Access.ProjectWorkspace.Roots) != 0 {
		t.Fatalf("same-project retarget refreshed Workspace roots after activation: %+v", rebound.Access.ProjectWorkspace.Roots)
	}
	if canonicalRetargetTestPath(t, rebound.Access.ExecutionTargetRoot.LexicalPath) != canonicalRetargetTestPath(t, fixture.targetWorkspaceRoot) {
		t.Fatalf("execution target root = %q, want %q", rebound.Access.ExecutionTargetRoot.LexicalPath, fixture.targetWorkspaceRoot)
	}
}

func TestSessionWorkspaceRetargeterSurfacesTargetBoundaryLookupFailureBeforeMovingArtifact(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	fixture.openRuntime(t)
	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &fixture.targetProject.ProjectID,
	}
	plan, err := fixture.metadata.PlanSessionWorkspaceRetarget(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanSessionWorkspaceRetarget: %v", err)
	}
	beforeWorkdir := fixture.runtimeWorkdir(t)
	lookupErr := errors.New("target boundary lookup failed")

	_, err = fixture.retargeter(failingSessionRetargetBoundary{Store: fixture.metadata, err: lookupErr}).execute(context.Background(), req, nil)
	if !errors.Is(err, lookupErr) {
		t.Fatalf("RetargetWorkspace error = %v, want %v", err, lookupErr)
	}
	if info, statErr := os.Stat(plan.SourceSessionDir); statErr != nil || !info.IsDir() {
		t.Fatalf("source artifact changed: %v, %v", info, statErr)
	}
	if _, statErr := os.Stat(plan.TargetSessionDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target artifact exists: %v", statErr)
	}
	if afterWorkdir := fixture.runtimeWorkdir(t); afterWorkdir != beforeWorkdir {
		t.Fatalf("runtime workdir = %q, want %q", afterWorkdir, beforeWorkdir)
	}
}

func TestSessionWorkspaceRetargeterRestoresArtifactOwnershipAndRuntimeWorkdirAfterCommitFailure(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	targetProjectID := fixture.targetProject.ProjectID
	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	plan, err := fixture.metadata.PlanSessionWorkspaceRetarget(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanSessionWorkspaceRetarget: %v", err)
	}
	fixture.openRuntime(t)
	beforeWorkdir := fixture.runtimeWorkdir(t)
	var beforeContext tools.FilesystemContext
	if err := fixture.authority.RunSessionMaintenance(context.Background(), fixture.childID.String(), func(_ context.Context, _ *session.Store, maintenance *sessionruntime.ActiveRuntimeMaintenance) error {
		beforeContext = maintenance.PreviousFilesystemContext.Clone()
		return nil
	}); err != nil {
		t.Fatalf("inspect pre-failure filesystem context: %v", err)
	}
	commitErr := errors.New("commit failed")
	metadataSource := failingSessionRetargetCommit{Store: fixture.metadata, err: commitErr}

	_, err = fixture.retargeter(metadataSource).execute(context.Background(), req, nil)
	if !errors.Is(err, commitErr) {
		t.Fatalf("RetargetWorkspace error = %v, want %v", err, commitErr)
	}
	if info, err := os.Stat(plan.SourceSessionDir); err != nil || !info.IsDir() {
		t.Fatalf("source artifact was not restored: %v, %v", info, err)
	}
	if _, err := os.Stat(plan.TargetSessionDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target artifact still exists: %v", err)
	}
	if afterWorkdir := fixture.runtimeWorkdir(t); afterWorkdir != beforeWorkdir {
		t.Fatalf("runtime workdir = %q, want rollback to %q", afterWorkdir, beforeWorkdir)
	}
	var afterContext tools.FilesystemContext
	if err := fixture.authority.RunSessionMaintenance(context.Background(), fixture.childID.String(), func(_ context.Context, _ *session.Store, maintenance *sessionruntime.ActiveRuntimeMaintenance) error {
		afterContext = maintenance.PreviousFilesystemContext.Clone()
		return nil
	}); err != nil {
		t.Fatalf("inspect post-failure filesystem context: %v", err)
	}
	if !afterContext.Equal(beforeContext) {
		t.Fatalf("filesystem context changed after failed retarget: before=%+v after=%+v", beforeContext, afterContext)
	}
	childInSource, err := fixture.metadata.SessionBelongsToProject(context.Background(), fixture.child.Meta().SessionID, fixture.sourceBinding.ProjectID)
	if err != nil {
		t.Fatalf("SessionBelongsToProject source: %v", err)
	}
	childInTarget, err := fixture.metadata.SessionBelongsToProject(context.Background(), fixture.child.Meta().SessionID, targetProjectID)
	if err != nil {
		t.Fatalf("SessionBelongsToProject target: %v", err)
	}
	if !childInSource || childInTarget {
		t.Fatalf("failed rebind ownership = source:%t target:%t", childInSource, childInTarget)
	}
	if projectContainsWorkspaceRoot(t, fixture.metadata, targetProjectID, fixture.targetWorkspaceRoot) {
		t.Fatalf("failed rebind attached workspace %q", fixture.targetWorkspaceRoot)
	}
}

func TestSessionWorkspaceRetargeterSchedulesWithoutWaitingAndPublishesCompletion(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	targetProjectID := fixture.targetProject.ProjectID
	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	blocking := &blockingSessionRetargetCommit{
		Store:   fixture.metadata,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	published := retargetOutcomePublisher{outcomes: make(chan serverapi.SessionRetargetOutcome, 1)}
	retargeter := fixture.retargeter(blocking).WithOutcomePublisher(published)
	t.Cleanup(func() { _ = retargeter.Close() })
	operationID := serverapi.NewWorktreeOperationID()

	response, err := retargeter.RetargetWorkspace(context.Background(), SessionWorkspaceRetargetInvocation{
		OperationID:    operationID,
		Request:        req,
		CompletionMode: serverapi.SessionRetargetCompletionScheduled,
	})
	if err != nil {
		t.Fatalf("RetargetWorkspace: %v", err)
	}
	if response.Acknowledgement.OperationID != operationID || response.Outcome != nil {
		t.Fatalf("scheduled response = %+v", response)
	}
	close(blocking.release)
	select {
	case outcome := <-published.outcomes:
		if outcome.Kind != serverapi.SessionRetargetOutcomeSucceeded || outcome.Success == nil {
			t.Fatalf("published outcome = %+v", outcome)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled retarget outcome was not published")
	}
}

func TestSessionWorkspaceRetargeterShutdownCancelsScheduledCallWithoutOutcome(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t)
	targetProjectID := fixture.targetProject.ProjectID
	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	blocking := &blockingSessionRetargetCommit{
		Store:   fixture.metadata,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	published := retargetOutcomePublisher{outcomes: make(chan serverapi.SessionRetargetOutcome, 1)}
	retargeter := fixture.retargeter(blocking).WithOutcomePublisher(published)

	if _, err := retargeter.RetargetWorkspace(context.Background(), SessionWorkspaceRetargetInvocation{
		OperationID:    serverapi.NewWorktreeOperationID(),
		Request:        req,
		CompletionMode: serverapi.SessionRetargetCompletionScheduled,
	}); err != nil {
		t.Fatalf("RetargetWorkspace: %v", err)
	}
	select {
	case <-blocking.started:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled retarget did not reach commit")
	}
	if err := retargeter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case outcome := <-published.outcomes:
		t.Fatalf("shutdown published terminal outcome: %+v", outcome)
	case <-time.After(100 * time.Millisecond):
	}
}
