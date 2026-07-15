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
	"core/server/registry"
	"core/server/requestmemo"
	"core/server/runtime"
	"core/server/runtimeops"
	"core/server/runtimeview"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/clientui"
	"core/shared/serverapi"
	"core/shared/sessioncontract"
	"core/shared/toolspec"
)

type stubRuntimeResolver struct {
	engine *runtime.Engine
}

func (s stubRuntimeResolver) ResolveRuntime(context.Context, string) (*runtime.Engine, error) {
	return s.engine, nil
}

func (s stubRuntimeResolver) WithGuardedRuntime(_ context.Context, _ string, fn func(*runtime.Engine) error) (bool, error) {
	if s.engine == nil {
		return false, nil
	}
	return true, fn(s.engine)
}

func (s stubRuntimeResolver) BeginSessionRun(string) (func(), bool) { return func() {}, true }

func (s stubRuntimeResolver) BeginCancellableSessionRun(string) (context.Context, func(), bool) {
	return context.Background(), func() {}, true
}

func (s stubRuntimeResolver) SessionRunsBlocked(string) bool { return false }

type countingBeginRuntimeResolver struct {
	stubRuntimeResolver
	beginCount atomic.Int32
}

func (s *countingBeginRuntimeResolver) BeginSessionRun(string) (func(), bool) {
	s.beginCount.Add(1)
	return func() {}, true
}

func (s *countingBeginRuntimeResolver) BeginCancellableSessionRun(string) (context.Context, func(), bool) {
	s.beginCount.Add(1)
	return context.Background(), func() {}, true
}

type cancelableStartRuntimeResolver struct {
	started  chan struct{}
	released chan struct{}
	once     sync.Once
}

func (s *cancelableStartRuntimeResolver) ResolveRuntime(context.Context, string) (*runtime.Engine, error) {
	return nil, nil
}

func (s *cancelableStartRuntimeResolver) WithGuardedRuntime(ctx context.Context, _ string, fn func(*runtime.Engine) error) (bool, error) {
	_ = fn
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return false, ctx.Err()
}

func (s *cancelableStartRuntimeResolver) BeginSessionRun(string) (func(), bool) {
	return func() {}, true
}

func (s *cancelableStartRuntimeResolver) BeginCancellableSessionRun(string) (context.Context, func(), bool) {
	ctx, cancel := context.WithCancel(context.Background())
	release := func() {
		cancel()
		select {
		case <-s.released:
		default:
			close(s.released)
		}
	}
	return ctx, release, true
}

func (s *cancelableStartRuntimeResolver) SessionRunsBlocked(string) bool { return false }

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
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
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
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
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

type fakeShellHandler struct{}

func (fakeShellHandler) Call(context.Context, tools.Call) (tools.Result, error) {
	return tools.Result{Output: json.RawMessage(`{"output":"ok","exit_code":0,"truncated":false}`)}, nil
}

var runtimeControlTestSessionPersistence = sessiontest.NewPersistence()

func newRuntimeControlTestEngine(t *testing.T, client llm.Client, registry *tools.Registry, cfg runtime.Config, opts ...session.StoreOption) (*session.Store, *runtime.Engine) {
	t.Helper()
	store, err := session.Create(
		t.TempDir(),
		"workspace-x",
		"/tmp/workspace-x",
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
	engine, err := runtime.New(store, client, registry, cfg)
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return store, engine
}

func newRuntimeControlTestService(t *testing.T, client llm.Client, registry *tools.Registry, cfg runtime.Config, opts ...session.StoreOption) (*session.Store, *runtime.Engine, *Service) {
	t.Helper()
	store, engine := newRuntimeControlTestEngine(t, client, registry, cfg, opts...)
	history := newRuntimeControlPromptHistoryStore(store.Meta().SessionID)
	service := NewService(stubRuntimeResolver{engine: engine}).WithPromptHistoryStore(history)
	return store, engine, service
}

func finalResponseRuntimeControlClient() *runtimeControlFakeClient {
	return &runtimeControlFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
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

func TestServiceInterruptReturnsUnavailableActivityWithoutEngine(t *testing.T) {
	service := NewService(stubRuntimeResolver{})
	resp, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID: "interrupt-1",
		SessionID:       "session-1",
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
	if resp.InputReconciliation.Version != resp.Version {
		t.Fatalf("reconciliation version = %+v, want %+v", resp.InputReconciliation.Version, resp.Version)
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
	service := NewService(stubRuntimeResolver{})
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
	service := NewService(stubRuntimeResolver{})
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
	submitDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "keep running")
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
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	submitDone := make(chan error, 1)
	go func() {
		_, err := engine.SubmitUserMessage(context.Background(), "keep running")
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
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x", sessioncontract.SessionCategoryMain, runtimeControlTestSessionPersistence.Options()...)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
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
	if resp.Version.Epoch == "" || resp.InputReconciliation.Version != resp.Version {
		t.Fatalf("invalid versioned response: %+v", resp)
	}
}

func TestServiceGoalMutationsSetShowComplete(t *testing.T) {
	store, engine := newRuntimeControlTestEngine(t, nil, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	service := NewService(stubRuntimeResolver{engine: engine})

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
	store, engine := newRuntimeControlTestEngine(t, nil, nil, runtime.Config{
		WorkflowRun: &workflowruntime.Config{
			Contract: workflowruntime.CompletionContract{RunID: workflow.RunID("run-1")},
		},
		EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
	})
	service := NewService(stubRuntimeResolver{engine: engine})

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
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	goalSetEvents := 0
	for _, evt := range events {
		if evt.Kind == "goal_set" {
			goalSetEvents++
		}
	}
	if goalSetEvents != 1 {
		t.Fatalf("goal_set event count = %d, want 1", goalSetEvents)
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
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	foundReplacement := false
	for _, event := range events {
		if event.Kind != "goal_set" {
			continue
		}
		var payload session.GoalSetEvent
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatalf("decode goal_set event: %v", err)
		}
		if payload.Goal.ID == resp.Goal.ID {
			foundReplacement = true
			if payload.ReplacedGoalID != completed.ID {
				t.Fatalf("replacement replaced_goal_id = %q, want completed goal %q", payload.ReplacedGoalID, completed.ID)
			}
		}
	}
	if !foundReplacement {
		t.Fatalf("replacement goal_set event for goal %q not found in %+v", resp.Goal.ID, events)
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
	events, readErr := sessiontest.CollectEvents(store)
	if readErr != nil {
		t.Fatalf("ReadEvents: %v", readErr)
	}
	if len(events) != 0 {
		t.Fatalf("events persisted after failed preflight: %+v", events)
	}
}

func TestServiceActiveGoalUpdatesEmitExactlyOneGoalStatusEventBeforeGoalLoopEvents(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, *runtime.Engine, func())
		call    func(context.Context, *Service, string) error
	}{
		{
			name: "set",
			call: func(ctx context.Context, service *Service, sessionID string) error {
				_, err := service.SetGoal(ctx, serverapi.RuntimeGoalSetRequest{
					ClientRequestID: "goal-status-set",
					SessionID:       sessionID,
					Objective:       "ship goal mode",
					Actor:           "user",
				})
				return err
			},
		},
		{
			name: "resume",
			prepare: func(t *testing.T, engine *runtime.Engine, resetEvents func()) {
				t.Helper()
				if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
					t.Fatalf("SetGoal: %v", err)
				}
				if _, err := engine.SetGoalStatus(session.GoalStatusPaused, session.GoalActorUser); err != nil {
					t.Fatalf("pause goal: %v", err)
				}
				resetEvents()
			},
			call: func(ctx context.Context, service *Service, sessionID string) error {
				_, err := service.ResumeGoal(ctx, serverapi.RuntimeGoalStatusRequest{
					ClientRequestID: "goal-status-resume",
					SessionID:       sessionID,
					Actor:           "user",
				})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var eventsMu sync.Mutex
			events := make([]runtime.Event, 0, 8)
			resetEvents := func() {
				eventsMu.Lock()
				defer eventsMu.Unlock()
				events = nil
			}
			store, engine := newRuntimeControlTestEngine(t, &blockingRuntimeControlClient{}, nil, runtime.Config{
				EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion},
				OnEvent: func(evt runtime.Event) {
					eventsMu.Lock()
					defer eventsMu.Unlock()
					events = append(events, evt)
				},
			})
			if tt.prepare != nil {
				tt.prepare(t, engine, resetEvents)
			}
			service := NewService(stubRuntimeResolver{engine: engine})

			if err := tt.call(context.Background(), service, store.Meta().SessionID); err != nil {
				t.Fatalf("active goal update: %v", err)
			}

			eventsMu.Lock()
			gotEvents := append([]runtime.Event(nil), events...)
			eventsMu.Unlock()
			statusIndexes := make([]int, 0, 1)
			for idx, evt := range gotEvents {
				if evt.Kind == runtime.EventGoalStatusUpdated {
					statusIndexes = append(statusIndexes, idx)
				}
			}
			if len(statusIndexes) != 1 {
				t.Fatalf("goal status event count = %d, want 1 events=%+v", len(statusIndexes), gotEvents)
			}
			statusIndex := statusIndexes[0]
			if statusIndex == 0 || gotEvents[statusIndex-1].Kind != runtime.EventConversationUpdated || !gotEvents[statusIndex-1].CommittedTranscriptChanged {
				t.Fatalf("goal status event not immediately after committed feedback: events=%+v", gotEvents)
			}
			for idx, evt := range gotEvents[:statusIndex] {
				if evt.Kind == runtime.EventRunStateChanged || evt.Kind == runtime.EventAssistantMessage || evt.Kind == runtime.EventToolCallStarted || evt.Kind == runtime.EventToolCallCompleted {
					t.Fatalf("event[%d]=%+v preceded goal feedback/status", idx, evt)
				}
			}
			finalGoal := runtimeview.StatusFromRuntime(engine).Goal
			status := gotEvents[statusIndex].GoalStatus
			if finalGoal == nil || status == nil || status.Cleared || status.State.ID != finalGoal.ID || status.State.Objective != finalGoal.Objective || string(status.State.Status) != string(finalGoal.Status) {
				t.Fatalf("goal status payload = %+v, final goal = %+v", status, finalGoal)
			}
		})
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

func TestServiceShowGoalReportsRuntimeSuspension(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, engine := newRuntimeControlTestEngine(t, client, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := engine.StartGoalLoop(); err != nil {
		t.Fatalf("StartGoalLoop: %v", err)
	}
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for goal loop to start")
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	defer func() {
		close(client.release)
	}()
	service := NewService(stubRuntimeResolver{engine: engine})

	resp, err := service.ShowGoal(context.Background(), serverapi.RuntimeGoalShowRequest{SessionID: store.Meta().SessionID})
	if err != nil {
		t.Fatalf("ShowGoal: %v", err)
	}
	if resp.Goal == nil || !resp.Goal.Suspended {
		t.Fatalf("goal response = %+v, want suspended", resp.Goal)
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
	before, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents before: %v", err)
	}
	if _, err := service.CompleteGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{ClientRequestID: "complete-2", SessionID: store.Meta().SessionID, Actor: "agent"}); err != nil {
		t.Fatalf("CompleteGoal second: %v", err)
	}
	after, err := sessiontest.CollectEvents(store)
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
	before, err := sessiontest.CollectEvents(store)
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
	after, err := sessiontest.CollectEvents(store)
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
	if messages[1].Content != prompts.RenderGoalResumePrompt("ship goal mode") {
		t.Fatalf("resume reminder content = %q", messages[1].Content)
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
	if messages[1].Content != prompts.RenderGoalResumePrompt("ship goal mode") {
		t.Fatalf("resume reminder content = %q", messages[1].Content)
	}
	_, _ = engine.SetGoalStatus(session.GoalStatusComplete, session.GoalActorSystem)
	client.releaseSecond()
}

func TestServiceCompleteGoalFeedbackIncludesCookDuration(t *testing.T) {
	now := time.Date(2026, 5, 9, 10, 0, 0, 0, time.UTC)
	store, engine, service := newRuntimeControlTestService(t, nil, nil, runtime.Config{EnabledTools: []toolspec.ID{toolspec.ToolAskQuestion}}, session.WithClock(func() time.Time {
		return now
	}))
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	now = now.Add(5*time.Hour + 32*time.Minute + 9*time.Second)

	if _, err := service.CompleteGoal(context.Background(), serverapi.RuntimeGoalStatusRequest{ClientRequestID: "complete-duration", SessionID: store.Meta().SessionID, Actor: "agent"}); err != nil {
		t.Fatalf("CompleteGoal: %v", err)
	}

	messages := runtimeControlGoalDeveloperMessages(t, store)
	if len(messages) != 2 {
		t.Fatalf("goal developer messages len = %d, want set+complete", len(messages))
	}
	if got, want := messages[1].CompactContent, "Goal complete. Cooked for 5h32m9s"; got != want {
		t.Fatalf("complete compact content = %q, want %q", got, want)
	}
}

func TestServiceSetSessionNameDedupesSuccessfulRetry(t *testing.T) {
	store, engine := newRuntimeControlTestEngine(t, nil, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeShellHandler{}}), runtime.Config{})
	if err := store.SetName("before"); err != nil {
		t.Fatalf("persist initial session name: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
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

func TestServiceSubmitUserTurnDuplicateInFlightUsesSinglePreBeginAdmission(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, engine := newRuntimeControlTestEngine(t, client, nil, runtime.Config{})
	resolver := &countingBeginRuntimeResolver{stubRuntimeResolver: stubRuntimeResolver{engine: engine}}
	service := NewService(resolver)
	req := runtimeControlUserTurnRequest(store, "req-1", "hello")

	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), req)
		firstErr <- err
	}()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("first submit did not reach generation")
	}
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), req)
		secondErr <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if got := resolver.beginCount.Load(); got != 1 {
		t.Fatalf("BeginSessionRun count while duplicate is in flight = %d, want 1", got)
	}
	close(client.release)
	for label, ch := range map[string]<-chan error{"first": firstErr, "second": secondErr} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("%s SubmitUserTurn: %v", label, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s SubmitUserTurn", label)
		}
	}
	if got := resolver.beginCount.Load(); got != 1 {
		t.Fatalf("BeginSessionRun final count = %d, want 1", got)
	}
}

func TestServiceSubmitUserTurnOperationTombstonePreventsPreActiveBegin(t *testing.T) {
	client := finalResponseRuntimeControlClient()
	store, engine := newRuntimeControlTestEngine(t, client, nil, runtime.Config{})
	resolver := &countingBeginRuntimeResolver{stubRuntimeResolver: stubRuntimeResolver{engine: engine}}
	operations := runtimeops.NewCoordinator()
	service := NewService(resolver).WithOperationCoordinator(operations)
	req := runtimeControlUserTurnRequest(store, "req-canceled", "hello")
	if err := operations.CancelOperation(store.Meta().SessionID, req.OperationRef); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}

	if _, err := service.SubmitUserTurn(context.Background(), req); !errors.Is(err, runtimeops.ErrOperationCanceled) {
		t.Fatalf("SubmitUserTurn after tombstone error = %v, want operation canceled", err)
	}
	if got := resolver.beginCount.Load(); got != 0 {
		t.Fatalf("BeginCancellableSessionRun count = %d, want 0", got)
	}
	assertRuntimeControlReconciliation(t, operations, store.Meta().SessionID, req.OperationRef, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestServiceTargetCancelReleasesStartMarkerBeforeRuntimeAccessReturns(t *testing.T) {
	resolver := &cancelableStartRuntimeResolver{started: make(chan struct{}), released: make(chan struct{})}
	operations := runtimeops.NewCoordinator()
	service := NewService(resolver).WithOperationCoordinator(operations)
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "cancel-start-marker"}
	req := serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: ref.ClientRequestID,
		SessionID:       "session-start-marker",
		Text:            "hello",
		OperationRef:    ref,
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), req)
		done <- err
	}()
	select {
	case <-resolver.started:
	case <-time.After(time.Second):
		t.Fatal("submit did not reach guarded runtime access")
	}
	if err := operations.CancelOperation(req.SessionID, ref); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	select {
	case <-resolver.released:
	case <-time.After(time.Second):
		t.Fatal("start marker was not released by operation cancellation")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SubmitUserTurn error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled submit")
	}
	assertRuntimeControlReconciliation(t, operations, req.SessionID, ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestServiceSubmitUserTurnOperationTombstonePreventsPreFlushSubmittedReconciliation(t *testing.T) {
	resolver := &cancelableStartRuntimeResolver{started: make(chan struct{}), released: make(chan struct{})}
	operations := runtimeops.NewCoordinator()
	service := NewService(resolver).WithOperationCoordinator(operations)
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "cancel-before-flush"}
	req := serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: ref.ClientRequestID,
		SessionID:       "session-cancel-before-flush",
		Text:            "restore me",
		OperationRef:    ref,
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), req)
		done <- err
	}()
	select {
	case <-resolver.started:
	case <-time.After(time.Second):
		t.Fatal("submit did not reach guarded runtime access")
	}
	if err := operations.CancelOperation(req.SessionID, ref); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("SubmitUserTurn error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled submit")
	}
	assertRuntimeControlReconciliation(t, operations, req.SessionID, ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestServiceSubmitUserTurnRecordsCommittedAtFlushBeforeAssistantCompletion(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-flush-boundary"}
	req := serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: ref.ClientRequestID,
		SessionID:       store.Meta().SessionID,
		Text:            "flush before model completes",
		OperationRef:    ref,
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

func TestServiceInterruptQueuedServerMessageDoesNotInterruptUnrelatedActiveRun(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	activeRef := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "active-submit"}
	done := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserTurn(context.Background(), serverapi.RuntimeSubmitUserTurnRequest{
			ClientRequestID: activeRef.ClientRequestID,
			SessionID:       store.Meta().SessionID,
			Text:            "keep running",
			OperationRef:    activeRef,
		})
		done <- err
	}()
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("active submit did not reach model request")
	}
	queued, err := service.QueueUserMessage(context.Background(), runtimeControlQueueUserMessageRequest(store, "queued-discard-only", "discard queued only"))
	if err != nil {
		t.Fatalf("QueueUserMessage: %v", err)
	}
	target := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, QueueItemID: queued.QueueItemID}
	if _, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID:    "interrupt-queued-discard-only",
		SessionID:          store.Meta().SessionID,
		TargetOperationRef: &target,
	}); err != nil {
		t.Fatalf("Interrupt queued target: %v", err)
	}
	select {
	case <-client.ctxCanceled:
		t.Fatal("queued-message discard interrupted unrelated active run")
	case <-time.After(100 * time.Millisecond):
	}
	close(client.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SubmitUserTurn: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for active submit completion")
	}
	assertRuntimeControlReconciliation(t, service.operations, store.Meta().SessionID, target, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestServiceInterruptCancelsInFlightQueuedMessageCreateByClientRef(t *testing.T) {
	resolver := &cancelableStartRuntimeResolver{started: make(chan struct{}), released: make(chan struct{})}
	operations := runtimeops.NewCoordinator()
	service := NewService(resolver).WithOperationCoordinator(operations)
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: "queue-create-cancel"}
	req := serverapi.RuntimeQueueUserMessageRequest{
		ClientRequestID: ref.ClientRequestID,
		SessionID:       "session-queue-create",
		OperationRef:    ref,
		Text:            "queued before server id",
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.QueueUserMessage(context.Background(), req)
		done <- err
	}()
	select {
	case <-resolver.started:
	case <-time.After(time.Second):
		t.Fatal("queue create did not reach guarded runtime access")
	}
	if _, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID:      "interrupt-queue-create",
		SessionID:            req.SessionID,
		TargetOperationRef:   &ref,
		PendingOperationRefs: []clientui.RuntimeOperationRef{ref},
	}); err != nil {
		t.Fatalf("Interrupt queued create: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("QueueUserMessage error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for canceled queue create")
	}
	assertRuntimeControlReconciliation(t, operations, req.SessionID, ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestServiceCanceledQueuedMessageCreateRejectsDuplicateRetries(t *testing.T) {
	store, engine, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	operations := runtimeops.NewCoordinator()
	service.WithOperationCoordinator(operations)
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: "queue-create-canceled-retry"}
	req := serverapi.RuntimeQueueUserMessageRequest{
		ClientRequestID: ref.ClientRequestID,
		SessionID:       store.Meta().SessionID,
		OperationRef:    ref,
		Text:            "must not enqueue after cancel",
	}
	if err := operations.CancelOperation(req.SessionID, ref); err != nil {
		t.Fatalf("CancelOperation: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := service.QueueUserMessage(context.Background(), req); !errors.Is(err, runtimeops.ErrOperationCanceled) {
			t.Fatalf("QueueUserMessage attempt %d error = %v, want operation canceled", attempt, err)
		}
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("canceled queued-message create retry enqueued runtime work")
	}
	assertRuntimeControlReconciliation(t, operations, req.SessionID, ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
}

func TestServiceInterruptWithTargetRecordsCancellationTombstoneWithoutRuntime(t *testing.T) {
	operations := runtimeops.NewCoordinator()
	service := NewService(stubRuntimeResolver{}).WithOperationCoordinator(operations)
	ref := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "submit-target"}
	resp, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID:    "interrupt-target",
		SessionID:          "session-target",
		TargetOperationRef: &ref,
	})
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if resp.Activity.State != clientui.RuntimeActivityUnavailable {
		t.Fatalf("activity = %+v, want unavailable", resp.Activity)
	}
	assertRuntimeControlReconciliation(t, operations, "session-target", ref, clientui.RuntimeInputReconciliationCanceledNotCommitted)
	if len(resp.InputReconciliation.Operations) != 1 || resp.InputReconciliation.Operations[0].OperationRef != ref || resp.InputReconciliation.Operations[0].State != clientui.RuntimeInputReconciliationCanceledNotCommitted {
		t.Fatalf("response reconciliation = %+v, want canceled target", resp.InputReconciliation)
	}
}

func TestServiceTargetedPreActiveInterruptDoesNotInterruptUnrelatedActiveRun(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, engine := newRuntimeControlTestEngine(t, client, nil, runtime.Config{})
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
	service := NewService(stubRuntimeResolver{engine: engine}).WithOperationCoordinator(operations)
	target := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "pre-active-target"}
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
	store, engine := newRuntimeControlTestEngine(t, client, nil, runtime.Config{})
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
	target := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: "terminal-target"}
	operations.RecordCommitted(store.Meta().SessionID, target)
	service := NewService(stubRuntimeResolver{engine: engine}).WithOperationCoordinator(operations)
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

func TestServiceInterruptDoesNotExposeLongLivedStartReservationAsActivity(t *testing.T) {
	reg := registry.NewRuntimeRegistry()
	startCtx, release, ok := reg.BeginCancellableSessionRun("session-starting")
	if !ok {
		t.Fatal("BeginCancellableSessionRun rejected")
	}
	defer release()
	service := NewService(reg)
	resp, err := service.Interrupt(context.Background(), serverapi.RuntimeInterruptRequest{
		ClientRequestID: "interrupt-starting",
		SessionID:       "session-starting",
	})
	if err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if resp.Activity.State != clientui.RuntimeActivityUnavailable {
		t.Fatalf("activity = %+v, want unavailable because start reservation is admission-only", resp.Activity)
	}
	release()
	if !errors.Is(startCtx.Err(), context.Canceled) {
		t.Fatalf("start context error = %v, want canceled", startCtx.Err())
	}
}

func assertRuntimeControlReconciliation(t *testing.T, operations *runtimeops.Coordinator, sessionID string, ref clientui.RuntimeOperationRef, want clientui.RuntimeInputReconciliationState) {
	t.Helper()
	version, err := clientui.NewReadModelVersion("test", 1, 1)
	if err != nil {
		t.Fatalf("NewReadModelVersion: %v", err)
	}
	snapshot := operations.Snapshot(sessionID, version, []clientui.RuntimeOperationRef{ref})
	for _, record := range snapshot.Operations {
		if record.OperationRef == ref {
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

func TestServiceQueueUserMessageDedupesSuccessfulRetry(t *testing.T) {
	client := finalResponseRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	req := runtimeControlQueueUserMessageRequest(store, "req-1", "hello")

	firstQueue, err := service.QueueUserMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("QueueUserMessage first: %v", err)
	}
	secondQueue, err := service.QueueUserMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("QueueUserMessage replay: %v", err)
	}
	if firstQueue.QueueItemID == "" || secondQueue.QueueItemID != firstQueue.QueueItemID {
		t.Fatalf("queue ids = (%q, %q), want stable non-empty id", firstQueue.QueueItemID, secondQueue.QueueItemID)
	}
	queueCreateSnapshot := service.operations.Snapshot(store.Meta().SessionID, clientui.ReadModelVersion{Epoch: "test", Generation: 1, Sequence: 1}, []clientui.RuntimeOperationRef{req.OperationRef})
	if len(queueCreateSnapshot.Operations) != 1 || queueCreateSnapshot.Operations[0].State != clientui.RuntimeInputReconciliationCommitted {
		t.Fatalf("queue create reconciliation = %+v, want committed for UI-owned operation ref", queueCreateSnapshot.Operations)
	}
	if got := countPromptHistoryEvents(t, store, "hello"); got != 1 {
		t.Fatalf("queued prompt history count = %d, want 1 immediately after queue acceptance", got)
	}
	if _, err := engine.SubmitQueuedUserMessages(context.Background()); err != nil {
		t.Fatalf("SubmitQueuedUserMessages: %v", err)
	}
	if got := countUserMessagesWithContent(t, store, "hello"); got != 1 {
		t.Fatalf("queued user message count = %d, want 1", got)
	}
	if got := countUserMessagesWithContent(t, store, "hello\n\nhello"); got != 0 {
		t.Fatalf("duplicate queued flush count = %d, want 0", got)
	}
}

func TestServiceQueueUserMessageDoesNotEnqueueWhenPromptHistoryRecordFails(t *testing.T) {
	ctx := context.Background()
	sessionStore, engine, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	history := service.promptStore.(*runtimeControlPromptHistoryStore)
	boom := errors.New("prompt history record failed")
	history.SetRecordError(boom)
	req := runtimeControlQueueUserMessageRequest(sessionStore, "req-record-fail", "hello record failure")

	if _, err := service.QueueUserMessage(ctx, req); !errors.Is(err, boom) {
		t.Fatalf("QueueUserMessage error = %v, want %v", err, boom)
	}
	if engine.HasQueuedUserWork() {
		t.Fatal("did not expect runtime queue mutation after prompt history record failure")
	}
}

func TestServiceDiscardQueuedUserMessageIsRuntimeOnly(t *testing.T) {
	ctx := context.Background()
	sessionStore, engine, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	queued, err := service.QueueUserMessage(ctx, runtimeControlQueueUserMessageRequest(sessionStore, "req-discard-runtime", "discard runtime only"))
	if err != nil {
		t.Fatalf("QueueUserMessage: %v", err)
	}
	discardReq := serverapi.RuntimeDiscardQueuedUserMessageRequest{
		ClientRequestID: "req-discard-runtime",
		SessionID:       sessionStore.Meta().SessionID,
		QueueItemID:     queued.QueueItemID,
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
	if got := countPromptHistoryEvents(t, sessionStore, "discard runtime only"); got != 1 {
		t.Fatalf("prompt history count after discard = %d, want 1", got)
	}
}

func TestServiceInterruptTargetQueuedServerMessageDiscardsRuntimeWork(t *testing.T) {
	ctx := context.Background()
	sessionStore, engine, service := newRuntimeControlTestService(t, finalResponseRuntimeControlClient(), nil, runtime.Config{})
	queued, err := service.QueueUserMessage(ctx, runtimeControlQueueUserMessageRequest(sessionStore, "req-interrupt-discard", "discard on interrupt"))
	if err != nil {
		t.Fatalf("QueueUserMessage: %v", err)
	}
	target := clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, QueueItemID: queued.QueueItemID}

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

func TestServiceQueueUserMessageRejectsClientRequestIDPayloadMismatch(t *testing.T) {
	client := finalResponseRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	first := runtimeControlQueueUserMessageRequest(store, "req-1", "hello")
	if _, err := service.QueueUserMessage(context.Background(), first); err != nil {
		t.Fatalf("QueueUserMessage first: %v", err)
	}
	second := first
	second.Text = "different"
	if _, err := service.QueueUserMessage(context.Background(), second); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("QueueUserMessage mismatch error = %v, want request id payload mismatch", err)
	}
	if _, err := engine.SubmitQueuedUserMessages(context.Background()); err != nil {
		t.Fatalf("SubmitQueuedUserMessages: %v", err)
	}
	if got := countUserMessagesWithContent(t, store, "hello"); got != 1 {
		t.Fatalf("queued user message count = %d, want 1", got)
	}
	if got := countUserMessagesWithContent(t, store, "different"); got != 0 {
		t.Fatalf("mismatched queued user message count = %d, want 0", got)
	}
	if got := countUserMessagesWithContent(t, store, "hello\n\ndifferent"); got != 0 {
		t.Fatalf("mixed queued flush count = %d, want 0", got)
	}
}

func countDirectShellCommandMessages(t *testing.T, store *session.Store, command string) int {
	t.Helper()
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	count := 0
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			t.Fatalf("decode message event: %v", err)
		}
		if msg.Role != llm.RoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
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

func runtimeControlGoalDeveloperMessages(t *testing.T, store *session.Store) []llm.Message {
	t.Helper()
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	out := []llm.Message{}
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			t.Fatalf("decode message event: %v", err)
		}
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeGoal {
			out = append(out, msg)
		}
	}
	return out
}

func countUserMessagesWithContent(t *testing.T, store *session.Store, content string) int {
	t.Helper()
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	count := 0
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var msg llm.Message
		if err := json.Unmarshal(evt.Payload, &msg); err != nil {
			t.Fatalf("decode message event: %v", err)
		}
		if msg.Role == llm.RoleUser && msg.Content == content {
			count++
		}
	}
	return count
}

func runtimeControlUserTurnRequest(store *session.Store, requestID string, text string) serverapi.RuntimeSubmitUserTurnRequest {
	return serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: requestID,
		SessionID:       store.Meta().SessionID,
		Text:            text,
		OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmit, ClientRequestID: requestID},
	}
}

func runtimeControlShellCommandRequest(store *session.Store, requestID string, command string) serverapi.RuntimeSubmitUserShellCommandRequest {
	return serverapi.RuntimeSubmitUserShellCommandRequest{
		ClientRequestID: requestID,
		SessionID:       store.Meta().SessionID,
		Command:         command,
		OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindUserShell, ClientRequestID: requestID},
	}
}

func runtimeControlQueueUserMessageRequest(store *session.Store, requestID string, text string) serverapi.RuntimeQueueUserMessageRequest {
	return serverapi.RuntimeQueueUserMessageRequest{
		ClientRequestID: requestID,
		SessionID:       store.Meta().SessionID,
		OperationRef:    clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindQueuedMessage, ClientRequestID: requestID},
		Text:            text,
	}
}
