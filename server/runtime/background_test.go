package runtime

import (
	"encoding/json"
	"errors"
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

func TestBackgroundFinalAnswerAppliesRuntimeMutationAtStepBoundary(t *testing.T) {
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
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{Model: "gpt-5"})
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
		t.Fatalf("Runtime mutation completed during protected background Agent Step: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(releaseProvider)
	select {
	case err := <-mutationDone:
		if err != nil {
			t.Fatalf("apply Runtime mutation at background Step Boundary: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("background Step Boundary stranded the Runtime mutation")
	}
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
				CallID:       "write-stdin-call",
				Name:         toolspec.ToolWriteStdin,
				Output:       json.RawMessage(`{"background_session_id":42,"background_running":false,"backgrounded":true}`),
				Presentation: &presentation,
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
