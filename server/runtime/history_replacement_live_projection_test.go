package runtime

import (
	"reflect"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestHistoryReplacementProjectsPreservedUserContextWithoutReplayingUserTurns(t *testing.T) {
	t.Parallel()
	var events []Event
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(
		t,
		store,
		&fakeClient{},
		tools.NewRegistry(),
		Config{
			Model:   "gpt-5",
			OnEvent: func(event Event) { events = append(events, event) },
		},
	)
	items := llm.ItemsFromMessages([]llm.Message{
		{Role: llm.RoleUser, Content: textutil.Value("first preserved prompt")},
		{Role: llm.RoleUser, Content: textutil.Value("second preserved prompt")},
		{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeEnvironment),
			Content:     textutil.Value("current environment"),
		},
	})

	if err := engine.steer(
		"compaction",
		steerHistoryReplacementIntent("local", compactionModeAuto, 1, "", "", items),
	); err != nil {
		t.Fatalf("persist history replacement: %v", err)
	}

	live := make([]TranscriptCommittedRowFact, 0)
	projectedRows := 0
	for index := range events {
		event := events[index]
		if event.Kind != EventLocalEntryAdded || !event.LocalEntryProjected {
			continue
		}
		if !event.CommittedEntryStartSet ||
			event.CommittedEntryStart != projectedRows ||
			event.CommittedEntryCount != projectedRows+1 {
			t.Fatalf("projected row coordinates = %+v at index %d", event, projectedRows)
		}
		projectedRows++
		live = append(live, TranscriptCommittedRowFactsFromEvent(event)...)
	}
	if projectedRows == 0 || len(live) == 0 {
		t.Fatalf("history replacement emitted no projected transcript facts: %+v", events)
	}
	if len(live) != 4 {
		t.Fatalf("projected transcript facts = %+v, want summary, two preserved messages, and environment", live)
	}
	wantMessageTypes := []llm.MessageType{
		llm.MessageTypeCompactionSummary,
		llm.MessageTypeCompactionPreservedUserMessage,
		llm.MessageTypeCompactionPreservedUserMessage,
		llm.MessageTypeEnvironment,
	}
	wantVisibility := []transcript.EntryVisibility{
		transcript.EntryVisibilityOngoing,
		transcript.EntryVisibilityDetail,
		transcript.EntryVisibilityDetail,
		transcript.EntryVisibilityDetail,
	}
	wantDetails := []string{
		"",
		"first preserved prompt",
		"second preserved prompt",
		"current environment",
	}
	for index, fact := range live {
		if fact.Kind != TranscriptCommittedRowFactNotice || fact.Notice == nil {
			t.Fatalf("projected fact %d = %+v, want typed notice", index, fact)
		}
		if fact.Notice.MessageType != wantMessageTypes[index] {
			t.Fatalf("projected fact %d message type = %q, want %q", index, fact.Notice.MessageType, wantMessageTypes[index])
		}
		if fact.Visibility != wantVisibility[index] {
			t.Fatalf("projected fact %d visibility = %q, want %q", index, fact.Visibility, wantVisibility[index])
		}
		if index > 0 && fact.Notice.DiagnosticDetail != wantDetails[index] {
			t.Fatalf("projected fact %d detail = %q, want %q", index, fact.Notice.DiagnosticDetail, wantDetails[index])
		}
	}

	page := mustEngineNewestSegmentPage(t, engine)
	hydrated := TranscriptCommittedRowFactsFromSnapshot(page.Snapshot)
	if !reflect.DeepEqual(hydrated, live) {
		t.Fatalf("persisted active segment facts = %+v, live facts = %+v", hydrated, live)
	}
	workingSet := engine.transcriptRuntimeState().SnapshotItems()
	if len(workingSet) < 2 {
		t.Fatalf("provider working set = %+v, want preserved user items", workingSet)
	}
	wantProviderContent := []string{"first preserved prompt", "second preserved prompt"}
	for index, wantContent := range wantProviderContent {
		item := workingSet[index]
		if item.Role == nil || *item.Role != llm.RoleUser ||
			item.MessageType != nil ||
			item.Content == nil || *item.Content != wantContent {
			t.Fatalf("provider item %d changed while projecting transcript provenance: %+v", index, item)
		}
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("close live engine: %v", err)
	}
	reopened := mustNewTestEngine(
		t,
		mustOpenTestSession(t, store.Dir()),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	reopenedPage := mustEngineNewestSegmentPage(t, reopened)
	reopenedFacts := TranscriptCommittedRowFactsFromSnapshot(reopenedPage.Snapshot)
	if !reflect.DeepEqual(reopenedFacts, live) {
		t.Fatalf("reopened active segment facts = %+v, live facts = %+v", reopenedFacts, live)
	}
}
