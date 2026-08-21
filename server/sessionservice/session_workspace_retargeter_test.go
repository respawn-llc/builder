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

type failingRetargetIdentityPublisher struct{ err error }

func (p failingRetargetIdentityPublisher) PublishSessionIdentity(string) error {
	return p.err
}

type retargetOutcomePublisher struct {
	outcomes chan serverapi.SessionRetargetOutcome
}

func (p retargetOutcomePublisher) PublishSessionRetargetOutcome(_ string, outcome serverapi.SessionRetargetOutcome) {
	p.outcomes <- outcome
}

type blockingRetargetOutcomePublisher struct {
	started chan struct{}
	release chan struct{}
}

func (p blockingRetargetOutcomePublisher) PublishSessionRetargetOutcome(
	_ string,
	_ serverapi.SessionRetargetOutcome,
) {
	close(p.started)
	<-p.release
}

type blockingSessionMetadataObserver struct {
	store   *metadata.Store
	mu      sync.Mutex
	block   bool
	started chan struct{}
	release chan struct{}
}

func newBlockingSessionMetadataObserver(store *metadata.Store) *blockingSessionMetadataObserver {
	return &blockingSessionMetadataObserver{
		store:   store,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (o *blockingSessionMetadataObserver) Arm() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.block = true
}

func (o *blockingSessionMetadataObserver) ObservePersistedStore(ctx context.Context, snapshot session.PersistedStoreSnapshot) error {
	o.mu.Lock()
	block := o.block
	o.block = false
	o.mu.Unlock()
	if block {
		close(o.started)
		select {
		case <-o.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return o.store.ImportSessionSnapshot(ctx, snapshot)
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
	storeOptions        []session.StoreOption
	observer            *blockingSessionMetadataObserver
}

func newRealSessionRetargetFixture(t *testing.T, useBlockingObserver bool) realSessionRetargetFixture {
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
	var observer *blockingSessionMetadataObserver
	if useBlockingObserver {
		observer = newBlockingSessionMetadataObserver(metadataStore)
		storeOptions[0] = session.WithPersistenceObserver(observer)
	}
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
		storeOptions:        storeOptions,
		observer:            observer,
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

type recordingRetargetRuntimeClient struct {
	response string
	requests []llm.Request
}

func (c *recordingRetargetRuntimeClient) Generate(_ context.Context, request llm.Request) (llm.Response, error) {
	c.requests = append(c.requests, request)
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value(c.response),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}, nil
}

func (*recordingRetargetRuntimeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
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
	fixture := newRealSessionRetargetFixture(t, false)
	targetProjectID := fixture.targetProject.ProjectID
	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	commitErr := errors.New("commit failed for active rebind")
	published := retargetOutcomePublisher{outcomes: make(chan serverapi.SessionRetargetOutcome, 1)}
	retargeter := fixture.retargeter(failingSessionRetargetCommit{Store: fixture.metadata, err: commitErr}).
		WithOutcomePublisher(published)
	t.Cleanup(func() { _ = retargeter.Close() })
	client := &selfRetargetRuntimeClient{ignoreRunError: true}
	engine := fixture.openRuntimeWithClient(t, client)
	operationID := serverapi.NewWorktreeOperationID()
	var acknowledgement serverapi.WorktreeScheduledAcknowledgement
	client.run = func() error {
		active := engine.ActiveRun()
		if active == nil {
			return errors.New("active Agent Step is required")
		}
		response, err := retargeter.RetargetWorkspace(context.Background(), SessionWorkspaceRetargetInvocation{
			OperationID: operationID,
			Request:     req,
			Origin: &serverapi.RuntimeStepOrigin{
				RunID:  active.RunID,
				StepID: active.StepID,
			},
			CompletionMode: serverapi.SessionRetargetCompletionScheduled,
		})
		acknowledgement = response.Acknowledgement
		return err
	}

	if _, err := engine.SubmitUserMessage(context.Background(), "try moving this Session"); err != nil {
		t.Fatalf("originating Agent Step: %v", err)
	}
	if client.runErr != nil || acknowledgement.OperationID != operationID {
		t.Fatalf("scheduled rebind = acknowledgement %+v error %v", acknowledgement, client.runErr)
	}
	outcome := <-published.outcomes
	if outcome.Kind != serverapi.SessionRetargetOutcomeFailed ||
		outcome.Failure == nil ||
		outcome.Failure.Diagnostic == "" ||
		outcome.Failure.UnchangedProject.ID != fixture.sourceBinding.ProjectID ||
		outcome.Failure.UnchangedWorkingDirectory != fixture.sourceBinding.CanonicalRoot {
		t.Fatalf("published failure outcome = %+v", *outcome.Failure)
	}
	if len(client.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(client.requests))
	}
	var failureNotice *llm.ResponseItem
	for _, item := range client.requests[1].Items {
		if item.MessageType != nil && *item.MessageType == llm.MessageTypeErrorFeedback &&
			item.Content != nil {
			copyItem := item
			failureNotice = &copyItem
			break
		}
	}
	if failureNotice == nil {
		t.Fatalf("second provider request lacks typed failure notice: %+v", client.requests[1].Items)
	}
	if workdir := fixture.runtimeWorkdir(t); canonicalRetargetTestPath(t, workdir) != canonicalRetargetTestPath(t, fixture.sourceBinding.CanonicalRoot) {
		t.Fatalf("failed rebind changed runtime workdir to %q", workdir)
	}
	if err := fixture.authority.WithCurrentRuntime(context.Background(), fixture.childID, func(_ context.Context, current *runtime.Engine) error {
		if current != engine {
			t.Fatalf("failed Session rebind replaced Engine: before=%p after=%p", engine, current)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect continued Engine: %v", err)
	}
}

func TestSessionWorkspaceRetargeterKeepsCommittedSuccessWhenIdentityPublicationFails(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	publishErr := errors.New("identity publication failed")
	published := retargetOutcomePublisher{outcomes: make(chan serverapi.SessionRetargetOutcome, 1)}
	retargeter := NewSessionWorkspaceRetargeter(
		fixture.metadata,
		fixture.authority,
		failingRetargetIdentityPublisher{err: publishErr},
	).WithOutcomePublisher(published)
	t.Cleanup(func() { _ = retargeter.Close() })

	response, err := retargeter.RetargetWorkspace(t.Context(), SessionWorkspaceRetargetInvocation{
		OperationID: serverapi.NewWorktreeOperationID(),
		Request: metadata.SessionWorkspaceRetargetRequest{
			SessionID:     fixture.child.Meta().SessionID,
			WorkspaceRoot: fixture.targetWorkspaceRoot,
		},
		CompletionMode: serverapi.SessionRetargetCompletionWait,
	})
	if err != nil ||
		response.Outcome == nil ||
		response.Outcome.Kind != serverapi.SessionRetargetOutcomeSucceeded ||
		response.Outcome.Success == nil {
		t.Fatalf("committed rebind response = %+v error %v", response, err)
	}
	if response.Outcome.Success.Binding.CanonicalRoot != canonicalRetargetTestPath(t, fixture.targetWorkspaceRoot) {
		t.Fatalf("committed binding = %+v", response.Outcome.Success.Binding)
	}
	if publishedOutcome := <-published.outcomes; publishedOutcome.OperationID != response.Outcome.OperationID ||
		publishedOutcome.Kind != serverapi.SessionRetargetOutcomeSucceeded {
		t.Fatalf("published wait outcome = %+v, want %+v", publishedOutcome, *response.Outcome)
	}
}

func TestSessionWorkspaceRetargeterWaitFailurePublishesAndSteersOutcome(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	commitErr := errors.New("wait-mode commit failed")
	published := retargetOutcomePublisher{outcomes: make(chan serverapi.SessionRetargetOutcome, 1)}
	retargeter := fixture.retargeter(failingSessionRetargetCommit{Store: fixture.metadata, err: commitErr}).
		WithOutcomePublisher(published)
	t.Cleanup(func() { _ = retargeter.Close() })
	client := &recordingRetargetRuntimeClient{response: t.Name()}
	engine := fixture.openRuntimeWithClient(t, client)
	operationID := serverapi.NewWorktreeOperationID()

	response, err := retargeter.RetargetWorkspace(t.Context(), SessionWorkspaceRetargetInvocation{
		OperationID: operationID,
		Request: metadata.SessionWorkspaceRetargetRequest{
			SessionID:     fixture.child.Meta().SessionID,
			WorkspaceRoot: fixture.targetWorkspaceRoot,
		},
		CompletionMode: serverapi.SessionRetargetCompletionWait,
	})
	if err != nil ||
		response.Outcome == nil ||
		response.Outcome.Kind != serverapi.SessionRetargetOutcomeFailed {
		t.Fatalf("wait failure response = %+v error %v", response, err)
	}
	if publishedOutcome := <-published.outcomes; publishedOutcome.OperationID != operationID ||
		publishedOutcome.Kind != serverapi.SessionRetargetOutcomeFailed {
		t.Fatalf("published wait failure = %+v", publishedOutcome)
	}
	if _, err := engine.SubmitUserMessage(t.Context(), t.Name()); err != nil {
		t.Fatalf("SubmitUserMessage after wait failure: %v", err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(client.requests))
	}
	for _, item := range client.requests[0].Items {
		if item.MessageType != nil && *item.MessageType == llm.MessageTypeErrorFeedback {
			return
		}
	}
	t.Fatalf("provider request lacks typed rebind failure notice: %+v", client.requests[0].Items)
}

type applyPlanFailureMetadata struct {
	*metadata.Store
	applyErr error
	source   metadata.SessionWorkspaceRetargetSource
	calls    int
}

func (m *applyPlanFailureMetadata) PlanSessionWorkspaceRetarget(
	ctx context.Context,
	request metadata.SessionWorkspaceRetargetRequest,
) (metadata.SessionWorkspaceRetargetPlan, error) {
	m.calls++
	if m.calls == 1 {
		return m.Store.PlanSessionWorkspaceRetarget(ctx, request)
	}
	return metadata.SessionWorkspaceRetargetPlan{}, m.applyErr
}

func (m *applyPlanFailureMetadata) ResolveSessionWorkspaceRetargetSource(
	context.Context,
	string,
) (metadata.SessionWorkspaceRetargetSource, error) {
	return m.source, nil
}

func TestSessionWorkspaceRetargeterFailureUsesApplyTimeSourceFacts(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	applyErr := errors.New("apply-time plan failed")
	applySource := metadata.SessionWorkspaceRetargetSource{
		Project:                   serverapi.ProjectReference{ID: "project-new", Name: "New"},
		EffectiveWorkingDirectory: filepath.Join(fixture.managedBase, "new"),
	}
	source := &applyPlanFailureMetadata{
		Store:    fixture.metadata,
		applyErr: applyErr,
		source:   applySource,
	}
	retargeter := fixture.retargeter(source)
	t.Cleanup(func() { _ = retargeter.Close() })

	response, err := retargeter.RetargetWorkspace(t.Context(), SessionWorkspaceRetargetInvocation{
		OperationID: serverapi.NewWorktreeOperationID(),
		Request: metadata.SessionWorkspaceRetargetRequest{
			SessionID:     fixture.child.Meta().SessionID,
			WorkspaceRoot: fixture.targetWorkspaceRoot,
		},
		CompletionMode: serverapi.SessionRetargetCompletionWait,
	})
	if err != nil ||
		response.Outcome == nil ||
		response.Outcome.Kind != serverapi.SessionRetargetOutcomeFailed ||
		response.Outcome.Failure == nil ||
		response.Outcome.Failure.Diagnostic != applyErr.Error() ||
		response.Outcome.Failure.UnchangedProject != applySource.Project ||
		response.Outcome.Failure.UnchangedWorkingDirectory != applySource.EffectiveWorkingDirectory {
		t.Fatalf("apply-time failure response = %+v error %v", response, err)
	}
}

func TestSessionWorkspaceRetargeterFailureUsesAuthoritativePostApplySourceFacts(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	overlappingRoot := filepath.Join(fixture.managedBase, "overlapping")
	if err := os.MkdirAll(overlappingRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll overlapping target: %v", err)
	}
	retargeter := fixture.retargeter(overlappingSessionRetargetCommit{
		Store: fixture.metadata,
		request: metadata.SessionWorkspaceRetargetRequest{
			SessionID:     fixture.child.Meta().SessionID,
			WorkspaceRoot: overlappingRoot,
		},
	})
	t.Cleanup(func() { _ = retargeter.Close() })

	response, err := retargeter.RetargetWorkspace(t.Context(), SessionWorkspaceRetargetInvocation{
		OperationID: serverapi.NewWorktreeOperationID(),
		Request: metadata.SessionWorkspaceRetargetRequest{
			SessionID:     fixture.child.Meta().SessionID,
			WorkspaceRoot: fixture.targetWorkspaceRoot,
		},
		CompletionMode: serverapi.SessionRetargetCompletionWait,
	})
	if err != nil ||
		response.Outcome == nil ||
		response.Outcome.Kind != serverapi.SessionRetargetOutcomeFailed ||
		response.Outcome.Failure == nil ||
		response.Outcome.Failure.UnchangedProject.ID != fixture.sourceBinding.ProjectID ||
		canonicalRetargetTestPath(t, response.Outcome.Failure.UnchangedWorkingDirectory) != canonicalRetargetTestPath(t, overlappingRoot) {
		t.Fatalf("overlapping rebind failure response = %+v error %v", response, err)
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

func TestSessionWorkspaceRetargeterMovesRealArtifactAndMetadataAcrossProjects(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	worktreeReminder := session.WorktreeReminderState{
		Mode: session.WorktreeReminderModeExit,
		WorktreeContext: session.WorktreeContext{
			WorktreePath:  filepath.Join(fixture.sourceBinding.CanonicalRoot, "previous-worktree"),
			WorkspaceRoot: fixture.sourceBinding.CanonicalRoot,
			EffectiveCwd:  fixture.sourceBinding.CanonicalRoot,
		},
	}
	if err := fixture.child.SetWorktreeReminderState(&worktreeReminder); err != nil {
		t.Fatalf("SetWorktreeReminderState: %v", err)
	}
	fixture.openRuntime(t)
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
	if !result.WorkspaceBindingCreated {
		t.Fatal("WorkspaceBindingCreated = false, want true")
	}
	if result.Binding.ProjectID != targetProjectID {
		t.Fatalf("target project = %q, want %q", result.Binding.ProjectID, targetProjectID)
	}
	if _, err := os.Stat(plan.SourceSessionDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source artifact still exists: %v", err)
	}
	if info, err := os.Stat(plan.TargetSessionDir); err != nil || !info.IsDir() {
		t.Fatalf("target artifact = %v, %v", info, err)
	}
	record, err := fixture.metadata.ResolvePersistedSession(context.Background(), fixture.child.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolvePersistedSession: %v", err)
	}
	if canonicalRetargetTestPath(t, record.SessionDir) != canonicalRetargetTestPath(t, plan.TargetSessionDir) {
		t.Fatalf("persisted session dir = %q, want %q", record.SessionDir, plan.TargetSessionDir)
	}
	reopened, err := session.OpenByID(
		fixture.metadata.PersistenceRoot(),
		fixture.child.Meta().SessionID,
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("session.OpenByID: %v", err)
	}
	if reopened.Meta().WorkspaceRoot != result.Binding.CanonicalRoot {
		t.Fatalf("reopened workspace root = %q, want %q", reopened.Meta().WorkspaceRoot, result.Binding.CanonicalRoot)
	}
	parentID, err := runtimeids.ParseSessionID(fixture.parent.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID parent: %v", err)
	}
	if reopened.Meta().PreviousSessionID == nil || *reopened.Meta().PreviousSessionID != parentID {
		t.Fatalf("reopened previous session = %v, want %q", reopened.Meta().PreviousSessionID, fixture.parent.Meta().SessionID)
	}
	parentInSource, err := fixture.metadata.SessionBelongsToProject(context.Background(), fixture.parent.Meta().SessionID, fixture.sourceBinding.ProjectID)
	if err != nil {
		t.Fatalf("SessionBelongsToProject parent: %v", err)
	}
	childInTarget, err := fixture.metadata.SessionBelongsToProject(context.Background(), fixture.child.Meta().SessionID, targetProjectID)
	if err != nil {
		t.Fatalf("SessionBelongsToProject child: %v", err)
	}
	if !parentInSource || !childInTarget {
		t.Fatalf("cross-project lineage ownership = parent-source:%t child-target:%t", parentInSource, childInTarget)
	}
	if !projectContainsWorkspaceRoot(t, fixture.metadata, targetProjectID, result.Binding.CanonicalRoot) {
		t.Fatalf("target project does not contain auto-attached workspace %q", result.Binding.CanonicalRoot)
	}
	sourceAttached, err := fixture.metadata.ProjectWorkspaceAttached(context.Background(), fixture.sourceBinding.ProjectID, result.Binding.CanonicalRoot)
	if err != nil {
		t.Fatalf("ProjectWorkspaceAttached source: %v", err)
	}
	if sourceAttached {
		t.Fatalf("source project unexpectedly retained target workspace %q", result.Binding.CanonicalRoot)
	}
	if published := fixture.publisher[fixture.child.Meta().SessionID]; published != 1 {
		t.Fatalf("identity publication count = %d, want one", published)
	}
	if workdir := fixture.runtimeWorkdir(t); workdir != result.Binding.CanonicalRoot {
		t.Fatalf("runtime workdir = %q, want %q", workdir, result.Binding.CanonicalRoot)
	}
	var activeMeta session.Meta
	if err := fixture.authority.RunSessionMaintenance(context.Background(), fixture.childID.String(), func(_ context.Context, store *session.Store, _ *sessionruntime.ActiveRuntimeMaintenance) error {
		activeMeta = store.Meta()
		return nil
	}); err != nil {
		t.Fatalf("inspect active Session metadata: %v", err)
	}
	if activeMeta.WorktreeReminder == nil ||
		activeMeta.WorktreeReminder.Mode != session.WorktreeReminderModeExit ||
		activeMeta.RebindReminder == nil {
		t.Fatalf("active reminder states = worktree %+v rebind %+v", activeMeta.WorktreeReminder, activeMeta.RebindReminder)
	}
}

func TestSessionWorkspaceRetargeterDoesNotBlockOnBackgroundProcessState(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
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
	fixture := newRealSessionRetargetFixture(t, false)
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
	var reminder *llm.ResponseItem
	for _, item := range client.requests[1].Items {
		if item.MessageType != nil && *item.MessageType == llm.MessageTypeSessionRebind {
			copyItem := item
			reminder = &copyItem
			break
		}
	}
	if reminder == nil || reminder.CompactContent == nil || reminder.Content == nil {
		t.Fatalf("second provider request Session move reminder = %+v", reminder)
	}
	if fixture.child.Meta().RebindReminder != nil {
		t.Fatalf("consumed Session move reminder remains durable: %+v", fixture.child.Meta().RebindReminder)
	}
	if err := fixture.authority.WithCurrentRuntime(context.Background(), fixture.childID, func(_ context.Context, current *runtime.Engine) error {
		if current != engine {
			t.Fatalf("Session rebind replaced Engine: before=%p after=%p", engine, current)
		}
		return nil
	}); err != nil {
		t.Fatalf("inspect continued Engine: %v", err)
	}
}

func TestSessionWorkspaceRetargeterNoOpPublishesSuccessWithoutReminder(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
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
	reopened, err := session.OpenByID(
		fixture.metadata.PersistenceRoot(),
		fixture.child.Meta().SessionID,
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	if reopened.Meta().RebindReminder != nil {
		t.Fatalf("no-op created rebind reminder: %+v", reopened.Meta().RebindReminder)
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
			fixture := newRealSessionRetargetFixture(t, false)
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
			}
			if test.crossProject {
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
				if len(rebound.Access.ProjectWorkspace.Roots) != 2 {
					t.Fatalf("rebound Project Workspace roots = %+v, want target project roots", rebound.Access.ProjectWorkspace.Roots)
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
	fixture := newRealSessionRetargetFixture(t, false)
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

func TestSessionWorkspaceRetargeterStaleObserverCannotRestorePreviousTarget(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, true)
	if fixture.observer == nil {
		t.Fatal("fixture did not install blocking metadata observer")
	}
	sourceContainer := filepath.Join(fixture.metadata.PersistenceRoot(), "projects", fixture.sourceBinding.ProjectID, "sessions")
	store, err := session.Create(
		sourceContainer,
		fixture.sourceBinding.WorkspaceName,
		fixture.sourceBinding.CanonicalRoot,
		sessioncontract.SessionCategoryMain,
		fixture.storeOptions...,
	)
	if err != nil {
		t.Fatalf("session.Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}

	fixture.observer.Arm()
	persistDone := make(chan error, 1)
	go func() {
		persistDone <- store.SetName("captured before rebind")
	}()
	select {
	case <-fixture.observer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("metadata observer did not block")
	}

	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     store.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
	}
	type retargetOutcome struct {
		result metadata.SessionWorkspaceRetargetResult
		err    error
	}
	retargetDone := make(chan retargetOutcome, 1)
	go func() {
		result, err := fixture.retargeter(fixture.metadata).execute(context.Background(), req, nil)
		retargetDone <- retargetOutcome{result: result, err: err}
	}()
	close(fixture.observer.release)
	if err := <-persistDone; err != nil {
		t.Fatalf("SetName observer completion: %v", err)
	}
	retargeted := <-retargetDone
	if retargeted.err != nil {
		t.Fatalf("RetargetWorkspace: %v", retargeted.err)
	}

	reopened, err := session.OpenByID(
		fixture.metadata.PersistenceRoot(),
		store.Meta().SessionID,
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	if reopened.Meta().WorkspaceRoot != retargeted.result.Binding.CanonicalRoot {
		t.Fatalf("workspace root = %q, want rebound root %q", reopened.Meta().WorkspaceRoot, retargeted.result.Binding.CanonicalRoot)
	}
	if reopened.Meta().WorkspaceContainer != retargeted.result.Binding.WorkspaceName {
		t.Fatalf("workspace container = %q, want %q", reopened.Meta().WorkspaceContainer, retargeted.result.Binding.WorkspaceName)
	}
	if reopened.Meta().Name != "captured before rebind" {
		t.Fatalf("session name = %q, want pre-rebind metadata mutation", reopened.Meta().Name)
	}
	target, err := fixture.metadata.ResolveSessionExecutionTarget(context.Background(), store.Meta().SessionID)
	if err != nil {
		t.Fatalf("ResolveSessionExecutionTarget: %v", err)
	}
	if target.WorkspaceID != retargeted.result.Binding.WorkspaceID {
		t.Fatalf("workspace id = %q, want rebound workspace %q", target.WorkspaceID, retargeted.result.Binding.WorkspaceID)
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

type shutdownRaceFailureMetadata struct {
	*metadata.Store
	retargeter *SessionWorkspaceRetargeter
	commitErr  error
	closeDone  chan error
}

func (s *shutdownRaceFailureMetadata) CommitSessionWorkspaceRetarget(
	context.Context,
	metadata.SessionWorkspaceRetargetPlan,
	time.Time,
) (metadata.SessionWorkspaceRetargetResult, error) {
	return metadata.SessionWorkspaceRetargetResult{}, s.commitErr
}

func (s *shutdownRaceFailureMetadata) ResolveSessionWorkspaceRetargetSource(
	_ context.Context,
	sessionID string,
) (metadata.SessionWorkspaceRetargetSource, error) {
	source, err := s.Store.ResolveSessionWorkspaceRetargetSource(context.Background(), sessionID)
	if err != nil {
		return metadata.SessionWorkspaceRetargetSource{}, err
	}
	go func() {
		s.closeDone <- s.retargeter.Close()
	}()
	<-s.retargeter.lifetimeCtx.Done()
	return source, nil
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
	fixture := newRealSessionRetargetFixture(t, false)
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
	fixture := newRealSessionRetargetFixture(t, false)
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
	fixture := newRealSessionRetargetFixture(t, false)
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
	fixture := newRealSessionRetargetFixture(t, false)
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
	select {
	case <-blocking.started:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled retarget did not start")
	}
	select {
	case outcome := <-published.outcomes:
		t.Fatalf("outcome published before commit release: %+v", outcome)
	default:
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
	fixture := newRealSessionRetargetFixture(t, false)
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

func TestSessionWorkspaceRetargeterShutdownCancelsWaitCallWithoutOutcome(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	blocking := &blockingSessionRetargetCommit{
		Store:   fixture.metadata,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	published := retargetOutcomePublisher{outcomes: make(chan serverapi.SessionRetargetOutcome, 1)}
	retargeter := fixture.retargeter(blocking).WithOutcomePublisher(published)
	type waitResult struct {
		response serverapi.SessionRetargetWorkspaceResponse
		err      error
	}
	done := make(chan waitResult, 1)
	go func() {
		response, err := retargeter.RetargetWorkspace(context.Background(), SessionWorkspaceRetargetInvocation{
			OperationID: serverapi.NewWorktreeOperationID(),
			Request: metadata.SessionWorkspaceRetargetRequest{
				SessionID:     fixture.child.Meta().SessionID,
				WorkspaceRoot: fixture.targetWorkspaceRoot,
			},
			CompletionMode: serverapi.SessionRetargetCompletionWait,
		})
		done <- waitResult{response: response, err: err}
	}()
	select {
	case <-blocking.started:
	case <-time.After(3 * time.Second):
		t.Fatal("waiting retarget did not reach commit")
	}
	if err := retargeter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) || result.response.Outcome != nil {
			t.Fatalf("shutdown wait result = %+v error %v", result.response, result.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiting retarget did not return after shutdown")
	}
	select {
	case outcome := <-published.outcomes:
		t.Fatalf("shutdown published wait terminal outcome: %+v", outcome)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSessionWorkspaceRetargeterShutdownRaceSuppressesOrdinaryFailureOutcome(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	source := &shutdownRaceFailureMetadata{
		Store:     fixture.metadata,
		commitErr: errors.New("ordinary commit failure"),
		closeDone: make(chan error, 1),
	}
	published := retargetOutcomePublisher{outcomes: make(chan serverapi.SessionRetargetOutcome, 1)}
	retargeter := fixture.retargeter(source).WithOutcomePublisher(published)
	source.retargeter = retargeter

	response, err := retargeter.RetargetWorkspace(context.Background(), SessionWorkspaceRetargetInvocation{
		OperationID: serverapi.NewWorktreeOperationID(),
		Request: metadata.SessionWorkspaceRetargetRequest{
			SessionID:     fixture.child.Meta().SessionID,
			WorkspaceRoot: fixture.targetWorkspaceRoot,
		},
		CompletionMode: serverapi.SessionRetargetCompletionWait,
	})
	if !errors.Is(err, context.Canceled) || response.Outcome != nil {
		t.Fatalf("shutdown race response = %+v error %v", response, err)
	}
	if closeErr := <-source.closeDone; closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
	select {
	case outcome := <-published.outcomes:
		t.Fatalf("shutdown race published terminal outcome: %+v", outcome)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSessionWorkspaceRetargeterClosedNoOpDoesNotPublishOutcome(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	published := retargetOutcomePublisher{outcomes: make(chan serverapi.SessionRetargetOutcome, 1)}
	retargeter := fixture.retargeter(fixture.metadata).WithOutcomePublisher(published)
	if err := retargeter.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	response, err := retargeter.RetargetWorkspace(context.Background(), SessionWorkspaceRetargetInvocation{
		OperationID: serverapi.NewWorktreeOperationID(),
		Request: metadata.SessionWorkspaceRetargetRequest{
			SessionID:     fixture.child.Meta().SessionID,
			WorkspaceRoot: fixture.sourceBinding.CanonicalRoot,
		},
		CompletionMode: serverapi.SessionRetargetCompletionScheduled,
	})
	if !errors.Is(err, context.Canceled) || response.Outcome != nil {
		t.Fatalf("closed no-op response = %+v error %v", response, err)
	}
	select {
	case outcome := <-published.outcomes:
		t.Fatalf("closed no-op published terminal outcome: %+v", outcome)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSessionWorkspaceRetargeterShutdownDoesNotOvertakeTerminalPublication(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	published := blockingRetargetOutcomePublisher{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	retargeter := fixture.retargeter(fixture.metadata).WithOutcomePublisher(published)
	retargetDone := make(chan error, 1)
	go func() {
		_, err := retargeter.RetargetWorkspace(context.Background(), SessionWorkspaceRetargetInvocation{
			OperationID: serverapi.NewWorktreeOperationID(),
			Request: metadata.SessionWorkspaceRetargetRequest{
				SessionID:     fixture.child.Meta().SessionID,
				WorkspaceRoot: fixture.sourceBinding.CanonicalRoot,
			},
			CompletionMode: serverapi.SessionRetargetCompletionScheduled,
		})
		retargetDone <- err
	}()
	select {
	case <-published.started:
	case <-time.After(3 * time.Second):
		t.Fatal("terminal publication did not start")
	}
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- retargeter.Close()
	}()
	select {
	case err := <-closeDone:
		t.Fatalf("shutdown overtook terminal publication: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(published.release)
	if err := <-retargetDone; err != nil {
		t.Fatalf("RetargetWorkspace: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("Close: %v", err)
	}
}
