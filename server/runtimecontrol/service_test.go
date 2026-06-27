package runtimecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/metadata"
	"core/server/requestmemo"
	"core/server/runtime"
	"core/server/runtimeview"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/serverapi"
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

type fakeShellHandler struct{}

func (fakeShellHandler) Call(context.Context, tools.Call) (tools.Result, error) {
	return tools.Result{Output: json.RawMessage(`{"output":"ok","exit_code":0,"truncated":false}`)}, nil
}

func newRuntimeControlTestEngine(t *testing.T, client llm.Client, registry *tools.Registry, cfg runtime.Config, opts ...session.StoreOption) (*session.Store, *runtime.Engine) {
	t.Helper()
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x", opts...)
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

func TestServiceSubmitUserMessageReturnsTypedRuntimeUnavailable(t *testing.T) {
	service := NewService(stubRuntimeResolver{})
	req := serverapi.RuntimeSubmitUserMessageRequest{
		ClientRequestID: "req-1",
		SessionID:       "session-1",
		Text:            "hello",
	}

	_, err := service.SubmitUserMessage(context.Background(), req)
	if !errors.Is(err, serverapi.ErrRuntimeUnavailable) {
		t.Fatalf("SubmitUserMessage error = %v, want ErrRuntimeUnavailable", err)
	}
}

func TestServiceSubmitUserMessageDetachesRunFromCallerCancellation(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserMessage(ctx, runtimeControlUserMessageRequest(store, "req-detached", "hello"))
		done <- err
	}()

	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for submit to start")
	}
	cancel()
	select {
	case <-client.ctxCanceled:
		t.Fatal("runtime context was canceled by caller cancellation")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case err := <-done:
		t.Fatalf("submit returned before runtime was released: %v", err)
	default:
	}

	close(client.release)
	if err := <-done; err != nil {
		t.Fatalf("SubmitUserMessage: %v", err)
	}
}

func TestServiceSubmitUserMessageStillCancelsOnExplicitInterrupt(t *testing.T) {
	client := newCancelObservingRuntimeControlClient()
	store, engine, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	done := make(chan error, 1)
	go func() {
		_, err := service.SubmitUserMessage(context.Background(), runtimeControlUserMessageRequest(store, "req-interrupt", "hello"))
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
	store, engine := newRuntimeControlTestEngine(t, nil, nil, runtime.Config{})
	if _, err := engine.SetGoal("ship goal mode", session.GoalActorUser); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}
	if err := engine.Interrupt(); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
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
	if reopened, err := session.Open(store.Dir()); err != nil {
		t.Fatalf("reopen session store: %v", err)
	} else if got := reopened.Meta().Name; got != "after" {
		t.Fatalf("reopened session name = %q, want after", got)
	}
}

func TestServiceSubmitUserMessageDedupesSuccessfulRetry(t *testing.T) {
	client := finalResponseRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	req := runtimeControlUserMessageRequest(store, "req-1", "hello")

	first, err := service.SubmitUserMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitUserMessage first: %v", err)
	}
	second, err := service.SubmitUserMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitUserMessage retry: %v", err)
	}
	if first.Message != "done" || second.Message != "done" {
		t.Fatalf("responses = (%q, %q), want both done", first.Message, second.Message)
	}
	if client.calls != 1 {
		t.Fatalf("generate call count = %d, want 1", client.calls)
	}
	if got := countPromptHistoryEvents(t, store, "hello"); got != 1 {
		t.Fatalf("prompt history count = %d, want 1", got)
	}
}

func TestServiceSubmitUserMessageRejectsClientRequestIDPayloadMismatch(t *testing.T) {
	client := finalResponseRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	first := runtimeControlUserMessageRequest(store, "req-1", "hello")
	if _, err := service.SubmitUserMessage(context.Background(), first); err != nil {
		t.Fatalf("SubmitUserMessage first: %v", err)
	}
	second := first
	second.Text = "different"
	if _, err := service.SubmitUserMessage(context.Background(), second); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("SubmitUserMessage mismatch error = %v, want request id payload mismatch", err)
	}
	if client.calls != 1 {
		t.Fatalf("generate call count = %d, want 1", client.calls)
	}
}

func TestServiceSubmitUserTurnRecordsPromptHistoryAndSubmits(t *testing.T) {
	client := finalResponseRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})

	resp, err := service.SubmitUserTurn(context.Background(), runtimeControlUserTurnRequest(store, "req-1", "hello"))
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if resp.Message != "done" {
		t.Fatalf("message = %q, want done", resp.Message)
	}
	if client.calls != 1 {
		t.Fatalf("generate call count = %d, want 1", client.calls)
	}
	if got := countPromptHistoryEvents(t, store, "hello"); got != 1 {
		t.Fatalf("prompt history count = %d, want 1", got)
	}
}

func TestServiceSubmitUserTurnRecordsPromptHistoryWithUncancelledRunContext(t *testing.T) {
	client := finalResponseRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	history := service.promptStore.(*runtimeControlPromptHistoryStore)
	cancelledRecordCtx := errors.New("record context was cancelled")
	history.SetRecordContextError(cancelledRecordCtx)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp, err := service.SubmitUserTurn(ctx, runtimeControlUserTurnRequest(store, "req-uncancelled-history", "hello after cancel"))
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if resp.Message != "done" {
		t.Fatalf("message = %q, want done", resp.Message)
	}
	if got := countPromptHistoryEvents(t, store, "hello after cancel"); got != 1 {
		t.Fatalf("prompt history count = %d, want 1", got)
	}
}

func TestServiceSubmitUserTurnSkipsPromptHistoryWhenAlreadyRecorded(t *testing.T) {
	client := finalResponseRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	req := runtimeControlUserTurnRequest(store, "req-1", "expanded hidden prompt")
	req.PromptHistoryRecorded = true

	resp, err := service.SubmitUserTurn(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitUserTurn: %v", err)
	}
	if resp.Message != "done" {
		t.Fatalf("response = %q, want done", resp.Message)
	}
	if got := countPromptHistoryEvents(t, store, "expanded hidden prompt"); got != 0 {
		t.Fatalf("hidden expanded prompt history count = %d, want 0", got)
	}
	if got := countUserMessagesWithContent(t, store, "expanded hidden prompt"); got != 1 {
		t.Fatalf("submitted user message count = %d, want 1", got)
	}
}

func TestServiceSubmitUserTurnRejectsPromptHistoryRecordedMismatch(t *testing.T) {
	client := finalResponseRuntimeControlClient()
	store, _, service := newRuntimeControlTestService(t, client, nil, runtime.Config{})
	first := runtimeControlUserTurnRequest(store, "req-1", "expanded hidden prompt")
	first.PromptHistoryRecorded = true
	if _, err := service.SubmitUserTurn(context.Background(), first); err != nil {
		t.Fatalf("SubmitUserTurn first: %v", err)
	}
	second := first
	second.PromptHistoryRecorded = false
	if _, err := service.SubmitUserTurn(context.Background(), second); !errors.Is(err, requestmemo.ErrClientRequestIDReused) {
		t.Fatalf("SubmitUserTurn mismatch error = %v, want request id payload mismatch", err)
	}
	if got := countPromptHistoryEvents(t, store, "expanded hidden prompt"); got != 0 {
		t.Fatalf("hidden expanded prompt history count = %d, want 0", got)
	}
	if client.calls != 1 {
		t.Fatalf("generate call count = %d, want 1", client.calls)
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
	if got := countPromptHistoryEvents(t, store, "hello"); got != 1 {
		t.Fatalf("prompt history count = %d, want 1", got)
	}
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

func runtimeControlUserMessageRequest(store *session.Store, requestID string, text string) serverapi.RuntimeSubmitUserMessageRequest {
	return serverapi.RuntimeSubmitUserMessageRequest{
		ClientRequestID: requestID,
		SessionID:       store.Meta().SessionID,
		Text:            text,
	}
}

func runtimeControlUserTurnRequest(store *session.Store, requestID string, text string) serverapi.RuntimeSubmitUserTurnRequest {
	return serverapi.RuntimeSubmitUserTurnRequest{
		ClientRequestID: requestID,
		SessionID:       store.Meta().SessionID,
		Text:            text,
	}
}

func runtimeControlShellCommandRequest(store *session.Store, requestID string, command string) serverapi.RuntimeSubmitUserShellCommandRequest {
	return serverapi.RuntimeSubmitUserShellCommandRequest{
		ClientRequestID: requestID,
		SessionID:       store.Meta().SessionID,
		Command:         command,
	}
}

func runtimeControlQueueUserMessageRequest(store *session.Store, requestID string, text string) serverapi.RuntimeQueueUserMessageRequest {
	return serverapi.RuntimeQueueUserMessageRequest{
		ClientRequestID: requestID,
		SessionID:       store.Meta().SessionID,
		Text:            text,
	}
}
