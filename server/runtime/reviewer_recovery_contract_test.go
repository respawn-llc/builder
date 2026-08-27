package runtime

import (
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestReviewerSkippedWhenNoToolCalls(t *testing.T) {
	pipeline := defaultReviewerPipeline{}
	if pipeline.ShouldRunTurn("edits", &fakeClient{}, false) {
		t.Fatal("Reviewer ran for edits frequency without a patch edit")
	}
}

func TestAppendCommittedEntryRecordDoesNotMutateChatOnAppendFailure(t *testing.T) {
	store := mustCreateTestSession(t)
	var events []Event
	engine := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { events = append(events, event) },
	})
	blocker := mustBlockTestEventLogAppends(t, store)
	if err := engine.steer(
		runtimeTestStepID("entry"),
		steerLocalEntryIntent(storedLocalEntry{Role: string(transcript.EntryRoleReviewerStatus), Text: "status"}),
	); err == nil {
		t.Fatal("local entry append failure was not surfaced")
	}
	if len(events) != 0 || len(mustTranscriptHydrationSnapshot(t, engine).CommittedRows) != 0 {
		t.Fatalf("uncommitted local entry changed projection: events=%+v rows=%+v", events, mustTranscriptHydrationSnapshot(t, engine).CommittedRows)
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event-log appends: %v", err)
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(4)
	if err != nil {
		t.Fatalf("read bounded records: %v", err)
	}
	for _, record := range window.Records {
		if _, ok := mustSessionEventPayload(record).(session.LocalEntryRecord); ok {
			t.Fatalf("uncommitted local entry persisted: %+v", record)
		}
	}
}

func TestBuildReviewerTranscriptMessagesKeepsOrphanToolOutputEntry(t *testing.T) {
	items := buildReviewerTranscriptItems([]llm.ResponseItem{{
		Type:   llm.ResponseItemTypeFunctionCallOutput,
		CallID: textutil.Value("orphan-call"),
		Name:   textutil.Value("tool"),
		Output: []byte(`{"ok":true}`),
	}})
	if len(items) != 1 || items[0].Type != llm.ResponseItemTypeMessage ||
		items[0].Role == nil || *items[0].Role != llm.RoleUser {
		t.Fatalf("orphan tool output Reviewer projection = %+v", items)
	}
}
