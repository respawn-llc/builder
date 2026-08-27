package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

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
	if err := engine.steer("user", steerMessagesWithPersistenceIntent(
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

	err := engine.CompactContext(context.Background(), "")
	if !errors.Is(err, errCompactionDisabledModeNone) {
		t.Fatalf("manual compaction error = %v, want disabled-mode rejection", err)
	}
	if len(client.calls) != 0 || len(client.compactionCalls) != 0 {
		t.Fatalf(
			"disabled compaction local/remote calls = %d/%d, want zero/zero",
			len(client.calls),
			len(client.compactionCalls),
		)
	}
}
