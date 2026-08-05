package runtime

import (
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestHistoryReplacementLocatorsSurviveReopenAsOneBoundedBatch(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 16)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { events = append(events, event) },
	})
	items := llm.ItemsFromMessages([]llm.Message{
		{Role: llm.RoleUser, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary")},
		{Role: llm.RoleUser, Content: textutil.Value("preserved")},
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment), Content: textutil.Value("environment")},
	})
	if err := engine.steer("compaction", steerHistoryReplacementIntent("local", compactionModeManual, 1, "", "", items)); err != nil {
		t.Fatalf("persist history replacement: %v", err)
	}

	live := make([]TranscriptCommittedRowFact, 0, 3)
	for _, event := range events {
		if event.LocalEntryProjected {
			live = append(live, TranscriptCommittedRowFactsFromEvent(event)...)
		}
	}
	if len(live) != 3 {
		t.Fatalf("live replacement facts = %+v, want three rows", live)
	}
	for index, fact := range live {
		if fact.Locator.RowOrdinal != int64(index+1) {
			t.Fatalf("live replacement fact %d locator = %+v, want ordinal %d", index, fact.Locator, index+1)
		}
		if fact.Locator.EventSequence <= 0 {
			t.Fatalf("live replacement fact %d locator = %+v, want positive sequence", index, fact.Locator)
		}
	}

	pageFacts := TranscriptCommittedRowFactsFromSnapshot(mustEngineNewestSegmentPage(t, engine).Snapshot)
	if len(pageFacts) != len(live) {
		t.Fatalf("active segment facts = %+v, live facts = %+v", pageFacts, live)
	}
	for index := range live {
		if pageFacts[index].Locator != live[index].Locator {
			t.Fatalf("active segment locator[%d] = %+v, live locator = %+v", index, pageFacts[index].Locator, live[index].Locator)
		}
	}
	hydrationFacts := hydrationSnapshot(t, engine).CommittedRows
	if len(hydrationFacts) != len(live) {
		t.Fatalf("current hydration facts = %+v, live facts = %+v", hydrationFacts, live)
	}
	for index := range live {
		if hydrationFacts[index].Locator != live[index].Locator {
			t.Fatalf("current hydration locator[%d] = %+v, live locator = %+v", index, hydrationFacts[index].Locator, live[index].Locator)
		}
	}
	if err := engine.Close(); err != nil {
		t.Fatalf("close engine: %v", err)
	}
	reopened := mustNewTestEngine(t, mustOpenTestSession(t, store.Dir()), &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	reopenedFacts := TranscriptCommittedRowFactsFromSnapshot(mustEngineNewestSegmentPage(t, reopened).Snapshot)
	if len(reopenedFacts) != len(live) {
		t.Fatalf("reopened facts = %+v, live facts = %+v", reopenedFacts, live)
	}
	for index := range live {
		if reopenedFacts[index].Locator != live[index].Locator {
			t.Fatalf("reopened locator[%d] = %+v, live locator = %+v", index, reopenedFacts[index].Locator, live[index].Locator)
		}
	}
	reopenedHydrationFacts := hydrationSnapshot(t, reopened).CommittedRows
	if len(reopenedHydrationFacts) != len(live) {
		t.Fatalf("reopened hydration facts = %+v, live facts = %+v", reopenedHydrationFacts, live)
	}
	for index := range live {
		if reopenedHydrationFacts[index].Locator != live[index].Locator {
			t.Fatalf("reopened hydration locator[%d] = %+v, live locator = %+v", index, reopenedHydrationFacts[index].Locator, live[index].Locator)
		}
	}

	_ = transcript.EntryVisibilityOngoing
}

func TestHistoryReplacementLocatorsSkipFilteredToolCallEntries(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	events := make([]Event, 0, 16)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(event Event) { events = append(events, event) },
	})
	items := llm.ItemsFromMessages([]llm.Message{
		{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("visible assistant"),
			ToolCalls: []llm.ToolCall{{
				ID:    "call-filtered",
				Name:  "shell",
				Input: []byte(`{"command":"pwd"}`),
			}},
		},
		{Role: llm.RoleUser, Content: textutil.Value("visible user")},
	})
	if err := engine.steer("compaction", steerHistoryReplacementIntent("local", compactionModeManual, 1, "", "", items)); err != nil {
		t.Fatalf("persist history replacement: %v", err)
	}

	live := make([]TranscriptCommittedRowFact, 0, 3)
	for _, event := range events {
		if event.LocalEntryProjected {
			live = append(live, TranscriptCommittedRowFactsFromEvent(event)...)
		}
	}
	if len(live) != 3 {
		t.Fatalf("live replacement facts = %+v, want three visible rows", live)
	}
	for index, fact := range live {
		if got, want := fact.Locator.RowOrdinal, int64(index+1); got != want {
			t.Fatalf("live fact %d locator = %+v, want ordinal %d", index, fact.Locator, want)
		}
	}
	pageFacts := TranscriptCommittedRowFactsFromSnapshot(mustEngineNewestSegmentPage(t, engine).Snapshot)
	hydrationFacts := hydrationSnapshot(t, engine).CommittedRows
	assertReplacementLocatorsMatch(t, pageFacts, live, "page")
	assertReplacementLocatorsMatch(t, hydrationFacts, live, "hydration")
}

func assertReplacementLocatorsMatch(
	t *testing.T,
	got []TranscriptCommittedRowFact,
	want []TranscriptCommittedRowFact,
	source string,
) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s facts = %+v, want %+v", source, got, want)
	}
	for index := range want {
		if got[index].Locator != want[index].Locator {
			t.Fatalf("%s locator[%d] = %+v, want %+v", source, index, got[index].Locator, want[index].Locator)
		}
	}
}
