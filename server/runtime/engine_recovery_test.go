package runtime

import (
	"context"
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
)

func TestSubmitUserMessageSurfacesInFlightClearFailure(t *testing.T) {
	clearErr := errors.New("pending model recovery observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	var events []Event
	failureArmed := false
	engine := mustNewTestEngine(t, store, &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("completed"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
	}}}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			events = append(events, event)
			if event.Kind == EventAssistantMessage && !failureArmed {
				failureArmed = true
				gate.FailNext(clearErr)
			}
		},
	})

	if _, err := engine.SubmitUserMessage(context.Background(), "input"); !errors.Is(err, errPendingModelRecoveryClear) {
		t.Fatalf("submit error = %v, want typed pending-recovery clear failure", err)
	}
	if !failureArmed {
		t.Fatal("assistant commit did not arm pending-recovery clear failure")
	}

	clearFailurePublished := false
	for _, event := range events {
		if event.Kind == EventInFlightClearFailed &&
			event.StepID != "" &&
			event.Error != "" {
			clearFailurePublished = true
			break
		}
	}
	if !clearFailurePublished {
		t.Fatalf("typed pending-recovery clear failure was not published: %+v", events)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	if reopened.Meta().PendingModelRecovery != nil {
		t.Fatalf("committed clear retained pending recovery: %+v", reopened.Meta().PendingModelRecovery)
	}
	window, err := mustMaterializeTestEventLog(t, reopened).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded post-clear records: %v", err)
	}
	for _, record := range window.Records {
		message, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if ok && message.Role == session.MessageRoleAssistant {
			return
		}
	}
	t.Fatalf("bounded post-clear records contain no committed assistant message: %+v", window.Records)
}

func TestNewConsumesPendingModelRecoveryOnReopen(t *testing.T) {
	const stepID = "interrupted-step"

	store := mustCreateTestSession(t)
	mustAppendTestEvent(t, store, stepID, llm.Message{
		Role:    llm.RoleUser,
		Content: textutil.Value("interrupted input"),
	})
	if err := store.SetPendingModelRecovery(session.PendingModelRecovery{
		RecoveryID: "recovery-1",
		StepID:     stepID,
		Reason:     "provider_visible_output_persisted",
		CreatedAt:  time.Unix(0, 0).UTC(),
	}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	_ = mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	if reopened.Meta().PendingModelRecovery != nil {
		t.Fatal("reopened runtime retained pending model recovery")
	}

	window, err := mustMaterializeTestEventLog(t, reopened).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded recovery records: %v", err)
	}
	for _, record := range window.Records {
		message, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if ok &&
			message.Role == session.MessageRoleDeveloper &&
			message.MessageType != nil &&
			*message.MessageType == session.MessageTypeInterruption {
			return
		}
	}
	t.Fatalf("bounded recovery records contain no durable interruption marker: %+v", window.Records)
}

func TestNewPublishesRecoveredDanglingToolStartOnReopen(t *testing.T) {
	const (
		stepID = "interrupted-tool-step"
		callID = "interrupted-tool-call"
	)

	store := mustCreateTestSession(t)
	mustAppendTestEvent(t, store, stepID, llm.Message{
		Role: llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{{
			ID:    callID,
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{}`),
		}},
	})
	if err := store.SetPendingModelRecovery(session.PendingModelRecovery{
		RecoveryID:             "recovery-2",
		StepID:                 stepID,
		Reason:                 "provider_visible_output_persisted",
		CreatedAt:              time.Unix(0, 0).UTC(),
		OutstandingToolCallIDs: []string{callID},
	}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	var events []Event
	_ = mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})

	for _, event := range events {
		if event.Kind == EventToolCallStarted &&
			event.StepID == stepID &&
			event.ToolCall != nil &&
			event.ToolCall.ID == callID &&
			event.ToolCall.Name == string(toolspec.ToolExecCommand) {
			return
		}
	}
	t.Fatalf("reopen events contain no recovered tool start: %+v", events)
}

func TestExclusiveStepLifecycleClearsPendingRecoveryBeforeSchedulingBackground(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	scheduled := false
	var recoveryAtSchedule *session.PendingModelRecovery
	lifecycle := &defaultExclusiveStepLifecycle{
		engine: engine,
		background: &recoverySchedulingObserver{onSchedule: func() {
			scheduled = true
			if recovery := store.Meta().PendingModelRecovery; recovery != nil {
				captured := cloneSessionPendingModelRecovery(recovery)
				recoveryAtSchedule = &captured
			}
		}},
	}

	if err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{ActiveKind: ActiveKindUserTurn},
		func(_ context.Context, stepID string) error {
			if err := engine.markProviderVisibleModelRecovery(stepID); err != nil {
				return err
			}
			recovery := store.Meta().PendingModelRecovery
			if recovery == nil || recovery.StepID != stepID {
				t.Fatalf("pending recovery during exclusive step = %+v", recovery)
			}
			return nil
		},
	); err != nil {
		t.Fatalf("run exclusive step: %v", err)
	}
	if !scheduled {
		t.Fatal("background work was not scheduled after the exclusive step")
	}
	if recoveryAtSchedule != nil {
		t.Fatalf("background work observed pending recovery = %+v", recoveryAtSchedule)
	}
	if recovery := store.Meta().PendingModelRecovery; recovery != nil {
		t.Fatalf("exclusive step retained pending recovery = %+v", recovery)
	}
}

func TestExclusiveStepLifecycleDoesNotClearSuccessorPendingRecovery(t *testing.T) {
	const (
		successorRecoveryID = "00000000-0000-4000-8000-000000000051"
		successorStepID     = "00000000-0000-4000-8000-000000000052"
	)
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}

	if err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{ActiveKind: ActiveKindUserTurn},
		func(_ context.Context, stepID string) error {
			if err := engine.markProviderVisibleModelRecovery(stepID); err != nil {
				return err
			}
			return store.SetPendingModelRecovery(session.PendingModelRecovery{
				RecoveryID: successorRecoveryID,
				StepID:     successorStepID,
				Reason:     "provider_visible_output_persisted",
				CreatedAt:  time.Unix(0, 0).UTC(),
			})
		},
	); err != nil {
		t.Fatalf("run exclusive step: %v", err)
	}
	recovery := store.Meta().PendingModelRecovery
	if recovery == nil {
		t.Fatal("exclusive step cleared successor pending recovery")
	}
	if recovery.StepID != successorStepID {
		t.Fatalf("pending recovery step = %q, want successor step", recovery.StepID)
	}
}

func TestExclusiveStepLifecyclePublishesTerminalActivityBeforeFinishPersistenceFailures(t *testing.T) {
	finishErr := errors.New("finish persistence failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(
		t,
		t.TempDir(),
		session.WithPersistenceObserver(gate),
	)
	sink := &finishFailureLifecycleSink{gate: gate, failure: finishErr}
	var events []Event
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:         "gpt-5",
		StepLifecycle: sink,
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})
	lifecycle := &defaultExclusiveStepLifecycle{engine: engine}

	err := lifecycle.Run(
		context.Background(),
		exclusiveStepOptions{ActiveKind: ActiveKindUserTurn, EmitRunState: true},
		func(_ context.Context, stepID string) error {
			return engine.markProviderVisibleModelRecovery(stepID)
		},
	)
	if !errors.Is(err, errPendingModelRecoveryClear) || !errors.Is(err, finishErr) {
		t.Fatalf("exclusive step finish error = %v", err)
	}
	if sink.ended == nil ||
		sink.ended.Transition != StepLifecycleTransitionEnded ||
		sink.ended.Status != RunStatusCompleted ||
		sink.ended.ActiveKind != ActiveKindUserTurn {
		t.Fatalf("terminal lifecycle publication = %+v", sink.ended)
	}

	var running, finished *RunState
	clearFailurePublished := false
	for index := range events {
		event := &events[index]
		if event.Kind == EventInFlightClearFailed && event.StepID == sink.ended.StepID {
			clearFailurePublished = true
		}
		if event.Kind != EventRunStateChanged || event.RunState == nil {
			continue
		}
		switch event.RunState.Lifecycle.Phase {
		case RunLifecycleRunning:
			running = event.RunState
		case RunLifecycleFinished:
			finished = event.RunState
		}
	}
	if !clearFailurePublished {
		t.Fatalf("events omitted typed pending-recovery clear failure: %+v", events)
	}
	if running == nil ||
		running.Status != RunStatusRunning ||
		running.RunID != sink.ended.RunID ||
		finished == nil ||
		finished.Status != RunStatusCompleted ||
		finished.RunID != sink.ended.RunID ||
		finished.FinishedAt.IsZero() {
		t.Fatalf("terminal run activity = running:%+v finished:%+v", running, finished)
	}
	if lifecycle.IsBusy() || lifecycle.Snapshot() != nil || engine.HasActiveLiveRunGroup() {
		t.Fatalf(
			"finish persistence failure retained live activity: lifecycle_busy=%t snapshot=%+v live_run=%t",
			lifecycle.IsBusy(),
			lifecycle.Snapshot(),
			engine.HasActiveLiveRunGroup(),
		)
	}
}

func TestReopenCarriesInterruptedAskQuestionToolAttemptIntoNextModelRequest(t *testing.T) {
	testReopenCarriesInterruptedToolAttemptIntoNextModelRequest(t, llm.ToolCall{
		ID:    "interrupted-question-call",
		Name:  string(toolspec.ToolAskQuestion),
		Input: json.RawMessage(`{"question":"continue?"}`),
	})
}

type recoverySchedulingObserver struct {
	onSchedule func()
}

func (s *recoverySchedulingObserver) HandleBackgroundShellUpdate(BackgroundShellEvent, bool) {}
func (s *recoverySchedulingObserver) QueueDeveloperNotice(llm.Message)                       {}
func (s *recoverySchedulingObserver) DrainPendingNotices() []steeringIntent                  { return nil }
func (s *recoverySchedulingObserver) HasPendingNotices() bool                                { return false }
func (s *recoverySchedulingObserver) ConsumePendingBackgroundNotice(string) bool             { return false }

func (s *recoverySchedulingObserver) ScheduleIfIdle() {
	if s != nil && s.onSchedule != nil {
		s.onSchedule()
	}
}

type finishFailureLifecycleSink struct {
	gate    *sessiontest.PersistenceGate
	failure error
	ended   *StepLifecycleSnapshot
}

func (s *finishFailureLifecycleSink) StepBegan(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (s *finishFailureLifecycleSink) StepEnded(
	_ context.Context,
	snapshot StepLifecycleSnapshot,
) error {
	s.ended = &snapshot
	s.gate.FailNext(s.failure)
	return nil
}

func TestReopenCarriesInterruptedShellToolAttemptIntoNextModelRequest(t *testing.T) {
	testReopenCarriesInterruptedToolAttemptIntoNextModelRequest(t, llm.ToolCall{
		ID:    "interrupted-shell-call",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd"}`),
	})
}

func TestReopenCarriesInterruptedApprovalBackedPatchToolAttemptIntoNextModelRequest(t *testing.T) {
	testReopenCarriesInterruptedToolAttemptIntoNextModelRequest(t, llm.ToolCall{
		ID:          "interrupted-patch-call",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: textutil.Value("*** Begin Patch\n*** Add File: ../outside.txt\n+content\n*** End Patch\n"),
	})
}

func testReopenCarriesInterruptedToolAttemptIntoNextModelRequest(
	t *testing.T,
	call llm.ToolCall,
) {
	t.Helper()
	const stepID = "interrupted-tool-attempt-step"

	store := mustCreateTestSession(t)
	mustAppendTestEvent(t, store, stepID, llm.Message{
		Role:    llm.RoleUser,
		Content: textutil.Value("input"),
	})
	mustAppendTestEvent(t, store, stepID, llm.Message{
		Role:      llm.RoleAssistant,
		ToolCalls: []llm.ToolCall{call},
	})
	if err := store.SetPendingModelRecovery(session.PendingModelRecovery{
		RecoveryID: "recovery-3",
		StepID:     stepID,
		Reason:     "provider_visible_output_persisted",
		CreatedAt:  time.Unix(0, 0).UTC(),
	}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("resumed"),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
		},
	}}}
	engine := mustNewTestEngine(t, reopened, client, tools.NewRegistry(), Config{Model: "gpt-5"})
	if _, err := engine.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit after reopen: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("resumed model requests = %d, want one", len(client.calls))
	}

	callItemType := llm.ResponseItemTypeFunctionCall
	if call.Custom {
		callItemType = llm.ResponseItemTypeCustomToolCall
	}
	foundCall := false
	foundOutput := false
	for _, item := range client.calls[0].Items {
		if item.CallID == nil || *item.CallID != call.ID {
			continue
		}
		if item.Type == callItemType &&
			item.Name != nil &&
			*item.Name == call.Name {
			foundCall = true
		}
		if item.Type == llm.ToolOutputItemType(call.Custom) {
			foundOutput = true
		}
	}
	if !foundCall || foundOutput {
		t.Fatalf("resumed tool attempt preservation = call:%t output:%t", foundCall, foundOutput)
	}
}
