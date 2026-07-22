package session

import (
	"os"
	"path/filepath"
	"testing"

	"core/internal/testharness/filemode"
)

func appendSessionTestRecord(
	t *testing.T,
	store *Store,
	stepID string,
	payload EventRecordPayload,
) EventRecord {
	t.Helper()
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist store before materializing event log: %v", err)
	}
	log := mustMaterializeSessionTestEventLog(t, store)
	record, _, err := log.AppendRecord(&stepID, payload)
	if err != nil {
		t.Fatalf("append typed event record: %v", err)
	}
	return record
}

func sessionTestLockedContract() LockedContract {
	toolPreambles := true
	return LockedContract{
		Model:             "gpt-5",
		SystemPrompt:      "prompt",
		HasSystemPrompt:   true,
		ReviewerPrompt:    "reviewer",
		HasReviewerPrompt: true,
		EnabledTools:      []string{"shell"},
		HasEnabledTools:   true,
		WebSearchMode:     "native",
		ToolPreambles:     &toolPreambles,
	}
}

func markSessionTestLocked(t *testing.T, store *Store, locked LockedContract) {
	t.Helper()
	if err := store.MarkModelDispatchLocked(locked); err != nil {
		t.Fatalf("mark model dispatch locked: %v", err)
	}
}

func TestNewLazyDoesNotPersistUntilFirstWrite(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if _, err := os.Stat(store.Dir()); !os.IsNotExist(err) {
		t.Fatalf("expected no session dir before first write, stat err=%v", err)
	}

	appendSessionTestRecord(t, store, "step1", sessionTestMessage(MessageRoleUser, "first write"))
	if _, err := os.Stat(filepath.Join(store.Dir(), eventsFile)); err != nil {
		t.Fatalf("expected events file after first write: %v", err)
	}
}

func TestAppendTypedBatchReportsUncommittedEventLogFailure(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	filemode.MustBlockEventLogAppends(t, store.eventsFP)

	stepID := "s1"
	events, receipt, err := log.AppendRecordsAtomic(&stepID, []EventRecordPayload{
		sessionTestMessage(MessageRoleUser, "must not commit"),
	})
	if err == nil {
		t.Fatal("append typed batch did not surface the event-log failure")
	}
	if receipt.Committed {
		t.Fatalf("append typed batch receipt = %+v, want uncommitted", receipt)
	}
	if len(events) != 1 {
		t.Fatalf("built events = %+v, want the attempted event", events)
	}
	if meta := storeTestMeta(store); meta.LastSequence != 0 || meta.FirstPromptPreview != "" {
		t.Fatalf("metadata mutated after uncommitted append: %+v", meta)
	}
}

func userMessageSeqAt(t *testing.T, store *Store, n int) int64 {
	t.Helper()
	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("collect events: %v", err)
	}
	visible := 0
	for _, evt := range events {
		isVisible, err := hasVisibleUserMessageRecord(evt)
		if err != nil {
			t.Fatalf("inspect visible user message: %v", err)
		}
		if isVisible {
			visible++
			if visible == n {
				return evt.Seq()
			}
		}
	}
	t.Fatalf("user message %d not found among %d events", n, len(events))
	return 0
}
