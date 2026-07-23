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
	if err := engine.steer("before", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{{
			ID:               preBoundaryID,
			EncryptedContent: "before",
		}}}},
	)); err != nil {
		t.Fatalf("persist pre-boundary reasoning: %v", err)
	}
	receipt, err := newCompactionPersistence(engine).replaceHistory(
		"checkpoint",
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			SourcePath:  textutil.Value(checkpointID),
			Content:     textutil.Value("checkpoint"),
		}}),
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("persist compaction checkpoint: receipt=%+v error=%v", receipt, err)
	}
	if err := engine.steer("after", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{{
			ID:               postBoundaryID,
			EncryptedContent: "after",
		}}}},
	)); err != nil {
		t.Fatalf("persist post-boundary reasoning: %v", err)
	}

	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact active segment: %v", err)
	}
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

func TestManualCompactionLocalFailsWhenModelAttemptsToolCalls(t *testing.T) {
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
		tools.NewRegistry(tools.HandlerRegistration{
			ID:      toolspec.ToolExecCommand,
			Handler: probe,
		}),
		Config{Model: "gpt-5", CompactionMode: "local"},
	)
	if err := engine.steer("input", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}

	err := engine.CompactContext(context.Background(), "")
	if !errors.Is(err, errLocalCompactionAttemptedToolCalls) {
		t.Fatalf("manual local compaction error = %v, want tool-call rejection", err)
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
