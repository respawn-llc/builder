package runtime

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestReviewerSkippedWhenNoToolCalls(t *testing.T) {
	pipeline := defaultReviewerPipeline{}
	if pipeline.ShouldRunTurn("edits", &fakeClient{}, false) {
		t.Fatal("reviewer ran for edits frequency without a patch edit")
	}
}

func TestReviewerStatusAppendFailureDoesNotPublishCompletion(t *testing.T) {
	store := mustCreateTestSession(t)
	main, reviewer := reviewerAppliedClients()
	finals := 0
	var blocker interface{ Restore() error }
	var events []Event
	engine := mustNewTestEngine(t, store, main, tools.NewRegistry(), Config{
		Model:    "gpt-5",
		Reviewer: ReviewerConfig{Frequency: "all", Model: "gpt-5", Client: reviewer},
		OnEvent: func(event Event) {
			events = append(events, event)
			if event.Kind == EventAssistantMessage && event.Message.Phase != nil && *event.Message.Phase == llm.MessagePhaseFinal {
				finals++
				if finals == 2 {
					blocker = mustBlockTestEventLogAppends(t, store)
				}
			}
		},
	})
	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); err == nil {
		t.Fatal("uncommitted reviewer status append failure was not surfaced")
	}
	if blocker != nil {
		if err := blocker.Restore(); err != nil {
			t.Fatalf("restore status blocker: %v", err)
		}
	}
	for _, event := range events {
		if event.Kind == EventReviewerCompleted {
			t.Fatalf("uncommitted reviewer status published completion: %+v", event)
		}
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded records: %v", err)
	}
	for _, record := range window.Records {
		if entry, ok := mustSessionEventPayload(record).(session.LocalEntryRecord); ok && entry.Role == string(transcript.EntryRoleReviewerStatus) {
			t.Fatalf("uncommitted reviewer status persisted: %+v", entry)
		}
	}
}

func TestReviewerCompletionPublicationFailureSurfacesAfterCommittedBoundary(t *testing.T) {
	store := mustCreateTestSession(t)
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	main := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value("initial"),
		},
	}}}
	reviewer := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value(`{"suggestions":[]}`),
		},
	}}}
	var engine *Engine
	var events []Event
	engine = mustNewTestEngine(t, store, main, tools.NewRegistry(), Config{
		Model:    "gpt-5",
		Reviewer: ReviewerConfig{Frequency: "all", Model: "gpt-5", Client: reviewer},
		OnEvent: func(event Event) {
			events = append(events, event)
			if event.Kind == EventLocalEntryAdded &&
				event.LocalEntry != nil &&
				event.LocalEntry.Role == string(transcript.EntryRoleReviewerStatus) {
				engine.eventLog = session.MaterializedEventLog{}
			}
		},
	})

	_, submitErr := engine.SubmitUserMessage(context.Background(), "turn")
	engine.eventLog = eventLog
	if submitErr == nil {
		t.Fatal("reviewer completion publication failure was not surfaced")
	}

	window, readErr := eventLog.ReadRecentRecords(32)
	if readErr != nil {
		t.Fatalf("read committed reviewer records: %v", readErr)
	}
	boundaries := 0
	statuses := 0
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.AgentStepBoundaryRecord:
			boundaries++
		case session.LocalEntryRecord:
			if payload.Role == string(transcript.EntryRoleReviewerStatus) {
				statuses++
			}
		}
	}
	if boundaries != 1 || statuses != 1 {
		t.Fatalf("committed reviewer facts = boundaries:%d statuses:%d, want 1/1", boundaries, statuses)
	}
	for _, event := range events {
		if event.Kind == EventReviewerCompleted {
			t.Fatalf("reviewer completion event was emitted despite publication failure: %+v", event)
		}
	}
}

func TestAppendCommittedEntryRecordDoesNotMutateChatOnAppendFailure(t *testing.T) {
	store := mustCreateTestSession(t)
	var events []Event
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5", OnEvent: func(event Event) { events = append(events, event) },
	})
	blocker := mustBlockTestEventLogAppends(t, store)
	if err := engine.steer("entry", steerLocalEntryIntent(storedLocalEntry{Role: string(transcript.EntryRoleReviewerStatus), Text: "status"})); err == nil {
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
	if len(items) != 1 || items[0].Type != llm.ResponseItemTypeMessage || items[0].Role == nil || *items[0].Role != llm.RoleUser {
		t.Fatalf("orphan tool output reviewer projection = %+v", items)
	}
}

func reviewerAppliedClients() (*fakeClient, *fakeClient) {
	return &fakeClient{responses: []llm.Response{
			{Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("initial")}},
			{Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("reviewed")}},
		}},
		&fakeClient{responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":["improve"]}`)},
		}}}
}
