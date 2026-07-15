package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/toolspec"
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

type blockingStepLifecycleSink struct {
	endedStarted chan StepLifecycleSnapshot
	releaseEnded chan struct{}
}

type callbackStepLifecycleSink struct {
	onTransition func(StepLifecycleTransition) error
	mu           sync.Mutex
	transitions  []StepLifecycleTransition
}

func newBlockingStepLifecycleSink() *blockingStepLifecycleSink {
	return &blockingStepLifecycleSink{endedStarted: make(chan StepLifecycleSnapshot, 1), releaseEnded: make(chan struct{})}
}

func (s *blockingStepLifecycleSink) StepBegan(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (s *blockingStepLifecycleSink) StepEnded(_ context.Context, snapshot StepLifecycleSnapshot) error {
	s.endedStarted <- snapshot
	<-s.releaseEnded
	return nil
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

func (s *stubBackgroundNoticeScheduler) HandleBackgroundShellUpdate(BackgroundShellEvent, bool) {}
func (s *stubBackgroundNoticeScheduler) QueueDeveloperNotice(llm.Message)                       {}
func (s *stubBackgroundNoticeScheduler) DrainPendingNotices() []steeringIntent                  { return nil }
func (s *stubBackgroundNoticeScheduler) HasPendingNotices() bool                                { return false }
func (s *stubBackgroundNoticeScheduler) ConsumePendingBackgroundNotice(string) bool             { return false }
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
	return fn(ctx, "stub-step")
}

func (s *stubExclusiveStepLifecycle) Interrupt() error {
	return nil
}

func (s *stubExclusiveStepLifecycle) InterruptCurrent(func(*RunSnapshot)) (*RunSnapshot, error) {
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
	return true, fn(stepID)
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
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
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
	case <-time.After(3 * time.Second):
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
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
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

func TestExclusiveStepLifecycleBlocksSuccessorWhileTerminalPublicationPending(t *testing.T) {
	store := mustCreateTestSession(t)
	sink := newBlockingStepLifecycleSink()
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", StepLifecycle: sink})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	releaseStep := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			<-releaseStep
			return nil
		})
	}()

	close(releaseStep)
	var ended StepLifecycleSnapshot
	select {
	case ended = <-sink.endedStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for terminal publication")
	}
	if ended.Transition != StepLifecycleTransitionEnded || ended.RunID == "" {
		t.Fatalf("unexpected terminal snapshot: %+v", ended)
	}
	if snapshot := lifecycle.Snapshot(); snapshot != nil {
		t.Fatalf("active snapshot must be cleared before terminal publication, got %+v", snapshot)
	}
	if !lifecycle.IsBusy() {
		t.Fatal("terminal publication must keep exclusive lifecycle busy")
	}
	err := lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error { return nil })
	if !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("successor run while terminal publication is pending err = %v, want ErrAgentBusy", err)
	}

	close(sink.releaseEnded)
	if err := <-firstDone; err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error { return nil }); err != nil {
		t.Fatalf("successor after terminal publication: %v", err)
	}
}

func TestRunWhenIdleWaitsForTerminalPublication(t *testing.T) {
	store := mustCreateTestSession(t)
	sink := newBlockingStepLifecycleSink()
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5", StepLifecycle: sink})
	eng.ensureOrchestrationCollaborators()
	lifecycle := eng.stepLifecycle.(*defaultExclusiveStepLifecycle)
	releaseStep := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn}, func(context.Context, string) error {
			<-releaseStep
			return nil
		})
	}()

	close(releaseStep)
	select {
	case <-sink.endedStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for terminal publication")
	}
	startedSuccessor := make(chan struct{})
	successorDone := make(chan error, 1)
	go func() {
		successorDone <- eng.RunWhenIdle(context.Background(), ActiveKindRuntimeMaintenance, func() error {
			close(startedSuccessor)
			return nil
		})
	}()
	select {
	case <-startedSuccessor:
		t.Fatal("RunWhenIdle successor started before terminal publication completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(sink.releaseEnded)
	if err := <-firstDone; err != nil {
		t.Fatalf("first run: %v", err)
	}
	select {
	case <-startedSuccessor:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for RunWhenIdle successor after terminal publication")
	}
	if err := <-successorDone; err != nil {
		t.Fatalf("RunWhenIdle successor: %v", err)
	}
}

func TestExclusiveStepLifecycleClosesActiveStepQueueBeforeFinalDrain(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	stepCtx, stepID, err := lifecycle.begin(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if stepCtx == nil || stepID == "" {
		t.Fatalf("begin returned ctx=%v stepID=%q, want active step", stepCtx, stepID)
	}

	called := false
	active, err := lifecycle.WithActiveStep(func(string) error {
		called = true
		return nil
	})
	if err != nil || !active || !called {
		t.Fatalf("WithActiveStep before close active=%t called=%t err=%v, want active callback", active, called, err)
	}

	lifecycle.closeActiveStepQueue(stepID)
	active, err = lifecycle.WithActiveStep(func(string) error {
		t.Fatal("active-step callback ran after queue close")
		return nil
	})
	if !errors.Is(err, ErrAgentBusy) {
		t.Fatalf("WithActiveStep after close error = %v, want ErrAgentBusy", err)
	}
	if !active {
		t.Fatal("WithActiveStep after close active=false, want true with busy error")
	}
	lifecycle.end()
}

func TestExclusiveStepLifecycleSnapshotTracksActiveRun(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
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
	case <-time.After(3 * time.Second):
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
	store := mustCreateTestSession(t)
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
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
	store := mustCreateTestSession(t)
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
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
	case <-time.After(3 * time.Second):
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

func TestExclusiveStepLifecycleInterruptPreservesPendingRecoveryUntilTerminalCleanup(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

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
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for interruptible step")
	}

	if err := lifecycle.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if store.Meta().PendingModelRecovery != nil {
		t.Fatal("interrupt request created model recovery before provider-visible output")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled run, got %v", err)
	}
	if store.Meta().PendingModelRecovery != nil {
		t.Fatal("expected pending recovery to remain cleared after interrupted run exits")
	}

	messages := eng.transcriptRuntimeState().SnapshotMessages()
	if len(messages) == 0 {
		t.Fatal("expected interruption message")
	}
	last := messages[len(messages)-1]
	if last.MessageType != llm.MessageTypeInterruption {
		t.Fatalf("expected interruption message type, got %+v", last)
	}
	if last.Content != interruptMessage {
		t.Fatalf("unexpected interruption content %q", last.Content)
	}
	if len(messages) != 1 {
		t.Fatalf("interrupt replay appended duplicate messages: %+v", messages)
	}
}

func TestExclusiveStepLifecycleDiscardsStreamingMessageOnInterrupt(t *testing.T) {
	store := mustCreateTestSession(t)
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn, EmitRunState: true}, func(stepCtx context.Context, stepID string) error {
			_ = eng.steer(stepID, steerAssistantDeltaIntent(llm.AssistantDelta{Text: "partial streamed answer"}))
			close(started)
			<-stepCtx.Done()
			return stepCtx.Err()
		})
	}()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
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
	store := mustCreateTestSession(t)
	var events []Event
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	if err := lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn, EmitRunState: true}, func(context.Context, string) error {
		return nil
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if runEvents := collectRunStateEvents(events); len(runEvents) != 2 {
		t.Fatalf("expected run-state events, got %+v", runEvents)
	}
}

func TestExclusiveStepLifecyclePublishesTerminalActivityBeforeFinishPersistenceFailures(t *testing.T) {
	observer := &armedTestPersistenceObserver{
		delegate: runtimeTestSessionPersistence,
		err:      errors.New("finish persistence failed"),
	}
	store := mustCreateNamedTestSession(t, "ws", t.TempDir(), session.WithPersistenceObserver(observer))
	sink := &callbackStepLifecycleSink{onTransition: func(transition StepLifecycleTransition) error {
		if transition == StepLifecycleTransitionEnded {
			observer.armed.Store(true)
		}
		return nil
	}}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5", StepLifecycle: sink})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	err := lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn, EmitRunState: true}, func(_ context.Context, stepID string) error {
		if err := eng.markProviderVisibleModelRecovery(stepID); err != nil {
			return err
		}
		return nil
	})

	if err == nil {
		t.Fatal("run err = nil, want finish persistence failure")
	}
	if !sink.seen(StepLifecycleTransitionEnded) {
		t.Fatal("terminal StepEnded publication was skipped before finish persistence failure")
	}
	if lifecycle.IsBusy() || lifecycle.Snapshot() != nil {
		t.Fatalf("lifecycle remained active after finish failure: busy=%v snapshot=%+v", lifecycle.IsBusy(), lifecycle.Snapshot())
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

func TestExclusiveStepLifecycleInterruptSkipsStaleRunCleanup(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}
	lifecycle.active = &exclusiveRunState{sequence: 1, cancel: func() {
		lifecycle.mu.Lock()
		lifecycle.active = &exclusiveRunState{sequence: 2}
		lifecycle.mu.Unlock()
	}}
	if err := store.SetPendingModelRecovery(session.PendingModelRecovery{RecoveryID: "stale", StepID: "step-stale", Reason: "test", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	if err := lifecycle.Interrupt(); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if store.Meta().PendingModelRecovery == nil {
		t.Fatal("expected stale interrupt to leave pending recovery intact")
	}
	if len(eng.transcriptRuntimeState().SnapshotMessages()) != 0 {
		t.Fatalf("expected stale interrupt to avoid appending interruption message, got %+v", eng.transcriptRuntimeState().SnapshotMessages())
	}
}

func TestExclusiveStepLifecycleClearsPendingRecoveryBeforeSchedulingBackground(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	scheduled := false
	lifecycle := &defaultExclusiveStepLifecycle{
		engine: eng,
		background: &stubBackgroundNoticeScheduler{scheduleIfIdle: func() {
			scheduled = true
			if store.Meta().PendingModelRecovery != nil {
				t.Fatal("expected pending recovery to be cleared before scheduling background work")
			}
		}},
	}

	if err := lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn}, func(_ context.Context, stepID string) error {
		if err := eng.markProviderVisibleModelRecovery(stepID); err != nil {
			return err
		}
		if store.Meta().PendingModelRecovery == nil {
			t.Fatal("expected pending recovery during exclusive run")
		}
		return nil
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !scheduled {
		t.Fatal("expected background scheduler to run after exclusive step completion")
	}
}

func TestExclusiveStepLifecycleDoesNotClearSuccessorPendingRecovery(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: eng}

	if err := lifecycle.Run(context.Background(), exclusiveStepOptions{ActiveKind: ActiveKindUserTurn}, func(_ context.Context, stepID string) error {
		if err := eng.markProviderVisibleModelRecovery(stepID); err != nil {
			return err
		}
		if err := store.SetPendingModelRecovery(session.PendingModelRecovery{RecoveryID: "successor", StepID: "successor-step", Reason: "test", CreatedAt: time.Now().UTC()}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if store.Meta().PendingModelRecovery == nil {
		t.Fatal("successor pending recovery was cleared by previous step")
	}
	if got := store.Meta().PendingModelRecovery.StepID; got != "successor-step" {
		t.Fatalf("pending recovery step = %q, want successor-step", got)
	}
}

func TestBackgroundNoticeSchedulerSchedulesAfterBusyStepEnds(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "background done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	steps := &stubExclusiveStepLifecycle{}
	steps.setBusy(true)
	scheduler := &defaultBackgroundNoticeScheduler{engine: eng, steps: steps}

	scheduler.QueueDeveloperNotice(llm.Message{
		Role:        llm.RoleDeveloper,
		MessageType: llm.MessageTypeBackgroundNotice,
		Name:        "1000",
		Content:     "Background shell 1000 completed.",
	})

	if steps.calls() != 0 {
		t.Fatalf("expected no scheduler run while busy, got %d", steps.calls())
	}
	client.mu.Lock()
	busyCalls := len(client.calls)
	client.mu.Unlock()
	if busyCalls != 0 {
		t.Fatalf("expected no model calls while scheduler busy, got %d", busyCalls)
	}

	steps.setBusy(false)
	scheduler.ScheduleIfIdle()

	deadline := time.After(3 * time.Second)
	for {
		client.mu.Lock()
		callCount := len(client.calls)
		client.mu.Unlock()
		if callCount == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for scheduled background run, calls=%d runs=%d", callCount, steps.calls())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if steps.calls() != 1 {
		t.Fatalf("expected one scheduled run after idle transition, got %d", steps.calls())
	}
	client.mu.Lock()
	request := client.calls[0]
	client.mu.Unlock()
	foundNotice := false
	for _, msg := range requestMessages(request) {
		if msg.Role == llm.RoleDeveloper && msg.MessageType == llm.MessageTypeBackgroundNotice && msg.Name == "1000" {
			foundNotice = true
			break
		}
	}
	if !foundNotice {
		t.Fatalf("expected scheduled request to include queued background notice, messages=%+v", requestMessages(request))
	}
	if pending := scheduler.pendingSnapshot(); len(pending) != 0 {
		t.Fatalf("expected queued notices to be drained, got %+v", pending)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
}

func TestContextCompactorUsesExclusiveStepLifecycle(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "summary"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", CompactionMode: "local"})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	steps := &stubExclusiveStepLifecycle{}
	compactor := &defaultContextCompactor{engine: eng, steps: steps}
	if err := compactor.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact context: %v", err)
	}
	if steps.calls() != 1 {
		t.Fatalf("expected compaction to execute through exclusive step lifecycle once, got %d", steps.calls())
	}
	client.mu.Lock()
	callCount := len(client.calls)
	client.mu.Unlock()
	if callCount != 1 {
		t.Fatalf("expected one local compaction model call, got %d", callCount)
	}
}
