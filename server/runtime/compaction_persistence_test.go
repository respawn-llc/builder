package runtime

import (
	"testing"

	"core/server/tools"
)

func TestEmitCompactionStatusStillPublishesFailureEventWhenErrorPersistenceFails(t *testing.T) {
	store := mustCreateTestSession(t)
	var events []Event
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { events = append(events, event) },
	})
	blocker := mustBlockTestEventLogAppends(t, store)
	t.Cleanup(func() {
		if err := blocker.Restore(); err != nil {
			t.Errorf("restore event-log appends: %v", err)
		}
	})

	err := newCompactionPersistence(engine).emitStatus(
		"compaction",
		EventCompactionFailed,
		compactionModeAuto,
		"remote",
		"openai",
		nil,
		1,
		"provider failure",
	)
	if err == nil {
		t.Fatal("compaction failure persistence error was not surfaced")
	}
	failedEvents := 0
	for _, event := range events {
		switch event.Kind {
		case EventCompactionFailed:
			failedEvents++
		case EventLocalEntryAdded:
			t.Fatalf("failed compaction emitted persisted local entry: %+v", events)
		}
	}
	if failedEvents != 1 {
		t.Fatalf("compaction failure events = %d, want one", failedEvents)
	}
}
