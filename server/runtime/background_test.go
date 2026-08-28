package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

type blockingBackgroundStepLifecycle struct {
	started chan struct{}
	stopped chan error
}

func (s *blockingBackgroundStepLifecycle) Run(ctx context.Context, _ exclusiveStepOptions, _ func(stepCtx context.Context, stepID string) error) error {
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	err := ctx.Err()
	select {
	case s.stopped <- err:
	default:
	}
	return err
}

func (s *blockingBackgroundStepLifecycle) RunNext(ctx context.Context, options exclusiveStepOptions, fn func(stepCtx context.Context, stepID string) error) error {
	return s.Run(ctx, options, fn)
}

func (s *blockingBackgroundStepLifecycle) AcquireReservation(*exclusiveStepReservation) error {
	return nil
}
func (s *blockingBackgroundStepLifecycle) ReleaseReservation(*exclusiveStepReservation) {}
func (s *blockingBackgroundStepLifecycle) Interrupt() error                             { return nil }
func (s *blockingBackgroundStepLifecycle) InterruptCurrent(func(*RunSnapshot)) (*RunSnapshot, error) {
	return nil, nil
}
func (s *blockingBackgroundStepLifecycle) InterruptCurrentAgentTurn(func(*RunSnapshot)) (*RunSnapshot, error) {
	return nil, nil
}
func (s *blockingBackgroundStepLifecycle) IsBusy() bool { return false }
func (s *blockingBackgroundStepLifecycle) Snapshot() *RunSnapshot {
	return nil
}
func (s *blockingBackgroundStepLifecycle) WithActiveStep(func(stepID string) error) (bool, error) {
	return false, nil
}
func (s *blockingBackgroundStepLifecycle) ApplyForActiveStep(string, func() error) error {
	return ErrActiveStepInactive
}
func (s *blockingBackgroundStepLifecycle) BeginAgentStepBoundary(context.Context) error {
	return nil
}
func (s *blockingBackgroundStepLifecycle) CompleteAgentStepBoundary(context.Context) error {
	return nil
}
func (s *blockingBackgroundStepLifecycle) DrainAgentStepBoundary(context.Context) error {
	return nil
}
func (s *blockingBackgroundStepLifecycle) EndAgentStepBoundary() {}

func TestBackgroundNoticeSchedulerCancelsQueuedContinuationOnEngineClose(t *testing.T) {
	t.Parallel()
	steps := &blockingBackgroundStepLifecycle{
		started: make(chan struct{}, 1),
		stopped: make(chan error, 1),
	}
	eng := &Engine{}
	scheduler := &defaultBackgroundNoticeScheduler{engine: eng, steps: steps}

	scheduler.QueueDeveloperNotice(llm.Message{Role: llm.RoleDeveloper, Content: textutil.Value("queued background notice")})

	select {
	case <-steps.started:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("background continuation did not start")
	}

	closeDone := make(chan struct{})
	go func() {
		_ = eng.Close()
		close(closeDone)
	}()

	select {
	case err := <-steps.stopped:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("step lifecycle stopped with %v, want context canceled", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("background continuation was not canceled on engine close")
	}

	select {
	case <-closeDone:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("engine close did not wait for queued background continuation")
	}
}

func TestSteerBackgroundContinuationFailureUsesDeveloperErrorFeedback(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	if err := engine.SteerBackgroundContinuationFailure(errors.New("provider unavailable")); err != nil {
		t.Fatalf("steer background continuation failure: %v", err)
	}
	entries := engine.ChatSnapshot().Entries
	if len(entries) != 1 ||
		entries[0].Role != string(transcript.EntryRoleDeveloperErrorFeedback) ||
		entries[0].Text == "" {
		t.Fatalf("background failure entries = %+v, want one developer error feedback entry", entries)
	}

	mustBlockTestEventLogAppends(t, store)
	if err := engine.SteerBackgroundContinuationFailure(errors.New("retry failed")); err == nil {
		t.Fatal("background continuation failure steering swallowed persistence error")
	}
}

func TestBackgroundNoticeSchedulerSchedulingRaceWithEngineCloseDoesNotPanic(t *testing.T) {
	t.Parallel()
	for i := 0; i < 200; i++ {
		steps := &blockingBackgroundStepLifecycle{
			started: make(chan struct{}, 1),
			stopped: make(chan error, 1),
		}
		eng := &Engine{}
		scheduler := &defaultBackgroundNoticeScheduler{engine: eng, steps: steps}
		panicErrs := make(chan error, 4)
		start := make(chan struct{})
		var wg sync.WaitGroup

		runSafe := func(fn func()) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() {
					if recovered := recover(); recovered != nil {
						panicErrs <- fmt.Errorf("panic: %v", recovered)
					}
				}()
				<-start
				fn()
			}()
		}

		runSafe(func() {
			scheduler.QueueDeveloperNotice(llm.Message{Role: llm.RoleDeveloper, Content: textutil.Value("queued background notice")})
		})
		runSafe(func() {
			scheduler.QueueDeveloperNotice(llm.Message{Role: llm.RoleDeveloper, Content: textutil.Value("queued schedule-if-idle")})
			scheduler.ScheduleIfIdle()
		})
		runSafe(func() {
			_ = eng.Close()
		})

		close(start)
		wg.Wait()
		close(panicErrs)
		for err := range panicErrs {
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
		}

		select {
		case err := <-steps.stopped:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("iteration %d: stopped with %v, want context canceled", i, err)
			}
		default:
		}

		closeDone := make(chan struct{})
		go func() {
			_ = eng.Close()
			close(closeDone)
		}()
		select {
		case <-closeDone:
		case <-time.After(runtimeTestSynchronizationTimeout):
			t.Fatalf("iteration %d: close remained blocked after race", i)
		}
	}
}

func TestBackgroundNoticeSchedulerPreservesNoticeWhenMetaContextPreparationFails(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{Model: "gpt-5"})
	mustBlockTestEventLogAppends(t, store)
	steps := &stubExclusiveStepLifecycle{busy: true}
	scheduler := &defaultBackgroundNoticeScheduler{engine: engine, steps: steps}

	scheduler.QueueDeveloperNotice(llm.Message{
		Role:    llm.RoleDeveloper,
		Content: textutil.Value("queued background notice"),
	})

	if _, err := scheduler.runQueuedNotices(context.Background()); err == nil {
		t.Fatal("background notice preparation unexpectedly succeeded")
	}
	if !scheduler.HasPendingNotices() {
		t.Fatal("background notice was lost after meta-context preparation failed")
	}
}

func TestTerminalBackgroundUpdateQueuesAcrossClosingOrInterruptedStep(t *testing.T) {
	for _, test := range []struct {
		name        string
		closing     bool
		interrupted bool
	}{
		{name: "closing", closing: true},
		{name: "interrupted", interrupted: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
			lifecycle := &defaultExclusiveStepLifecycle{
				engine: engine,
				active: &exclusiveRunState{
					sequence:    1,
					activeKind:  ActiveKindUserTurn,
					cancel:      func() {},
					runID:       "11111111-1111-4111-8111-111111111111",
					stepID:      "22222222-2222-4222-8222-222222222222",
					startedAt:   time.Now().UTC(),
					closing:     test.closing,
					interrupted: test.interrupted,
				},
			}
			engine.stepLifecycle = lifecycle
			engine.ensureOrchestrationCollaborators()
			if err := engine.pauseRuntimeOperations(t.Context()); err != nil {
				t.Fatalf("pause Runtime operations: %v", err)
			}

			done := make(chan struct{})
			go func() {
				engine.HandleBackgroundShellUpdate(BackgroundShellEvent{
					Type:  BackgroundShellEventCompleted,
					ID:    "shell",
					State: "completed",
				}, true)
				close(done)
			}()
			deadline := time.Now().Add(runtimeTestSynchronizationTimeout)
			for !engine.hasPendingRuntimeOperations() {
				if !time.Now().Before(deadline) {
					t.Fatal("timed out waiting for queued background update")
				}
				time.Sleep(time.Millisecond)
			}
			if engine.backgroundFlow.HasPendingNotices() {
				t.Fatal("terminal background continuation queued before its live update was applied")
			}

			lifecycle.end()
			if err := engine.drainRuntimeOperations(t.Context()); err != nil {
				t.Fatalf("drain Runtime operations: %v", err)
			}
			select {
			case <-done:
			case <-time.After(runtimeTestSynchronizationTimeout):
				t.Fatal("queued background update did not complete after Runtime drain")
			}
			deadline = time.Now().Add(runtimeTestSynchronizationTimeout)
			for {
				foundNotice := false
				for _, entry := range engine.ChatSnapshot().Entries {
					if entry.MessageType == llm.MessageTypeBackgroundNotice {
						foundNotice = true
						break
					}
				}
				if foundNotice {
					break
				}
				if !time.Now().Before(deadline) {
					t.Fatalf(
						"terminal background continuation was not eventually persisted; pending=%t",
						engine.backgroundFlow.HasPendingNotices(),
					)
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestTerminalBackgroundUpdateDoesNotQueueWhenRuntimeIsClosed(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	engine.closed.Store(true)

	engine.HandleBackgroundShellUpdate(BackgroundShellEvent{
		Type:  BackgroundShellEventCompleted,
		ID:    "shell",
		State: "completed",
	}, true)

	if engine.backgroundFlow.HasPendingNotices() {
		t.Fatal("closed Runtime retained a notice for an unrecorded background completion")
	}
}

func TestRuntimeSteeringRejectsClosedEngineWithoutQueueing(t *testing.T) {
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	engine.closed.Store(true)

	err := engine.steerRuntime(
		steerEventIntent(Event{Kind: EventStreamingErrorUpdated}),
	)
	if !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("closed Runtime steering error = %v, want ErrEngineClosed", err)
	}
	if engine.hasPendingRuntimeOperations() {
		t.Fatal("closed Runtime accepted pending Steering")
	}
}

func TestBackgroundAgentStepDoesNotBlockImmediateSettingMutation(t *testing.T) {
	providerStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	client := &hookClient{
		response: llm.Response{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("background work handled"),
			},
		},
		beforeReturn: func() error {
			close(providerStarted)
			<-releaseProvider
			return nil
		},
	}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:                   "gpt-5",
		SupportedThinkingValues: []string{"low", "medium"},
	})
	scheduler := engine.backgroundFlow.(*defaultBackgroundNoticeScheduler)
	scheduler.queueDeveloperNotice(llm.Message{
		Role:    llm.RoleDeveloper,
		Content: textutil.Value("background process completed"),
	}, false)

	backgroundDone := make(chan error, 1)
	go func() {
		_, err := scheduler.runQueuedNotices(t.Context())
		backgroundDone <- err
	}()
	select {
	case <-providerStarted:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for background provider request")
	}

	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- engine.SetThinkingLevel(t.Context(), "low")
	}()
	select {
	case err := <-mutationDone:
		if err != nil {
			t.Fatalf("apply immediate setting during background Agent Step: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("background Agent Step blocked immediate setting mutation")
	}
	if got := engine.ThinkingLevel(); got != "low" {
		t.Fatalf("Thinking during background Agent Step = %q, want low", got)
	}

	close(releaseProvider)
	select {
	case err := <-backgroundDone:
		if err != nil {
			t.Fatalf("background final answer: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for background final answer")
	}
	if err := engine.SetThinkingLevel(t.Context(), "medium"); err != nil {
		t.Fatalf("apply Runtime mutation after background final answer: %v", err)
	}
}

func TestBackgroundNoticeOwnershipFollowsWriteStdinCompletionCommitReceipt(t *testing.T) {
	for _, tt := range []struct {
		name        string
		block       bool
		wantPending bool
	}{
		{name: "committed"},
		{name: "uncommitted append failure", block: true, wantPending: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
			steps := &stubExclusiveStepLifecycle{
				busy:     true,
				snapshot: &RunSnapshot{RunID: "11111111-1111-4111-8111-111111111111", StepID: "step"},
			}
			scheduler := &defaultBackgroundNoticeScheduler{engine: engine, steps: steps}
			engine.stepLifecycle = &stubExclusiveStepLifecycle{busy: true}
			engine.stepLifecycle = steps
			engine.backgroundFlow = scheduler

			scheduler.QueueDeveloperNotice(llm.Message{
				Role:    llm.RoleDeveloper,
				Name:    textutil.Value("42"),
				Content: textutil.Value("queued background notice"),
			})
			if tt.block {
				mustBlockTestEventLogAppends(t, store)
			}

			presentation := transcript.NormalizeToolCallMeta(transcript.ToolCallMeta{ToolName: string(toolspec.ToolWriteStdin)})
			receipt, _, err := engine.persistToolCompletionRaw("step", tools.Result{
				CallID:                       "write-stdin-call",
				Name:                         toolspec.ToolWriteStdin,
				Output:                       json.RawMessage(`{"background_session_id":42,"background_running":false,"backgrounded":true}`),
				Presentation:                 &presentation,
				CompletedBackgroundSessionID: textutil.Value(42),
			})
			if receipt.Committed == tt.wantPending {
				t.Fatalf("completion receipt = %+v, want committed=%t", receipt, !tt.wantPending)
			}
			if tt.wantPending && err == nil {
				t.Fatal("uncommitted completion did not surface append failure")
			}
			if !tt.wantPending && err != nil {
				t.Fatalf("persist committed completion: %v", err)
			}
			if got := scheduler.HasPendingNotices(); got != tt.wantPending {
				t.Fatalf("pending notice after completion = %t, want %t", got, tt.wantPending)
			}
		})
	}
}

func TestInvalidWriteStdinCompletionProvenanceFailsWithoutPersistence(t *testing.T) {
	for _, test := range []struct {
		name  string
		debug bool
	}{
		{name: "production", debug: false},
		{name: "debug", debug: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{
				Model: "gpt-5",
				Debug: test.debug,
			})
			presentation := transcript.NormalizeToolCallMeta(transcript.ToolCallMeta{ToolName: string(toolspec.ToolWriteStdin)})

			persist := func() (session.CommitReceipt, error) {
				receipt, _, err := engine.persistToolCompletionRaw("step", tools.Result{
					CallID:                       "write-stdin-call",
					Name:                         toolspec.ToolWriteStdin,
					Output:                       json.RawMessage(`{"error":"invalid completion"}`),
					IsError:                      true,
					Presentation:                 &presentation,
					CompletedBackgroundSessionID: textutil.Value(0),
				})
				return receipt, err
			}

			if test.debug {
				defer func() {
					recovered := recover()
					var provenanceErr invalidBackgroundCompletionProvenanceError
					panicErr, ok := recovered.(error)
					if !ok || !errors.As(panicErr, &provenanceErr) {
						t.Fatalf("debug invalid completion panic = %v, want typed provenance error", recovered)
					}
				}()
				_, _ = persist()
				t.Fatal("debug invalid completion did not panic")
			}

			receipt, err := persist()
			if receipt.Committed {
				t.Fatal("invalid completion provenance was persisted")
			}
			var provenanceErr invalidBackgroundCompletionProvenanceError
			if !errors.As(err, &provenanceErr) {
				t.Fatalf("persist invalid completion error = %v, want typed provenance error", err)
			}
			if _, found := engine.transcriptRuntimeState().ToolCompletionSnapshot("write-stdin-call"); found {
				t.Fatal("invalid completion provenance was applied to runtime state")
			}
		})
	}
}

func TestFlushPendingUserInjectionsRestoresOnlyLaterNoticeAfterCommittedObserverFailure(t *testing.T) {
	observerErr := errors.New("background notice observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	stepID := runtimeTestStepID("background-notice-observer-failure")
	steps := &stubExclusiveStepLifecycle{
		busy:         true,
		snapshot:     &RunSnapshot{RunID: "11111111-1111-4111-8111-111111111111", StepID: stepID},
		activeStepID: stepID,
	}
	scheduler := &defaultBackgroundNoticeScheduler{engine: engine, steps: steps}
	engine.stepLifecycle = steps
	engine.backgroundFlow = scheduler
	lifecycle := newDefaultMessageLifecycle(engine, scheduler)
	for _, sessionID := range []string{"first", "second"} {
		scheduler.QueueDeveloperNotice(llm.Message{
			Role:    llm.RoleDeveloper,
			Name:    textutil.Value(sessionID),
			Content: textutil.Value(sessionID + " notice"),
		})
	}
	gate.FailNext(observerErr)

	_, err := lifecycle.FlushPendingUserInjections(stepID, allPendingUserInjectionSelection{})
	if !errors.Is(err, observerErr) {
		t.Fatalf("flush error = %v, want observer failure", err)
	}
	pending := scheduler.pendingSnapshot()
	if len(pending) != 1 || pending[0].sessionID != "second" {
		t.Fatalf("pending notices after committed failure = %+v", pending)
	}
}
