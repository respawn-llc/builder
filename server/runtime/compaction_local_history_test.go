package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
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
