package runtimecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"core/prompts"
	"core/server/llm"
	"core/server/metadata"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimewire"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/sessionruntime"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/clientui"
	"core/shared/config"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
)

var runtimeControlOpenAICapabilities = llm.ProviderCapabilities{
	ProviderID:               "openai",
	SupportsResponsesAPI:     true,
	SupportsResponsesCompact: true,
	IsOpenAIFirstParty:       true,
}

type sequenceRuntimeActivityResolver struct {
	snapshots []runtimeactivity.ResponseSnapshot
	calls     int
}

type runtimeControlPromptFeed struct {
	mu            sync.Mutex
	pending       chan struct{}
	resolved      chan struct{}
	pendingCount  int
	resolvedCount int
}

func (f *runtimeControlPromptFeed) PromptPendingScope(
	sessionruntime.ExecutionScope,
	tools.AskQuestionRequest,
	time.Time,
) error {
	f.mu.Lock()
	f.pendingCount++
	f.mu.Unlock()
	select {
	case f.pending <- struct{}{}:
	default:
	}
	return nil
}

func (f *runtimeControlPromptFeed) PromptResolvedScope(sessionruntime.ExecutionScope, string) error {
	f.mu.Lock()
	f.resolvedCount++
	f.mu.Unlock()
	if f.resolved != nil {
		select {
		case f.resolved <- struct{}{}:
		default:
		}
	}
	return nil
}

func (f *runtimeControlPromptFeed) Counts() (pending int, resolved int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pendingCount, f.resolvedCount
}

func (r *sequenceRuntimeActivityResolver) RuntimeReadModelSnapshot(context.Context, string) (runtimeactivity.ResponseSnapshot, error) {
	if r.calls >= len(r.snapshots) {
		return r.snapshots[len(r.snapshots)-1], nil
	}
	snapshot := r.snapshots[r.calls]
	r.calls++
	return snapshot, nil
}

type missingMetadataPersistedSessionResolver struct{}

type runtimeControlPromptCommandResolver struct {
	content  string
	err      error
	calls    atomic.Int32
	resolved chan struct{}
}

func (r *runtimeControlPromptCommandResolver) ResolvePromptCommand(context.Context, string, string, string) (string, error) {
	r.calls.Add(1)
	if r.resolved != nil {
		select {
		case r.resolved <- struct{}{}:
		default:
		}
	}
	if r.err != nil {
		return "", r.err
	}
	return r.content, nil
}

func (missingMetadataPersistedSessionResolver) ResolvePersistedSession(context.Context, string) (session.PersistedSessionRecord, error) {
	return session.PersistedSessionRecord{SessionDir: "/tmp/session"}, nil
}

var runtimeControlPromptHistoryStores sync.Map

func runtimeControlCurrentNodeInstructions() workflowruntime.TaskInstructions {
	return workflowruntime.TaskInstructions{
		CurrentNode: workflow.CurrentNodeReference{
			TaskID: "task-1",
			NodeID: "node-1",
		},
	}
}

func mustQueueRuntimeControlMessage(t *testing.T, engine *runtime.Engine, text string) runtime.QueuedUserMessage {
	t.Helper()
	item, err := engine.QueueUserMessage(text)
	if err != nil {
		t.Fatalf("queue runtime message: %v", err)
	}
	return item
}

type runtimeControlPromptHistoryStore struct {
	mu            sync.Mutex
	records       []metadata.PromptHistoryRecord
	recordErr     error
	recordCtxErr  error
	recordEntered chan struct{}
	recordRelease <-chan struct{}
}

func newRuntimeControlPromptHistoryStore(sessionID string) *runtimeControlPromptHistoryStore {
	store := &runtimeControlPromptHistoryStore{}
	runtimeControlPromptHistoryStores.Store(sessionID, store)
	return store
}

func (s *runtimeControlPromptHistoryStore) RecordPromptHistoryEntry(ctx context.Context, entry metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recordErr != nil {
		return metadata.PromptHistoryRecord{}, s.recordErr
	}
	if s.recordCtxErr != nil && ctx.Err() != nil {
		return metadata.PromptHistoryRecord{}, s.recordCtxErr
	}
	if s.recordEntered != nil {
		close(s.recordEntered)
		s.recordEntered = nil
	}
	if s.recordRelease != nil {
		<-s.recordRelease
	}
	record := metadata.PromptHistoryRecord{
		Sequence:  int64(len(s.records) + 1),
		SessionID: entry.SessionID,
		Text:      entry.Text,
		CreatedAt: entry.CreatedAt,
	}
	s.records = append(s.records, record)
	return record, nil
}

func (s *runtimeControlPromptHistoryStore) SetRecordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordErr = err
}

func (s *runtimeControlPromptHistoryStore) SetRecordContextError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordCtxErr = err
}

func (s *runtimeControlPromptHistoryStore) CountText(text string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, record := range s.records {
		if record.Text == text {
			count++
		}
	}
	return count
}

func waitForRuntimeControlPromptHistoryCount(t *testing.T, store *runtimeControlPromptHistoryStore, text string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if got := store.CountText(text); got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("prompt history count for %q = %d, want %d", text, store.CountText(text), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForRuntimeControlAssistantFinal(t *testing.T, engine *runtime.Engine, text string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		found := false
		if err := engine.WithTranscriptHydrationSnapshot(func(snapshot runtime.TranscriptHydrationSnapshot) error {
			for _, row := range snapshot.CommittedRows {
				if row.Kind == runtime.TranscriptCommittedRowFactAssistant &&
					row.Assistant != nil &&
					row.Assistant.Phase == llm.MessagePhaseFinal &&
					row.Assistant.Text == text {
					found = true
					break
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("read transcript hydration snapshot: %v", err)
		}
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for committed assistant final %q", text)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForRuntimeControlIdle(t *testing.T, engine *runtime.Engine) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for engine.HasActiveLiveRunGroup() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for Runtime execution to finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForRuntimeControlQueuedStatus(
	t *testing.T,
	statuses <-chan runtime.QueuedUserMessageStatusEvent,
	queueItemID string,
	status runtime.QueuedUserMessageStatus,
) runtime.QueuedUserMessageStatusEvent {
	t.Helper()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case event := <-statuses:
			if event.QueueItemID == queueItemID && event.Status == status {
				return event
			}
		case <-timeout.C:
			t.Fatalf("timed out waiting for queue item %q status %q", queueItemID, status)
		}
	}
}

type runtimeControlLiveSteerResult struct {
	response serverapi.RuntimeLiveSteerResponse
	err      error
}

type runtimeControlSubmitUserTurnResult struct {
	response serverapi.RuntimeSubmitUserTurnResponse
	err      error
}

func submitUserTurnRuntimeControlAsync(
	service *Service,
	request serverapi.RuntimeSubmitUserTurnRequest,
) <-chan runtimeControlSubmitUserTurnResult {
	done := make(chan runtimeControlSubmitUserTurnResult, 1)
	go func() {
		response, err := service.SubmitUserTurn(context.Background(), request)
		done <- runtimeControlSubmitUserTurnResult{response: response, err: err}
	}()
	return done
}

func liveSteerRuntimeControlAsync(
	service *Service,
	request serverapi.RuntimeLiveSteerRequest,
) <-chan runtimeControlLiveSteerResult {
	done := make(chan runtimeControlLiveSteerResult, 1)
	go func() {
		response, err := service.LiveSteer(context.Background(), request)
		done <- runtimeControlLiveSteerResult{response: response, err: err}
	}()
	return done
}

func countPromptHistoryEvents(t *testing.T, store *session.Store, text string) int {
	t.Helper()
	registered, ok := runtimeControlPromptHistoryStores.Load(store.Meta().SessionID)
	if !ok {
		return 0
	}
	history, ok := registered.(*runtimeControlPromptHistoryStore)
	if !ok {
		t.Fatalf("prompt history store type = %T, want *runtimeControlPromptHistoryStore", registered)
	}
	return history.CountText(text)
}

type staticRuntimeControlWorkflowTaskResolver struct {
	workflow bool
}

func (r staticRuntimeControlWorkflowTaskResolver) SessionHasWorkflowTask(context.Context, string) (bool, error) {
	return r.workflow, nil
}

type runtimeControlWorkflowSessionReactivatorFunc func(context.Context, runtimeids.SessionID) error

func (f runtimeControlWorkflowSessionReactivatorFunc) ReactivateWorkflowSession(
	ctx context.Context,
	sessionID runtimeids.SessionID,
) error {
	return f(ctx, sessionID)
}

type runtimeControlFakeClient struct {
	mu                  sync.Mutex
	responses           []llm.Response
	compactionResponses []llm.CompactionResponse
	capabilities        llm.ProviderCapabilities
	calls               int
	compactionCalls     int
}

type blockingRuntimeControlClient struct {
	runtimeControlFakeClient
}

func (c *blockingRuntimeControlClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

type blockingCompactionRuntimeControlClient struct {
	runtimeControlFakeClient
	started chan struct{}
	release chan struct{}
}

func (c *blockingCompactionRuntimeControlClient) Compact(ctx context.Context, req llm.CompactionRequest) (llm.CompactionResponse, error) {
	select {
	case <-c.started:
	default:
		close(c.started)
	}
	select {
	case <-c.release:
	case <-ctx.Done():
		return llm.CompactionResponse{}, context.Cause(ctx)
	}
	return c.runtimeControlFakeClient.Compact(ctx, req)
}

type cancelObservingRuntimeControlClient struct {
	started     chan struct{}
	release     chan struct{}
	ctxCanceled chan struct{}
	cancelOnce  sync.Once
}

func newCancelObservingRuntimeControlClient() *cancelObservingRuntimeControlClient {
	return &cancelObservingRuntimeControlClient{
		started:     make(chan struct{}),
		release:     make(chan struct{}),
		ctxCanceled: make(chan struct{}),
	}
}

func (c *cancelObservingRuntimeControlClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = req
	select {
	case <-c.started:
	default:
		close(c.started)
	}
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			c.cancelOnce.Do(func() { close(c.ctxCanceled) })
		}()
	}
	<-c.release
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func (c *cancelObservingRuntimeControlClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{}, nil
}

type restartableRuntimeControlClient struct {
	call1Started chan struct{}
	call2Started chan struct{}
	release1     chan struct{}
	release2     chan struct{}
	release1Once sync.Once
	release2Once sync.Once
	mu           sync.Mutex
	calls        int
}

func newRestartableRuntimeControlClient() *restartableRuntimeControlClient {
	return &restartableRuntimeControlClient{
		call1Started: make(chan struct{}),
		call2Started: make(chan struct{}),
		release1:     make(chan struct{}),
		release2:     make(chan struct{}),
	}
}

func (c *restartableRuntimeControlClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = req
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	switch call {
	case 1:
		close(c.call1Started)
		<-c.release1
	case 2:
		close(c.call2Started)
		<-c.release2
	}
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, nil
}

func (c *restartableRuntimeControlClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{}, nil
}

func (c *restartableRuntimeControlClient) releaseFirst() {
	c.release1Once.Do(func() { close(c.release1) })
}

func (c *restartableRuntimeControlClient) releaseSecond() {
	c.release2Once.Do(func() { close(c.release2) })
}

type steeringDrainRuntimeControlClient struct {
	firstStarted  chan struct{}
	secondStarted chan struct{}
	releaseFirst  chan struct{}
	releaseSecond chan struct{}
	mu            sync.Mutex
	requests      []llm.Request
}

func newSteeringDrainRuntimeControlClient() *steeringDrainRuntimeControlClient {
	return &steeringDrainRuntimeControlClient{
		firstStarted:  make(chan struct{}),
		secondStarted: make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		releaseSecond: make(chan struct{}),
	}
}

func (c *steeringDrainRuntimeControlClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	call := len(c.requests)
	c.mu.Unlock()
	switch call {
	case 1:
		close(c.firstStarted)
		select {
		case <-c.releaseFirst:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("still working"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{{
				ID:    "call-boundary",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"command":"printf tool-boundary"}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		}, nil
	case 2:
		close(c.secondStarted)
		select {
		case <-c.releaseSecond:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return llm.Response{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		}, nil
	default:
		return llm.Response{}, errors.New("unexpected model request after final response")
	}
}

func (c *steeringDrainRuntimeControlClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return llm.ProviderCapabilities{}, nil
}

func (c *steeringDrainRuntimeControlClient) request(index int) llm.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests[index]
}

type fakeShellHandler struct{}

func (fakeShellHandler) Call(_ context.Context, call tools.Call) (tools.Result, error) {
	return tools.Result{
		CallID: call.ID,
		Name:   call.Name,
		Output: json.RawMessage(`{"output":"ok","exit_code":0,"truncated":false}`),
	}, nil
}

var runtimeControlTestSessionPersistence = sessiontest.NewPersistence()

func newRuntimeControlTestEngine(t *testing.T, client llm.Client, registry *tools.Registry, cfg runtime.Config, opts ...session.StoreOption) (*session.Store, *runtime.Engine) {
	t.Helper()
	workspace := t.TempDir()
	store, err := session.Create(
		t.TempDir(),
		"workspace-x",
		workspace,
		sessioncontract.SessionCategoryMain,
		append(runtimeControlTestSessionPersistence.Options(), opts...)...,
	)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	if client == nil {
		client = &runtimeControlFakeClient{}
	}
	if registry == nil {
		registry = tools.NewRegistry()
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-5"
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	engine, err := runtime.New(store, eventLog, client, registry, cfg)
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return store, engine
}

func runtimeControlExactExecution(t *testing.T) *workflowruntime.CurrentNodeExecutionConfig {
	t.Helper()
	reference, err := workflow.NewCurrentNodeReference("runtime-control-test-task", "runtime-control-test-node", nil)
	if err != nil {
		t.Fatalf("create runtime-control Current Node reference: %v", err)
	}
	return &workflowruntime.CurrentNodeExecutionConfig{
		ScopeID: runtimeids.NewExecutionScopeID(),
		Instructions: workflowruntime.TaskInstructions{
			CurrentNode: reference,
		},
	}
}

func newRuntimeControlTestService(t *testing.T, client llm.Client, registry *tools.Registry, cfg runtime.Config, opts ...session.StoreOption) (*session.Store, *runtime.Engine, *Service) {
	return newRuntimeControlTestServiceWithFeeds(t, client, registry, cfg, nil, nil, opts...)
}

func newRuntimeControlTestServiceWithEventFeed(
	t *testing.T,
	client llm.Client,
	registry *tools.Registry,
	cfg runtime.Config,
	eventFeed sessionruntime.AgentResourceEventFeed,
	opts ...session.StoreOption,
) (*session.Store, *runtime.Engine, *Service) {
	return newRuntimeControlTestServiceWithFeeds(t, client, registry, cfg, eventFeed, nil, opts...)
}

func newRuntimeControlTestServiceWithFeeds(
	t *testing.T,
	client llm.Client,
	registry *tools.Registry,
	cfg runtime.Config,
	eventFeed sessionruntime.AgentResourceEventFeed,
	promptFeed sessionruntime.ExecutionPromptFeed,
	opts ...session.StoreOption,
) (*session.Store, *runtime.Engine, *Service) {
	t.Helper()
	store, _ := newRuntimeControlTestEngine(t, client, registry, cfg, opts...)
	if client == nil {
		client = &runtimeControlFakeClient{}
	}
	settings := config.DefaultOnboardingSettings()
	settings.ProviderOverride = "openai"
	settings.Reviewer.Frequency = "off"
	settings.CompactionMode = config.CompactionModeNative
	if cfg.Model != "" {
		settings.Model = cfg.Model
	}
	enabledTools := append([]toolspec.ID(nil), cfg.EnabledTools...)
	if registry != nil {
		if _, ok := registry.Get(toolspec.ToolExecCommand); ok {
			found := false
			for _, id := range enabledTools {
				found = found || id == toolspec.ToolExecCommand
			}
			if !found {
				enabledTools = append(enabledTools, toolspec.ToolExecCommand)
			}
		}
	}
	var reviewerClientFactory runtimewire.RuntimeClientFactory
	if cfg.Reviewer.ClientFactory != nil {
		reviewerClientFactory = runtimewire.RuntimeClientFactoryFunc(func(context.Context, runtimewire.RuntimeClientRequest) (llm.Client, error) {
			return cfg.Reviewer.ClientFactory()
		})
	}
	plan, err := sessionruntime.NewAgentRuntimePlan(sessionruntime.AgentRuntimePlanOptions{
		Settings:              settings,
		EnabledTools:          enabledTools,
		QuestionsEnabled:      textutil.Value(true),
		AutoCompactionEnabled: textutil.Value(true),
		FilesystemContext: func() tools.FilesystemContext {
			context, err := runtimewire.NewFilesystemContext(store.Meta().WorkspaceRoot, store.Meta().WorkspaceRoot, metadata.ProjectWorkspaceBoundary{ProjectID: "test"})
			if err != nil {
				t.Fatalf("NewFilesystemContext: %v", err)
			}
			return context
		}(),
		Client:                       client,
		ReviewerClientFactory:        reviewerClientFactory,
		ProviderCapabilitiesOverride: cfg.ProviderCapabilitiesOverride,
		OnEvent:                      cfg.OnEvent,
	})
	if err != nil {
		t.Fatalf("new authority runtime plan: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: t.TempDir(),
		StoreOptions:    append(runtimeControlTestSessionPersistence.Options(), opts...),
		EventFeed:       eventFeed,
		PromptFeed:      promptFeed,
	})
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	if _, err := authority.OpenRuntime(context.Background(), sessionruntime.RuntimeOpenRequest{
		SessionID: sessionID,
		OwnerID:   "runtimecontrol-test",
		Runtime:   &plan,
	}); err != nil {
		t.Fatalf("open authority runtime: %v", err)
	}
	t.Cleanup(func() {
		if err := authority.Close(context.Background()); err != nil {
			t.Errorf("close authority: %v", err)
		}
	})
	var engine *runtime.Engine
	if err := authority.WithCurrentRuntime(context.Background(), sessionID, func(_ context.Context, current *runtime.Engine) error {
		engine = current
		return nil
	}); err != nil {
		t.Fatalf("resolve authority runtime: %v", err)
	}
	descriptor, err := session.NewOpenSessionDescriptor(sessionID)
	if err != nil {
		t.Fatalf("new session descriptor: %v", err)
	}
	if err := authority.WithSessionStore(context.Background(), descriptor, func(_ context.Context, current *session.Store) error {
		store = current
		return nil
	}); err != nil {
		t.Fatalf("resolve authority session store: %v", err)
	}
	history := newRuntimeControlPromptHistoryStore(store.Meta().SessionID)
	service := NewService(authority).
		WithPromptHistoryStore(history).
		WithPersistedSessionResolver(runtimeControlTestSessionPersistence)
	return store, engine, service
}

func newRuntimeControlWorkflowTestService(
	t *testing.T,
	client llm.Client,
	registry *tools.Registry,
	cfg runtime.Config,
	execution *workflowruntime.CurrentNodeExecutionConfig,
) (*session.Store, *runtime.Engine, *Service) {
	t.Helper()
	store, engine, service := newRuntimeControlTestService(t, client, registry, cfg)
	publication, err := engine.PrepareCurrentNodeExecutionPublication(execution)
	if err != nil {
		t.Fatalf("prepare Current Node execution publication: %v", err)
	}
	if err := publication.Begin(); err != nil {
		t.Fatalf("begin Current Node execution publication: %v", err)
	}
	binding := publication.Commit()
	t.Cleanup(func() {
		if err := binding.Close(); err != nil && !errors.Is(err, runtime.ErrEngineClosed) {
			t.Errorf("close Current Node execution binding: %v", err)
		}
	})
	return store, engine, service
}

func finalResponseRuntimeControlClient() *runtimeControlFakeClient {
	return &runtimeControlFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
}

func TestServiceSubmitUserTurnReactivatesRetainedWorkflowSessionBeforeSubmitting(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(
		t,
		finalResponseRuntimeControlClient(),
		nil,
		runtime.Config{Model: "gpt-5"},
	)
	config := runtimeControlExactExecution(t)
	workflowRef := sessionruntime.WorkflowExecutionRef{
		ProjectID:   "runtime-control-test-project",
		WorkflowID:  runtimeids.NewWorkflowID(),
		CurrentNode: config.Instructions.CurrentNode,
	}
	config.Instructions.WorkflowID = workflowRef.WorkflowID
	publication, err := engine.PrepareCurrentNodeExecutionPublication(config)
	if err != nil {
		t.Fatalf("prepare retained Workflow activation: %v", err)
	}
	if err := publication.Begin(); err != nil {
		t.Fatalf("begin retained Workflow activation: %v", err)
	}
	binding := publication.Commit()
	bindingClosed := false
	var execution sessionruntime.ExecutionHandle
	t.Cleanup(func() {
		if execution != nil {
			execution.RequestStop()
			_ = execution.Close(context.Background())
		}
		if !bindingClosed {
			_ = binding.Close()
		}
	})
	sessionID, err := runtimeids.ParseSessionID(store.Meta().SessionID)
	if err != nil {
		t.Fatalf("parse session id: %v", err)
	}
	reactivations := 0
	service.WithWorkflowSessionReactivator(runtimeControlWorkflowSessionReactivatorFunc(
		func(_ context.Context, got runtimeids.SessionID) error {
			reactivations++
			if got != sessionID {
				t.Fatalf("reactivated session = %s, want %s", got, sessionID)
			}
			bindingClosed = true
			if err := binding.Close(); err != nil {
				return err
			}
			descriptor, err := session.NewOpenSessionDescriptor(sessionID)
			if err != nil {
				return err
			}
			detached, err := service.authority.PrepareDetachedAgentExecution(
				context.Background(),
				sessionruntime.DetachedAgentExecutionRequest{
					Descriptor: descriptor,
					Workflow:   workflowRef,
					Resource:   sessionruntime.CurrentAgentResource{},
					Config:     config,
					Runner: func(ctx context.Context, _ sessionruntime.ExecutionScope, _ sessionruntime.AgentRuntimeBridge) error {
						<-ctx.Done()
						return context.Cause(ctx)
					},
				},
			)
			if err != nil {
				return err
			}
			handle, launch, err := detached.Publish(context.Background(), func() error { return nil }, nil)
			if err != nil {
				return err
			}
			execution = handle
			launch()
			return nil
		},
	))

	response, err := service.SubmitUserTurn(
		context.Background(),
		runtimeControlUserTurnRequest(store, "reactivate-retained", "continue"),
	)
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if reactivations != 1 {
		t.Fatalf("workflow reactivations = %d, want 1", reactivations)
	}
	if response.ResultKind != clientui.UserTurnResultKindQueued || response.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn response = %+v, want queued acceptance", response)
	}
	if !engine.HasQueuedUserWork() {
		t.Fatal("original input was not accepted by the reactivated Workflow execution")
	}
}

func (c *runtimeControlFakeClient) Generate(context.Context, llm.Request) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if len(c.responses) == 0 {
		return llm.Response{}, nil
	}
	resp := c.responses[0]
	c.responses = c.responses[1:]
	return resp, nil
}

func (c *runtimeControlFakeClient) Compact(context.Context, llm.CompactionRequest) (llm.CompactionResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.compactionCalls++
	if len(c.compactionResponses) == 0 {
		return llm.CompactionResponse{}, nil
	}
	resp := c.compactionResponses[0]
	c.compactionResponses = c.compactionResponses[1:]
	return resp, nil
}

func (c *runtimeControlFakeClient) ProviderCapabilities(context.Context) (llm.ProviderCapabilities, error) {
	return c.capabilities, nil
}

func TestServiceSubmitUserTurnStillCancelsOnExplicitInterrupt(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	response, err := service.SubmitUserTurn(
		context.Background(),
		runtimeControlUserTurnRequest(store, "req-interrupt", "hello"),
	)
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if response.ResultKind != clientui.UserTurnResultKindQueued || response.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn response = %+v, want queued acceptance", response)
	}
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for submit to start")
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	select {
	case <-client.ctxCanceled:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime context was not canceled by explicit interrupt")
	}
	close(client.release)
	waitForRuntimeControlIdle(t, engine)
}

func TestServicePendingQuestionInterruptsAndAllowsNextTurn(t *testing.T) {
	toolStarted := make(chan string, 1)
	client := &runtimeControlFakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant},
			ToolCalls: []llm.ToolCall{{
				ID:    "ask-cancel",
				Name:  string(toolspec.ToolAskQuestion),
				Input: json.RawMessage(`{"question":"Continue?"}`),
			},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("next turn completed"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	promptFeed := &runtimeControlPromptFeed{
		pending:  make(chan struct{}, 1),
		resolved: make(chan struct{}, 1),
	}
	store, engine, service := newRuntimeControlTestServiceWithFeeds(t, client, nil, runtime.Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
		OnEvent: func(event runtime.Event) {
			if event.Kind != runtime.EventToolCallStarted || event.ToolCall == nil || event.ToolCall.ID != "ask-cancel" {
				return
			}
			if event.StepID == nil {
				t.Fatal("tool start event is missing its Step identity")
			}
			select {
			case toolStarted <- *event.StepID:
			default:
			}
		},
	}, nil, promptFeed)

	firstRequest := runtimeControlUserTurnRequest(store, "ask-cancel", "ask then cancel")
	first, err := service.SubmitUserTurn(context.Background(), firstRequest)
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if first.ResultKind != clientui.UserTurnResultKindQueued || first.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn response = %+v, want queued acceptance", first)
	}
	select {
	case rawStepID := <-toolStarted:
		if _, err := runtimeids.ParseStepID(rawStepID); err != nil {
			t.Fatalf("parse prompt step id: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ask_question to start")
	}
	select {
	case <-promptFeed.pending:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for pending Question publication")
	}
	if _, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		SessionID: store.Meta().SessionID,
	}); err != nil {
		t.Fatalf("Interrupt pending Question: %v", err)
	}
	next, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "after-ask-cancel", "next user message"))
	if err != nil {
		t.Fatalf("submit next user turn after interrupted ask_question: %v", err)
	}
	if next.ResultKind != clientui.UserTurnResultKindQueued || next.QueueItemID == "" {
		t.Fatalf("next user turn response = %+v, want queued acceptance", next)
	}
	select {
	case <-promptFeed.resolved:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for interrupted Question to disappear")
	}
	waitForRuntimeControlAssistantFinal(t, engine, "next turn completed")
	waitForRuntimeControlIdle(t, engine)
	if engine.HasActiveLiveRunGroup() {
		t.Fatal("interrupted ask_question turn retained live-run ownership after next user turn")
	}
	pendingPrompts, resolvedPrompts := promptFeed.Counts()
	if pendingPrompts != 1 || resolvedPrompts != 1 {
		t.Fatalf("Question publications = (%d pending, %d resolved), want exactly one of each", pendingPrompts, resolvedPrompts)
	}
	nextUserMessageCount := 0
	finalResponseCount := 0
	var finalResponseText string
	if err := engine.WithTranscriptHydrationSnapshot(func(snapshot runtime.TranscriptHydrationSnapshot) error {
		for _, row := range snapshot.CommittedRows {
			if row.Kind == runtime.TranscriptCommittedRowFactUser && row.User != nil && row.User.Text == "next user message" {
				nextUserMessageCount++
			}
			if row.Kind == runtime.TranscriptCommittedRowFactAssistant &&
				row.Assistant != nil &&
				row.Assistant.Phase == llm.MessagePhaseFinal {
				finalResponseCount++
				finalResponseText = row.Assistant.Text
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read transcript hydration snapshot: %v", err)
	}
	if nextUserMessageCount != 1 {
		t.Fatalf("next user message rows = %d, want exactly 1", nextUserMessageCount)
	}
	if finalResponseCount != 1 || finalResponseText != "next turn completed" {
		t.Fatalf("final response rows/text = %d/%q, want exactly one next-turn response", finalResponseCount, finalResponseText)
	}
}

func TestServiceInterruptWithoutEngineIsNotAccepted(t *testing.T) {
	service := NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{}))
	_, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		SessionID: "018fdd67-89ab-4cde-8123-456789abcdef",
	})
	if !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
		t.Fatalf("Interrupt without engine error = %v, want not accepted", err)
	}
}

type failingRuntimeActivityResolver struct {
	err             error
	observedContext context.Context
}

func (r *failingRuntimeActivityResolver) RuntimeReadModelSnapshot(ctx context.Context, _ string) (runtimeactivity.ResponseSnapshot, error) {
	r.observedContext = ctx
	return runtimeactivity.ResponseSnapshot{}, r.err
}

func TestServiceInterruptReturnsDiagnosticActivityWhenPostInterruptSnapshotFails(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(client.release) }) }
	defer release()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	snapshotErr := errors.New("activity snapshot failed")
	resolver := &failingRuntimeActivityResolver{err: snapshotErr}
	service.WithRuntimeActivityResolver(resolver)

	submission, err := service.SubmitUserTurn(
		context.Background(),
		runtimeControlUserTurnRequest(store, "interrupt-snapshot-failure", "keep running"),
	)
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if submission.ResultKind != clientui.UserTurnResultKindQueued || submission.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn response = %+v, want queued acceptance", submission)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("active turn did not reach model thinking")
	}

	type interruptContextKey struct{}
	interruptCtx := context.WithValue(context.Background(), interruptContextKey{}, "interrupt-context")
	resp, err := service.Interrupt(interruptCtx, serverapi.RuntimeInterruptRequest{
		SessionID: store.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if err := resp.Version.Validate(); err != nil {
		t.Fatalf("fallback version: %v", err)
	}
	if resp.Activity.State != clientui.RuntimeActivityUnavailable || !resp.Activity.DiagnosticRecovery {
		t.Fatalf("fallback activity = %+v, want diagnostic unavailable", resp.Activity)
	}
	if resolver.observedContext == nil || resolver.observedContext.Value(interruptContextKey{}) != "interrupt-context" {
		t.Fatal("post-interrupt snapshot did not receive the caller context")
	}
	release()
	waitForRuntimeControlIdle(t, engine)
}

func TestServiceLiveSteerRequiresActiveRun(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	_, err := service.LiveSteer(context.Background(), serverapi.RuntimeLiveSteerRequest{
		SessionID: store.Meta().SessionID,
		Text:      "steer while idle",
	})
	if !errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
		t.Fatalf("LiveSteer idle error = %v, want ErrRuntimeNoActiveRun", err)
	}
	if countPromptHistoryEvents(t, store, "steer while idle") != 0 {
		t.Fatal("idle LiveSteer recorded prompt history")
	}
}

func TestServiceLiveSteerUnavailableRuntimeStaysUnavailable(t *testing.T) {
	service := NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{}))
	_, err := service.LiveSteer(context.Background(), serverapi.RuntimeLiveSteerRequest{
		SessionID: "018fdd67-89ab-4cde-8123-456789abcdef",
		Text:      "steer closed runtime",
	})
	if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("LiveSteer unavailable runtime error = %v, want ErrRuntimeUnavailable", err)
	}
	if errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
		t.Fatalf("LiveSteer unavailable runtime also returned ErrRuntimeNoActiveRun: %v", err)
	}
}

func TestServiceLiveWaitUnavailableRuntimeStaysUnavailable(t *testing.T) {
	service := NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{}))
	_, err := service.LiveWait(context.Background(), serverapi.RuntimeLiveWaitRequest{
		SessionID: "018fdd67-89ab-4cde-8123-456789abcdef",
	})
	if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("LiveWait unavailable runtime error = %v, want ErrRuntimeUnavailable", err)
	}
	if errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
		t.Fatalf("LiveWait unavailable runtime also returned ErrRuntimeNoActiveRun: %v", err)
	}
}

func TestServiceLiveSteerRecordsHistoryAfterActiveAdmission(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	submission, err := service.SubmitUserTurn(
		context.Background(),
		runtimeControlUserTurnRequest(store, "keep-running", "keep running"),
	)
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if submission.ResultKind != clientui.UserTurnResultKindQueued || submission.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn response = %+v, want queued acceptance", submission)
	}
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active runtime")
	}
	steerDone := liveSteerRuntimeControlAsync(service, serverapi.RuntimeLiveSteerRequest{
		SessionID: store.Meta().SessionID,
		Text:      " steer live ",
	})
	select {
	case result := <-steerDone:
		t.Fatalf("LiveSteer completed before the protected Step boundary: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	close(client.release)
	result := <-steerDone
	if result.err != nil {
		t.Fatalf("LiveSteer: %v", result.err)
	}
	resp := result.response
	if resp.QueueItemID == "" || resp.Text != "steer live" {
		t.Fatalf("LiveSteer response = %+v", resp)
	}
	waitForRuntimeControlPromptHistoryCount(t, runtimeControlPromptHistoryStoresLoad(t, store.Meta().SessionID), "steer live", 1)
	waitForRuntimeControlAssistantFinal(t, engine, "done")
	waitForRuntimeControlIdle(t, engine)
}

func TestServiceLiveSteerAgentCallerUsesOneWrappedDeveloperMessage(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	submission, err := service.SubmitUserTurn(
		context.Background(),
		runtimeControlUserTurnRequest(store, "keep-running-agent", "keep running"),
	)
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if submission.ResultKind != clientui.UserTurnResultKindQueued || submission.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn response = %+v, want queued acceptance", submission)
	}
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active runtime")
	}
	sourceID := runtimeids.NewSessionID()
	steer, err := runtime.NewAgentSteer(sourceID, "steer live")
	if err != nil {
		t.Fatalf("NewAgentSteer: %v", err)
	}
	sourceText := sourceID.String()
	steerDone := liveSteerRuntimeControlAsync(service, serverapi.RuntimeLiveSteerRequest{
		SessionID:       store.Meta().SessionID,
		CallerSessionID: &sourceText,
		Text:            "steer live",
	})
	select {
	case result := <-steerDone:
		t.Fatalf("LiveSteer completed before the protected Step boundary: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	close(client.release)
	result := <-steerDone
	if result.err != nil {
		t.Fatalf("LiveSteer: %v", result.err)
	}
	resp := result.response
	if steer.Message().Content == nil || resp.Text != *steer.Message().Content {
		t.Fatalf("LiveSteer response = %+v, want wrapped text", resp)
	}
	waitForRuntimeControlPromptHistoryCount(t, runtimeControlPromptHistoryStoresLoad(t, store.Meta().SessionID), *steer.Message().Content, 1)
	waitForRuntimeControlAssistantFinal(t, engine, "done")
	waitForRuntimeControlIdle(t, engine)
}

func TestServiceLiveSteerReportsAdmittedPromptHistoryErrorDiagnostically(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	diagnostics := make(chan runtime.Event, 1)
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{
		OnEvent: func(event runtime.Event) {
			if event.Kind == runtime.EventPromptHistoryPersistFailed {
				diagnostics <- event
			}
		},
	})
	submission, err := service.SubmitUserTurn(
		context.Background(),
		runtimeControlUserTurnRequest(store, "keep-running", "keep running"),
	)
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if submission.ResultKind != clientui.UserTurnResultKindQueued || submission.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn response = %+v, want queued acceptance", submission)
	}
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active runtime")
	}
	historyErr := errors.New("prompt history failed")
	runtimeControlPromptHistoryStoresLoad(t, store.Meta().SessionID).SetRecordError(historyErr)
	steerDone := liveSteerRuntimeControlAsync(service, serverapi.RuntimeLiveSteerRequest{
		SessionID: store.Meta().SessionID,
		Text:      "steer live",
	})
	select {
	case result := <-steerDone:
		t.Fatalf("LiveSteer completed before the protected Step boundary: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	close(client.release)
	result := <-steerDone
	if result.err != nil {
		t.Fatalf("LiveSteer after accepted prompt-history failure: %v", result.err)
	}
	resp := result.response
	if resp.QueueItemID == "" || resp.Text != "steer live" {
		t.Fatalf("LiveSteer response = %+v, want accepted Queue item", resp)
	}
	select {
	case diagnostic := <-diagnostics:
		if diagnostic.Error != historyErr.Error() {
			t.Fatalf("prompt-history diagnostic = %q, want %q", diagnostic.Error, historyErr)
		}
	case <-time.After(time.Second):
		t.Fatal("LiveSteer did not report prompt-history failure diagnostically")
	}
	waitForRuntimeControlAssistantFinal(t, engine, "done")
	waitForRuntimeControlIdle(t, engine)
}

func TestServiceLiveStopIdleReturnsIdle(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	resp, err := service.LiveStop(context.Background(), serverapi.RuntimeLiveStopRequest{
		SessionID: store.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("LiveStop idle: %v", err)
	}
	if resp.Status != serverapi.RuntimeLiveStopStatusIdle {
		t.Fatalf("LiveStop idle status = %q", resp.Status)
	}
}

func runtimeControlPromptHistoryStoresLoad(t *testing.T, sessionID string) *runtimeControlPromptHistoryStore {
	t.Helper()
	registered, ok := runtimeControlPromptHistoryStores.Load(sessionID)
	if !ok {
		t.Fatalf("prompt history store for %q not registered", sessionID)
	}
	store, ok := registered.(*runtimeControlPromptHistoryStore)
	if !ok {
		t.Fatalf("prompt history store type = %T", registered)
	}
	return store
}

func TestServiceInterruptIdleIsNotAccepted(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	service.WithRuntimeActivityResolver(&sequenceRuntimeActivityResolver{
		snapshots: []runtimeactivity.ResponseSnapshot{{
			Version: clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 1},
			Activity: clientui.RuntimeActivity{
				State:          clientui.RuntimeActivityRegisteredIdle,
				Reviewer:       clientui.ReviewerActivityInactive,
				QueueAccepting: true,
			},
		}},
	})
	_, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		SessionID: store.Meta().SessionID,
	})
	if !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
		t.Fatalf("Interrupt idle error = %v, want runtime command not accepted", err)
	}
}

func TestServiceGoalMutationsSetShowComplete(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})

	setResp, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		SessionID: store.Meta().SessionID,
		Objective: "ship goal mode",
		Actor:     "user",
	})
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if setResp.Goal == nil || setResp.Goal.Objective != "ship goal mode" || setResp.Goal.Status != "active" {
		t.Fatalf("set goal response = %+v", setResp.Goal)
	}
	showResp, err := service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("ShowGoal: %v", err)
	}
	if showResp.Goal == nil || showResp.Goal.ID != setResp.Goal.ID {
		t.Fatalf("show goal response = %+v, want id %q", showResp.Goal, setResp.Goal.ID)
	}
	completeResp, err := service.CompleteGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{
		SessionID: store.Meta().SessionID,
		Actor:     "agent",
	})
	if err != nil {
		t.Fatalf("CompleteGoal: %v", err)
	}
	if completeResp.Goal == nil || completeResp.Goal.Status != "complete" {
		t.Fatalf("complete goal response = %+v", completeResp.Goal)
	}
}

func TestServiceShowGoalReturnsPersistedGoalWithoutRuntime(t *testing.T) {
	store, _ := newRuntimeControlTestEngine(t, nil, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	goal, _, err := store.SetGoal("inspect dormant goals", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	goal, _, _, err = store.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoalStatus: %v", err)
	}
	sessionID := store.Meta().SessionID
	service := NewService(nil).WithPersistedSessionResolver(runtimeControlTestSessionPersistence)

	resp, err := service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("ShowGoal: %v", err)
	}
	if resp.Goal == nil {
		t.Fatal("ShowGoal goal = nil, want persisted goal")
	}
	if resp.Goal.ID != goal.ID ||
		resp.Goal.Objective != goal.Objective ||
		resp.Goal.Status != string(goal.Status) ||
		!resp.Goal.CreatedAt.Equal(goal.CreatedAt) ||
		!resp.Goal.UpdatedAt.Equal(goal.UpdatedAt) {
		t.Fatalf("ShowGoal goal = %+v, want %+v", resp.Goal, goal)
	}
}

func TestServiceShowGoalReturnsEmptyResponseForPersistedSessionWithoutGoal(t *testing.T) {
	store, _ := newRuntimeControlTestEngine(t, nil, nil, runtime.Config{})
	sessionID := store.Meta().SessionID
	service := NewService(nil).WithPersistedSessionResolver(runtimeControlTestSessionPersistence)

	resp, err := service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("ShowGoal: %v", err)
	}
	if resp.Goal != nil {
		t.Fatalf("ShowGoal goal = %+v, want nil", resp.Goal)
	}
}

func TestServiceShowGoalReturnsPersistedSessionResolutionFailures(t *testing.T) {
	const sessionID = "64ee658a-138d-47d6-8624-e25aebcb7a5a"
	tests := []struct {
		name    string
		service *Service
		wantIs  error
	}{
		{
			name:    "resolver required",
			service: NewService(nil),
		},
		{
			name: "resolver failure",
			service: NewService(nil).
				WithPersistedSessionResolver(runtimeControlTestSessionPersistence),
			wantIs: session.ErrSessionNotFound,
		},
		{
			name: "metadata required",
			service: NewService(nil).
				WithPersistedSessionResolver(missingMetadataPersistedSessionResolver{}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := tt.service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: sessionID})
			if err == nil {
				t.Fatal("ShowGoal error = nil, want failure")
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Fatalf("ShowGoal error = %v, want wrapping %v", err, tt.wantIs)
			}
			if resp.Goal != nil {
				t.Fatalf("ShowGoal goal = %+v, want nil on failure", resp.Goal)
			}
		})
	}
}

func TestServiceShowGoalReturnsCommittedStateAroundQueuedGoalDrain(t *testing.T) {
	client := newRestartableRuntimeControlClient()
	defer client.releaseFirst()
	defer client.releaseSecond()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	initialGoal, err := engine.SetGoal("committed before active step", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	submission, err := service.SubmitUserTurn(
		context.Background(),
		runtimeControlUserTurnRequest(store, "turn-1", "work"),
	)
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if submission.ResultKind != clientui.UserTurnResultKindQueued || submission.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn response = %+v, want queued acceptance", submission)
	}
	select {
	case <-client.call1Started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active model step")
	}

	type goalResult struct {
		response serverapi.RuntimeGoalShowResponse
		err      error
	}
	goalDone := make(chan goalResult, 1)
	go func() {
		response, goalErr := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
			SessionID: store.Meta().SessionID,
			Objective: "accepted pending goal",
			Actor:     string(session.GoalActorUser),
		})
		goalDone <- goalResult{response: response, err: goalErr}
	}()
	select {
	case result := <-goalDone:
		t.Fatalf("SetGoal completed before the protected Step boundary: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}

	beforeDrain, err := service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("ShowGoal before drain: %v", err)
	}
	if beforeDrain.Goal == nil || beforeDrain.Goal.ID != initialGoal.ID || beforeDrain.Goal.Objective != initialGoal.Objective {
		t.Fatalf("ShowGoal before drain = %+v, want prior committed goal %+v", beforeDrain.Goal, initialGoal)
	}

	client.releaseFirst()
	var accepted serverapi.RuntimeGoalShowResponse
	select {
	case result := <-goalDone:
		if result.err != nil {
			t.Fatalf("SetGoal queued mutation: %v", result.err)
		}
		accepted = result.response
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Goal mutation at the Step boundary")
	}
	if accepted.Goal == nil || accepted.Goal.Objective != "accepted pending goal" || accepted.Goal.Status != string(session.GoalStatusActive) {
		t.Fatalf("SetGoal accepted response = %+v, want active pending goal", accepted.Goal)
	}

	afterDrain, err := service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("ShowGoal after drain: %v", err)
	}
	if afterDrain.Goal == nil || afterDrain.Goal.Objective != accepted.Goal.Objective || afterDrain.Goal.Status != accepted.Goal.Status {
		t.Fatalf("ShowGoal after drain = %+v, want committed accepted goal %+v", afterDrain.Goal, accepted.Goal)
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt Goal loop: %v", err)
	}
	client.releaseSecond()
}

func TestServiceWorkflowRuntimeAllowsGoalControl(t *testing.T) {
	store, engine, service := newRuntimeControlWorkflowTestService(
		t, nil, nil,
		runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}},
		runtimeControlExactExecution(t),
	)
	engine.SetQuestionsEnabled(false)
	resp, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		SessionID: store.Meta().SessionID,
		Objective: "steer the workflow",
		Actor:     "user",
	})
	if err != nil {
		t.Fatalf("SetGoal in workflow run = %v, want allowed", err)
	}
	if resp.Goal == nil || resp.Goal.Status != string(session.GoalStatusActive) {
		t.Fatalf("goal response = %+v, want active goal", resp.Goal)
	}
	if goal := engine.Goal(); goal == nil || goal.Status != session.GoalStatusActive {
		t.Fatalf("engine goal = %+v, want active", goal)
	}
}

func TestServiceWorkflowAgentStepGoalSetDoesNotBypassStepQueue(t *testing.T) {
	store, engine, service := newRuntimeControlWorkflowTestService(
		t, nil, nil,
		runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}},
		runtimeControlExactExecution(t),
	)

	_, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		SessionID: store.Meta().SessionID,
		Objective: "queued by shell",
		Actor:     string(session.GoalActorAgent),
		StepID:    "step-from-shell",
	})
	if err == nil {
		t.Fatal("agent step-scoped workflow goal set mutated directly without an active step")
	}
	if goal := engine.Goal(); goal != nil {
		t.Fatalf("agent step-scoped workflow goal set bypassed queue and mutated goal: %+v", goal)
	}
}

func TestServiceWorkflowSessionGoalMutationAllowed(t *testing.T) {
	store, _, service := newRuntimeControlWorkflowTestService(
		t, nil, nil,
		runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}},
		runtimeControlExactExecution(t),
	)

	resp, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		SessionID: store.Meta().SessionID,
		Objective: "steer despite the held lease",
		Actor:     "user",
	})
	if err != nil {
		t.Fatalf("SetGoal in workflow runtime = %v, want allowed", err)
	}
	if resp.Goal == nil || resp.Goal.Status != string(session.GoalStatusActive) {
		t.Fatalf("goal response = %+v, want active goal", resp.Goal)
	}
}

func TestServiceWorkflowAgentStepGoalCompleteDoesNotBypassStepQueue(t *testing.T) {
	store, engine, service := newRuntimeControlWorkflowTestService(
		t, nil, nil,
		runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}},
		runtimeControlExactExecution(t),
	)
	sessionID := store.Meta().SessionID
	if _, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{SessionID: sessionID, Objective: "workflow goal", Actor: "user"}); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	_, err := service.CompleteGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{
		SessionID: sessionID,
		Actor:     string(session.GoalActorAgent),
		StepID:    "step-from-shell",
	})
	if err == nil {
		t.Fatal("agent step-scoped workflow goal complete mutated directly without an active step")
	}
	if goal := engine.Goal(); goal == nil || goal.Status != session.GoalStatusActive {
		t.Fatalf("agent step-scoped workflow goal complete bypassed queue; goal = %+v, want active", goal)
	}
}

func TestServiceWorkflowRuntimeAllowsGoalStatusTransitions(t *testing.T) {
	store, engine, service := newRuntimeControlWorkflowTestService(
		t, nil, nil,
		runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}},
		runtimeControlExactExecution(t),
	)
	sessionID := store.Meta().SessionID
	if _, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{SessionID: sessionID, Objective: "workflow goal", Actor: "user"}); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, err := service.PauseGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{SessionID: sessionID, Actor: "user"}); err != nil {
		t.Fatalf("PauseGoal: %v", err)
	}
	if goal := engine.Goal(); goal == nil || goal.Status != session.GoalStatusPaused {
		t.Fatalf("goal after pause = %+v, want paused", goal)
	}
	engine.SetQuestionsEnabled(false)
	if _, err := service.ResumeGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{SessionID: sessionID, Actor: "user"}); err != nil {
		t.Fatalf("ResumeGoal: %v", err)
	}
	if goal := engine.Goal(); goal == nil || goal.Status != session.GoalStatusActive {
		t.Fatalf("goal after resume = %+v, want active", goal)
	}
	if _, err := service.CompleteGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{SessionID: sessionID, Actor: "user"}); err != nil {
		t.Fatalf("CompleteGoal: %v", err)
	}
	if goal := engine.Goal(); goal == nil || goal.Status != session.GoalStatusComplete {
		t.Fatalf("goal after complete = %+v, want complete", goal)
	}
}

func TestServiceDurableWorkflowSessionAllowsGoalControl(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	service = service.WithWorkflowTaskSessionResolver(staticRuntimeControlWorkflowTaskResolver{workflow: true})
	if _, err := service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: store.Meta().SessionID}); err != nil {
		t.Fatalf("ShowGoal for workflow task session = %v, want allowed", err)
	}
}

func TestServiceDurableWorkflowSessionRejectsAutoCompactionDisable(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	service = service.WithWorkflowTaskSessionResolver(staticRuntimeControlWorkflowTaskResolver{workflow: true})

	_, err := service.SetAutoCompactionEnabled(context.Background(), serverapi.RuntimeSetAutoCompactionEnabledRequest{
		SessionID: store.Meta().SessionID,
		Enabled:   false,
	})
	if !errors.Is(err, errWorkflowTaskSessionAutoCompactionDisable) {
		t.Fatalf("SetAutoCompactionEnabled error = %v, want workflow auto-compaction rejection", err)
	}
	if !engine.AutoCompactionEnabled() {
		t.Fatal("auto-compaction disabled despite durable workflow session marker")
	}
}

func TestServiceSetGoalAllowsAgentWithoutExistingGoal(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, &blockingRuntimeControlClient{}, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})

	resp, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		SessionID: store.Meta().SessionID,
		Objective: "agent self-goal",
		Actor:     "agent",
	})
	if err != nil {
		t.Fatalf("SetGoal agent: %v", err)
	}
	if resp.Goal == nil || resp.Goal.Objective != "agent self-goal" || resp.Goal.Status != "active" {
		t.Fatalf("agent set response = %+v", resp.Goal)
	}
}

func TestServiceSetGoalRejectsAgentOverwrite(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status session.GoalStatus
	}{
		{name: "active", status: session.GoalStatusActive},
		{name: "paused", status: session.GoalStatusPaused},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, engine, service := newRuntimeControlTestService(t, &blockingRuntimeControlClient{}, nil, runtime.Config{})
			if _, err := engine.SetGoal("existing goal\n\n- keep markdown", session.GoalActorUser); err != nil {
				t.Fatalf("SetGoal initial: %v", err)
			}
			if tt.status == session.GoalStatusPaused {
				if _, err := engine.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser); err != nil {
					t.Fatalf("SetGoalStatus paused: %v", err)
				}
			}

			_, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
				SessionID: store.Meta().SessionID,
				Objective: "agent replacement",
				Actor:     "agent",
			})
			var denied goalAgentOverwriteDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("agent overwrite error = %v, want goalAgentOverwriteDeniedError", err)
			}
			if denied.Objective != "existing goal\n\n- keep markdown" {
				t.Fatalf("denied objective = %q, want existing goal text", denied.Objective)
			}
			if denied.Status != string(tt.status) {
				t.Fatalf("denied status = %q, want %q", denied.Status, string(tt.status))
			}
			if goal := store.Meta().Goal; goal == nil || goal.Objective != "existing goal\n\n- keep markdown" || goal.Status != tt.status {
				t.Fatalf("goal after rejected overwrite = %+v", goal)
			}
		})
	}
}

func TestServiceSetGoalAllowsAgentAfterCompletedGoal(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, &blockingRuntimeControlClient{}, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	completed, err := engine.SetGoal("completed goal", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal initial: %v", err)
	}
	if _, err := engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorAgent); err != nil {
		t.Fatalf("SetGoalStatus complete: %v", err)
	}
	if goal := store.Meta().Goal; goal == nil || goal.ID != completed.ID || goal.Status != session.GoalStatusComplete {
		t.Fatalf("goal before follow-up set = %+v, want completed goal %q", goal, completed.ID)
	}

	resp, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		SessionID: store.Meta().SessionID,
		Objective: "next goal",
		Actor:     "agent",
	})
	if err != nil {
		t.Fatalf("SetGoal after complete: %v", err)
	}
	if resp.Goal == nil || resp.Goal.Objective != "next goal" || resp.Goal.Status != "active" {
		t.Fatalf("set goal response = %+v", resp.Goal)
	}
	if resp.Goal.ID == completed.ID {
		t.Fatalf("next goal reused completed goal id %q", completed.ID)
	}
	if goal := store.Meta().Goal; goal == nil || goal.ID != resp.Goal.ID || goal.Objective != "next goal" || goal.Status != session.GoalStatusActive {
		t.Fatalf("persisted replacement goal = %+v, want response goal %+v", goal, resp.Goal)
	}
}

func TestServiceSetGoalPropagatesGoalLoopStartError(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})

	_, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		SessionID: store.Meta().SessionID,
		Objective: "ship goal mode",
		Actor:     "user",
	})
	if !errors.Is(err, runtime.ErrGoalRequiresAskQuestion) {
		t.Fatalf("SetGoal error = %v, want ErrGoalRequiresAskQuestion", err)
	}
	if goal := store.Meta().Goal; goal != nil {
		t.Fatalf("goal persisted after failed preflight: %+v", goal)
	}
	events, readErr := sessiontest.CollectRecords(store)
	if readErr != nil {
		t.Fatalf("ReadEvents: %v", readErr)
	}
	if len(events) != 0 {
		t.Fatalf("events persisted after failed preflight: %+v", events)
	}
}

func TestServiceResumeGoalPreflightFailureDoesNotMutateOrEmit(t *testing.T) {
	var events []runtime.Event
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{
		OnEvent: func(evt runtime.Event) {
			events = append(events, evt)
		},
	})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, err := engine.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser); err != nil {
		t.Fatalf("pause goal: %v", err)
	}
	events = nil

	_, err := service.ResumeGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{
		SessionID: store.Meta().SessionID,
		Actor:     "user",
	})
	if !errors.Is(err, runtime.ErrGoalRequiresAskQuestion) {
		t.Fatalf("ResumeGoal error = %v, want ErrGoalRequiresAskQuestion", err)
	}
	if goal := store.Meta().Goal; goal == nil || goal.Status != session.GoalStatusPaused {
		t.Fatalf("goal after failed resume preflight = %+v, want paused", goal)
	}
	if len(events) != 0 {
		t.Fatalf("live events emitted after failed resume preflight: %+v", events)
	}
}

func TestServiceCompleteGoalAlreadyCompleteDoesNotDuplicateAudit(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, err := service.CompleteGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{SessionID: store.Meta().SessionID, Actor: "agent"}); err != nil {
		t.Fatalf("CompleteGoal first: %v", err)
	}
	before, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("ReadEvents before: %v", err)
	}
	if _, err := service.CompleteGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{SessionID: store.Meta().SessionID, Actor: "agent"}); err != nil {
		t.Fatalf("CompleteGoal second: %v", err)
	}
	after, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("ReadEvents after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("events after duplicate complete = %d, want %d", len(after), len(before))
	}
}

func TestServiceResumeActiveRunningGoalIsNoOp(t *testing.T) {
	client := newRestartableRuntimeControlClient()
	defer client.releaseFirst()
	defer client.releaseSecond()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	goal, err := engine.SetGoal("ship goal mode", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop: %v", err)
	}
	select {
	case <-client.call1Started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for running goal loop")
	}
	before := runtimeControlGoalDeveloperMessages(t, store)
	type resumeResult struct {
		response serverapi.RuntimeGoalShowResponse
		err      error
	}
	resumeDone := make(chan resumeResult, 1)
	go func() {
		response, resumeErr := service.ResumeGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{
			SessionID: store.Meta().SessionID,
			Actor:     "user",
		})
		resumeDone <- resumeResult{response: response, err: resumeErr}
	}()
	select {
	case result := <-resumeDone:
		t.Fatalf("ResumeGoal completed before the protected Step boundary: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	client.releaseFirst()
	result := <-resumeDone
	if result.err != nil {
		t.Fatalf("ResumeGoal: %v", result.err)
	}
	resp := result.response
	if resp.Goal == nil || resp.Goal.ID != goal.ID || resp.Goal.Status != string(session.GoalStatusActive) {
		t.Fatalf("resume active response = %+v, want existing active goal", resp.Goal)
	}
	select {
	case <-client.call2Started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for running Goal loop to continue")
	}
	after := runtimeControlGoalDeveloperMessages(t, store)
	if len(after) != len(before)+1 {
		t.Fatalf("running Goal boundary appended %d Goal notices, want one continuation reminder", len(after)-len(before))
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt Goal loop: %v", err)
	}
	client.releaseSecond()
	waitForRuntimeControlIdle(t, engine)
	if _, err := engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorSystem); err != nil {
		t.Fatalf("complete Goal cleanup: %v", err)
	}
}

func TestServiceResumeOwnerlessActiveGoalRestartsLoopWithReminder(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	goal, err := engine.SetGoal("ship goal mode", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	before := runtimeControlGoalDeveloperMessages(t, store)
	if len(before) != 1 {
		t.Fatalf("goal developer messages before resume = %d, want set only", len(before))
	}

	resp, err := service.ResumeGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{
		SessionID: store.Meta().SessionID,
		Actor:     "user",
	})
	if err != nil {
		t.Fatalf("ResumeGoal: %v", err)
	}
	if resp.Goal == nil || resp.Goal.ID != goal.ID || resp.Goal.Status != string(session.GoalStatusActive) {
		t.Fatalf("resume ownerless active response = %+v, want existing active goal", resp.Goal)
	}
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ownerless active goal loop restart")
	}
	defer func() {
		_ = engine.Interrupt()
		close(client.release)
		waitForRuntimeControlIdle(t, engine)
		_, _ = engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorSystem)
	}()
	messages := runtimeControlGoalDeveloperMessages(t, store)
	if len(messages) != 2 {
		t.Fatalf("goal developer messages after resume = %d, want set+resume", len(messages))
	}
	if messages[1].Content == nil || *messages[1].Content != prompts.RenderGoalResumePrompt("ship goal mode") {
		t.Fatalf("resume reminder content = %v", messages[1].Content)
	}
}

func TestServiceResumeGoalDuringInterruptSchedulesRestartWithReminder(t *testing.T) {
	client := newRestartableRuntimeControlClient()
	defer func() {
		client.releaseFirst()
		client.releaseSecond()
	}()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	goal, err := engine.SetGoal("ship goal mode", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop: %v", err)
	}
	select {
	case <-client.call1Started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for initial goal loop call")
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	type resumeResult struct {
		response serverapi.RuntimeGoalShowResponse
		err      error
	}
	resumeDone := make(chan resumeResult, 1)
	go func() {
		response, resumeErr := service.ResumeGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{
			SessionID: store.Meta().SessionID,
			Actor:     "user",
		})
		resumeDone <- resumeResult{response: response, err: resumeErr}
	}()
	select {
	case result := <-resumeDone:
		t.Fatalf("ResumeGoal completed before interrupted Step retirement: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	client.releaseFirst()
	result := <-resumeDone
	if result.err != nil {
		t.Fatalf("ResumeGoal: %v", result.err)
	}
	resp := result.response
	if resp.Goal == nil || resp.Goal.ID != goal.ID || resp.Goal.Status != string(session.GoalStatusActive) {
		t.Fatalf("resume suspending active response = %+v, want existing active goal", resp.Goal)
	}
	select {
	case <-client.call2Started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for resumed goal loop call after interrupted turn finished")
	}
	messages := runtimeControlGoalDeveloperMessages(t, store)
	if len(messages) != 2 {
		t.Fatalf("goal developer messages after interrupted turn drain = %d, want set+resume", len(messages))
	}
	if messages[1].Content == nil || *messages[1].Content != prompts.RenderGoalResumePrompt("ship goal mode") {
		t.Fatalf("resume reminder content = %v", messages[1].Content)
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt resumed Goal loop: %v", err)
	}
	client.releaseSecond()
	waitForRuntimeControlIdle(t, engine)
	if _, err := engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorSystem); err != nil {
		t.Fatalf("complete Goal cleanup: %v", err)
	}
}

func TestServiceSubmitUserTurnPromptCommandUsesExpandedExecutionAndCanonicalHistory(t *testing.T) {
	client := finalResponseRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	resolver := &runtimeControlPromptCommandResolver{content: "expanded current body"}
	service.WithPromptCommandResolver(resolver)
	req := serverapi.RuntimeSubmitUserTurnRequest{
		SessionID: store.Meta().SessionID,
		Input:     runtimeinput.BuiltinCommand(runtimeinput.BuiltinPromptCommandReview, "src/internal"),
	}
	resp, err := service.SubmitUserTurn(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if resp.ResultKind != clientui.UserTurnResultKindQueued || resp.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn response = %+v, want queued acceptance", resp)
	}
	waitForRuntimeControlAssistantFinal(t, engine, "done")
	if calls := resolver.calls.Load(); calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", calls)
	}
	if got := countUserMessagesWithContent(t, store, "expanded current body"); got != 1 {
		t.Fatalf("expanded user message count = %d, want 1", got)
	}
	if got := countPromptHistoryEvents(t, store, "/review src/internal"); got != 1 {
		t.Fatalf("canonical history count = %d, want 1", got)
	}
	if got := countPromptHistoryEvents(t, store, "expanded current body"); got != 0 {
		t.Fatalf("expanded body history count = %d, want 0", got)
	}
}

func TestServiceSubmitUserTurnPromptResolutionFailureIsNotAcceptedAndRemainsRetryable(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	resolutionErr := errors.New("prompt command disappeared")
	resolver := &runtimeControlPromptCommandResolver{err: resolutionErr}
	service.WithPromptCommandResolver(resolver)
	req := runtimeControlUserTurnRequest(store, "missing-prompt", "unused")
	req.Input = runtimeinput.BuiltinCommand(runtimeinput.BuiltinPromptCommandReview, "")

	if _, err := service.SubmitUserTurn(context.Background(), req); !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) || !errors.Is(err, resolutionErr) {
		t.Fatalf("SubmitUserTurn missing prompt command error = %v, want typed not-accepted resolution failure", err)
	}
	req.Input = runtimeinput.Text("retry after resolution failure")
	response, err := service.SubmitUserTurn(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitUserTurn retry after pre-acceptance failure: %v", err)
	}
	if response.ResultKind != clientui.UserTurnResultKindQueued || response.QueueItemID == "" {
		t.Fatalf("retry response = %+v, want queued acceptance", response)
	}
	waitForRuntimeControlAssistantFinal(t, engine, "done")
	if got := countUserMessagesWithContent(t, store, "retry after resolution failure"); got != 1 {
		t.Fatalf("retried user message count = %d, want 1", got)
	}
}

func TestServiceSubmitUserTurnRecordsCommittedAtFlushBeforeAssistantCompletion(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	req := serverapi.RuntimeSubmitUserTurnRequest{
		SessionID: store.Meta().SessionID,
		Input:     runtimeinput.Text("flush before model completes"),
	}
	response, err := service.SubmitUserTurn(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if response.ResultKind != clientui.UserTurnResultKindQueued || response.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn response = %+v, want queued acceptance", response)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("submit did not reach model request after user-message flush")
	}
	if got := countUserMessagesWithContent(t, store, "flush before model completes"); got != 1 {
		t.Fatalf("committed user message count before assistant completion = %d, want 1", got)
	}
	if _, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		SessionID: req.SessionID,
	}); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	select {
	case <-client.ctxCanceled:
	case <-time.After(time.Second):
		t.Fatal("active submit interrupt did not reach engine interrupt path")
	}
	close(client.release)
	waitForRuntimeControlIdle(t, engine)
}

func TestServiceSubmitUserShellCommandDoesNotRecordPromptHistory(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeShellHandler{}}), runtime.Config{})
	req := runtimeControlShellCommandRequest(store, "req-1", "pwd")

	if err := service.SubmitUserShellCommand(context.Background(), req); err != nil {
		t.Fatalf("SubmitUserShellCommand: %v", err)
	}
	history := service.promptStore.(*runtimeControlPromptHistoryStore)
	if err := service.SubmitUserShellCommand(context.Background(), req); err != nil {
		t.Fatalf("SubmitUserShellCommand second: %v", err)
	}
	if got := history.CountText("$ pwd"); got != 0 {
		t.Fatalf("shell prompt history count = %d, want 0", got)
	}
}

func TestServiceQueuedSteeringDrainsAtNextSafeBoundary(t *testing.T) {
	client := newSteeringDrainRuntimeControlClient()
	queuedStatuses := make(chan runtime.QueuedUserMessageStatusEvent, 4)
	registry := newTestToolRegistry(t, tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: fakeShellHandler{},
	})
	store, _, service := newRuntimeControlTestService(t, client, registry, runtime.Config{
		OnEvent: func(event runtime.Event) {
			if event.QueuedUserMessageStatus != nil {
				queuedStatuses <- *event.QueuedUserMessageStatus
			}
		},
	})
	submission, err := service.SubmitUserTurn(
		context.Background(),
		runtimeControlUserTurnRequest(store, "active-turn", "start"),
	)
	if err != nil {
		t.Fatalf("SubmitUserTurn active turn: %v", err)
	}
	if submission.ResultKind != clientui.UserTurnResultKindQueued || submission.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn active turn = %+v, want queued acceptance", submission)
	}
	waitForRuntimeControlQueuedStatus(t, queuedStatuses, submission.QueueItemID, runtime.QueuedUserMessageAccepted)
	select {
	case <-client.firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("active turn did not reach the first model request")
	}
	queuedText := "use the existing lld installation"
	steeringReq := runtimeControlUserTurnRequest(store, "queued-steering", queuedText)
	steeringDone := submitUserTurnRuntimeControlAsync(service, steeringReq)
	select {
	case result := <-steeringDone:
		t.Fatalf("SubmitUserTurn completed before the protected Step boundary: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	close(client.releaseFirst)
	var steeringResult runtimeControlSubmitUserTurnResult
	select {
	case steeringResult = <-steeringDone:
	case <-time.After(5 * time.Second):
		t.Fatal("SubmitUserTurn did not complete after the protected Step boundary")
	}
	if steeringResult.err != nil {
		t.Fatalf("SubmitUserTurn while model was thinking: %v", steeringResult.err)
	}
	steered := steeringResult.response
	if steered.ResultKind != clientui.UserTurnResultKindQueued || !steered.Steered || steered.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn while model was thinking = %+v, want accepted steering", steered)
	}
	waitForRuntimeControlQueuedStatus(t, queuedStatuses, steered.QueueItemID, runtime.QueuedUserMessageAccepted)
	select {
	case <-client.secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("active turn did not reach the next safe-boundary model request")
	}
	defer close(client.releaseSecond)

	for _, message := range llm.MessagesFromItems(client.request(1).Items) {
		if message.Role == llm.RoleUser && message.Content != nil && *message.Content == queuedText {
			return
		}
	}
	t.Fatalf("next model request did not receive accepted steering: %+v", llm.MessagesFromItems(client.request(1).Items))
}

func TestServiceSubmitUserTurnPromptCommandResolvesBeforeActiveRunQueueAdmission(t *testing.T) {
	client := newSteeringDrainRuntimeControlClient()
	queuedStatuses := make(chan runtime.QueuedUserMessageStatusEvent, 4)
	registry := newTestToolRegistry(t, tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: fakeShellHandler{},
	})
	store, _, service := newRuntimeControlTestService(t, client, registry, runtime.Config{
		OnEvent: func(event runtime.Event) {
			if event.QueuedUserMessageStatus != nil {
				queuedStatuses <- *event.QueuedUserMessageStatus
			}
		},
	})
	resolver := &runtimeControlPromptCommandResolver{
		content:  "expanded prompt body",
		resolved: make(chan struct{}, 1),
	}
	service.WithPromptCommandResolver(resolver)
	submission, err := service.SubmitUserTurn(
		context.Background(),
		runtimeControlUserTurnRequest(store, "active-prompt-turn", "start"),
	)
	if err != nil {
		t.Fatalf("SubmitUserTurn active prompt turn: %v", err)
	}
	if submission.ResultKind != clientui.UserTurnResultKindQueued || submission.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn active prompt turn = %+v, want queued acceptance", submission)
	}
	waitForRuntimeControlQueuedStatus(t, queuedStatuses, submission.QueueItemID, runtime.QueuedUserMessageAccepted)
	select {
	case <-client.firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("active turn did not reach the first model request")
	}

	steeringReq := runtimeControlUserTurnRequest(store, "queued-prompt-command", "unused")
	steeringReq.Input = runtimeinput.BuiltinCommand(runtimeinput.BuiltinPromptCommandReview, "src")
	steeringDone := submitUserTurnRuntimeControlAsync(service, steeringReq)
	select {
	case <-resolver.resolved:
	case <-time.After(5 * time.Second):
		t.Fatal("prompt command was not resolved before Runtime queue admission")
	}
	if calls := resolver.calls.Load(); calls != 1 {
		t.Fatalf("resolver calls before queue admission = %d, want 1", calls)
	}
	select {
	case result := <-steeringDone:
		t.Fatalf("SubmitUserTurn completed before the protected Step boundary: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}

	close(client.releaseFirst)
	var steeringResult runtimeControlSubmitUserTurnResult
	select {
	case steeringResult = <-steeringDone:
	case <-time.After(5 * time.Second):
		t.Fatal("SubmitUserTurn prompt command did not complete after the protected Step boundary")
	}
	if steeringResult.err != nil {
		t.Fatalf("SubmitUserTurn prompt command while model was thinking: %v", steeringResult.err)
	}
	steered := steeringResult.response
	if steered.ResultKind != clientui.UserTurnResultKindQueued || !steered.Steered || steered.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn prompt command while model was thinking = %+v, want accepted steering", steered)
	}
	status := waitForRuntimeControlQueuedStatus(t, queuedStatuses, steered.QueueItemID, runtime.QueuedUserMessageAccepted)
	if status.Text != "expanded prompt body" {
		t.Fatalf("accepted prompt-command queue status = %+v", status)
	}
	if got := countPromptHistoryEvents(t, store, "/review src"); got != 1 {
		t.Fatalf("canonical prompt history count = %d, want 1", got)
	}
	if got := countPromptHistoryEvents(t, store, "expanded prompt body"); got != 0 {
		t.Fatalf("expanded prompt history count = %d, want 0", got)
	}

	select {
	case <-client.secondStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("active turn did not reach the prompt-command safe-boundary request")
	}
	defer close(client.releaseSecond)
	if calls := resolver.calls.Load(); calls != 1 {
		t.Fatalf("resolver calls after queue drain = %d, want 1", calls)
	}

	for _, message := range llm.MessagesFromItems(client.request(1).Items) {
		if message.Role == llm.RoleUser && message.Content != nil && *message.Content == "expanded prompt body" {
			return
		}
	}
	t.Fatalf("next model request did not receive expanded prompt command: %+v", llm.MessagesFromItems(client.request(1).Items))
}

func TestServiceSubmitUserTurnPromptResolutionFailureDuringActiveRunReturnsWithoutQueueing(t *testing.T) {
	for _, kind := range []serverapi.PromptCommandErrorKind{
		serverapi.PromptCommandErrorKindCommandNotFound,
		serverapi.PromptCommandErrorKindCommandRead,
	} {
		t.Run(string(kind), func(t *testing.T) {
			client := newSteeringDrainRuntimeControlClient()
			queuedStatuses := make(chan runtime.QueuedUserMessageStatusEvent, 4)
			registry := newTestToolRegistry(t, tools.HandlerRegistration{
				ID:      toolspec.ToolExecCommand,
				Handler: fakeShellHandler{},
			})
			store, engine, service := newRuntimeControlTestService(t, client, registry, runtime.Config{
				OnEvent: func(event runtime.Event) {
					if event.QueuedUserMessageStatus != nil {
						queuedStatuses <- *event.QueuedUserMessageStatus
					}
				},
			})
			command := runtimeinput.PromptCommandReviewName
			resolutionErr := &serverapi.PromptCommandError{Kind: kind, Command: &command}
			resolver := &runtimeControlPromptCommandResolver{err: resolutionErr}
			service.WithPromptCommandResolver(resolver)
			submission, err := service.SubmitUserTurn(
				context.Background(),
				runtimeControlUserTurnRequest(store, "active-before-failed-prompt", "start"),
			)
			if err != nil {
				t.Fatalf("SubmitUserTurn active turn: %v", err)
			}
			if submission.ResultKind != clientui.UserTurnResultKindQueued || submission.QueueItemID == "" {
				t.Fatalf("SubmitUserTurn active turn = %+v, want queued acceptance", submission)
			}
			waitForRuntimeControlQueuedStatus(t, queuedStatuses, submission.QueueItemID, runtime.QueuedUserMessageAccepted)
			select {
			case <-client.firstStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("active turn did not reach the first model request")
			}
			waitForRuntimeControlQueuedStatus(t, queuedStatuses, submission.QueueItemID, runtime.QueuedUserMessageSubmitted)

			req := runtimeControlUserTurnRequest(store, "failed-prompt-during-active-run", "unused")
			req.Input = runtimeinput.BuiltinCommand(runtimeinput.BuiltinPromptCommandReview, "")
			_, err = service.SubmitUserTurn(context.Background(), req)
			var typed *serverapi.PromptCommandError
			if !errors.As(err, &typed) || typed.Kind != kind {
				t.Fatalf("SubmitUserTurn prompt resolution error = %T %v, want typed %s", err, err, kind)
			}
			if calls := resolver.calls.Load(); calls != 1 {
				t.Fatalf("resolver calls = %d, want 1", calls)
			}
			if engine.HasQueuedUserWork() {
				t.Fatal("failed prompt command was admitted to the runtime queue")
			}
			select {
			case status := <-queuedStatuses:
				t.Fatalf("failed prompt command emitted queued status %+v", status)
			default:
			}
			if got := countPromptHistoryEvents(t, store, "/review"); got != 0 {
				t.Fatalf("failed prompt command history count = %d, want 0", got)
			}

			close(client.releaseFirst)
			select {
			case <-client.secondStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("active turn did not reach its second model request")
			}
			close(client.releaseSecond)
			waitForRuntimeControlIdle(t, engine)
		})
	}
}

func TestServiceSubmitUserTurnQueuesWhileCompactionOwnsSessionExecution(t *testing.T) {
	trimmed := 1
	client := &blockingCompactionRuntimeControlClient{
		runtimeControlFakeClient: runtimeControlFakeClient{
			responses: []llm.Response{
				{
					Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("seeded"), Phase: textutil.Value(llm.MessagePhaseFinal)},
					Usage:     llm.Usage{WindowTokens: 200000},
				},
				{
					Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("queued message handled"), Phase: textutil.Value(llm.MessagePhaseFinal)},
					Usage:     llm.Usage{WindowTokens: 200000},
				},
			},
			compactionResponses: []llm.CompactionResponse{{
				OutputItems: []llm.ResponseItem{
					{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleUser), MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
					{Type: llm.ResponseItemTypeCompaction, EncryptedContent: textutil.Value("checkpoint")},
				},
				Usage:             llm.Usage{WindowTokens: 200000},
				TrimmedItemsCount: &trimmed,
			}},
		},
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseOnce sync.Once
	releaseCompaction := func() {
		releaseOnce.Do(func() { close(client.release) })
	}
	defer releaseCompaction()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{
		Model:                        "gpt-5",
		ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities,
	})
	if _, err := engine.SubmitUserMessage(context.Background(), "seed compaction"); err != nil {
		t.Fatalf("seed runtime transcript: %v", err)
	}

	if err := service.CompactContext(context.Background(), serverapi.RuntimeCompactContextRequest{
		SessionID: store.Meta().SessionID,
		Args:      "compact",
	}); err != nil {
		t.Fatalf("CompactContext scheduling: %v", err)
	}
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("compaction did not start")
	}

	if _, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		SessionID: store.Meta().SessionID,
	}); !errors.Is(err, serverapi.ErrRuntimeCommandNotAccepted) {
		t.Fatalf("targeted Interrupt while compacting error = %v, want Runtime Command not accepted", err)
	}

	queuedText := "queue after compaction"
	response, err := service.SubmitUserTurn(
		context.Background(),
		runtimeControlUserTurnRequest(store, "queue-while-compacting", queuedText),
	)
	if err != nil {
		t.Fatalf("SubmitUserTurn while compacting: %v", err)
	}
	if response.ResultKind != clientui.UserTurnResultKindQueued || !response.Steered || response.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn while compacting = %+v, want queued acceptance", response)
	}
	if !engine.HasQueuedUserWork() {
		t.Fatal("human Steering was not retained while compaction was active")
	}

	releaseCompaction()
	waitForRuntimeControlAssistantFinal(t, engine, "queued message handled")
	if got := countUserMessagesWithContent(t, store, queuedText); got != 1 {
		t.Fatalf("steered user message count = %d, want 1", got)
	}
}

func TestServiceInterruptRejectsPendingSteeringBeforeStoppingActiveRun(t *testing.T) {
	client := newSteeringDrainRuntimeControlClient()
	defer close(client.releaseFirst)
	defer close(client.releaseSecond)
	registry := newTestToolRegistry(t, tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: fakeShellHandler{},
	})
	store, engine, service := newRuntimeControlTestServiceWithEventFeed(
		t,
		client,
		registry,
		runtime.Config{},
		func(runtimeids.SessionResourceRef, runtime.Event) {},
	)
	activeReq := runtimeControlUserTurnRequest(store, "active-turn", "start")
	submission, err := service.SubmitUserTurn(context.Background(), activeReq)
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if submission.ResultKind != clientui.UserTurnResultKindQueued || submission.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn response = %+v, want queued acceptance", submission)
	}
	select {
	case <-client.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("active turn did not reach model thinking")
	}

	steeringReq := runtimeControlUserTurnRequest(store, "queued-steering", "do not continue after interrupt")
	type steeringResult struct {
		response serverapi.RuntimeSubmitUserTurnResponse
		err      error
	}
	steeringDone := make(chan steeringResult, 1)
	go func() {
		response, steeringErr := service.SubmitUserTurn(context.Background(), steeringReq)
		steeringDone <- steeringResult{response: response, err: steeringErr}
	}()
	select {
	case result := <-steeringDone:
		t.Fatalf("Steering completed before the protected Step boundary: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}

	_, err = service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		SessionID: store.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("interrupt left accepted steering queued")
	}
	result := <-steeringDone
	if !errors.Is(result.err, serverapi.ErrRuntimeCommandNotAccepted) ||
		!errors.Is(result.err, context.Canceled) {
		t.Fatalf("SubmitUserTurn steering error = %v, want canceled not-accepted result", result.err)
	}
	if result.response != (serverapi.RuntimeSubmitUserTurnResponse{}) {
		t.Fatalf("SubmitUserTurn steering response = %+v, want zero response", result.response)
	}
	select {
	case <-client.secondStarted:
		t.Fatal("queued steering started a model continuation after interrupt")
	case <-time.After(100 * time.Millisecond):
	}
	waitForRuntimeControlIdle(t, engine)
}

func TestServiceDiscardQueuedUserMessageIsRuntimeOnly(t *testing.T) {
	ctx := context.Background()
	sessionStore, engine, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	queued := mustQueueRuntimeControlMessage(t, engine, "discard runtime only")
	discardReq := serverapi.RuntimeDiscardQueuedUserMessageRequest{
		SessionID:   sessionStore.Meta().SessionID,
		QueueItemID: queued.ID,
	}
	discarded, err := service.DiscardQueuedUserMessage(ctx, discardReq)
	if err != nil {
		t.Fatalf("DiscardQueuedUserMessage: %v", err)
	}
	if !discarded.Discarded {
		t.Fatal("expected runtime discard to remove pending queue item")
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("expected runtime queue item removed")
	}
	if got := countPromptHistoryEvents(t, sessionStore, "discard runtime only"); got != 0 {
		t.Fatalf("prompt history count after runtime-only discard = %d, want 0", got)
	}
}

func countDirectShellCommandMessages(t *testing.T, store *session.Store, command string) int {
	t.Helper()
	count := 0
	for _, message := range runtimeControlMessageRecords(t, store) {
		if message.Role != session.MessageRoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Name != string(toolspec.ToolExecCommand) {
				continue
			}
			var in struct {
				Cmd           string `json:"cmd"`
				UserInitiated bool   `json:"user_initiated"`
			}
			if err := json.Unmarshal(call.Input, &in); err != nil {
				continue
			}
			if in.UserInitiated && in.Cmd == command {
				count++
			}
		}
	}
	return count
}

func runtimeControlMessageRecords(t *testing.T, store *session.Store) []session.MessageRecord {
	t.Helper()
	records, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("collect event records: %v", err)
	}
	messages := make([]session.MessageRecord, 0)
	for _, record := range records {
		payload, payloadErr := record.Payload()
		if payloadErr != nil {
			t.Fatalf("read event record payload: %v", payloadErr)
		}
		message, ok := payload.(session.MessageRecord)
		if !ok {
			continue
		}
		messages = append(messages, message)
	}
	return messages
}

func runtimeControlGoalDeveloperMessages(t *testing.T, store *session.Store) []session.MessageRecord {
	t.Helper()
	out := make([]session.MessageRecord, 0)
	for _, message := range runtimeControlMessageRecords(t, store) {
		if message.Role == session.MessageRoleDeveloper &&
			message.MessageType != nil &&
			*message.MessageType == session.MessageTypeGoal {
			out = append(out, message)
		}
	}
	return out
}

func countUserMessagesWithContent(t *testing.T, store *session.Store, content string) int {
	t.Helper()
	count := 0
	for _, message := range runtimeControlMessageRecords(t, store) {
		if message.Role == session.MessageRoleUser &&
			message.Content != nil &&
			*message.Content == content {
			count++
		}
	}
	return count
}

func runtimeControlUserTurnRequest(store *session.Store, _ string, text string) serverapi.RuntimeSubmitUserTurnRequest {
	return serverapi.RuntimeSubmitUserTurnRequest{
		SessionID: store.Meta().SessionID,
		Input:     runtimeinput.Text(text),
	}
}

func runtimeControlShellCommandRequest(store *session.Store, _ string, command string) serverapi.RuntimeSubmitUserShellCommandRequest {
	return serverapi.RuntimeSubmitUserShellCommandRequest{
		SessionID: store.Meta().SessionID,
		Command:   command,
	}
}
