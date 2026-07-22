package runtime

import (
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
)

func TestReplaceHistoryDoesNotMutateRuntimeStateWhenEventAppendFails(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	usage := &session.UsageState{InputTokens: 42, WindowTokens: 100}
	if _, err := store.SetUsageState(usage); err != nil {
		t.Fatalf("persist seed usage: %v", err)
	}
	engine.compactionRuntimeState().SetSoonReminderIssued(true)
	blocker := mustBlockTestEventLogAppends(t, store)
	t.Cleanup(func() {
		if err := blocker.Restore(); err != nil {
			t.Errorf("restore event-log appends: %v", err)
		}
	})

	receipt, err := newCompactionPersistence(engine).replaceHistory(
		"compact",
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			Content:     textutil.Value("summary"),
		}}),
	)
	if err == nil || receipt.Committed {
		t.Fatalf("uncommitted history replacement outcome: receipt=%+v error=%v", receipt, err)
	}

	items := engine.transcriptRuntimeState().SnapshotItems()
	if len(items) != 1 ||
		items[0].Type != llm.ResponseItemTypeMessage ||
		items[0].Role == nil ||
		*items[0].Role != llm.RoleUser {
		t.Fatalf("uncommitted replacement changed active items: %+v", items)
	}
	if !engine.compactionRuntimeState().SoonReminderIssued() {
		t.Fatal("uncommitted replacement cleared compaction reminder")
	}
	storedUsage := store.Meta().UsageState
	if storedUsage == nil || storedUsage.InputTokens != usage.InputTokens {
		t.Fatalf("uncommitted replacement changed persisted usage: %+v", storedUsage)
	}
}
