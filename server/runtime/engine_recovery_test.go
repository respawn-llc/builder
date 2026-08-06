package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	t.Parallel()
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

	clearFailureEvents := 0
	for _, event := range events {
		if event.Kind == EventInFlightClearFailed {
			clearFailureEvents++
		}
	}
	if clearFailureEvents != 1 {
		t.Fatalf("typed pending-recovery clear failures = %d, want one", clearFailureEvents)
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
	t.Parallel()
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

func TestNewTerminalRecoveredStepDoesNotPublishInterruption(t *testing.T) {
	t.Parallel()
	const stepID = "terminal-step"
	store := mustCreateTestSession(t)
	mustAppendTestEvent(t, store, stepID, llm.Message{Role: llm.RoleUser, Content: textutil.Value("input")})
	mustAppendTestEvent(t, store, stepID, llm.Message{
		Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("completed"),
	})
	if err := store.SetPendingModelRecovery(session.PendingModelRecovery{
		RecoveryID: "terminal-recovery", StepID: stepID, Reason: "provider_visible_output_persisted", CreatedAt: time.Unix(1, 0).UTC(),
	}); err != nil {
		t.Fatalf("set terminal recovery: %v", err)
	}
	reopened := mustOpenTestSession(t, store.Dir())
	_ = mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if reopened.Meta().PendingModelRecovery != nil {
		t.Fatalf("terminal recovery retained pending metadata: %+v", reopened.Meta().PendingModelRecovery)
	}
	assertNoBoundedInterruptionRecord(t, reopened)
}

func TestNewRecoveryWithoutStepIDDiscardsCandidateWithoutInterruption(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	if err := store.SetPendingModelRecovery(session.PendingModelRecovery{
		RecoveryID: "missing-step-recovery", Reason: "provider_visible_output_persisted", CreatedAt: time.Unix(2, 0).UTC(),
	}); err != nil {
		t.Fatalf("set missing-step recovery: %v", err)
	}
	reopened := mustOpenTestSession(t, store.Dir())
	_ = mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if reopened.Meta().PendingModelRecovery != nil {
		t.Fatalf("missing-step recovery retained pending metadata: %+v", reopened.Meta().PendingModelRecovery)
	}
	assertNoBoundedInterruptionRecord(t, reopened)
}

func assertNoBoundedInterruptionRecord(t *testing.T, store *session.Store) {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded recovery records: %v", err)
	}
	for _, record := range window.Records {
		message, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if ok && message.MessageType != nil && *message.MessageType == session.MessageTypeInterruption {
			t.Fatalf("unexpected interruption record: %+v", message)
		}
	}
}

func TestNewRepairsRecoveredDanglingToolBeforeReturningReadyEngine(t *testing.T) {
	t.Parallel()
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
	engine := mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})

	if !repairRequestHasToolOutput(engine.transcriptRuntimeState().SnapshotItems(), callID) {
		t.Fatal("fresh runtime returned before appending a neutral output for the dangling tool call")
	}
	if live := engine.transcriptRuntimeState().LiveToolSnapshot(); len(live) != 0 {
		t.Fatalf("fresh runtime restored stale live tool starts: %+v", live)
	}
	for _, event := range events {
		if event.Kind == EventToolCallStarted && event.ToolCall != nil && event.ToolCall.ID == callID {
			t.Fatalf("fresh runtime published a stale recovered tool start: %+v", event)
		}
	}
	if reopened.Meta().PendingModelRecovery != nil {
		t.Fatalf("fresh runtime retained pending recovery after neutral repair: %+v", reopened.Meta().PendingModelRecovery)
	}
}

func TestReopenedSessionRestoresUsageCheckpointDeltaAccounting(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:               "gpt-5",
		ContextWindowTokens: 2_000,
	})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value(strings.Repeat("seed ", 64))}},
	)); err != nil {
		t.Fatalf("persist checkpoint input: %v", err)
	}
	receipt, err := engine.recordLastUsage(llm.Usage{
		InputTokens:       900,
		OutputTokens:      120,
		WindowTokens:      2_000,
		CachedInputTokens: textutil.Value(45),
	})
	if err != nil || !receipt.Committed {
		t.Fatalf("persist usage checkpoint: receipt=%+v error=%v", receipt, err)
	}
	if err := engine.steer("delta", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value(strings.Repeat("delta ", 32))}},
	)); err != nil {
		t.Fatalf("persist post-checkpoint input: %v", err)
	}

	want := engine.ContextUsage()
	if want.UsedTokens <= 900 ||
		want.WindowTokens != 2_000 ||
		!want.HasCacheHitPercentage ||
		want.CacheHitPercent != 5 {
		t.Fatalf("live checkpoint usage = %+v", want)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	restored := mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{
		Model:               "gpt-5",
		ContextWindowTokens: 2_000,
	})
	if got := restored.ContextUsage(); got != want {
		t.Fatalf("reopened checkpoint usage = %+v, want %+v", got, want)
	}
}

func TestReopenedSessionRestoresLastAssistantFinalAnswerAcrossCompaction(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if err := engine.steer("initial", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{
			{Role: llm.RoleUser, Content: textutil.Value("input")},
			{
				Role:    llm.RoleAssistant,
				Phase:   textutil.Value(llm.MessagePhaseFinal),
				Content: textutil.Value("final"),
			},
		},
	)); err != nil {
		t.Fatalf("persist typed final answer: %v", err)
	}
	if engine.LastCommittedAssistantFinalAnswer() == "" {
		t.Fatal("typed final answer did not establish the live final-answer fact")
	}

	receipt, err := newCompactionPersistence(engine).replaceHistory(
		"compaction",
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			Content:     textutil.Value("summary"),
		}}),
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("persist compaction replacement: receipt=%+v error=%v", receipt, err)
	}

	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(4)
	if err != nil {
		t.Fatalf("read bounded compaction records: %v", err)
	}
	replacements := 0
	carriedFinalAnswer := false
	for _, record := range window.Records {
		replacement, ok := mustSessionEventPayload(record).(session.HistoryReplacementRecord)
		if !ok {
			continue
		}
		replacements++
		carriedFinalAnswer = replacement.LastCommittedAssistantFinalAnswer != nil &&
			*replacement.LastCommittedAssistantFinalAnswer != ""
	}
	if replacements != 1 || !carriedFinalAnswer {
		t.Fatalf(
			"typed compaction replacements=%d carried-final-answer=%t, want one and true",
			replacements,
			carriedFinalAnswer,
		)
	}
	if engine.LastCommittedAssistantFinalAnswer() == "" {
		t.Fatal("committed compaction cleared the live final-answer fact")
	}

	reopened := mustOpenTestSession(t, store.Dir())
	restored := mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if restored.LastCommittedAssistantFinalAnswer() == "" {
		t.Fatal("reopened compaction lost the carried final-answer fact")
	}
}

func TestExclusiveStepLifecycleClearsPendingRecoveryBeforeSchedulingBackground(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestReopenRepairsAskQuestionToolAttemptBeforeNextModelRequest(t *testing.T) {
	t.Parallel()
	testReopenRepairsToolAttemptBeforeNextModelRequest(t, llm.ToolCall{
		ID:    "interrupted-question-call",
		Name:  string(toolspec.ToolAskQuestion),
		Input: json.RawMessage(`{"question":"continue?"}`),
	})
}

type recoverySchedulingObserver struct {
	onSchedule func()
}

func (s *recoverySchedulingObserver) HandleBackgroundShellUpdate(BackgroundShellEvent, bool) {}
func (s *recoverySchedulingObserver) RecordBackgroundShellUpdate(BackgroundShellEvent) error {
	return nil
}
func (s *recoverySchedulingObserver) QueueBackgroundShellContinuation(BackgroundShellEvent) {}
func (s *recoverySchedulingObserver) RunBackgroundShellContinuation(context.Context, BackgroundShellEvent) error {
	return nil
}
func (s *recoverySchedulingObserver) QueueDeveloperNotice(llm.Message)           {}
func (s *recoverySchedulingObserver) flushPendingNotices(string) (int, error)    { return 0, nil }
func (s *recoverySchedulingObserver) HasPendingNotices() bool                    { return false }
func (s *recoverySchedulingObserver) ConsumePendingBackgroundNotice(string) bool { return false }

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

func TestReopenRepairsShellToolAttemptBeforeNextModelRequest(t *testing.T) {
	t.Parallel()
	testReopenRepairsToolAttemptBeforeNextModelRequest(t, llm.ToolCall{
		ID:    "interrupted-shell-call",
		Name:  string(toolspec.ToolExecCommand),
		Input: json.RawMessage(`{"command":"pwd"}`),
	})
}

func TestReopenRepairsApprovalBackedPatchToolAttemptBeforeNextModelRequest(t *testing.T) {
	t.Parallel()
	testReopenRepairsToolAttemptBeforeNextModelRequest(t, llm.ToolCall{
		ID:          "interrupted-patch-call",
		Name:        string(toolspec.ToolPatch),
		Custom:      true,
		CustomInput: textutil.Value("*** Begin Patch\n*** Add File: ../outside.txt\n+content\n*** End Patch\n"),
	})
}

func testReopenRepairsToolAttemptBeforeNextModelRequest(
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
	var output json.RawMessage
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
			output = append(json.RawMessage(nil), item.Output...)
		}
	}
	if !foundCall || !foundOutput {
		t.Fatalf("resumed neutral repair = call:%t output:%t", foundCall, foundOutput)
	}
	if !bytes.Equal(output, missingToolOutputUnavailableOutput) {
		t.Fatalf("resumed repair output = %s, want neutral fresh-resource disposition", output)
	}
}
