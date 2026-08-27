package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestPersistedMessageAppliesProjectionByCommitReceipt(t *testing.T) {
	t.Parallel()
	t.Run("uncommitted error", func(t *testing.T) {
		store := mustCreateTestSession(t)
		var events []Event
		eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		})
		mustBlockTestEventLogAppends(t, store)

		err := eng.steer(runtimeTestStepID("step-1"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("uncommitted")}}))
		if err == nil {
			t.Fatal("persisted message did not surface the event-log append failure")
		}
		if rows := mustTranscriptHydrationSnapshot(t, eng).CommittedRows; len(rows) != 0 {
			t.Fatalf("uncommitted message projected rows: %+v", rows)
		}
		if len(events) != 0 {
			t.Fatalf("uncommitted message published events: %+v", events)
		}
	})

	t.Run("committed observer error", func(t *testing.T) {
		observerErr := errors.New("message observer failed")
		gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
		var events []Event
		eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		})
		gate.FailNext(observerErr)

		err := eng.steer(runtimeTestStepID("step-1"), steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("committed")}}))
		if !errors.Is(err, observerErr) {
			t.Fatalf("persisted message error = %v, want observer error", err)
		}
		if rows := mustTranscriptHydrationSnapshot(t, eng).CommittedRows; len(rows) != 1 {
			t.Fatalf("committed message projected rows: %+v", rows)
		}
		if len(events) != 1 || events[0].Kind != EventConversationUpdated {
			t.Fatalf("committed message events: %+v", events)
		}
	})
}

func TestCommittedControlFeedbackAppliesStateByCommitReceipt(t *testing.T) {
	t.Parallel()
	type controlCase struct {
		name      string
		newEngine func(*testing.T, *session.Store) *Engine
		apply     func(*Engine) (bool, session.CommitReceipt, error)
		isApplied func(*Engine) bool
	}
	cases := []controlCase{
		{
			name: "fast mode",
			newEngine: func(t *testing.T, store *session.Store) *Engine {
				return mustNewExecTestEngine(t, store, &fakeClient{caps: llm.ProviderCapabilities{
					ProviderID:           "openai",
					SupportsResponsesAPI: true,
					IsOpenAIFirstParty:   true,
				}}, Config{Model: "gpt-5.3-codex"})
			},
			apply: func(engine *Engine) (bool, session.CommitReceipt, error) {
				return engine.SetFastModeEnabledWithCommittedFeedback(context.Background(), true, func(bool) string {
					return "feedback"
				})
			},
			isApplied: func(engine *Engine) bool { return engine.FastModeEnabled() },
		},
		{
			name: "questions",
			newEngine: func(t *testing.T, store *session.Store) *Engine {
				return mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
			},
			apply: func(engine *Engine) (bool, session.CommitReceipt, error) {
				changed, _, receipt, err := engine.SetQuestionsEnabledWithCommittedFeedback(context.Background(), false, func(bool, bool) string {
					return "feedback"
				})
				return changed, receipt, err
			},
			isApplied: func(engine *Engine) bool { return !engine.QuestionsEnabled() },
		},
		{
			name: "reviewer",
			newEngine: func(t *testing.T, store *session.Store) *Engine {
				return mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{
					ID:      toolspec.ToolExecCommand,
					Handler: fakeTool{name: toolspec.ToolExecCommand},
				}), Config{
					Model: "gpt-5",
					Reviewer: ReviewerConfig{
						Frequency:     "off",
						Model:         "gpt-5",
						ThinkingLevel: "low",
						Client:        &fakeClient{},
					},
				})
			},
			apply: func(engine *Engine) (bool, session.CommitReceipt, error) {
				changed, _, receipt, err := engine.SetReviewerEnabledWithCommittedFeedback(context.Background(), true, func(bool, string, bool) string {
					return "feedback"
				})
				return changed, receipt, err
			},
			isApplied: func(engine *Engine) bool { return engine.ReviewerFrequency() == "edits" },
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			observerErr := errors.New("control feedback observer failed")
			gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
			store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
			engine := testCase.newEngine(t, store)
			gate.FailNext(observerErr)

			changed, receipt, err := testCase.apply(engine)
			if !errors.Is(err, observerErr) {
				t.Fatalf("control error = %v, want observer error", err)
			}
			if !receipt.Committed || !changed || !testCase.isApplied(engine) {
				t.Fatalf("committed control feedback did not apply state: receipt=%+v changed=%v", receipt, changed)
			}
			if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 1 {
				t.Fatalf("committed control feedback projected rows: %+v", rows)
			}
		})
	}
}

func TestFastModePersistsBeforeFeedbackAndLiveProjection(t *testing.T) {
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewExecTestEngine(t, store, &fakeClient{caps: llm.ProviderCapabilities{
		ProviderID:           "openai",
		SupportsResponsesAPI: true,
		IsOpenAIFirstParty:   true,
	}}, Config{Model: "gpt-5.3-codex"})
	entered, release := gate.BlockNext()
	defer release()

	result := make(chan struct {
		changed bool
		receipt session.CommitReceipt
		err     error
	}, 1)
	go func() {
		changed, receipt, err := engine.SetFastModeEnabledWithCommittedFeedback(t.Context(), true, func(bool) string {
			return "feedback"
		})
		result <- struct {
			changed bool
			receipt session.CommitReceipt
			err     error
		}{changed: changed, receipt: receipt, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Fast Mode metadata persistence")
	}
	requireSessionFastModeOverride(t, store, true)
	if engine.FastModeEnabled() {
		t.Fatal("Fast Mode applied to live Runtime before Session metadata persistence completed")
	}
	if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 0 {
		t.Fatalf("Fast Mode feedback committed before Session metadata persistence: %+v", rows)
	}

	release()
	select {
	case got := <-result:
		if got.err != nil || !got.changed || !got.receipt.Committed {
			t.Fatalf("SetFastModeEnabledWithCommittedFeedback = %+v", got)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Fast Mode completion")
	}
	if !engine.FastModeEnabled() {
		t.Fatal("Fast Mode was not applied after Session metadata persistence")
	}
}

func TestQuestionsPersistBeforeFeedbackAndLiveProjection(t *testing.T) {
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
	entered, release := gate.BlockNext()
	defer release()

	result := make(chan struct {
		changed bool
		enabled bool
		receipt session.CommitReceipt
		err     error
	}, 1)
	go func() {
		changed, enabled, receipt, err := engine.SetQuestionsEnabledWithCommittedFeedback(t.Context(), false, func(bool, bool) string {
			return "feedback"
		})
		result <- struct {
			changed bool
			enabled bool
			receipt session.CommitReceipt
			err     error
		}{changed: changed, enabled: enabled, receipt: receipt, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Questions metadata persistence")
	}
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.Questions == nil || *meta.ChatSettings.Questions {
		t.Fatalf("Session Questions override = %+v, want false", meta.ChatSettings)
	}
	if !engine.QuestionsEnabled() {
		t.Fatal("Questions applied to live Runtime before Session metadata persistence completed")
	}
	if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 0 {
		t.Fatalf("Questions feedback committed before Session metadata persistence: %+v", rows)
	}

	release()
	select {
	case got := <-result:
		if got.err != nil || !got.changed || got.enabled || !got.receipt.Committed {
			t.Fatalf("SetQuestionsEnabledWithCommittedFeedback = %+v", got)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Questions completion")
	}
	if engine.QuestionsEnabled() {
		t.Fatal("Questions was not applied after Session metadata persistence")
	}
}

func TestReviewerPersistsBeforeFeedbackAndLiveProjection(t *testing.T) {
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{
		ID:      toolspec.ToolExecCommand,
		Handler: fakeTool{name: toolspec.ToolExecCommand},
	}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "off",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        &fakeClient{},
		},
	})
	entered, release := gate.BlockNext()
	defer release()

	result := make(chan struct {
		changed bool
		mode    string
		receipt session.CommitReceipt
		err     error
	}, 1)
	go func() {
		changed, mode, receipt, err := engine.SetReviewerEnabledWithCommittedFeedback(t.Context(), true, func(bool, string, bool) string {
			return "feedback"
		})
		result <- struct {
			changed bool
			mode    string
			receipt session.CommitReceipt
			err     error
		}{changed: changed, mode: mode, receipt: receipt, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Reviewer metadata persistence")
	}
	meta := store.Meta()
	if meta.ChatSettings == nil || meta.ChatSettings.Supervisor == nil || *meta.ChatSettings.Supervisor != "edits" {
		t.Fatalf("Session Supervisor override = %+v, want edits", meta.ChatSettings)
	}
	if engine.ReviewerFrequency() != "off" {
		t.Fatal("Reviewer applied to live Runtime before Session metadata persistence completed")
	}
	if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 0 {
		t.Fatalf("Reviewer feedback committed before Session metadata persistence: %+v", rows)
	}

	release()
	select {
	case got := <-result:
		if got.err != nil || !got.changed || got.mode != "edits" || !got.receipt.Committed {
			t.Fatalf("SetReviewerEnabledWithCommittedFeedback = %+v", got)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Reviewer completion")
	}
	if engine.ReviewerFrequency() != "edits" {
		t.Fatal("Reviewer was not applied after Session metadata persistence")
	}
}

func TestStateOnlyChatSettingsPersistBeforeLiveProjection(t *testing.T) {
	t.Run("Thinking", func(t *testing.T) {
		gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
		engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
			Model:                   "gpt-5",
			ThinkingLevel:           "medium",
			SupportedThinkingValues: []string{"low", "medium", "high"},
		})
		entered, release := gate.BlockNext()
		defer release()

		result := make(chan error, 1)
		go func() {
			result <- engine.SetThinkingLevel(t.Context(), "high")
		}()
		select {
		case <-entered:
		case <-time.After(runtimeTestSynchronizationTimeout):
			t.Fatal("timed out waiting for Thinking metadata persistence")
		}
		meta := store.Meta()
		if meta.ChatSettings == nil || meta.ChatSettings.Thinking == nil || *meta.ChatSettings.Thinking != "high" {
			t.Fatalf("Session Thinking override = %+v, want high", meta.ChatSettings)
		}
		if got := engine.ThinkingLevel(); got != "medium" {
			t.Fatalf("live Thinking = %q before metadata persistence completed, want medium", got)
		}

		release()
		select {
		case err := <-result:
			if err != nil {
				t.Fatalf("SetThinkingLevel: %v", err)
			}
		case <-time.After(runtimeTestSynchronizationTimeout):
			t.Fatal("timed out waiting for Thinking completion")
		}
		if got := engine.ThinkingLevel(); got != "high" {
			t.Fatalf("live Thinking = %q after metadata persistence, want high", got)
		}
	})

	t.Run("Auto-compaction", func(t *testing.T) {
		gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
		store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
		engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
		entered, release := gate.BlockNext()
		defer release()

		result := make(chan struct {
			changed bool
			enabled bool
			err     error
		}, 1)
		go func() {
			changed, enabled, err := engine.SetAutoCompactionEnabled(t.Context(), false)
			result <- struct {
				changed bool
				enabled bool
				err     error
			}{changed: changed, enabled: enabled, err: err}
		}()
		select {
		case <-entered:
		case <-time.After(runtimeTestSynchronizationTimeout):
			t.Fatal("timed out waiting for Auto-compaction metadata persistence")
		}
		meta := store.Meta()
		if meta.ChatSettings == nil || meta.ChatSettings.AutoCompaction == nil || *meta.ChatSettings.AutoCompaction {
			t.Fatalf("Session Auto-compaction override = %+v, want false", meta.ChatSettings)
		}
		if !engine.AutoCompactionEnabled() {
			t.Fatal("Auto-compaction applied to live Runtime before Session metadata persistence completed")
		}

		release()
		select {
		case got := <-result:
			if got.err != nil || !got.changed || got.enabled {
				t.Fatalf("SetAutoCompactionEnabled = %+v", got)
			}
		case <-time.After(runtimeTestSynchronizationTimeout):
			t.Fatal("timed out waiting for Auto-compaction completion")
		}
		if engine.AutoCompactionEnabled() {
			t.Fatal("Auto-compaction was not applied after Session metadata persistence")
		}
	})
}

func TestDefinitelyUncommittedStateOnlyChatSettingsStopBeforeLiveProjection(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*testing.T, *Engine) error
		state func(*Engine) bool
	}{
		{
			name: "Thinking",
			apply: func(t *testing.T, engine *Engine) error {
				return engine.SetThinkingLevel(t.Context(), "low")
			},
			state: func(engine *Engine) bool { return engine.ThinkingLevel() == "high" },
		},
		{
			name: "Auto-compaction",
			apply: func(t *testing.T, engine *Engine) error {
				_, _, err := engine.SetAutoCompactionEnabled(t.Context(), false)
				return err
			},
			state: func(engine *Engine) bool { return engine.AutoCompactionEnabled() },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
				Model:                   "gpt-5",
				ThinkingLevel:           "high",
				SupportedThinkingValues: []string{"low", "high"},
			})
			blockTestSessionMetadataMutations(t, store)

			if err := test.apply(t, engine); err == nil {
				t.Fatal("definitely uncommitted setting mutation succeeded")
			}
			if !test.state(engine) {
				t.Fatal("definitely uncommitted setting changed live Runtime state")
			}
			if err := engine.SetSessionName(t.Context(), "closed"); !errors.Is(err, ErrEngineClosed) {
				t.Fatalf("mutation after uncommitted settings failure = %v, want Engine closed", err)
			}
		})
	}
}

func TestCommittedControlFeedbackCallerCancellationStopsOnlyWait(t *testing.T) {
	store := mustCreateTestSession(t)
	events := make(chan EventKind, 4)
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { events <- event.Kind },
	})
	if err := engine.pauseRuntimeOperations(t.Context()); err != nil {
		t.Fatalf("pause Runtime FIFO: %v", err)
	}

	caller, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, _, _, err := engine.SetQuestionsEnabledWithCommittedFeedback(caller, false, func(bool, bool) string {
			return "feedback"
		})
		result <- err
	}()
	waitForPendingRuntimeOperation(t, engine)

	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled control wait error = %v, want canceled", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("canceled control caller remained blocked")
	}
	if !engine.QuestionsEnabled() {
		t.Fatal("control mutation applied before the paused Runtime FIFO drained")
	}

	if err := engine.drainRuntimeOperations(t.Context()); err != nil {
		t.Fatalf("drain accepted control mutation: %v", err)
	}
	if engine.QuestionsEnabled() {
		t.Fatal("accepted control mutation did not continue after caller cancellation")
	}
	if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 1 {
		t.Fatalf("accepted control feedback projected rows: %+v", rows)
	}
	for {
		select {
		case kind := <-events:
			if kind == EventSessionStatusChanged {
				return
			}
		default:
			t.Fatal("accepted control mutation did not publish Session status after caller cancellation")
		}
	}
}

func TestRuntimeSetterCallerCancellationStopsOnlyWait(t *testing.T) {
	tests := []struct {
		name        string
		apply       func(context.Context, *Engine) error
		applied     func(*Engine) bool
		publication EventKind
	}{
		{
			name: "Session name",
			apply: func(ctx context.Context, engine *Engine) error {
				return engine.SetSessionName(ctx, "renamed")
			},
			applied:     func(engine *Engine) bool { return engine.SessionName() == "renamed" },
			publication: EventSessionIdentityChanged,
		},
		{
			name: "Thinking",
			apply: func(ctx context.Context, engine *Engine) error {
				return engine.SetThinkingLevel(ctx, "low")
			},
			applied:     func(engine *Engine) bool { return engine.ThinkingLevel() == "low" },
			publication: EventSessionStatusChanged,
		},
		{
			name: "Auto-compaction",
			apply: func(ctx context.Context, engine *Engine) error {
				_, _, err := engine.SetAutoCompactionEnabled(ctx, false)
				return err
			},
			applied:     func(engine *Engine) bool { return !engine.AutoCompactionEnabled() },
			publication: EventSessionStatusChanged,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			events := make(chan EventKind, 1)
			engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
				Model:                   "gpt-5",
				ThinkingLevel:           "high",
				SupportedThinkingValues: []string{"low", "high"},
				OnEvent:                 func(event Event) { events <- event.Kind },
			})
			if err := engine.pauseRuntimeOperations(t.Context()); err != nil {
				t.Fatalf("pause Runtime FIFO: %v", err)
			}

			caller, cancel := context.WithCancel(t.Context())
			result := make(chan error, 1)
			go func() {
				result <- test.apply(caller, engine)
			}()
			waitForPendingRuntimeOperation(t, engine)
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled setter wait error = %v, want canceled", err)
				}
			case <-time.After(runtimeTestSynchronizationTimeout):
				t.Fatal("canceled setter caller remained blocked")
			}
			if test.applied(engine) {
				t.Fatal("setter applied before the paused Runtime FIFO drained")
			}

			if err := engine.drainRuntimeOperations(t.Context()); err != nil {
				t.Fatalf("drain accepted setter: %v", err)
			}
			if !test.applied(engine) {
				t.Fatal("accepted setter did not continue after caller cancellation")
			}
			select {
			case kind := <-events:
				if kind != test.publication {
					t.Fatalf("setter publication = %q, want %q", kind, test.publication)
				}
			default:
				t.Fatal("accepted setter did not publish after caller cancellation")
			}
		})
	}
}

func waitForPendingRuntimeOperation(t *testing.T, engine *Engine) {
	t.Helper()
	deadline := time.After(runtimeTestSynchronizationTimeout)
	for !engine.HasPendingRuntimeOperations() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for accepted Runtime operation")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func mustTranscriptHydrationSnapshot(t *testing.T, engine *Engine) TranscriptHydrationSnapshot {
	t.Helper()
	var snapshot TranscriptHydrationSnapshot
	if err := engine.WithTranscriptHydrationSnapshot(func(value TranscriptHydrationSnapshot) error {
		snapshot = value
		return nil
	}); err != nil {
		t.Fatalf("read transcript hydration snapshot: %v", err)
	}
	return snapshot
}
