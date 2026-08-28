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
	"core/shared/clientui"
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

func TestImmediateSettingsApplyCommittedStateOnPersistenceObserverFailure(t *testing.T) {
	t.Parallel()
	type controlCase struct {
		name      string
		newEngine func(*testing.T, *session.Store) *Engine
		apply     func(*Engine) (bool, error)
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
			apply: func(engine *Engine) (bool, error) {
				return engine.SetFastModeEnabledWithPublication(context.Background(), true, nil)
			},
			isApplied: func(engine *Engine) bool { return engine.FastModeEnabled() },
		},
		{
			name: "questions",
			newEngine: func(t *testing.T, store *session.Store) *Engine {
				return mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
			},
			apply: func(engine *Engine) (bool, error) {
				changed, _, err := engine.SetQuestionsEnabledWithPublication(context.Background(), false, nil)
				return changed, err
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
			apply: func(engine *Engine) (bool, error) {
				changed, _, err := engine.SetReviewerEnabledWithPublication(context.Background(), true, nil)
				return changed, err
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

			changed, err := testCase.apply(engine)
			if !errors.Is(err, observerErr) {
				t.Fatalf("control error = %v, want observer error", err)
			}
			if !changed || !testCase.isApplied(engine) {
				t.Fatalf("committed setting did not apply: changed=%v", changed)
			}
			if rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows; len(rows) != 0 {
				t.Fatalf("immediate setting projected committed rows: %+v", rows)
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
		err     error
	}, 1)
	publishing := make(chan struct{})
	releasePublication := make(chan struct{})
	go func() {
		changed, err := engine.SetFastModeEnabledWithPublication(t.Context(), true, func(clientui.TranscriptSessionSettingFeedback) error {
			close(publishing)
			<-releasePublication
			return nil
		})
		result <- struct {
			changed bool
			err     error
		}{changed: changed, err: err}
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
	case <-publishing:
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for Fast Mode publication")
	}
	if !engine.FastModeEnabled() {
		t.Fatal("Fast Mode was not applied after Session metadata persistence")
	}

	secondDone := make(chan error, 1)
	go func() {
		_, err := engine.SetFastModeEnabledWithPublication(t.Context(), false, nil)
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		t.Fatalf("second setting completed before first publication: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	requireSessionFastModeOverride(t, store, true)
	if !engine.FastModeEnabled() {
		t.Fatal("second setting applied live before first publication completed")
	}

	close(releasePublication)
	select {
	case got := <-result:
		if got.err != nil || !got.changed {
			t.Fatalf("first Fast Mode setting = %+v", got)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for first setting")
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second setting: %v", err)
		}
	case <-time.After(runtimeTestSynchronizationTimeout):
		t.Fatal("timed out waiting for second setting")
	}
	requireSessionFastModeOverride(t, store, false)
	if engine.FastModeEnabled() {
		t.Fatal("second setting was not final live value")
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
		err     error
	}, 1)
	go func() {
		changed, enabled, err := engine.SetQuestionsEnabledWithPublication(t.Context(), false, nil)
		result <- struct {
			changed bool
			enabled bool
			err     error
		}{changed: changed, enabled: enabled, err: err}
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
		if got.err != nil || !got.changed || got.enabled {
			t.Fatalf("SetQuestionsEnabledWithPublication = %+v", got)
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
		err     error
	}, 1)
	go func() {
		changed, mode, err := engine.SetReviewerEnabledWithPublication(t.Context(), true, nil)
		result <- struct {
			changed bool
			mode    string
			err     error
		}{changed: changed, mode: mode, err: err}
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
		if got.err != nil || !got.changed || got.mode != "edits" {
			t.Fatalf("SetReviewerEnabledWithPublication = %+v", got)
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
