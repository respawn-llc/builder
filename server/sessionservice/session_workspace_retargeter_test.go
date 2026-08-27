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

type retargetProcessSource []shelltool.Snapshot

func (s retargetProcessSource) List() []shelltool.Snapshot {
	return append([]shelltool.Snapshot(nil), s...)
}

type retargetIdentityPublisher map[string]int

func (p retargetIdentityPublisher) PublishSessionIdentity(sessionID string) error {
	p[sessionID]++
	return nil
}

type retargetIdentityPublisherFunc func(string) error

func (f retargetIdentityPublisherFunc) PublishSessionIdentity(sessionID string) error {
	return f(sessionID)
}

func TestScheduledRetargetAdmissionKeepsAcceptedOperationServerOwned(t *testing.T) {
	var admission scheduledRetargetAdmission
	if !admission.accept() {
		t.Fatal("pending rebind was not accepted")
	}
	if admission.cancelPending() {
		t.Fatal("request cancellation reclaimed an accepted rebind")
	}
	if !admission.accepted() {
		t.Fatal("accepted rebind lost server ownership")
	}
}

type staleFirstProjectBoundaryMetadata struct {
	sessionRetargetMetadata

	mu    sync.Mutex
	calls int
}

func (m *staleFirstProjectBoundaryMetadata) PlanSessionWorkspaceRetarget(
	ctx context.Context,
	req metadata.SessionWorkspaceRetargetRequest,
) (metadata.SessionWorkspaceRetargetPlan, error) {
	plan, err := m.sessionRetargetMetadata.PlanSessionWorkspaceRetarget(ctx, req)
	if err != nil {
		return metadata.SessionWorkspaceRetargetPlan{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls == 1 {
		plan.SourceProject = plan.TargetProject
	}
	return plan, nil
}

type secondRetargetPlanSignal struct {
	sessionRetargetMetadata

	mu     sync.Mutex
	calls  int
	second chan struct{}
}

func (m *secondRetargetPlanSignal) PlanSessionWorkspaceRetarget(
	ctx context.Context,
	req metadata.SessionWorkspaceRetargetRequest,
) (metadata.SessionWorkspaceRetargetPlan, error) {
	plan, err := m.sessionRetargetMetadata.PlanSessionWorkspaceRetarget(ctx, req)
	if err != nil {
		return metadata.SessionWorkspaceRetargetPlan{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.calls == 2 {
		close(m.second)
	}
	return plan, nil
}

type failingRetargetResourceLifecycle struct {
	err error
}

func (f failingRetargetResourceLifecycle) ResourceReady(
	context.Context,
	sessionruntime.AgentResourceDescriptor,
	*runtime.Engine,
	sessionruntime.AgentResourceRetainer,
) error {
	return nil
}

func (f failingRetargetResourceLifecycle) ResourceDraining(context.Context, sessionruntime.AgentResourceDescriptor) error {
	return f.err
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

func (o *blockingSessionMetadataObserver) ObserveEventLogReconciliation(context.Context, session.PersistedEventLogReconciliation) error {
	return nil
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
	return newRealSessionRetargetFixtureWithLifecycle(t, useBlockingObserver, nil)
}

func newRealSessionRetargetFixtureWithLifecycle(
	t *testing.T,
	useBlockingObserver bool,
	lifecycle sessionruntime.AgentResourceLifecycle,
) realSessionRetargetFixture {
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
		PersistenceRoot:   persistenceRoot,
		StoreOptions:      storeOptions,
		ResourceLifecycle: lifecycle,
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

func (f realSessionRetargetFixture) retargeter(metadataSource sessionRetargetMetadata, processes sessionProcessSource) *SessionWorkspaceRetargeter {
	return NewSessionWorkspaceRetargeter(metadataSource, f.authority, f.publisher, processes)
}

func (f realSessionRetargetFixture) openRuntime(t *testing.T) {
	f.openRuntimeWithClient(t, retargetRuntimeClient{})
}

func (f realSessionRetargetFixture) runtimePlan(t *testing.T, client llm.Client) sessionruntime.AgentRuntimePlan {
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
	return plan
}

func (f realSessionRetargetFixture) openRuntimeWithClient(t *testing.T, client llm.Client) *runtime.Engine {
	t.Helper()
	plan := f.runtimePlan(t, client)
	_, err := f.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
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

func (f realSessionRetargetFixture) runtimeAvailable(t *testing.T) bool {
	t.Helper()
	err := f.authority.WithCurrentRuntime(t.Context(), f.childID, func(context.Context, *runtime.Engine) error {
		return nil
	})
	switch {
	case err == nil:
		return true
	case errors.Is(err, serverapi.ErrRuntimeUnavailable):
		return false
	default:
		t.Fatalf("inspect Runtime availability: %v", err)
		return false
	}
}

type retargetRuntimeClient struct{}

func (retargetRuntimeClient) Generate(context.Context, llm.Request, llm.StreamCallbacks) (llm.Response, error) {
	return llm.Response{}, nil
}

func (retargetRuntimeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

type selfRetargetRuntimeClient struct {
	run      func() error
	mu       sync.Mutex
	requests []llm.Request
}

func (c *selfRetargetRuntimeClient) Generate(_ context.Context, request llm.Request, _ llm.StreamCallbacks) (llm.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	index := len(c.requests)
	c.mu.Unlock()
	if index == 1 {
		if err := c.run(); err != nil {
			return llm.Response{}, err
		}
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("scheduled"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		}, nil
	}
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("done"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}, nil
}

func (c *selfRetargetRuntimeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func (c *selfRetargetRuntimeClient) requestCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

type queuedFailureRetargetRuntimeClient struct {
	run           func() error
	scheduled     chan struct{}
	releaseFirst  chan struct{}
	secondStarted chan struct{}
	mu            sync.Mutex
	requests      []llm.Request
}

func (c *queuedFailureRetargetRuntimeClient) Generate(_ context.Context, request llm.Request, _ llm.StreamCallbacks) (llm.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	index := len(c.requests)
	c.mu.Unlock()
	if index == 1 {
		if err := c.run(); err != nil {
			return llm.Response{}, err
		}
		close(c.scheduled)
		<-c.releaseFirst
	} else if index == 2 {
		close(c.secondStarted)
	}
	return llm.Response{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("done"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
		Usage: llm.Usage{WindowTokens: 200000},
	}, nil
}

func (c *queuedFailureRetargetRuntimeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.InferProviderCapabilities("openai")
}

func (c *queuedFailureRetargetRuntimeClient) request(index int) llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests[index]
}

func TestSessionWorkspaceRetargeterSchedulesSelfRebindAtStepBoundary(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	targetProjectID := fixture.targetProject.ProjectID
	request := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	processes := retargetProcessSource{{
		ID:             "still-running",
		OwnerSessionID: request.SessionID,
		Running:        true,
	}}
	reopenPlan := fixture.runtimePlan(t, retargetRuntimeClient{})
	published := make(chan error, 1)
	retargeter := NewSessionWorkspaceRetargeter(
		fixture.metadata,
		fixture.authority,
		retargetIdentityPublisherFunc(func(string) error {
			_, openErr := fixture.authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
				SessionID: fixture.childID,
				OwnerID:   "destination-reopen",
				Runtime:   &reopenPlan,
			})
			published <- openErr
			return nil
		}),
		processes,
	)
	client := &selfRetargetRuntimeClient{}
	engine := fixture.openRuntimeWithClient(t, client)
	client.run = func() error {
		active := engine.ActiveRun()
		if active == nil {
			return errors.New("active Agent Step is required")
		}
		_, err := retargeter.ScheduleWorkspaceRetarget(
			t.Context(),
			request,
			serverapi.RuntimeStepOrigin{RunID: active.RunID, StepID: active.StepID},
			serverapi.NewWorktreeOperationID(),
		)
		return err
	}

	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "move this Session")
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
	if requestCount := client.requestCount(); requestCount != 1 {
		t.Fatalf("provider requests after rebind acknowledgement = %d, want no forced continuation", requestCount)
	}
	select {
	case err := <-published:
		if !errors.Is(err, sessionruntime.ErrSessionStartsBlocked) {
			t.Fatalf("destination reopen during handoff error = %v, want Session start block", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("moved Session identity was not published")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if !fixture.runtimeAvailable(t) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("source Runtime remained active after cross-Project self-rebind")
		}
		time.Sleep(10 * time.Millisecond)
	}
	reopened, err := session.OpenByID(
		fixture.metadata.PersistenceRoot(),
		fixture.childID.String(),
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("open moved Session: %v", err)
	}
	reminder := reopened.Meta().RebindReminder
	if reminder == nil || reminder.Kind != session.SessionRebindReminderSucceeded {
		t.Fatalf("persisted Session rebind reminder = %+v", reminder)
	}
	if reminder.WorkingDirectory == nil ||
		canonicalRetargetTestPath(t, *reminder.WorkingDirectory) != canonicalRetargetTestPath(t, fixture.targetWorkspaceRoot) {
		t.Fatalf("persisted Session rebind Working Directory = %v, want %q", reminder.WorkingDirectory, fixture.targetWorkspaceRoot)
	}
}

func TestSessionWorkspaceRetargeterAllowsSelfRebindWhileHumanSameProjectRebindWaits(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	if _, err := fixture.metadata.AttachWorkspaceToProject(
		context.Background(),
		fixture.sourceBinding.ProjectID,
		fixture.targetWorkspaceRoot,
	); err != nil {
		t.Fatalf("AttachWorkspaceToProject: %v", err)
	}
	request := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
	}
	metadataSource := &secondRetargetPlanSignal{
		sessionRetargetMetadata: fixture.metadata,
		second:                  make(chan struct{}),
	}
	retargeter := fixture.retargeter(metadataSource, retargetProcessSource{})
	invokeSelfRebind := make(chan struct{})
	agentStarted := make(chan struct{})
	client := &queuedFailureRetargetRuntimeClient{
		scheduled:     make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	engine := fixture.openRuntimeWithClient(t, client)
	client.run = func() error {
		close(agentStarted)
		<-invokeSelfRebind
		active := engine.ActiveRun()
		if active == nil {
			return errors.New("active Agent Step is required")
		}
		_, err := retargeter.ScheduleWorkspaceRetarget(
			t.Context(),
			request,
			serverapi.RuntimeStepOrigin{RunID: active.RunID, StepID: active.StepID},
			serverapi.NewWorktreeOperationID(),
		)
		return err
	}

	stepDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "move this Session")
		stepDone <- err
	}()
	select {
	case <-agentStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("active Agent Step did not start")
	}

	humanDone := make(chan error, 1)
	go func() {
		_, err := retargeter.RetargetWorkspace(context.Background(), request)
		humanDone <- err
	}()
	select {
	case <-metadataSource.second:
	case <-time.After(3 * time.Second):
		t.Fatal("human rebind did not reach its current maintenance plan")
	}
	time.Sleep(50 * time.Millisecond)
	close(invokeSelfRebind)
	select {
	case <-client.scheduled:
	case <-time.After(3 * time.Second):
		t.Fatal("self-rebind could not schedule while the human rebind waited")
	}
	close(client.releaseFirst)
	if err := <-stepDone; err != nil {
		t.Fatalf("originating Agent Step: %v", err)
	}
	if err := <-humanDone; err != nil {
		t.Fatalf("human rebind: %v", err)
	}
}

func TestSessionWorkspaceRetargeterKeepsSuccessReminderWhenRuntimeRetirementFails(t *testing.T) {
	retirementErr := errors.New("runtime lifecycle draining failed")
	fixture := newRealSessionRetargetFixtureWithLifecycle(
		t,
		false,
		failingRetargetResourceLifecycle{err: retirementErr},
	)
	targetProjectID := fixture.targetProject.ProjectID
	request := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	retargeter := fixture.retargeter(fixture.metadata, retargetProcessSource{})
	client := &selfRetargetRuntimeClient{}
	engine := fixture.openRuntimeWithClient(t, client)
	client.run = func() error {
		active := engine.ActiveRun()
		if active == nil {
			return errors.New("active Agent Step is required")
		}
		_, err := retargeter.ScheduleWorkspaceRetarget(
			t.Context(),
			request,
			serverapi.RuntimeStepOrigin{RunID: active.RunID, StepID: active.StepID},
			serverapi.NewWorktreeOperationID(),
		)
		return err
	}

	done := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "move this Session")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("originating Agent Step: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("self-rebind did not finish after Runtime retirement error")
	}

	deadline := time.Now().Add(2 * time.Second)
	for fixture.runtimeAvailable(t) {
		if time.Now().After(deadline) {
			t.Fatal("source Runtime remained active after retirement error")
		}
		time.Sleep(10 * time.Millisecond)
	}
	reopened, err := session.OpenByID(
		fixture.metadata.PersistenceRoot(),
		fixture.childID.String(),
		fixture.metadata.AuthoritativeSessionStoreOptions()...,
	)
	if err != nil {
		t.Fatalf("open moved Session: %v", err)
	}
	if reminder := reopened.Meta().RebindReminder; reminder == nil || reminder.Kind != session.SessionRebindReminderSucceeded {
		t.Fatalf("persisted Session rebind reminder = %+v, want committed success", reminder)
	}
}

func TestSessionWorkspaceRetargeterPublishesFailureBeforeQueuedModelWorkResumes(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	targetProjectID := fixture.targetProject.ProjectID
	request := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	applyErr := errors.New("target boundary unavailable")
	retargeter := fixture.retargeter(
		failingSessionRetargetBoundary{Store: fixture.metadata, err: applyErr},
		retargetProcessSource{},
	)
	client := &queuedFailureRetargetRuntimeClient{
		scheduled:     make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	engine := fixture.openRuntimeWithClient(t, client)
	client.run = func() error {
		active := engine.ActiveRun()
		if active == nil {
			return errors.New("active Agent Step is required")
		}
		_, err := retargeter.ScheduleWorkspaceRetarget(
			t.Context(),
			request,
			serverapi.RuntimeStepOrigin{RunID: active.RunID, StepID: active.StepID},
			serverapi.NewWorktreeOperationID(),
		)
		return err
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "move this Session")
		firstDone <- err
	}()
	select {
	case <-client.scheduled:
	case <-time.After(3 * time.Second):
		t.Fatal("self-rebind was not scheduled")
	}
	type queuedResult struct {
		accepted bool
		err      error
	}
	queued := make(chan queuedResult, 1)
	go func() {
		_, accepted, err := engine.QueueUserMessageForActiveRun(
			context.Background(),
			"continue after the failed move",
			nil,
		)
		queued <- queuedResult{accepted: accepted, err: err}
	}()
	close(client.releaseFirst)
	if result := <-queued; result.err != nil || !result.accepted {
		t.Fatalf("queue successor accepted=%t error=%v", result.accepted, result.err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("originating Agent Step: %v", err)
	}
	select {
	case <-client.secondStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("queued user work did not resume")
	}
	requestAfterFailure := client.request(1)
	for _, item := range requestAfterFailure.Items {
		if item.MessageType != nil && *item.MessageType == llm.MessageTypeErrorFeedback {
			return
		}
	}
	messageTypes := make([]string, 0, len(requestAfterFailure.Items))
	for _, item := range requestAfterFailure.Items {
		if item.MessageType == nil {
			messageTypes = append(messageTypes, "<none>")
		} else {
			messageTypes = append(messageTypes, string(*item.MessageType))
		}
	}
	t.Fatalf("queued request lacks rebind failure notice; message types: %v", messageTypes)
}

func TestSessionWorkspaceRetargeterRejectsActiveCrossProjectRuntimeImmediately(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	targetProjectID := fixture.targetProject.ProjectID
	request := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	client := &queuedFailureRetargetRuntimeClient{
		run:           func() error { return nil },
		scheduled:     make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	engine := fixture.openRuntimeWithClient(t, client)
	stepDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "keep this Session active")
		stepDone <- err
	}()
	select {
	case <-client.scheduled:
	case <-time.After(3 * time.Second):
		t.Fatal("active Agent Step did not start")
	}
	defer func() {
		close(client.releaseFirst)
		if err := <-stepDone; err != nil {
			t.Errorf("finish active Agent Step: %v", err)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := fixture.retargeter(fixture.metadata, retargetProcessSource{}).RetargetWorkspace(ctx, request)
	var retargetErr *serverapi.SessionRetargetError
	if !errors.As(err, &retargetErr) || retargetErr.Reason != serverapi.SessionRetargetRuntimeActive {
		t.Fatalf("RetargetWorkspace error = %v, want active-Runtime rejection", err)
	}
}

func TestSessionWorkspaceRetargeterChoosesAdmissionFromCurrentProjectBoundary(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	targetProjectID := fixture.targetProject.ProjectID
	request := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	client := &queuedFailureRetargetRuntimeClient{
		run:           func() error { return nil },
		scheduled:     make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	engine := fixture.openRuntimeWithClient(t, client)
	stepDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "keep this Session active")
		stepDone <- err
	}()
	select {
	case <-client.scheduled:
	case <-time.After(3 * time.Second):
		t.Fatal("active Agent Step did not start")
	}
	defer func() {
		close(client.releaseFirst)
		if err := <-stepDone; err != nil {
			t.Errorf("finish active Agent Step: %v", err)
		}
	}()

	metadataSource := &staleFirstProjectBoundaryMetadata{sessionRetargetMetadata: fixture.metadata}
	retargetDone := make(chan error, 1)
	go func() {
		_, err := fixture.retargeter(metadataSource, retargetProcessSource{}).RetargetWorkspace(context.Background(), request)
		retargetDone <- err
	}()
	select {
	case err := <-retargetDone:
		var retargetErr *serverapi.SessionRetargetError
		if !errors.As(err, &retargetErr) || retargetErr.Reason != serverapi.SessionRetargetRuntimeActive {
			t.Fatalf("RetargetWorkspace error = %v, want active-Runtime rejection", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RetargetWorkspace waited using the stale same-Project classification")
	}
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

	result, err := fixture.retargeter(fixture.metadata, retargetProcessSource{}).RetargetWorkspace(context.Background(), req)
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
	if fixture.runtimeAvailable(t) {
		t.Fatal("cross-Project retarget retained the source Runtime")
	}
}

func TestSessionWorkspaceRetargeterTreatsCommittedIdentityPublicationFailureAsNotificationOnly(t *testing.T) {
	fixture := newRealSessionRetargetFixture(t, false)
	fixture.openRuntime(t)
	targetProjectID := fixture.targetProject.ProjectID
	req := metadata.SessionWorkspaceRetargetRequest{
		SessionID:     fixture.child.Meta().SessionID,
		WorkspaceRoot: fixture.targetWorkspaceRoot,
		ProjectID:     &targetProjectID,
	}
	publicationErr := errors.New("identity projection unavailable")
	retargeter := NewSessionWorkspaceRetargeter(
		fixture.metadata,
		fixture.authority,
		retargetIdentityPublisherFunc(func(string) error { return publicationErr }),
		retargetProcessSource{},
	)

	result, err := retargeter.RetargetWorkspace(context.Background(), req)
	if err != nil {
		t.Fatalf("committed RetargetWorkspace reported notification failure: %v", err)
	}
	if result.Binding.ProjectID != targetProjectID {
		t.Fatalf("target project = %q, want %q", result.Binding.ProjectID, targetProjectID)
	}
	if fixture.runtimeAvailable(t) {
		t.Fatal("cross-Project retarget retained the source Runtime")
	}
}

func TestSessionWorkspaceRetargeterRejectsBackgroundProcessWithoutMovingArtifact(t *testing.T) {
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
	for _, owner := range []string{req.SessionID, ""} {
		process := shelltool.Snapshot{ID: "blocking-process", OwnerSessionID: owner, Running: true}
		_, retargetErr := fixture.retargeter(fixture.metadata, retargetProcessSource{process}).RetargetWorkspace(context.Background(), req)
		var typed *serverapi.SessionRetargetError
		if owner == "" && retargetErr == nil {
			t.Fatal("RetargetWorkspace accepted a running process without owner identity")
		}
		if owner != "" && (!errors.As(retargetErr, &typed) || typed.Reason != serverapi.SessionRetargetBackgroundProcess) {
			t.Fatalf("RetargetWorkspace error = %v, want background-process error", retargetErr)
		}
		if info, statErr := os.Stat(plan.SourceSessionDir); statErr != nil || !info.IsDir() {
			t.Fatalf("source artifact changed: %v, %v", info, statErr)
		}
		if _, statErr := os.Stat(plan.TargetSessionDir); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("target artifact exists: %v", statErr)
		}
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

			result, err := fixture.retargeter(fixture.metadata, retargetProcessSource{}).RetargetWorkspace(context.Background(), req)
			if err != nil {
				t.Fatalf("RetargetWorkspace: %v", err)
			}
			if result.Binding.ProjectID != wantProjectID {
				t.Fatalf("binding project = %q, want %q", result.Binding.ProjectID, wantProjectID)
			}
			if test.crossProject {
				if fixture.runtimeAvailable(t) {
					t.Fatal("cross-Project retarget retained the source Runtime")
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

	result, err := fixture.retargeter(fixture.metadata, retargetProcessSource{}).RetargetWorkspace(context.Background(), req)
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
		result, err := fixture.retargeter(fixture.metadata, retargetProcessSource{}).RetargetWorkspace(context.Background(), req)
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
	if _, err := fixture.retargeter(fixture.metadata, retargetProcessSource{}).RetargetWorkspace(context.Background(), req); err != nil {
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

	_, err = fixture.retargeter(failingSessionRetargetBoundary{Store: fixture.metadata, err: lookupErr}, retargetProcessSource{}).RetargetWorkspace(context.Background(), req)
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

	_, err = fixture.retargeter(metadataSource, retargetProcessSource{}).RetargetWorkspace(context.Background(), req)
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
