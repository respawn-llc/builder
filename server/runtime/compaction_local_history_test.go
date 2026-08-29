package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/runtimeids"
	"core/shared/runtimeinput"
	"core/shared/serverapi"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func completeManualEligibilityAgentStep(t *testing.T, engine *Engine) {
	t.Helper()
	engine.compactionRuntimeState().SetManualCompactionEligible(true)
}

func TestManualCompactionRevalidatesAtBoundary(t *testing.T) {
	tests := []struct {
		name       string
		activeKind ActiveKind
		mutate     func(*Engine)
		wantEvent  EventKind
	}{
		{
			name:       "accepted during Agent Step",
			activeKind: ActiveKindUserTurn,
			wantEvent:  EventCompactionCompleted,
		},
		{
			name:       "disabled policy",
			activeKind: ActiveKindRuntimeMaintenance,
			mutate: func(engine *Engine) {
				engine.contextPolicy.CompactionMode = serverapi.ChatContextCompactionModeDisabled
			},
			wantEvent: EventCompactionFailed,
		},
		{
			name:       "active compaction",
			activeKind: ActiveKindRuntimeMaintenance,
			mutate: func(engine *Engine) {
				engine.compactionRuntimeState().SetActive(
					runtimeTestStepID("other-compaction"),
					nil,
					string(compactionModeManual),
					1,
				)
			},
			wantEvent: EventCompactionFailed,
		},
		{
			name:       "too soon",
			activeKind: ActiveKindRuntimeMaintenance,
			mutate: func(engine *Engine) {
				engine.compactionRuntimeState().SetManualCompactionEligible(false)
			},
			wantEvent: EventCompactionFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestID := runtimeids.NewCompactionRequestID()
			var terminal *Event
			client := &fakeCompactionClient{responses: []llm.Response{{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
			}}}
			engine := mustNewTestEngine(
				t,
				mustCreateTestSession(t),
				client,
				newTestToolRegistry(t),
				Config{
					Model:          "gpt-5",
					CompactionMode: "local",
					OnEvent: func(event Event) {
						if event.Compaction != nil &&
							event.Compaction.RequestID != nil &&
							*event.Compaction.RequestID == requestID {
							copyEvent := event
							terminal = &copyEvent
						}
					},
				},
			)
			engine.compactionRuntimeState().SetManualCompactionEligible(true)

			started, release, stepDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				stepDone <- engine.stepLifecycle.Run(
					t.Context(),
					exclusiveStepOptions{ActiveKind: test.activeKind},
					func(context.Context, string) error {
						close(started)
						<-release
						return nil
					},
				)
			}()
			pendingWorkTestWait(t, started, "active Step")

			admitted := make(chan error, 1)
			go func() {
				_, err := engine.CompactContextAdmissionForRequestWithAcceptance(
					t.Context(),
					requestID,
					runtimeinput.ManualCompactionAdmission{},
					nil,
				)
				admitted <- err
			}()
			pendingWorkTestNoError(t, pendingWorkTestWaitValue(t, admitted, "manual compaction admission"))

			pending := pendingWorkTestSnapshot(t, engine)
			if len(pending.Items) != 1 || pending.Items[0].Kind != runtimeinput.PendingWorkItemKindManualCompaction {
				t.Fatalf("accepted manual compaction Pending Work = %+v", pending.Items)
			}
			if test.mutate != nil {
				test.mutate(engine)
			}

			close(release)
			pendingWorkTestNoError(t, <-stepDone)
			waitEngineLifecycleTasks(t, engine)

			if terminal == nil || terminal.Kind != test.wantEvent {
				t.Fatalf("terminal compaction event = %+v, want %s for request %s", terminal, test.wantEvent, requestID)
			}
		})
	}
}

func TestManualCompactionLocalUsesHistorySinceLastCompactionCheckpoint(t *testing.T) {
	t.Parallel()
	const (
		preBoundaryID  = "reasoning-before-boundary"
		checkpointID   = "reasoning-at-boundary"
		postBoundaryID = "reasoning-after-boundary"
	)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("summary")},
	}}}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:          "gpt-5",
		CompactionMode: "local",
	})
	if err := steerTestActiveStep(engine, "before", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{{
		ID:               preBoundaryID,
		EncryptedContent: "before",
	}}}})); err != nil {
		t.Fatalf("persist pre-boundary reasoning: %v", err)
	}
	checkpointStepID := runtimeTestStepID("checkpoint")
	restoreStep := setTestActiveStep(engine, checkpointStepID)
	receipt, err := newCompactionPersistence(engine).replaceHistory(
		checkpointStepID,
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			SourcePath:  textutil.Value(checkpointID),
			Content:     textutil.Value("checkpoint"),
		}}),
	)
	restoreStep()
	if err != nil || !receipt.Committed {
		t.Fatalf("persist compaction checkpoint: receipt=%+v error=%v", receipt, err)
	}
	if err := steerTestActiveStep(engine, "after", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{{
		ID:               postBoundaryID,
		EncryptedContent: "after",
	}}}})); err != nil {
		t.Fatalf("persist post-boundary reasoning: %v", err)
	}

	completeManualEligibilityAgentStep(t, engine)
	scheduleManualCompactionAndWait(t, engine)
	if len(client.calls) != 1 {
		t.Fatalf("local compaction calls = %d, want one", len(client.calls))
	}
	seen := make(map[string]bool)
	checkpointSeen := false
	for _, item := range client.calls[0].Items {
		if item.Type == llm.ResponseItemTypeMessage &&
			item.MessageType != nil &&
			*item.MessageType == llm.MessageTypeCompactionSummary &&
			item.SourcePath != nil &&
			*item.SourcePath == checkpointID {
			checkpointSeen = true
		}
		if item.ID != nil {
			seen[*item.ID] = true
		}
	}
	if !checkpointSeen || !seen[postBoundaryID] || seen[preBoundaryID] {
		t.Fatalf(
			"local compaction request checkpoint=%t IDs=%+v, want checkpoint/post present and pre-boundary absent",
			checkpointSeen,
			seen,
		)
	}
}

func TestPreSubmitCompactionLocalCarriesPreservedUserMessageInOrder(t *testing.T) {
	t.Parallel()
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("pre-submit summary")},
	}}}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, newTestToolRegistry(t), Config{
		Model:          "gpt-5",
		CompactionMode: "local",
	})
	if err := steerTestActiveStep(engine, "user", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("pre-submit carryover")}},
	)); err != nil {
		t.Fatalf("persist pre-submit carryover prompt: %v", err)
	}

	if err := engine.CompactContextForPreSubmit(context.Background()); err != nil {
		t.Fatalf("pre-submit compaction: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("local Generate calls = %d, want one", len(client.calls))
	}
	assertCompactionReplacementOrder(t, engine.transcriptRuntimeState().SnapshotItems(), false)
}

func TestManualCompactionLocalFailsWhenModelAttemptsToolCalls(t *testing.T) {
	t.Parallel()
	probe := &toolExecutionProbe{}
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant},
		ToolCalls: []llm.ToolCall{{
			ID:    "compaction-tool-call",
			Name:  string(toolspec.ToolExecCommand),
			Input: json.RawMessage(`{"cmd":"pwd"}`),
		}},
	}}}
	engine := mustNewTestEngine(
		t,
		store,
		client,
		newTestToolRegistry(t, tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: probe,
		}),
		Config{Model: "gpt-5", CompactionMode: "local"},
	)
	if err := steerTestActiveStep(engine, "input", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}

	completeManualEligibilityAgentStep(t, engine)
	var events []Event
	engine.cfg.OnEvent = func(event Event) {
		events = append(events, event)
	}
	scheduleManualCompactionAndWait(t, engine)
	if !hasEventKind(events, EventCompactionFailed) {
		t.Fatalf("manual local compaction events = %+v, want failed event", events)
	}
	if probe.called || len(client.calls) != 1 {
		t.Fatalf(
			"manual local compaction tool-execution/model-calls = %t/%d, want false/one",
			probe.called,
			len(client.calls),
		)
	}

	recent, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(4)
	if readErr != nil {
		t.Fatalf("read bounded compaction records: %v", readErr)
	}
	for _, record := range recent.Records {
		if _, ok := mustSessionEventPayload(record).(session.HistoryReplacementRecord); ok {
			t.Fatalf("tool-call failure committed history replacement: %+v", record)
		}
	}
}

func TestManualCompactionDisabledWhenModeNone(t *testing.T) {
	t.Parallel()
	client := &fakeCompactionClient{}
	engine := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		Model:          "gpt-5",
		CompactionMode: "none",
	})
	if err := steerTestActiveStep(engine, "input", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}

	requestID := runtimeids.NewCompactionRequestID()
	var failed *CompactionStatus
	engine.cfg.OnEvent = func(event Event) {
		if event.Kind == EventCompactionFailed && event.Compaction != nil {
			copyStatus := *event.Compaction
			failed = &copyStatus
		}
	}
	if _, err := engine.CompactContextAdmissionForRequestWithAcceptance(
		context.Background(),
		requestID,
		runtimeinput.ManualCompactionAdmission{},
		nil,
	); err != nil {
		t.Fatalf("manual compaction admission: %v", err)
	}
	waitEngineLifecycleTasks(t, engine)
	if failed == nil || failed.RequestID == nil || *failed.RequestID != requestID {
		t.Fatalf("disabled compaction status = %+v, want request %s", failed, requestID)
	}
	if len(client.calls) != 0 || len(client.compactionCalls) != 0 {
		t.Fatalf(
			"disabled compaction local/remote calls = %d/%d, want zero/zero",
			len(client.calls),
			len(client.compactionCalls),
		)
	}
}
