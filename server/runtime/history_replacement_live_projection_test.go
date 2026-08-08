package runtime

import (
	"context"
	"reflect"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestRemoteCompactionReplacementOwnsExactlyOneTranscriptSummary(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	var events []Event
	client := &fakeCompactionClient{compactionResponses: []llm.CompactionResponse{{
		OutputItems: []llm.ResponseItem{
			{
				Type:    llm.ResponseItemTypeMessage,
				Role:    textutil.Value(llm.RoleUser),
				Content: textutil.Value("provider-preserved prompt"),
			},
			{
				Type:             llm.ResponseItemTypeCompaction,
				ID:               textutil.Value("checkpoint"),
				EncryptedContent: textutil.Value("encrypted"),
			},
		},
		Usage: llm.Usage{InputTokens: 100, WindowTokens: 200_000},
	}}}
	engine := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:          "gpt-5",
		CompactionMode: "native",
		OnEvent:        func(event Event) { events = append(events, event) },
	})
	if err := engine.steer(
		"input",
		steerMessagesWithPersistenceIntent(
			steeringPriorityNormal,
			steeringMessageEventNone,
			true,
			[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
		),
	); err != nil {
		t.Fatalf("persist compaction input: %v", err)
	}
	if err := engine.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact remote context: %v", err)
	}

	liveFacts := make([]TranscriptCommittedRowFact, 0)
	for _, event := range events {
		liveFacts = append(liveFacts, TranscriptCommittedRowFactsFromEvent(event)...)
	}
	assertSingleCompactionSummaryAndPreservedUserFact(t, liveFacts)

	page := mustEngineNewestSegmentPage(t, engine)
	assertSingleCompactionSummaryAndPreservedUserFact(
		t,
		TranscriptCommittedRowFactsFromSnapshot(page.Snapshot),
	)
	if err := engine.Close(); err != nil {
		t.Fatalf("close live engine: %v", err)
	}

	reopenedStore := mustOpenTestSession(t, store.Dir())
	reopened := mustNewTestEngine(
		t,
		reopenedStore,
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	reopenedPage := mustEngineNewestSegmentPage(t, reopened)
	assertSingleCompactionSummaryAndPreservedUserFact(
		t,
		TranscriptCommittedRowFactsFromSnapshot(reopenedPage.Snapshot),
	)

	eventLog := mustMaterializeTestEventLog(t, reopenedStore)
	window, err := eventLog.ReadRecentRecords(32)
	if err != nil {
		t.Fatalf("read bounded dormant records: %v", err)
	}
	scan := NewPersistedTranscriptScan(PersistedTranscriptScanRequest{})
	for _, record := range window.Records {
		if err := scan.ApplyPersistedEvent(record); err != nil {
			t.Fatalf("apply dormant record: %v", err)
		}
	}
	assertSingleCompactionSummaryAndPreservedUserFact(
		t,
		TranscriptCommittedRowFactsFromSnapshot(scan.CollectedPageSnapshot()),
	)
}

func assertSingleCompactionSummaryAndPreservedUserFact(
	t *testing.T,
	facts []TranscriptCommittedRowFact,
) {
	t.Helper()
	summaries := 0
	preservedUsers := 0
	for _, fact := range facts {
		if fact.Kind == TranscriptCommittedRowFactUser &&
			fact.User != nil &&
			fact.User.Text == "provider-preserved prompt" {
			t.Fatalf("provider-preserved prompt replayed as a user turn: %+v", facts)
		}
		if fact.Kind != TranscriptCommittedRowFactNotice || fact.Notice == nil {
			continue
		}
		switch fact.Notice.MessageType {
		case llm.MessageTypeCompactionSummary:
			summaries++
		case llm.MessageTypeCompactionPreservedUserMessage:
			if fact.Notice.DiagnosticDetail == "provider-preserved prompt" {
				preservedUsers++
			}
		}
	}
	if summaries != 1 || preservedUsers != 1 {
		t.Fatalf(
			"compaction summary/preserved-user facts = %d/%d, want one/one: %+v",
			summaries,
			preservedUsers,
			facts,
		)
	}
}

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
	const (
		firstPreservedPrompt  = "  first preserved prompt\n"
		secondPreservedPrompt = "\tsecond preserved prompt  "
	)
	items := llm.ItemsFromMessages([]llm.Message{
		{Role: llm.RoleUser, Content: textutil.Value(firstPreservedPrompt)},
		{Role: llm.RoleUser, Content: textutil.Value(secondPreservedPrompt)},
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
		firstPreservedPrompt,
		secondPreservedPrompt,
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
	if err := engine.Close(); err != nil {
		t.Fatalf("close live engine: %v", err)
	}
	providerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("done"),
		},
	}}}
	reopened := mustNewTestEngine(
		t,
		mustOpenTestSession(t, store.Dir()),
		providerClient,
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	reopenedPage := mustEngineNewestSegmentPage(t, reopened)
	reopenedFacts := TranscriptCommittedRowFactsFromSnapshot(reopenedPage.Snapshot)
	if !reflect.DeepEqual(reopenedFacts, live) {
		t.Fatalf("reopened active segment facts = %+v, live facts = %+v", reopenedFacts, live)
	}
	if _, err := reopened.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit post-compaction message: %v", err)
	}
	if len(providerClient.calls) != 1 || len(providerClient.calls[0].Items) < 2 {
		t.Fatalf("post-compaction provider calls = %+v, want one request with preserved items", providerClient.calls)
	}
	wantProviderContent := []string{firstPreservedPrompt, secondPreservedPrompt}
	for index, wantContent := range wantProviderContent {
		item := providerClient.calls[0].Items[index]
		if item.Role == nil || *item.Role != llm.RoleUser ||
			item.MessageType != nil ||
			item.Content == nil || *item.Content != wantContent {
			t.Fatalf("provider item %d changed across compaction: %+v", index, item)
		}
	}
}
