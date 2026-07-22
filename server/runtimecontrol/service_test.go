package runtimecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"core/prompts"
	"core/server/llm"
	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/runtimeops"
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
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
)

type missingMetadataPersistedSessionResolver struct{}

func (missingMetadataPersistedSessionResolver) ResolvePersistedSession(context.Context, string) (session.PersistedSessionRecord, error) {
	return session.PersistedSessionRecord{SessionDir: "/tmp/session"}, nil
}

var runtimeControlPromptHistoryStores sync.Map

type runtimeControlPromptHistoryStore struct {
	mu             sync.Mutex
	records        []metadata.PromptHistoryRecord
	recordInserted []bool
	recordErr      error
	recordCtxErr   error
}

func newRuntimeControlPromptHistoryStore(sessionID string) *runtimeControlPromptHistoryStore {
	store := &runtimeControlPromptHistoryStore{}
	runtimeControlPromptHistoryStores.Store(sessionID, store)
	return store
}

func (s *runtimeControlPromptHistoryStore) RecordPromptHistoryEntry(ctx context.Context, entry metadata.PromptHistoryEntry) (metadata.PromptHistoryRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recordErr != nil {
		return metadata.PromptHistoryRecord{}, false, s.recordErr
	}
	if s.recordCtxErr != nil && ctx.Err() != nil {
		return metadata.PromptHistoryRecord{}, false, s.recordCtxErr
	}
	for _, record := range s.records {
		if record.SessionID == entry.SessionID && record.SourceID == entry.SourceID {
			if record.Text != entry.Text {
				return metadata.PromptHistoryRecord{}, false, metadata.ErrPromptHistoryConflict
			}
			s.recordInserted = append(s.recordInserted, false)
			return record, false, nil
		}
	}
	record := metadata.PromptHistoryRecord{
		Sequence:  int64(len(s.records) + 1),
		SessionID: entry.SessionID,
		SourceID:  entry.SourceID,
		Text:      entry.Text,
		CreatedAt: entry.CreatedAt,
	}
	s.records = append(s.records, record)
	s.recordInserted = append(s.recordInserted, true)
	return record, true, nil
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

type staticRuntimeControlSessionResolver struct {
	store *session.Store
}

func (r staticRuntimeControlSessionResolver) ResolveSessionStore(context.Context, string) (*session.Store, error) {
	return r.store, nil
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
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist session store: %v", err)
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

func newRuntimeControlTestService(t *testing.T, client llm.Client, registry *tools.Registry, cfg runtime.Config, opts ...session.StoreOption) (*session.Store, *runtime.Engine, *Service) {
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
		Settings:                     settings,
		EnabledTools:                 enabledTools,
		Workdir:                      store.Meta().WorkspaceRoot,
		Client:                       client,
		ReviewerClientFactory:        reviewerClientFactory,
		WorkflowRun:                  cfg.WorkflowRun,
		ProviderCapabilitiesOverride: cfg.ProviderCapabilitiesOverride,
		OnEvent:                      cfg.OnEvent,
	})
	if err != nil {
		t.Fatalf("new authority runtime plan: %v", err)
	}
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: t.TempDir(),
		StoreOptions:    append(runtimeControlTestSessionPersistence.Options(), opts...),
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

func finalResponseRuntimeControlClient() *runtimeControlFakeClient {
	return &runtimeControlFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
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
	done := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "req-interrupt", "hello"))
		done <- err
	}()

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
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("SubmitUserMessage error = %v, want context canceled", err)
	}
}

func TestServiceCanceledAskQuestionTurnReleasesExecutionForNextUserTurn(t *testing.T) {
	toolStarted := make(chan struct{})
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
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
		OnEvent: func(event runtime.Event) {
			if event.Kind != runtime.EventToolCallStarted || event.ToolCall == nil || event.ToolCall.ID != "ask-cancel" {
				return
			}
			select {
			case <-toolStarted:
			default:
				close(toolStarted)
			}
		},
	})

	firstRequest := runtimeControlUserTurnRequest(store, "ask-cancel", "ask then cancel")
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), firstRequest)
		firstDone <- err
	}()
	select {
	case <-toolStarted:
	case err := <-firstDone:
		t.Fatalf("ask_question turn ended before tool start: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for ask_question to start")
	}
	if _, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID:    "interrupt-ask-cancel",
		SessionID:          store.Meta().SessionID,
		TargetOperationRef: &firstRequest.OperationRef,
	}); err != nil {
		t.Fatalf("interrupt canceled ask_question turn: %v", err)
	}
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled ask_question turn error = %v, want context canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled ask_question turn retained its execution")
	}
	if _, err := service.LiveSteer(context.Background(), serverapi.RuntimeLiveSteerRequest{
		ClientRequestID: "b8349273-19d6-4a4b-94fb-895b48103d02",
		SessionID:       store.Meta().SessionID,
		Text:            "must not join the canceled execution",
	}); !errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
		t.Fatalf("live steer after canceled ask_question error = %v, want no active run", err)
	}

	next, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "after-ask-cancel", "next user message"))
	if err != nil {
		t.Fatalf("submit next user turn after canceled ask_question: %v", err)
	}
	if next.Message != "next turn completed" {
		t.Fatalf("next user turn response = %+v, want completed response", next)
	}
	if engine.HasActiveLiveRunGroup() {
		t.Fatal("canceled ask_question turn retained live-run ownership after next user turn")
	}
	nextUserMessageCommitted := false
	if err := engine.WithTranscriptHydrationSnapshot(func(snapshot runtime.TranscriptHydrationSnapshot) error {
		for _, row := range snapshot.CommittedRows {
			if row.Kind == runtime.TranscriptCommittedRowFactUser && row.User != nil && row.User.Text == "next user message" {
				nextUserMessageCommitted = true
				return nil
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("read transcript hydration snapshot: %v", err)
	}
	if !nextUserMessageCommitted {
		t.Fatal("next user message was not committed to the transcript")
	}
}

func TestServiceInterruptReturnsUnavailableActivityWithoutEngine(t *testing.T) {
	service := NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{}))
	resp, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID: "interrupt-1",
		SessionID:       "018fdd67-89ab-4cde-8123-456789abcdef",
	})
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if err := resp.Version.Validate(); err != nil {
		t.Fatalf("response version invalid: %v", err)
	}
	if resp.Activity.State != clientui.RuntimeActivityUnavailable {
		t.Fatalf("activity = %+v, want unavailable", resp.Activity)
	}
}

func TestServiceLiveSteerRequiresActiveRun(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	_, err := service.LiveSteer(context.Background(), serverapi.RuntimeLiveSteerRequest{
		ClientRequestID: "8b0364cc-5c6c-412e-a4e8-31380661d1e1",
		SessionID:       store.Meta().SessionID,
		Text:            "steer while idle",
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
		ClientRequestID: "8b0364cc-5c6c-412e-a4e8-31380661d1e1",
		SessionID:       "018fdd67-89ab-4cde-8123-456789abcdef",
		Text:            "steer closed runtime",
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
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	submitDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "keep-running", "keep running"))
		submitDone <- err
	}()
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active runtime")
	}
	resp, err := service.LiveSteer(context.Background(), serverapi.RuntimeLiveSteerRequest{
		ClientRequestID: "8b0364cc-5c6c-412e-a4e8-31380661d1e1",
		SessionID:       store.Meta().SessionID,
		Text:            " steer live ",
	})
	if err != nil {
		t.Fatalf("LiveSteer: %v", err)
	}
	if resp.QueueItemID == "" || resp.Text != "steer live" || resp.ClientRequestID != "8b0364cc-5c6c-412e-a4e8-31380661d1e1" {
		t.Fatalf("LiveSteer response = %+v", resp)
	}
	waitForRuntimeControlPromptHistoryCount(t, runtimeControlPromptHistoryStoresLoad(t, store.Meta().SessionID), "steer live", 1)
	_, _ = service.LiveStop(context.Background(), serverapi.RuntimeLiveStopRequest{
		ClientRequestID: "6859fdfa-6808-4109-a031-de3d432e88dd",
		SessionID:       store.Meta().SessionID,
	})
	close(client.release)
	<-submitDone
}

func TestServiceLiveSteerPreservesAdmittedPromptHistoryError(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	submitDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "keep-running", "keep running"))
		submitDone <- err
	}()
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active runtime")
	}
	historyErr := errors.New("prompt history failed")
	runtimeControlPromptHistoryStoresLoad(t, store.Meta().SessionID).SetRecordError(historyErr)
	_, err := service.LiveSteer(context.Background(), serverapi.RuntimeLiveSteerRequest{
		ClientRequestID: "8b0364cc-5c6c-412e-a4e8-31380661d1e1",
		SessionID:       store.Meta().SessionID,
		Text:            "steer live",
	})
	if !errors.Is(err, historyErr) {
		t.Fatalf("LiveSteer error = %v, want prompt history failure", err)
	}
	if errors.Is(err, serverapi.ErrRuntimeNoActiveRun) {
		t.Fatalf("LiveSteer mapped prompt history failure to no-active: %v", err)
	}
	_, _ = service.LiveStop(context.Background(), serverapi.RuntimeLiveStopRequest{
		ClientRequestID: "6859fdfa-6808-4109-a031-de3d432e88dd",
		SessionID:       store.Meta().SessionID,
	})
	close(client.release)
	<-submitDone
}

func TestServiceLiveStopIdleReturnsIdle(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	resp, err := service.LiveStop(context.Background(), serverapi.RuntimeLiveStopRequest{
		ClientRequestID: "8b0364cc-5c6c-412e-a4e8-31380661d1e1",
		SessionID:       store.Meta().SessionID,
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

func TestServiceInterruptReturnsCurrentActivitySnapshot(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	service.WithRuntimeActivityResolver(&sequenceRuntimeActivityResolver{
		snapshots: []runtimeactivity.ResponseSnapshot{{
			Version:  clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 1},
			Activity: clientui.RuntimeActivity{State: clientui.RuntimeActivityRegisteredIdle, QueueAccepting: true},
		}},
	})
	resp, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID: "interrupt-1",
		SessionID:       store.Meta().SessionID,
	})
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if resp.Activity.State != clientui.RuntimeActivityRegisteredIdle {
		t.Fatalf("activity = %+v, want idle", resp.Activity)
	}
	if err := resp.Version.Validate(); err != nil {
		t.Fatalf("invalid response version: %v", err)
	}
}

func TestServiceGoalMutationsSetShowComplete(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})

	setResp, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "goal-set-1",
		SessionID:       store.Meta().SessionID,
		Objective:       "ship goal mode",
		Actor:           "user",
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
		ClientRequestID: "goal-complete-1",
		SessionID:       store.Meta().SessionID,
		Actor:           "agent",
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
	client := newCancelObservingRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	initialGoal, err := engine.SetGoal("committed before active step", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	turnDone := make(chan error, 1)
	go func() {
		_, submitErr := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "turn-1", "work"))
		turnDone <- submitErr
	}()
	released := false
	defer func() {
		if !released {
			close(client.release)
		}
		select {
		case <-turnDone:
		case <-time.After(3 * time.Second):
		}
	}()
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active model step")
	}

	accepted, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "goal-set-queued",
		SessionID:       store.Meta().SessionID,
		Objective:       "accepted pending goal",
		Actor:           string(session.GoalActorUser),
	})
	if err != nil {
		t.Fatalf("SetGoal queued mutation: %v", err)
	}
	if accepted.Goal == nil || accepted.Goal.Objective != "accepted pending goal" || accepted.Goal.Status != string(session.GoalStatusActive) {
		t.Fatalf("SetGoal accepted response = %+v, want active pending goal", accepted.Goal)
	}
	cleared, err := service.ClearGoal(context.Background(), serverapi.RuntimeGoalClearRequest{
		ClientRequestID: "goal-clear-queued",
		SessionID:       store.Meta().SessionID,
		Actor:           string(session.GoalActorUser),
	})
	if err != nil {
		t.Fatalf("ClearGoal queued mutation: %v", err)
	}
	if cleared.Goal != nil {
		t.Fatalf("ClearGoal accepted response = %+v, want no goal", cleared.Goal)
	}

	beforeDrain, err := service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("ShowGoal before drain: %v", err)
	}
	if beforeDrain.Goal == nil || beforeDrain.Goal.ID != initialGoal.ID || beforeDrain.Goal.Objective != initialGoal.Objective {
		t.Fatalf("ShowGoal before drain = %+v, want prior committed goal %+v", beforeDrain.Goal, initialGoal)
	}

	close(client.release)
	released = true
	select {
	case err := <-turnDone:
		if err != nil {
			t.Fatalf("SubmitUserTurn: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for active step drain")
	}

	afterDrain, err := service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("ShowGoal after drain: %v", err)
	}
	if afterDrain.Goal != nil {
		t.Fatalf("ShowGoal after drain = %+v, want queued clear to remove goal", afterDrain.Goal)
	}
}

func TestServiceWorkflowRuntimeAllowsGoalControl(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{
		WorkflowRun: &workflowruntime.Config{
			Contract: workflowruntime.CompletionContract{RunID: workflow.RunID("run-1")},
		},
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	engine.SetQuestionsEnabled(false)
	resp, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "req-goal-workflow",
		SessionID:       store.Meta().SessionID,
		Objective:       "steer the workflow",
		Actor:           "user",
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
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{
		WorkflowRun: &workflowruntime.Config{
			Contract: workflowruntime.CompletionContract{RunID: workflow.RunID("run-1")},
		},
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})

	_, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "req-agent-step-goal",
		SessionID:       store.Meta().SessionID,
		Objective:       "queued by shell",
		Actor:           string(session.GoalActorAgent),
		StepID:          "step-from-shell",
	})
	if err == nil {
		t.Fatal("agent step-scoped workflow goal set mutated directly without an active step")
	}
	if goal := engine.Goal(); goal != nil {
		t.Fatalf("agent step-scoped workflow goal set bypassed queue and mutated goal: %+v", goal)
	}
}

func TestServiceWorkflowSessionGoalMutationAllowed(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{
		WorkflowRun: &workflowruntime.Config{
			Contract: workflowruntime.CompletionContract{RunID: workflow.RunID("run-1")},
		},
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})

	resp, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "req-goal-workflow-busy-gate",
		SessionID:       store.Meta().SessionID,
		Objective:       "steer despite the held lease",
		Actor:           "user",
	})
	if err != nil {
		t.Fatalf("SetGoal in workflow runtime = %v, want allowed", err)
	}
	if resp.Goal == nil || resp.Goal.Status != string(session.GoalStatusActive) {
		t.Fatalf("goal response = %+v, want active goal", resp.Goal)
	}
}

func TestServiceWorkflowAgentStepGoalCompleteDoesNotBypassStepQueue(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{
		WorkflowRun: &workflowruntime.Config{
			Contract: workflowruntime.CompletionContract{RunID: workflow.RunID("run-1")},
		},
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	sessionID := store.Meta().SessionID
	if _, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{ClientRequestID: "set-user-goal", SessionID: sessionID, Objective: "workflow goal", Actor: "user"}); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	_, err := service.CompleteGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{
		ClientRequestID: "complete-agent-step-goal",
		SessionID:       sessionID,
		Actor:           string(session.GoalActorAgent),
		StepID:          "step-from-shell",
	})
	if err == nil {
		t.Fatal("agent step-scoped workflow goal complete mutated directly without an active step")
	}
	if goal := engine.Goal(); goal == nil || goal.Status != session.GoalStatusActive {
		t.Fatalf("agent step-scoped workflow goal complete bypassed queue; goal = %+v, want active", goal)
	}
}

func TestServiceRetiredAgentStepGoalMutationsAreRejectedWithoutMutation(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	initial, err := engine.SetGoal("existing goal", session.GoalActorUser)
	if err != nil {
		t.Fatalf("set initial goal: %v", err)
	}
	noticeCount := len(runtimeControlGoalDeveloperMessages(t, store))
	turnDone := make(chan error, 1)
	go func() {
		_, submitErr := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "retired-agent-step", "start a real step"))
		turnDone <- submitErr
	}()
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for live step")
	}
	active := engine.ActiveRun()
	if active == nil || active.RunID == "" || active.StepID == "" {
		t.Fatalf("active run = %+v, want real run and step identifiers", active)
	}
	close(client.release)
	if submitErr := <-turnDone; submitErr != nil {
		t.Fatalf("submit user turn: %v", submitErr)
	}
	if closeErr := service.authority.Close(context.Background()); closeErr != nil {
		t.Fatalf("retire runtime: %v", closeErr)
	}

	_, setErr := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "retired-agent-step-set",
		SessionID:       store.Meta().SessionID,
		Objective:       "stale replacement",
		Actor:           string(session.GoalActorAgent),
		RunID:           active.RunID,
		StepID:          active.StepID,
	})
	if !errors.Is(setErr, runtime.ErrAgentGoalStepInactive) {
		t.Fatalf("retired agent set error = %v, want inactive step", setErr)
	}
	_, completeErr := service.CompleteGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{
		ClientRequestID: "retired-agent-step-complete",
		SessionID:       store.Meta().SessionID,
		Actor:           string(session.GoalActorAgent),
		RunID:           active.RunID,
		StepID:          active.StepID,
	})
	if !errors.Is(completeErr, runtime.ErrAgentGoalStepInactive) {
		t.Fatalf("retired agent complete error = %v, want inactive step", completeErr)
	}
	if goal := store.Meta().Goal; goal == nil || goal.ID != initial.ID || goal.Status != initial.Status || goal.Objective != initial.Objective {
		t.Fatalf("goal after retired agent commands = %+v, want %+v", goal, initial)
	}
	if count := len(runtimeControlGoalDeveloperMessages(t, store)); count != noticeCount {
		t.Fatalf("goal notices after retired agent commands = %d, want %d", count, noticeCount)
	}
}

func TestServiceAgentGoalWithoutStepSucceedsDormant(t *testing.T) {
	store, _ := newRuntimeControlTestEngine(t, nil, nil, runtime.Config{})
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: t.TempDir(),
		StoreOptions:    runtimeControlTestSessionPersistence.Options(),
	})
	t.Cleanup(func() {
		if closeErr := authority.Close(context.Background()); closeErr != nil {
			t.Errorf("close dormant authority: %v", closeErr)
		}
	})
	service := NewService(authority).WithPersistedSessionResolver(runtimeControlTestSessionPersistence)

	resp, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "dormant-agent-goal",
		SessionID:       store.Meta().SessionID,
		Objective:       "agent dormant goal",
		Actor:           string(session.GoalActorAgent),
		RunID:           "run-without-step",
	})
	if err != nil {
		t.Fatalf("set non-step-scoped dormant agent goal: %v", err)
	}
	if resp.Goal == nil || resp.Goal.Objective != "agent dormant goal" || resp.Goal.Status != string(session.GoalStatusActive) {
		t.Fatalf("dormant agent response = %+v, want active goal", resp.Goal)
	}
	if count := len(runtimeControlGoalDeveloperMessages(t, store)); count != 1 {
		t.Fatalf("dormant agent goal notices = %d, want 1", count)
	}
	sessionID, parseErr := runtimeids.ParseSessionID(store.Meta().SessionID)
	if parseErr != nil {
		t.Fatalf("parse session id: %v", parseErr)
	}
	if runtimeErr := authority.WithCurrentRuntime(context.Background(), sessionID, func(context.Context, *runtime.Engine) error {
		return nil
	}); !errors.Is(runtimeErr, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("dormant agent runtime access error = %v, want unavailable", runtimeErr)
	}
}

func TestServiceDormantGoalLifecycleAppendsOneNoticePerEffectiveMutation(t *testing.T) {
	store, _ := newRuntimeControlTestEngine(t, nil, nil, runtime.Config{})
	authority := sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{
		PersistenceRoot: t.TempDir(),
		StoreOptions:    runtimeControlTestSessionPersistence.Options(),
	})
	t.Cleanup(func() {
		if closeErr := authority.Close(context.Background()); closeErr != nil {
			t.Errorf("close dormant authority: %v", closeErr)
		}
	})
	service := NewService(authority).WithPersistedSessionResolver(runtimeControlTestSessionPersistence)
	sessionID := store.Meta().SessionID
	noticeCount := func() int {
		return len(runtimeControlGoalDeveloperMessages(t, store))
	}
	assertGoal := func(response serverapi.RuntimeGoalShowResponse, status session.GoalStatus, objective string) {
		t.Helper()
		if response.Goal == nil || response.Goal.Status != string(status) || response.Goal.Objective != objective {
			t.Fatalf("goal response = %+v, want %q %q", response.Goal, status, objective)
		}
	}

	set, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "dormant-lifecycle-set",
		SessionID:       sessionID,
		Objective:       "finish dormant lifecycle",
		Actor:           string(session.GoalActorUser),
	})
	if err != nil {
		t.Fatalf("set dormant goal: %v", err)
	}
	assertGoal(set, session.GoalStatusActive, "finish dormant lifecycle")
	if count := noticeCount(); count != 1 {
		t.Fatalf("notices after set = %d, want 1", count)
	}

	pauseRequest := serverapi.RuntimeGoalStatusRequest{SessionID: sessionID, Actor: string(session.GoalActorUser)}
	pauseRequest.ClientRequestID = "dormant-lifecycle-pause"
	paused, err := service.PauseGoal(context.Background(), pauseRequest)
	if err != nil {
		t.Fatalf("pause dormant goal: %v", err)
	}
	assertGoal(paused, session.GoalStatusPaused, "finish dormant lifecycle")
	if count := noticeCount(); count != 2 {
		t.Fatalf("notices after pause = %d, want 2", count)
	}
	pauseRequest.ClientRequestID = "dormant-lifecycle-pause-noop"
	if _, err := service.PauseGoal(context.Background(), pauseRequest); err != nil {
		t.Fatalf("pause dormant goal no-op: %v", err)
	}
	if count := noticeCount(); count != 2 {
		t.Fatalf("notices after paused no-op = %d, want 2", count)
	}

	resumeRequest := serverapi.RuntimeGoalStatusRequest{SessionID: sessionID, Actor: string(session.GoalActorUser)}
	resumeRequest.ClientRequestID = "dormant-lifecycle-resume"
	resumed, err := service.ResumeGoal(context.Background(), resumeRequest)
	if err != nil {
		t.Fatalf("resume dormant goal: %v", err)
	}
	assertGoal(resumed, session.GoalStatusActive, "finish dormant lifecycle")
	if count := noticeCount(); count != 3 {
		t.Fatalf("notices after resume = %d, want 3", count)
	}
	resumeRequest.ClientRequestID = "dormant-lifecycle-resume-noop"
	if _, err := service.ResumeGoal(context.Background(), resumeRequest); err != nil {
		t.Fatalf("resume dormant goal no-op: %v", err)
	}
	if count := noticeCount(); count != 3 {
		t.Fatalf("notices after active no-op = %d, want 3", count)
	}

	completeRequest := serverapi.RuntimeGoalStatusRequest{SessionID: sessionID, Actor: string(session.GoalActorUser)}
	completeRequest.ClientRequestID = "dormant-lifecycle-complete"
	completed, err := service.CompleteGoal(context.Background(), completeRequest)
	if err != nil {
		t.Fatalf("complete dormant goal: %v", err)
	}
	assertGoal(completed, session.GoalStatusComplete, "finish dormant lifecycle")
	if count := noticeCount(); count != 4 {
		t.Fatalf("notices after complete = %d, want 4", count)
	}
	completeRequest.ClientRequestID = "dormant-lifecycle-complete-noop"
	if _, err := service.CompleteGoal(context.Background(), completeRequest); err != nil {
		t.Fatalf("complete dormant goal no-op: %v", err)
	}
	if count := noticeCount(); count != 4 {
		t.Fatalf("notices after complete no-op = %d, want 4", count)
	}

	reopened, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "dormant-lifecycle-reopen",
		SessionID:       sessionID,
		Objective:       "reopened dormant goal",
		Actor:           string(session.GoalActorUser),
	})
	if err != nil {
		t.Fatalf("reopen completed dormant goal: %v", err)
	}
	assertGoal(reopened, session.GoalStatusActive, "reopened dormant goal")
	if count := noticeCount(); count != 5 {
		t.Fatalf("notices after reopen = %d, want 5", count)
	}

	cleared, err := service.ClearGoal(context.Background(), serverapi.RuntimeGoalClearRequest{
		ClientRequestID: "dormant-lifecycle-clear",
		SessionID:       sessionID,
		Actor:           string(session.GoalActorUser),
	})
	if err != nil {
		t.Fatalf("clear dormant goal: %v", err)
	}
	if cleared.Goal != nil {
		t.Fatalf("clear response goal = %+v, want nil", cleared.Goal)
	}
	if count := noticeCount(); count != 6 {
		t.Fatalf("notices after clear = %d, want 6", count)
	}
	shown, err := service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: sessionID})
	if err != nil {
		t.Fatalf("show cleared dormant goal: %v", err)
	}
	if shown.Goal != nil {
		t.Fatalf("shown cleared goal = %+v, want nil", shown.Goal)
	}
	parsedSessionID, parseErr := runtimeids.ParseSessionID(sessionID)
	if parseErr != nil {
		t.Fatalf("parse session id: %v", parseErr)
	}
	if runtimeErr := authority.WithCurrentRuntime(context.Background(), parsedSessionID, func(context.Context, *runtime.Engine) error {
		return nil
	}); !errors.Is(runtimeErr, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("dormant lifecycle runtime access error = %v, want unavailable", runtimeErr)
	}
}

func TestServiceWorkflowRuntimeAllowsGoalStatusTransitions(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{
		WorkflowRun: &workflowruntime.Config{
			Contract: workflowruntime.CompletionContract{RunID: workflow.RunID("run-1")},
		},
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	sessionID := store.Meta().SessionID
	if _, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{ClientRequestID: "set", SessionID: sessionID, Objective: "workflow goal", Actor: "user"}); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if _, err := service.PauseGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{ClientRequestID: "pause", SessionID: sessionID, Actor: "user"}); err != nil {
		t.Fatalf("PauseGoal: %v", err)
	}
	if goal := engine.Goal(); goal == nil || goal.Status != session.GoalStatusPaused {
		t.Fatalf("goal after pause = %+v, want paused", goal)
	}
	engine.SetQuestionsEnabled(false)
	if _, err := service.ResumeGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{ClientRequestID: "resume", SessionID: sessionID, Actor: "user"}); err != nil {
		t.Fatalf("ResumeGoal: %v", err)
	}
	if goal := engine.Goal(); goal == nil || goal.Status != session.GoalStatusActive {
		t.Fatalf("goal after resume = %+v, want active", goal)
	}
	if _, err := service.CompleteGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{ClientRequestID: "complete", SessionID: sessionID, Actor: "user"}); err != nil {
		t.Fatalf("CompleteGoal: %v", err)
	}
	if goal := engine.Goal(); goal == nil || goal.Status != session.GoalStatusComplete {
		t.Fatalf("goal after complete = %+v, want complete", goal)
	}
}

func TestServiceDurableWorkflowSessionAllowsGoalControl(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	if err := store.SetWorkflowSessionState(&session.WorkflowSessionState{RunID: "run-1", TaskID: "task-1", WorkflowID: "workflow-1"}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}
	service = service.WithWorkflowSessionResolver(staticRuntimeControlSessionResolver{store: store})
	if _, err := service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: store.Meta().SessionID}); err != nil {
		t.Fatalf("ShowGoal for durable workflow session = %v, want allowed", err)
	}
}

func TestServiceDurableWorkflowSessionRejectsAutoCompactionDisable(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{})
	if err := store.SetWorkflowSessionState(&session.WorkflowSessionState{RunID: "run-1", TaskID: "task-1", WorkflowID: "workflow-1"}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}
	service = service.WithWorkflowSessionResolver(staticRuntimeControlSessionResolver{store: store})

	_, err := service.SetAutoCompactionEnabled(context.Background(), serverapi.RuntimeSetAutoCompactionEnabledRequest{
		ClientRequestID: "req-auto-off-durable-workflow",
		SessionID:       store.Meta().SessionID,
		Enabled:         false,
	})
	if !errors.Is(err, errWorkflowTaskSessionAutoCompactionDisable) {
		t.Fatalf("SetAutoCompactionEnabled error = %v, want workflow auto-compaction rejection", err)
	}
	if !engine.AutoCompactionEnabled() {
		t.Fatal("auto-compaction disabled despite durable workflow session marker")
	}
}

func TestServiceSetGoalMemoNormalizesObjectiveWhitespace(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, &blockingRuntimeControlClient{}, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})

	req := serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "goal-set-retry",
		SessionID:       store.Meta().SessionID,
		Objective:       "  ship memo goal  ",
		Actor:           "user",
	}
	first, err := service.SetGoal(context.Background(), req)
	if err != nil {
		t.Fatalf("SetGoal first: %v", err)
	}
	req.Objective = "ship memo goal"
	second, err := service.SetGoal(context.Background(), req)
	if err != nil {
		t.Fatalf("SetGoal equivalent retry: %v", err)
	}
	if first.Goal == nil || second.Goal == nil || first.Goal.ID != second.Goal.ID {
		t.Fatalf("retry goal = %+v, want same id as %+v", second.Goal, first.Goal)
	}
	if messages := runtimeControlGoalDeveloperMessages(t, store); len(messages) != 1 {
		t.Fatalf("goal developer message count = %d, want 1", len(messages))
	}
}

func TestServiceSetGoalAllowsAgentWithoutExistingGoal(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, &blockingRuntimeControlClient{}, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})

	resp, err := service.SetGoal(context.Background(), serverapi.RuntimeGoalSetRequest{
		ClientRequestID: "agent-goal-set",
		SessionID:       store.Meta().SessionID,
		Objective:       "agent self-goal",
		Actor:           "agent",
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
				ClientRequestID: "agent-goal-overwrite-" + tt.name,
				SessionID:       store.Meta().SessionID,
				Objective:       "agent replacement",
				Actor:           "agent",
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
		ClientRequestID: "agent-goal-after-complete",
		SessionID:       store.Meta().SessionID,
		Objective:       "next goal",
		Actor:           "agent",
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
		ClientRequestID: "goal-set-ask-disabled",
		SessionID:       store.Meta().SessionID,
		Objective:       "ship goal mode",
		Actor:           "user",
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
		ClientRequestID: "goal-resume-ask-disabled",
		SessionID:       store.Meta().SessionID,
		Actor:           "user",
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
	if _, err := service.CompleteGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{ClientRequestID: "complete-1", SessionID: store.Meta().SessionID, Actor: "agent"}); err != nil {
		t.Fatalf("CompleteGoal first: %v", err)
	}
	before, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("ReadEvents before: %v", err)
	}
	if _, err := service.CompleteGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{ClientRequestID: "complete-2", SessionID: store.Meta().SessionID, Actor: "agent"}); err != nil {
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
	client := newCancelObservingRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	goal, err := engine.SetGoal("ship goal mode", session.GoalActorUser)
	if err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop: %v", err)
	}
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for running goal loop")
	}
	defer func() {
		_, _ = engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorSystem)
		close(client.release)
	}()
	before, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("CollectEvents before: %v", err)
	}
	resp, err := service.ResumeGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{
		ClientRequestID: "resume-active-1",
		SessionID:       store.Meta().SessionID,
		Actor:           "user",
	})
	if err != nil {
		t.Fatalf("ResumeGoal: %v", err)
	}
	if resp.Goal == nil || resp.Goal.ID != goal.ID || resp.Goal.Status != string(session.GoalStatusActive) {
		t.Fatalf("resume active response = %+v, want existing active goal", resp.Goal)
	}
	after, err := sessiontest.CollectRecords(store)
	if err != nil {
		t.Fatalf("CollectEvents after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("resume active appended %d events, want no duplicate goal status event", len(after)-len(before))
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
		ClientRequestID: "resume-ownerless-active-1",
		SessionID:       store.Meta().SessionID,
		Actor:           "user",
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
		_, _ = engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorSystem)
		close(client.release)
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

	resp, err := service.ResumeGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{
		ClientRequestID: "resume-suspending-active-1",
		SessionID:       store.Meta().SessionID,
		Actor:           "user",
	})
	if err != nil {
		t.Fatalf("ResumeGoal: %v", err)
	}
	if resp.Goal == nil || resp.Goal.ID != goal.ID || resp.Goal.Status != string(session.GoalStatusActive) {
		t.Fatalf("resume suspending active response = %+v, want existing active goal", resp.Goal)
	}
	client.releaseFirst()
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
	_, _ = engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorSystem)
	client.releaseSecond()
}

func TestServiceSetSessionNameDedupesSuccessfulRetry(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeShellHandler{}}), runtime.Config{})
	if err := store.SetName("before"); err != nil {
		t.Fatalf("persist initial session name: %v", err)
	}
	req := serverapi.RuntimeSetSessionNameRequest{
		ClientRequestID: "req-1",
		SessionID:       store.Meta().SessionID,
		Name:            "after",
	}

	if err := service.SetSessionName(context.Background(), req); err != nil {
		t.Fatalf("SetSessionName first: %v", err)
	}
	if err := service.SetSessionName(context.Background(), req); err != nil {
		t.Fatalf("SetSessionName replay: %v", err)
	}
	if got := store.Meta().Name; got != "after" {
		t.Fatalf("session name = %q, want after", got)
	}
	if reopened, err := runtimeControlTestSessionPersistence.Open(store.Dir()); err != nil {
		t.Fatalf("reopen session store: %v", err)
	} else if got := reopened.Meta().Name; got != "after" {
		t.Fatalf("reopened session name = %q, want after", got)
	}
}

func TestServiceSubmitUserTurnDedupesSuccessfulRetry(t *testing.T) {
	client := finalResponseRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	req := runtimeControlUserTurnRequest(store, "req-1", "hello")

	first, err := service.SubmitUserTurn(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitUserTurn first: %v", err)
	}
	second, err := service.SubmitUserTurn(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitUserTurn retry: %v", err)
	}
	if first.Message != "done" || second.Message != "done" {
		t.Fatalf("responses = (%q, %q), want both done", first.Message, second.Message)
	}
	if client.calls != 1 {
		t.Fatalf("generate call count = %d, want 1", client.calls)
	}
}

func TestServiceSubmitUserTurnOperationTombstonePreventsPreActiveBegin(t *testing.T) {
	client := finalResponseRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	operations := runtimeops.NewCoordinator()
	service.WithOperationCoordinator(operations)
	req := runtimeControlUserTurnRequest(store, "req-canceled", "hello")
	if err := operations.CancelOperation(store.Meta().SessionID, req.OperationRef); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}

	if _, err := service.SubmitUserTurn(context.Background(), req); !errors.Is(err, runtimeops.ErrOperationCanceled) {
		t.Fatalf("SubmitUserTurn after tombstone error = %v, want operation canceled", err)
	}
	if client.calls != 0 {
		t.Fatalf("generate call count = %d, want 0", client.calls)
	}
	assertRuntimeControlReconciliation(t, operations, store.Meta().SessionID, req.OperationRef, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestServiceSubmitUserTurnRecordsCommittedAtFlushBeforeAssistantCompletion(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	ref := runtimeControlOperationRef(clientui.RuntimeOperationKindSubmit)
	req := serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: ref.ClientRequestID.String(),
		SessionID:       store.Meta().SessionID,
		Text:            "flush before model completes",
		OperationRef:    ref,
		PreSubmitCompactionOperationRef: runtimeControlOperationRef(
			clientui.RuntimeOperationKindPreSubmitCompact,
		),
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), req)
		done <- err
	}()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("submit did not reach model request after user-message flush")
	}
	if _, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID:    "interrupt-flushed-submit",
		SessionID:          req.SessionID,
		TargetOperationRef: &ref,
	}); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	select {
	case <-client.ctxCanceled:
	case <-time.After(time.Second):
		t.Fatal("active submit interrupt did not reach engine interrupt path")
	}
	close(client.release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SubmitUserTurn error = %v, want context canceled after engine interrupt", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for interrupted submit")
	}
	assertRuntimeControlReconciliation(t, service.operations, req.SessionID, ref, clientui.RuntimeInputReconciliationCommitted)
}

func TestServiceInterruptWithTargetRecordsCancellationTombstoneWithoutRuntime(t *testing.T) {
	operations := runtimeops.NewCoordinator()
	service := NewService(sessionruntime.NewAuthority(sessionruntime.AuthorityOptions{})).WithOperationCoordinator(operations)
	ref := runtimeControlOperationRef(clientui.RuntimeOperationKindSubmit)
	sessionID := "018fdd67-89ab-4cde-8123-456789abcdef"
	resp, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID:    "interrupt-target",
		SessionID:          sessionID,
		TargetOperationRef: &ref,
	})
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if resp.Activity.State != clientui.RuntimeActivityUnavailable {
		t.Fatalf("activity = %+v, want unavailable", resp.Activity)
	}
	assertRuntimeControlReconciliation(t, operations, sessionID, ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
	if len(resp.InputReconciliation.Operations) != 1 || resp.InputReconciliation.Operations[0].Operation != ref || resp.InputReconciliation.Operations[0].State != clientui.RuntimeInputReconciliationCanceledNotCommitted {
		t.Fatalf("response reconciliation = %+v, want canceled target", resp.InputReconciliation)
	}
}

func TestServiceTargetedPreActiveInterruptDoesNotInterruptUnrelatedActiveRun(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	activeDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "active unrelated turn")
		activeDone <- err
	}()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("active run did not start")
	}
	operations := runtimeops.NewCoordinator()
	service.WithOperationCoordinator(operations)
	target := runtimeControlOperationRef(clientui.RuntimeOperationKindSubmit)
	if _, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID:    "interrupt-pre-active-target",
		SessionID:          store.Meta().SessionID,
		TargetOperationRef: &target,
	}); err != nil {
		t.Fatalf("Interrupt targeted pre-active: %v", err)
	}
	select {
	case <-client.ctxCanceled:
		t.Fatal("targeted pre-active cancellation interrupted unrelated active run")
	case <-time.After(50 * time.Millisecond):
	}
	close(client.release)
	select {
	case err := <-activeDone:
		if err != nil {
			t.Fatalf("active run after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active run did not finish after release")
	}
	assertRuntimeControlReconciliation(t, operations, store.Meta().SessionID, target, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestServiceTargetedTerminalOperationDoesNotInterruptUnrelatedActiveRun(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	activeDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "active unrelated turn")
		activeDone <- err
	}()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("active run did not start")
	}
	operations := runtimeops.NewCoordinator()
	target := runtimeControlOperationRef(clientui.RuntimeOperationKindSubmit)
	operations.RecordCommitted(store.Meta().SessionID, target)
	service.WithOperationCoordinator(operations)
	if _, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID:    "interrupt-terminal-target",
		SessionID:          store.Meta().SessionID,
		TargetOperationRef: &target,
	}); err != nil {
		t.Fatalf("Interrupt targeted terminal: %v", err)
	}
	select {
	case <-client.ctxCanceled:
		t.Fatal("targeted terminal cancellation interrupted unrelated active run")
	case <-time.After(50 * time.Millisecond):
	}
	close(client.release)
	select {
	case err := <-activeDone:
		if err != nil {
			t.Fatalf("active run after release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active run did not finish after release")
	}
	assertRuntimeControlReconciliation(t, operations, store.Meta().SessionID, target, clientui.RuntimeInputReconciliationCommitted)
}

func assertRuntimeControlReconciliation(t *testing.T, operations *runtimeops.Coordinator, sessionID string, ref clientui.RuntimeOperationRef, want clientui.RuntimeInputReconciliationState) {
	t.Helper()
	snapshot := runtimeControlFeedSnapshot(t, operations, sessionID, []clientui.RuntimeOperationRef{ref})
	for _, record := range snapshot.Operations {
		if record.Operation.Key() == ref.Key() {
			if record.State != want {
				t.Fatalf("reconciliation state = %q, want %q", record.State, want)
			}
			return
		}
	}
	t.Fatalf("missing reconciliation for %+v in %+v", ref, snapshot.Operations)
}

func TestServiceSubmitUserShellCommandDedupesSuccessfulRetry(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeShellHandler{}}), runtime.Config{})
	req := runtimeControlShellCommandRequest(store, "req-1", "pwd")

	if err := service.SubmitUserShellCommand(context.Background(), req); err != nil {
		t.Fatalf("SubmitUserShellCommand first: %v", err)
	}
	afterFirst := countDirectShellCommandMessages(t, store, "pwd")
	if afterFirst != 1 {
		t.Fatalf("direct shell message count after first call = %d, want 1", afterFirst)
	}
	if err := service.SubmitUserShellCommand(context.Background(), req); err != nil {
		t.Fatalf("SubmitUserShellCommand replay: %v", err)
	}
	afterReplay := countDirectShellCommandMessages(t, store, "pwd")
	if afterReplay != 1 {
		t.Fatalf("direct shell message count after replay = %d, want 1", afterReplay)
	}
}

func TestServiceSubmitUserShellCommandDoesNotRecordPromptHistory(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeShellHandler{}}), runtime.Config{})
	req := runtimeControlShellCommandRequest(store, "req-1", "pwd")

	if err := service.SubmitUserShellCommand(context.Background(), req); err != nil {
		t.Fatalf("SubmitUserShellCommand: %v", err)
	}
	history := service.promptStore.(*runtimeControlPromptHistoryStore)
	if err := service.SubmitUserShellCommand(context.Background(), req); err != nil {
		t.Fatalf("SubmitUserShellCommand replay: %v", err)
	}
	if got := history.CountText("$ pwd"); got != 0 {
		t.Fatalf("shell prompt history count = %d, want 0", got)
	}
}

func TestServiceSubmitUserShellCommandRejectsClientRequestIDPayloadMismatch(t *testing.T) {
	store, _, service := newRuntimeControlTestService(t, nil, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeShellHandler{}}), runtime.Config{})
	first := runtimeControlShellCommandRequest(store, "req-1", "pwd")
	if err := service.SubmitUserShellCommand(context.Background(), first); err != nil {
		t.Fatalf("SubmitUserShellCommand first: %v", err)
	}
	second := first
	second.Command = "ls"
	if err := service.SubmitUserShellCommand(context.Background(), second); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("SubmitUserShellCommand mismatch error = %v, want request id payload mismatch", err)
	}
	if got := countDirectShellCommandMessages(t, store, "pwd"); got != 1 {
		t.Fatalf("direct shell message count = %d, want 1", got)
	}
}

func TestServiceQueuedSteeringDrainsAtNextSafeBoundary(t *testing.T) {
	client := newSteeringDrainRuntimeControlClient()
	queuedStatuses := make(chan runtime.QueuedUserMessageStatusEvent, 4)
	registry := tools.NewRegistry(tools.HandlerRegistration{
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
	submitDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(
			context.Background(),
			runtimeControlUserTurnRequest(store, "active-turn", "start"),
		)
		submitDone <- err
	}()
	select {
	case <-client.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("active turn did not reach the first model request")
	}
	queuedText := "use the existing lld installation"
	steeringReq := runtimeControlUserTurnRequest(store, "queued-steering", queuedText)
	steered, err := service.SubmitUserTurn(
		context.Background(),
		steeringReq,
	)
	if err != nil {
		t.Fatalf("SubmitUserTurn while model was thinking: %v", err)
	}
	if !steered.Steered || steered.QueueItemID == "" {
		t.Fatalf("SubmitUserTurn while model was thinking = %+v, want accepted steering", steered)
	}
	select {
	case status := <-queuedStatuses:
		if status.ClientRequestID != steeringReq.OperationRef.ClientRequestID.String() {
			t.Fatalf(
				"accepted steering client request id = %q, want %q",
				status.ClientRequestID,
				steeringReq.OperationRef.ClientRequestID,
			)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted steering emitted no queue status")
	}
	close(client.releaseFirst)
	select {
	case <-client.secondStarted:
	case <-time.After(time.Second):
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

func TestServiceInterruptDiscardsPendingSteeringBeforeStoppingActiveRun(t *testing.T) {
	client := newSteeringDrainRuntimeControlClient()
	defer close(client.releaseFirst)
	defer close(client.releaseSecond)
	registry := tools.NewRegistry(tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: fakeShellHandler{},
	})
	store, engine, service := newRuntimeControlTestService(t, client, registry, runtime.Config{})
	activeReq := runtimeControlUserTurnRequest(store, "active-turn", "start")
	submitDone := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), activeReq)
		submitDone <- err
	}()
	select {
	case <-client.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("active turn did not reach model thinking")
	}

	steeringReq := runtimeControlUserTurnRequest(store, "queued-steering", "do not continue after interrupt")
	steered, err := service.SubmitUserTurn(context.Background(), steeringReq)
	if err != nil {
		t.Fatalf("SubmitUserTurn steering: %v", err)
	}
	queueItemID := mustRuntimeControlQueueItemID(t, steered.QueueItemID)
	queuedRef := clientui.RuntimeOperationRef{
		Kind:            clientui.RuntimeOperationKindQueuedMessage,
		ClientRequestID: steeringReq.OperationRef.ClientRequestID,
		QueueItemID:     &queueItemID,
	}
	runID := mustRuntimeControlRunID(t)
	stepID := mustRuntimeControlStepID(t)
	service.WithRuntimeActivityResolver(&sequenceRuntimeActivityResolver{
		snapshots: []runtimeactivity.ResponseSnapshot{
			{
				Version: clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 1},
				Activity: clientui.RuntimeActivity{
					State: clientui.RuntimeActivityRunning,
					ActiveStep: &clientui.RuntimeActiveStep{
						RunID:      runID,
						StepID:     stepID,
						ActiveKind: clientui.RuntimeActivityActiveKindUserTurn,
					},
				},
			},
			{
				Version:  clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 2},
				Activity: clientui.RuntimeActivity{State: clientui.RuntimeActivityRegisteredIdle, QueueAccepting: true},
			},
		},
	})

	resp, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID:      "interrupt-active-with-steering",
		SessionID:            store.Meta().SessionID,
		TargetOperationRef:   &activeReq.OperationRef,
		PendingOperationRefs: []clientui.RuntimeOperationRef{activeReq.OperationRef, queuedRef},
	})
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("interrupt left accepted steering queued")
	}
	for _, record := range resp.InputReconciliation.Operations {
		if record.Operation.Key() == queuedRef.Key() {
			if record.State != clientui.RuntimeInputReconciliationCanceledNotCommitted {
				t.Fatalf("queued steering reconciliation = %q, want canceled_not_committed", record.State)
			}
			goto reconciled
		}
	}
	t.Fatalf("interrupt response omitted queued steering reconciliation: %+v", resp.InputReconciliation.Operations)

reconciled:
	select {
	case <-client.secondStarted:
		t.Fatal("queued steering started a model continuation after interrupt")
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case err := <-submitDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("active submit error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("active submit did not stop after interrupt")
	}
}

func TestServiceDiscardQueuedUserMessageIsRuntimeOnly(t *testing.T) {
	ctx := context.Background()
	sessionStore, engine, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	queued := engine.QueueUserMessageWithClientRequestID("discard runtime only", runtimeids.NewRuntimeClientRequestID().String())
	discardReq := serverapi.RuntimeDiscardQueuedUserMessageRequest{
		ClientRequestID: "req-discard-runtime",
		SessionID:       sessionStore.Meta().SessionID,
		QueueItemID:     queued.ID,
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

func TestServiceInterruptTargetQueuedServerMessageDiscardsRuntimeWork(t *testing.T) {
	ctx := context.Background()
	sessionStore, engine, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	target := runtimeControlOperationRef(clientui.RuntimeOperationKindQueuedMessage)
	queued := engine.QueueUserMessageWithClientRequestID("discard on interrupt", target.ClientRequestID.String())
	queueItemID := mustRuntimeControlQueueItemID(t, queued.ID)
	target.QueueItemID = &queueItemID
	if err := service.operations.RecordQueuedMessageStatus(
		sessionStore.Meta().SessionID,
		target,
		clientui.RuntimeInputReconciliationAccepted,
	); err != nil {
		t.Fatalf("record accepted queued message: %v", err)
	}

	if _, err := service.Interrupt(ctx, serverapi.RuntimeInterruptRequest{
		ClientRequestID:      "interrupt-queued-server",
		SessionID:            sessionStore.Meta().SessionID,
		TargetOperationRef:   &target,
		PendingOperationRefs: []clientui.RuntimeOperationRef{target},
	}); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}

	if engine.HasQueuedUserWork() {
		t.Fatal("queued runtime work remained after interrupt targeted server queue item")
	}
	assertRuntimeControlReconciliation(t, service.operations, sessionStore.Meta().SessionID, target, clientui.RuntimeInputReconciliationCanceledNotCommitted)
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
	ref := runtimeControlOperationRef(clientui.RuntimeOperationKindSubmit)
	return serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: ref.ClientRequestID.String(),
		SessionID:       store.Meta().SessionID,
		Text:            text,
		OperationRef:    ref,
		PreSubmitCompactionOperationRef: runtimeControlOperationRef(
			clientui.RuntimeOperationKindPreSubmitCompact,
		),
	}
}

func runtimeControlShellCommandRequest(store *session.Store, _ string, command string) serverapi.RuntimeSubmitUserShellCommandRequest {
	ref := runtimeControlOperationRef(clientui.RuntimeOperationKindUserShell)
	return serverapi.RuntimeSubmitUserShellCommandRequest{
		ClientRequestID: ref.ClientRequestID.String(),
		SessionID:       store.Meta().SessionID,
		Command:         command,
		OperationRef:    ref,
	}
}
