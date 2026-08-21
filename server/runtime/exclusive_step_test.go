package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"

	"github.com/google/uuid"
)

type stubExclusiveStepLifecycle struct {
	mu           sync.Mutex
	busy         bool
	runCalls     int
	runFn        func(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error
	snapshot     *RunSnapshot
	activeStepID string
}

type stubBackgroundNoticeScheduler struct {
	scheduleIfIdle func()
}

type callbackStepLifecycleSink struct {
	onTransition func(StepLifecycleTransition) error
	mu           sync.Mutex
	transitions  []StepLifecycleTransition
}

func (s *callbackStepLifecycleSink) StepBegan(context.Context, StepLifecycleSnapshot) error {
	return s.record(StepLifecycleTransitionBegan)
}

func (s *callbackStepLifecycleSink) StepEnded(context.Context, StepLifecycleSnapshot) error {
	return s.record(StepLifecycleTransitionEnded)
}

func (s *callbackStepLifecycleSink) record(transition StepLifecycleTransition) error {
	s.mu.Lock()
	s.transitions = append(s.transitions, transition)
	s.mu.Unlock()
	if s.onTransition != nil {
		return s.onTransition(transition)
	}
	return nil
}

func (s *callbackStepLifecycleSink) seen(transition StepLifecycleTransition) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.transitions {
		if item == transition {
			return true
		}
	}
	return false
}

func TestBackgroundStepBoundarySkipsAgentFIFOAndCompactionBookkeeping(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, newTestToolRegistry(t), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}
	engine.stepLifecycle = lifecycle
	if err := engine.pauseRuntimeOperations(t.Context()); err != nil {
		t.Fatalf("pause Runtime FIFO: %v", err)
	}
	operationStarted := make(chan struct{})
	deferred := submitEngineRuntimeOperation(engine, func(context.Context) (struct{}, error) {
		close(operationStarted)
		return struct{}{}, nil
	})

	err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{ActiveKind: ActiveKindBackground},
		func(context.Context, string) error {
			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			if err := lifecycle.CompleteAgentStepBoundary(canceled); err != nil {
				return err
			}
			if engine.compactionRuntimeState().ManualCompactionEligible() {
				return errors.New("background boundary enabled manual compaction")
			}
			select {
			case <-operationStarted:
				return errors.New("background boundary drained the Runtime FIFO")
			default:
			}
			return nil
		},
	)
	if drainErr := engine.drainRuntimeOperations(t.Context()); drainErr != nil {
		err = errors.Join(err, drainErr)
	}
	if _, awaitErr := deferred.Await(t.Context()); awaitErr != nil {
		err = errors.Join(err, awaitErr)
	}
	if err != nil {
		t.Fatalf("complete background boundary: %v", err)
	}
}

func TestExclusiveStepLifecycleEagerCompactsAfterSuccessfulFinalAtConsumedThreshold(t *testing.T) {
	t.Parallel()
	client := &fakeCompactionClient{
		compactionResponses: []llm.CompactionResponse{
			remoteCompactionReplacement(100, 10, 2_000),
		},
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_900,
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}

	if err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn},
		func(_ context.Context, stepID string) error {
			if err := engine.steer(runtimeTestStepID(stepID), steerMessagesWithPersistenceIntent(
				steeringMessageEventNone,
				true,
				[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
			)); err != nil {
				return err
			}
			engine.setLastUsage(llm.Usage{InputTokens: 1_760, WindowTokens: 2_000})
			engine.recordLiveRunAssistantFinalAnswer(stepID, llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("final"),
			})
			return nil
		},
	); err != nil {
		t.Fatalf("run exclusive step: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if got := len(client.compactionCalls); got != 1 {
		t.Fatalf("eager compaction calls = %d, want 1", got)
	}
}

func TestExclusiveStepLifecycleEagerCompactsEligibleAgentKinds(t *testing.T) {
	t.Parallel()
	for _, kind := range []ActiveKind{ActiveKindGoalLoop} {
		t.Run(string(kind), func(t *testing.T) {
			client := &fakeCompactionClient{
				compactionResponses: []llm.CompactionResponse{
					remoteCompactionReplacement(100, 10, 2_000),
				},
			}
			engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
				Model:                 "gpt-5",
				ContextWindowTokens:   2_000,
				AutoCompactTokenLimit: 1_900,
			})
			lifecycle := &defaultExclusiveStepLifecycle{engine: engine}
			if err := lifecycle.Run(
				context.Background(),
				exclusiveStepOptions{EmitRunState: true, ActiveKind: kind},
				func(_ context.Context, stepID string) error {
					if err := engine.steer(runtimeTestStepID(stepID), steerMessagesWithPersistenceIntent(
						steeringMessageEventNone,
						true,
						[]llm.Message{{Role: llm.RoleDeveloper, Content: textutil.Value("input")}},
					)); err != nil {
						return err
					}
					engine.setLastUsage(llm.Usage{InputTokens: 1_760, WindowTokens: 2_000})
					engine.recordLiveRunAssistantFinalAnswer(stepID, llm.Message{
						Role:    llm.RoleAssistant,
						Phase:   textutil.Value(llm.MessagePhaseFinal),
						Content: textutil.Value("final"),
					})
					return nil
				},
			); err != nil {
				t.Fatalf("run exclusive step: %v", err)
			}
			waitEngineLifecycleTasks(t, engine)
			if got := len(client.compactionCalls); got != 1 {
				t.Fatalf("eager compaction calls = %d, want 1", got)
			}
		})
	}
}

func TestSubmitUserMessageEagerCompactsAfterSuccessfulFinal(t *testing.T) {
	t.Parallel()
	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("final"),
			},
			Usage: llm.Usage{InputTokens: 8_800, WindowTokens: 10_000},
		}},
		compactionResponses: []llm.CompactionResponse{
			remoteCompactionReplacement(100, 10, 10_000),
		},
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   10_000,
		AutoCompactTokenLimit: 9_500,
	})
	if _, err := engine.SubmitUserMessage(context.Background(), "input"); err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if got := len(client.compactionCalls); got != 1 {
		t.Fatalf("eager compaction calls = %d, want 1", got)
	}
}

func TestSubmitAgentSteerEagerCompactsAfterSuccessfulFinal(t *testing.T) {
	t.Parallel()
	sourceID := runtimeids.NewSessionID()
	steer, err := NewAgentSteer(sourceID, "continue")
	if err != nil {
		t.Fatalf("new agent steer: %v", err)
	}
	client := &fakeCompactionClient{
		responses: []llm.Response{{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("final"),
			},
			Usage: llm.Usage{InputTokens: 8_800, WindowTokens: 10_000},
		}},
		compactionResponses: []llm.CompactionResponse{
			remoteCompactionReplacement(100, 10, 10_000),
		},
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   10_000,
		AutoCompactTokenLimit: 9_500,
	})
	if _, err := engine.SubmitAgentSteerWithHooks(context.Background(), steer, nil, nil); err != nil {
		t.Fatalf("submit agent steer: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if got := len(client.compactionCalls); got != 1 {
		t.Fatalf("eager compaction calls = %d, want 1", got)
	}
}

func TestExclusiveStepLifecycleEagerCompactionExcludesIneligibleResults(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		kind ActiveKind
	}{
		{name: "workflow", kind: ActiveKindWorkflowTurn},
		{name: "compaction", kind: ActiveKindCompaction},
		{name: "inspection", kind: ActiveKindInspection},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeCompactionClient{
				compactionResponses: []llm.CompactionResponse{
					remoteCompactionReplacement(100, 10, 2_000),
				},
			}
			engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
				Model:                 "gpt-5",
				ContextWindowTokens:   2_000,
				AutoCompactTokenLimit: 1_900,
			})
			lifecycle := &defaultExclusiveStepLifecycle{engine: engine}
			if err := lifecycle.Run(
				context.Background(),
				exclusiveStepOptions{EmitRunState: true, ActiveKind: test.kind},
				func(_ context.Context, stepID string) error {
					engine.setLastUsage(llm.Usage{InputTokens: 1_760, WindowTokens: 2_000})
					engine.recordLiveRunAssistantFinalAnswer(stepID, llm.Message{
						Role:    llm.RoleAssistant,
						Phase:   textutil.Value(llm.MessagePhaseFinal),
						Content: textutil.Value("final"),
					})
					return nil
				},
			); err != nil {
				t.Fatalf("run exclusive step: %v", err)
			}
			waitEngineLifecycleTasks(t, engine)
			if got := len(client.compactionCalls); got != 0 {
				t.Fatalf("eager compaction calls = %d, want 0", got)
			}
		})
	}
}

func TestExclusiveStepLifecycleEagerCompactionExcludesWorkflowBackgroundContinuation(t *testing.T) {
	t.Parallel()
	client := &fakeCompactionClient{
		compactionResponses: []llm.CompactionResponse{
			remoteCompactionReplacement(100, 10, 2_000),
		},
	}
	engine := mustNewWorkflowTestEngine(t, mustCreateTestSession(t), client, testWorkflowConfig(&fakeWorkflowController{}, "tool"), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_900,
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}
	if err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn},
		func(_ context.Context, stepID string) error {
			engine.setLastUsage(llm.Usage{InputTokens: 1_760, WindowTokens: 2_000})
			engine.recordLiveRunAssistantFinalAnswer(stepID, llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("final"),
			})
			return nil
		},
	); err != nil {
		t.Fatalf("run exclusive step: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if got := len(client.compactionCalls); got != 0 {
		t.Fatalf("workflow background eager compaction calls = %d, want 0", got)
	}
}

func TestExclusiveStepLifecycleEagerCompactionExcludesNoFinalAndInterruptedSteps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		final     llm.Message
		stepError error
	}{
		{name: "no final"},
		{
			name: "blank final",
			final: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value(" "),
			},
		},
		{name: "interrupted", final: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("final"),
		}, stepError: context.Canceled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeCompactionClient{
				compactionResponses: []llm.CompactionResponse{
					remoteCompactionReplacement(100, 10, 2_000),
				},
			}
			engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
				Model:                 "gpt-5",
				ContextWindowTokens:   2_000,
				AutoCompactTokenLimit: 1_900,
			})
			lifecycle := &defaultExclusiveStepLifecycle{engine: engine}
			if err := lifecycle.Run(
				context.Background(),
				exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn},
				func(_ context.Context, stepID string) error {
					engine.setLastUsage(llm.Usage{InputTokens: 1_760, WindowTokens: 2_000})
					if test.final.Role != "" {
						engine.recordLiveRunAssistantFinalAnswer(stepID, test.final)
					}
					return test.stepError
				},
			); !errors.Is(err, test.stepError) {
				t.Fatalf("run exclusive step error = %v, want %v", err, test.stepError)
			}
			waitEngineLifecycleTasks(t, engine)
			if got := len(client.compactionCalls); got != 0 {
				t.Fatalf("eager compaction calls = %d, want 0", got)
			}
		})
	}
}

func TestExclusiveStepLifecycleEagerCompactionDoesNotReserveAfterTerminalCleanupFailure(t *testing.T) {
	t.Parallel()
	cleanupErr := errors.New("finish cleanup failed")
	client := &fakeCompactionClient{
		compactionResponses: []llm.CompactionResponse{
			remoteCompactionReplacement(100, 10, 2_000),
		},
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_900,
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}
	if err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn},
		func(_ context.Context, stepID string) error {
			engine.setLastUsage(llm.Usage{InputTokens: 1_760, WindowTokens: 2_000})
			engine.recordLiveRunAssistantFinalAnswer(stepID, llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("final"),
			})
			return cleanupErr
		},
	); !errors.Is(err, cleanupErr) {
		t.Fatalf("run exclusive step error = %v, want cleanup error", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if got := len(client.compactionCalls); got != 0 {
		t.Fatalf("eager compaction calls = %d, want 0", got)
	}
}

func TestExclusiveStepLifecycleFailedEagerCompactionDoesNotRetry(t *testing.T) {
	t.Parallel()
	compactionErr := llm.ErrInvalidRequest
	client := &fakeCompactionClient{compactionErrors: []error{compactionErr}}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_900,
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}
	if err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{EmitRunState: true, ActiveKind: ActiveKindUserTurn},
		func(_ context.Context, stepID string) error {
			if err := engine.steer(runtimeTestStepID(stepID), steerMessagesWithPersistenceIntent(
				steeringMessageEventNone,
				true,
				[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
			)); err != nil {
				return err
			}
			engine.setLastUsage(llm.Usage{InputTokens: 1_760, WindowTokens: 2_000})
			engine.recordLiveRunAssistantFinalAnswer(stepID, llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("final"),
			})
			return nil
		},
	); err != nil {
		t.Fatalf("run exclusive step: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if got := len(client.compactionCalls); got != 1 {
		t.Fatalf("eager compaction calls = %d, want one attempt", got)
	}
}

func TestEagerCompactionRevalidatesCurrentPolicyBeforeDispatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		mutate       func(*Engine)
		wantCompacts int
	}{
		{name: "disabled", mutate: func(engine *Engine) {
			engine.SetAutoCompactionEnabled(false)
		}},
		{name: "below threshold", mutate: func(engine *Engine) {
			engine.setLastUsage(llm.Usage{InputTokens: 100, WindowTokens: 2_000})
		}},
		{name: "still eligible", wantCompacts: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &fakeCompactionClient{
				compactionResponses: []llm.CompactionResponse{
					remoteCompactionReplacement(100, 10, 2_000),
				},
			}
			engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
				Model:                 "gpt-5",
				ContextWindowTokens:   2_000,
				AutoCompactTokenLimit: 1_900,
			})
			if err := engine.steerRuntime(steerMessagesWithPersistenceIntent(
				steeringMessageEventNone,
				true,
				[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}},
			)); err != nil {
				t.Fatalf("seed transcript: %v", err)
			}
			engine.ensureOrchestrationCollaborators()
			lifecycle := engine.stepLifecycle.(*defaultExclusiveStepLifecycle)
			started := make(chan struct{})
			release := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
					close(started)
					<-release
					return nil
				})
			}()
			select {
			case <-started:
			case <-time.After(runtimeTestSynchronizationTimeout):
				t.Fatal("timed out waiting for active step")
			}
			engine.setLastUsage(llm.Usage{InputTokens: 1_760, WindowTokens: 2_000})
			engine.maybeQueueEagerCompaction(ActiveKindUserTurn, LiveRunResultAssistantFinalAnswer, llm.Message{
				Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("final"),
			})
			if test.mutate != nil {
				test.mutate(engine)
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatalf("blocking step: %v", err)
			}
			waitEngineLifecycleTasks(t, engine)
			if got := len(client.compactionCalls); got != test.wantCompacts {
				t.Fatalf("compaction calls = %d, want %d", got, test.wantCompacts)
			}
		})
	}
}

func (s *stubBackgroundNoticeScheduler) HandleBackgroundShellUpdate(BackgroundShellEvent, bool) {}
func (s *stubBackgroundNoticeScheduler) RecordBackgroundShellUpdate(BackgroundShellEvent) error {
	return nil
}
func (s *stubBackgroundNoticeScheduler) QueueBackgroundShellContinuation(BackgroundShellEvent) {}
func (s *stubBackgroundNoticeScheduler) RunBackgroundShellContinuation(context.Context, BackgroundShellEvent) error {
	return nil
}
func (s *stubBackgroundNoticeScheduler) QueueDeveloperNotice(llm.Message)           {}
func (s *stubBackgroundNoticeScheduler) flushPendingNotices(string) (int, error)    { return 0, nil }
func (s *stubBackgroundNoticeScheduler) HasPendingNotices() bool                    { return false }
func (s *stubBackgroundNoticeScheduler) ConsumePendingBackgroundNotice(string) bool { return false }
func (s *stubBackgroundNoticeScheduler) ScheduleIfIdle() {
	if s != nil && s.scheduleIfIdle != nil {
		s.scheduleIfIdle()
	}
}

func (s *stubExclusiveStepLifecycle) Run(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error {
	s.mu.Lock()
	s.runCalls++
	s.mu.Unlock()
	if s.runFn != nil {
		return s.runFn(ctx, options, fn)
	}
	return fn(ctx, runtimeTestStepID("stub-step"))
}

func (s *stubExclusiveStepLifecycle) RunNext(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error {
	return s.Run(ctx, options, fn)
}

func (s *stubExclusiveStepLifecycle) AcquireReservation(*exclusiveStepReservation) error {
	return nil
}

func (s *stubExclusiveStepLifecycle) ReleaseReservation(*exclusiveStepReservation) {}

func (s *stubExclusiveStepLifecycle) Interrupt() error {
	return nil
}

func (s *stubExclusiveStepLifecycle) InterruptCurrent(func(*RunSnapshot)) (*RunSnapshot, error) {
	return nil, nil
}

func (s *stubExclusiveStepLifecycle) InterruptCurrentAgentTurn(func(*RunSnapshot)) (*RunSnapshot, error) {
	return nil, nil
}

func (s *stubExclusiveStepLifecycle) IsBusy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.busy
}

func (s *stubExclusiveStepLifecycle) Snapshot() *RunSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneRunSnapshot(s.snapshot)
}

func (s *stubExclusiveStepLifecycle) WithActiveStep(fn func(stepID string) error) (bool, error) {
	s.mu.Lock()
	stepID := s.activeStepID
	s.mu.Unlock()
	if stepID == "" || fn == nil {
		return false, nil
	}
	return true, fn(runtimeTestStepID(stepID))
}

func (s *stubExclusiveStepLifecycle) ResolveActiveOutputStep(expectedStepID *string) (*string, error) {
	s.mu.Lock()
	stepID := s.activeStepID
	s.mu.Unlock()
	if stepID == "" {
		return nil, nil
	}
	if expectedStepID != nil && stepID != *expectedStepID {
		return nil, ErrActiveStepInactive
	}
	return &stepID, nil
}

func (s *stubExclusiveStepLifecycle) ApplyForActiveStep(stepID string, apply func() error) error {
	s.mu.Lock()
	activeStepID := s.activeStepID
	s.mu.Unlock()
	if activeStepID == "" || activeStepID != stepID || apply == nil {
		return ErrActiveStepInactive
	}
	return apply()
}

func (s *stubExclusiveStepLifecycle) BeginAgentStepBoundary(context.Context) error {
	return nil
}

func (s *stubExclusiveStepLifecycle) CompleteAgentStepBoundary(context.Context) error {
	return nil
}

func (s *stubExclusiveStepLifecycle) DrainAgentStepBoundary(context.Context) error {
	return nil
}

func (s *stubExclusiveStepLifecycle) EndAgentStepBoundary() {}

func (s *stubExclusiveStepLifecycle) ApplyForExactGoalStep(runID string, stepID string, apply func() error) error {
	s.mu.Lock()
	activeStepID := s.activeStepID
	snapshot := cloneRunSnapshot(s.snapshot)
	s.mu.Unlock()
	if activeStepID == "" || activeStepID != stepID || snapshot == nil ||
		snapshot.RunID != runID || snapshot.StepID != stepID || apply == nil {
		return ErrActiveStepInactive
	}
	return apply()
}

func (s *stubExclusiveStepLifecycle) ValidateExactOutput(stepID string, _ bool) error {
	s.mu.Lock()
	activeStepID := s.activeStepID
	s.mu.Unlock()
	if activeStepID == "" || activeStepID != stepID {
		return ErrActiveStepInactive
	}
	return nil
}

func (s *stubExclusiveStepLifecycle) setBusy(busy bool) {
	s.mu.Lock()
	s.busy = busy
	s.mu.Unlock()
}

func (s *stubExclusiveStepLifecycle) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runCalls
}

func TestExclusiveStepLifecycleRejectsConcurrentRun(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	started := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn}, func(stepCtx context.Context, stepID string) error {
			close(started)
			<-release
			return nil
		})
	}()

	select {
	case <-started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for first exclusive step")
	}

	err := lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn}, func(stepCtx context.Context, stepID string) error {
		return nil
	})
	if !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("expected busy error, got %v", err)
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first run: %v", err)
	}
	if lifecycle.IsBusy() {
		t.Fatal("expected exclusive step lifecycle to be idle after completion")
	}
}

func TestExclusiveStepLifecycleRejectsCanceledContextBeforeActiveRun(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := lifecycle.Run(ctx, exclusiveStepOptions{ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
		t.Fatal("canceled operation must not enter exclusive step body")
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context canceled", err)
	}
	if snapshot := lifecycle.Snapshot(); snapshot != nil {
		t.Fatalf("canceled pre-active run left active snapshot: %+v", snapshot)
	}
}

func TestExclusiveStepLifecycleClosesActiveStepQueueBeforeFinalDrain(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	stepCtx, stepID, err := lifecycle.begin(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if stepCtx == nil || stepID == "" {
		t.Fatalf("begin returned ctx=%v stepID=%q, want active step", stepCtx, stepID)
	}

	activeStepID, err := lifecycle.ResolveActiveOutputStep(nil)
	if err != nil || activeStepID == nil || *activeStepID != stepID {
		t.Fatalf("ResolveActiveOutputStep before close = %v/%v, want %q", activeStepID, err, stepID)
	}

	lifecycle.closeActiveStepQueue(stepID)
	activeStepID, err = lifecycle.ResolveActiveOutputStep(nil)
	if activeStepID != nil || !errors.Is(err, ErrActiveStepInactive) {
		t.Fatalf("ResolveActiveOutputStep after close = %v/%v, want inactive", activeStepID, err)
	}
	lifecycle.end()
}

func TestExclusiveStepAuthorityRejectsInterruptedStepBeforeFinalDrain(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	stepCtx, stepID, err := lifecycle.begin(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if stepCtx == nil || stepID == "" {
		t.Fatalf("begin returned ctx=%v stepID=%q, want active step", stepCtx, stepID)
	}
	if _, err := lifecycle.InterruptCurrent(nil); err != nil {
		t.Fatalf("InterruptCurrent: %v", err)
	}
	err = eng.ApplyForActiveStep(stepID, func() error {
		t.Fatal("active-step callback ran after interruption")
		return nil
	})
	if !errors.Is(err, ErrActiveStepInactive) {
		t.Fatalf("ApplyForActiveStep after interruption error = %v, want ErrActiveStepInactive", err)
	}
	lifecycle.end()
}

func TestExclusiveStepLifecycleSnapshotTracksActiveRun(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn}, func(stepCtx context.Context, stepID string) error {
			close(started)
			<-release
			return nil
		})
	}()

	select {
	case <-started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for run start")
	}

	snapshot := lifecycle.Snapshot()
	if snapshot == nil {
		t.Fatal("expected active run snapshot")
	}
	if snapshot.RunID == "" || snapshot.StepID == "" {
		t.Fatalf("expected run and step ids, got %+v", snapshot)
	}
	if snapshot.Status != RunStatusRunning {
		t.Fatalf("run status = %q, want running", snapshot.Status)
	}
	if snapshot.StartedAt.IsZero() {
		t.Fatal("expected started timestamp")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if snapshot := lifecycle.Snapshot(); snapshot != nil {
		t.Fatalf("expected run snapshot cleared after completion, got %+v", snapshot)
	}
}

func TestExclusiveStepLifecycleEmitsCompletedRunStatePayloads(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	if err := lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn, EmitRunState: true}, func(context.Context, string) error {
		return nil
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	runEvents := collectRunStateEvents(events)
	if len(runEvents) != 2 {
		t.Fatalf("expected 2 run-state events, got %+v", runEvents)
	}
	started := runEvents[0]
	finished := runEvents[1]
	if !started.Lifecycle.IsRunning() || started.RunID == "" {
		t.Fatalf("expected busy start event with run id, got %+v", started)
	}
	if started.Status != RunStatusRunning || started.StartedAt.IsZero() || !started.FinishedAt.IsZero() {
		t.Fatalf("unexpected start event payload: %+v", started)
	}
	if finished.Lifecycle.IsRunning() {
		t.Fatalf("expected final run-state event to clear busy, got %+v", finished)
	}
	if finished.RunID != started.RunID {
		t.Fatalf("expected stable run id across lifecycle, started=%+v finished=%+v", started, finished)
	}
	if finished.Status != RunStatusCompleted || finished.StartedAt.IsZero() || finished.FinishedAt.IsZero() {
		t.Fatalf("unexpected finished payload: %+v", finished)
	}
	if finished.FinishedAt.Before(finished.StartedAt) {
		t.Fatalf("expected finished timestamp after start, got %+v", finished)
	}
}

func TestExclusiveStepLifecycleEmitsInterruptedRunStatePayloads(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn, EmitRunState: true}, func(stepCtx context.Context, stepID string) error {
			close(started)
			<-stepCtx.Done()
			return stepCtx.Err()
		})
	}()

	select {
	case <-started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for interruptible step")
	}

	if err := lifecycle.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if err := lifecycle.Interrupt(); err != nil {
		t.Fatalf("interrupt replay: %v", err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled run, got %v", err)
	}

	runEvents := collectRunStateEvents(events)
	if len(runEvents) != 2 {
		t.Fatalf("expected 2 run-state events, got %+v", runEvents)
	}
	startedEvent := runEvents[0]
	finished := runEvents[1]
	if startedEvent.RunID == "" || startedEvent.Status != RunStatusRunning {
		t.Fatalf("unexpected start event payload: %+v", startedEvent)
	}
	if finished.RunID != startedEvent.RunID {
		t.Fatalf("expected stable run id across interruption, started=%+v finished=%+v", startedEvent, finished)
	}
	if finished.Lifecycle.IsRunning() || finished.Status != RunStatusInterrupted {
		t.Fatalf("expected interrupted final state, got %+v", finished)
	}
	if finished.FinishedAt.IsZero() || finished.StartedAt.IsZero() {
		t.Fatalf("expected interrupted payload timestamps, got %+v", finished)
	}
}

func TestExclusiveStepLifecycleAgentTurnInterruptMatchesOnlyAgentTurns(t *testing.T) {
	for _, test := range []struct {
		name      string
		kind      ActiveKind
		closing   bool
		wantMatch bool
	}{
		{name: string(ActiveKindUserTurn), kind: ActiveKindUserTurn, wantMatch: true},
		{name: string(ActiveKindWorkflowTurn), kind: ActiveKindWorkflowTurn, wantMatch: true},
		{name: string(ActiveKindGoalLoop), kind: ActiveKindGoalLoop, wantMatch: true},
		{name: "closing_user_turn", kind: ActiveKindUserTurn, closing: true},
		{name: string(ActiveKindCompaction), kind: ActiveKindCompaction},
		{name: string(ActiveKindInspection), kind: ActiveKindInspection},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
			canceled := false
			lifecycle := &defaultExclusiveStepLifecycle{
				engine: eng,
				active: &exclusiveRunState{
					sequence:   1,
					activeKind: test.kind,
					cancel:     func() { canceled = true },
					runID:      uuid.NewString(),
					stepID:     uuid.NewString(),
					startedAt:  time.Now().UTC(),
					closing:    test.closing,
				},
			}

			interrupted, err := lifecycle.InterruptCurrentAgentTurn(nil)
			if matched := interrupted != nil; err != nil || matched != test.wantMatch {
				t.Fatalf("agent-turn interrupt matched/error=%t/%v, want %t for %s", matched, err, test.wantMatch, test.kind)
			}
			if canceled != test.wantMatch {
				t.Fatalf("agent-turn interrupt canceled=%t, want %t for %s", canceled, test.wantMatch, test.kind)
			}
			if lifecycle.active.interrupted != test.wantMatch {
				t.Fatalf("agent-turn interrupted state=%t, want %t for %s", lifecycle.active.interrupted, test.wantMatch, test.kind)
			}
		})
	}
}

func TestExclusiveStepLifecycleAgentTurnPersistenceFailureDoesNotCancel(t *testing.T) {
	persistErr := errors.New("interruption persistence failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	canceled := false
	lifecycle := &defaultExclusiveStepLifecycle{
		engine: eng,
		active: &exclusiveRunState{
			sequence:   1,
			activeKind: ActiveKindUserTurn,
			cancel:     func() { canceled = true },
			runID:      uuid.NewString(),
			stepID:     uuid.NewString(),
			startedAt:  time.Now().UTC(),
		},
	}
	gate.FailNext(persistErr)

	snapshot, err := lifecycle.InterruptCurrentAgentTurn(nil)
	if snapshot != nil || !errors.Is(err, persistErr) {
		t.Fatalf("agent-turn interrupt = (%+v, %v), want persistence failure", snapshot, err)
	}
	if canceled {
		t.Fatal("Agent Turn context was canceled before interruption persisted")
	}
	if lifecycle.active.interrupted {
		t.Fatal("persistence failure left Agent Turn marked interrupted")
	}
}

func TestExclusiveStepLifecycleInterruptPreservesPendingRecoveryUntilTerminalCleanup(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn}, func(stepCtx context.Context, stepID string) error {
			close(started)
			<-stepCtx.Done()
			return stepCtx.Err()
		})
	}()

	select {
	case <-started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for interruptible step")
	}

	if err := lifecycle.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled run, got %v", err)
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	if len(messages) == 0 {
		t.Fatal("expected interruption message")
	}
	last := messages[len(messages)-1]
	if last.MessageType == nil || *last.MessageType != llm.MessageTypeInterruption {
		t.Fatalf("expected interruption message type, got %+v", last)
	}
	if messageContent(last) != interruptMessage {
		t.Fatalf("unexpected interruption content %q", messageContent(last))
	}
	if len(messages) != 1 {
		t.Fatalf("interrupt replay appended duplicate messages: %+v", messages)
	}
}

func TestExclusiveStepLifecycleDiscardsStreamingMessageOnInterrupt(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn, EmitRunState: true}, func(stepCtx context.Context, stepID string) error {
			_ = eng.steer(runtimeTestStepID(stepID), steerAssistantDeltaIntent(llm.AssistantDelta{Text: "partial streamed answer"}))
			close(started)
			<-stepCtx.Done()
			return stepCtx.Err()
		})
	}()

	select {
	case <-started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for streaming step")
	}

	if err := lifecycle.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled run, got %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	sawReset := false
	for _, evt := range events {
		if evt.Kind == EventAssistantDeltaReset {
			sawReset = true
			break
		}
	}
	if !sawReset {
		t.Fatal("expected assistant delta reset event after interrupting a streaming step")
	}
}

func TestExclusiveStepLifecycleCanEmitRunStateWithoutPersistingDurableRun(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var events []Event
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	eng.stepLifecycle = lifecycle
	if err := lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn, EmitRunState: true}, func(context.Context, string) error {
		return nil
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if runEvents := collectRunStateEvents(events); len(runEvents) != 2 {
		t.Fatalf("expected run-state events, got %+v", runEvents)
	}
}

func TestExclusiveStepRuntimeAbortClosesAdmission(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	lifecycleErr := errors.New("step-ended publication failed")
	stepLifecycle := &callbackStepLifecycleSink{onTransition: func(transition StepLifecycleTransition) error {
		if transition == StepLifecycleTransitionEnded {
			return lifecycleErr
		}
		return nil
	}}
	eng := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5", StepLifecycle: stepLifecycle},
	)
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	cause := errors.New("result group persistence failed")
	fatal := &resultGroupFatal{Committed: false, Cause: cause}
	err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{
			ActiveKind:   ActiveKindUserTurn,
			EmitRunState: true,
		},
		func(_ context.Context, stepID string) error {
			return fatal
		},
	)
	if !errors.Is(err, fatal) || !errors.Is(err, lifecycleErr) {
		t.Fatalf("runtime abort error = %v, want fatal and step-ended failure", err)
	}
	diagnostics := 0
	for _, entry := range eng.ChatSnapshot().Entries {
		if entry.Role == string(transcript.EntryRoleDeveloperErrorFeedback) {
			diagnostics++
		}
	}
	if diagnostics != 1 {
		t.Fatalf("runtime abort terminal-publication diagnostics = %d, want one", diagnostics)
	}
	if _, submitErr := eng.SubmitUserMessage(context.Background(), "later"); !errors.Is(submitErr, ErrEngineClosed) {
		t.Fatalf("later submission error = %v, want ErrEngineClosed", submitErr)
	}
	if snapshot := eng.ChatSnapshot(); snapshot.StreamingError == "" {
		t.Fatal("runtime abort did not publish transient streaming failure")
	}
}

func collectRunStateEvents(events []Event) []RunState {
	runEvents := make([]RunState, 0, len(events))
	for _, evt := range events {
		if evt.Kind != EventRunStateChanged || evt.RunState == nil {
			continue
		}
		runEvents = append(runEvents, *evt.RunState)
	}
	return runEvents
}

func TestContextCompactorUsesExclusiveStepLifecycle(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	eng.compactionRuntimeState().SetManualCompactionEligible(true)
	if _, err := eng.compactionFlow.CompactContextWithAcceptance(context.Background(), "", nil, nil); err != nil {
		t.Fatalf("compact context: %v", err)
	}
	if snapshot := eng.stepLifecycle.Snapshot(); snapshot != nil {
		t.Fatalf("manual compaction retained active Step: %+v", snapshot)
	}
	client.mu.Lock()
	callCount := len(client.calls)
	client.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("expected one local compaction model call, got %d", callCount)
	}
}
