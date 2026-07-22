package session

import (
	"os"
	"path/filepath"
	"testing"

	"core/internal/testharness/filemode"
)

func appendSessionTestEvent(t *testing.T, store *Store, stepID, kind string, payload any) Event {
	t.Helper()
	event, _, err := store.AppendEvent(stepID, kind, payload)
	if err != nil {
		t.Fatalf("append %s event: %v", kind, err)
	}
	return event
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

	appendSessionTestEvent(t, store, "step1", "message", map[string]any{"a": 1})
	if _, err := os.Stat(filepath.Join(store.Dir(), eventsFile)); err != nil {
		t.Fatalf("expected events file after first write: %v", err)
	}
}

func TestAppendTurnAtomicReportsUncommittedEventLogFailure(t *testing.T) {
	store := newSessionTestStore(t)
	filemode.MustBlockEventLogAppends(t, store.eventsFP)

	events, receipt, err := store.AppendTurnAtomic("s1", []EventInput{{
		Kind:    "message",
		Payload: map[string]any{"role": "user", "content": "must not commit"},
	}})
	if err == nil {
		t.Fatal("append turn did not surface the event-log failure")
	}
	if receipt.Committed {
		t.Fatalf("append turn receipt = %+v, want uncommitted", receipt)
	}
	if len(events) != 1 {
		t.Fatalf("built events = %+v, want the attempted event", events)
	}
	if meta := store.Meta(); meta.LastSequence != 0 || meta.FirstPromptPreview != "" {
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
		if hasVisibleUserMessageEvent(evt.Kind, evt.Payload) {
			visible++
			if visible == n {
				return evt.Seq
			}
		}
	}
	t.Fatalf("user message %d not found among %d events", n, len(events))
	return 0
}
