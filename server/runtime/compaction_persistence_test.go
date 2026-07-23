package runtime

import (
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
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

func TestReplaceHistoryPublishesProjectedTranscriptEntriesBeforeCompactionStatus(t *testing.T) {
	var events []Event
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { events = append(events, event) },
	})
	receipt, err := newCompactionPersistence(engine).replaceHistory(
		"compact",
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{
			{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
			{Role: llm.RoleUser, Content: textutil.Value("continued input")},
		}),
	)
	if err != nil || !receipt.Committed {
		t.Fatalf("replace history: receipt=%+v error=%v", receipt, err)
	}
	if err := newCompactionPersistence(engine).emitStatus("compact", EventCompactionCompleted, compactionModeManual, "local", "openai", nil, 1, ""); err != nil {
		t.Fatalf("emit completion status: %v", err)
	}

	localIndexes := []int{}
	statusIndex := -1
	for index, event := range events {
		switch event.Kind {
		case EventLocalEntryAdded:
			if !event.LocalEntryProjected || !event.CommittedEntryStartSet {
				t.Fatalf("projected replacement event = %+v", event)
			}
			localIndexes = append(localIndexes, index)
		case EventCompactionCompleted:
			statusIndex = index
		}
	}
	if len(localIndexes) != 2 || statusIndex < 0 || localIndexes[0] >= localIndexes[1] || localIndexes[1] >= statusIndex {
		t.Fatalf("replacement/status publication order = locals:%v status:%d events:%+v", localIndexes, statusIndex, events)
	}
	rows := mustTranscriptHydrationSnapshot(t, engine).CommittedRows
	if len(rows) != 2 {
		t.Fatalf("replacement hydration rows = %+v", rows)
	}
}

func TestAutoCompactionStatusEventDoesNotPublishCommittedEntryStart(t *testing.T) {
	var events []Event
	engine := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { events = append(events, event) },
	})
	if err := newCompactionPersistence(engine).emitStatus("compact", EventCompactionCompleted, compactionModeAuto, "local", "openai", nil, 1, ""); err != nil {
		t.Fatalf("emit auto completion status: %v", err)
	}
	if len(events) != 1 || events[0].Kind != EventCompactionCompleted || events[0].CommittedEntryStartSet {
		t.Fatalf("auto compaction status facts = %+v", events)
	}
}
