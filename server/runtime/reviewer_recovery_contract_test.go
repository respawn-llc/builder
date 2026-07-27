package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestReviewerSkippedWhenNoToolCalls(t *testing.T) {
	t.Parallel()
	pipeline := defaultReviewerPipeline{}
	if pipeline.ShouldRunTurn("edits", &fakeClient{}, false) {
		t.Fatal("reviewer ran for edits frequency without a patch edit")
	}
}

func TestReviewerCompletionPersistsStatusWithoutCommittedCoordinates(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	main := &fakeClient{responses: []llm.Response{
		{Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("initial")}},
		{Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("reviewed")}},
	}}
	reviewer := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":["apply improvement"]}`)},
	}}}
	var events []Event
	engine := mustNewTestEngine(t, store, main, tools.NewRegistry(), Config{
		Model:    "gpt-5",
		Reviewer: ReviewerConfig{Frequency: "all", Model: "gpt-5", Client: reviewer},
		OnEvent:  func(event Event) { events = append(events, event) },
	})
	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); err != nil {
		t.Fatalf("submit reviewed turn: %v", err)
	}
	completed := 0
	for _, event := range events {
		if event.Kind != EventReviewerCompleted {
			continue
		}
		completed++
		if event.Reviewer == nil || event.Reviewer.Outcome != "applied" || event.Reviewer.SuggestionsCount != 1 || event.CommittedEntryStartSet {
			t.Fatalf("reviewer completion facts = %+v", event)
		}
	}
	if completed != 1 {
		t.Fatalf("reviewer completions = %d, want one; events=%+v", completed, events)
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded reviewer records: %v", err)
	}
	statuses, finalRows := 0, 0
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.LocalEntryRecord:
			if payload.Role == string(transcript.EntryRoleReviewerStatus) {
				statuses++
			}
		case session.MessageRecord:
			message, restoreErr := llmMessageFromSessionRecord(payload)
			if restoreErr != nil {
				t.Fatalf("restore reviewer record: %v", restoreErr)
			}
			if message.Role == llm.RoleAssistant && message.Phase != nil && *message.Phase == llm.MessagePhaseFinal {
				finalRows++
			}
		}
	}
	if statuses != 1 || finalRows != 2 {
		t.Fatalf("reviewer status/final records = statuses:%d finals:%d records:%+v", statuses, finalRows, window.Records)
	}
}

func TestReviewerInstructionAppendFailureKeepsOriginalFinalIdentity(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5", Reviewer: ReviewerConfig{Model: "gpt-5"},
		ProviderCapabilitiesOverride: &llm.ProviderCapabilities{ProviderID: "test"},
	})
	blocker := mustBlockTestEventLogAppends(t, store)
	t.Cleanup(func() { _ = blocker.Restore() })
	original := llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("original")}
	result, err := (&defaultReviewerPipeline{engine: engine}).RunFollowUp(
		context.Background(), "review", original, 7, true,
		&fakeClient{
			caps:      llm.ProviderCapabilities{ProviderID: "test"},
			responses: []llm.Response{{Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":["fix"]}`)}}},
		},
	)
	if err != nil {
		t.Fatalf("run reviewer follow-up: %v", err)
	}
	if result.Completion == nil || result.Completion.Outcome != "followup_failed" || result.Completion.SuggestionsCount != 1 ||
		result.Message.Role != original.Role ||
		result.AssistantCommittedStart != 7 || !result.AssistantCommittedStartSet {
		t.Fatalf("follow-up failure result = completion:%+v message:%+v", result.Completion, result.Message)
	}
}

func TestReviewerStatusAppendFailureDoesNotPublishCompletion(t *testing.T) {
	t.Parallel()
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

func TestCommittedReviewerStatusObserverFailureDoesNotPublishCompletion(t *testing.T) {
	t.Parallel()
	observerErr := errors.New("reviewer status observer failed")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	main, reviewer := reviewerAppliedClients()
	finals := 0
	var events []Event
	engine := mustNewTestEngine(t, store, main, tools.NewRegistry(), Config{
		Model:    "gpt-5",
		Reviewer: ReviewerConfig{Frequency: "all", Model: "gpt-5", Client: reviewer},
		OnEvent: func(event Event) {
			events = append(events, event)
			if event.Kind == EventAssistantMessage && event.Message.Phase != nil && *event.Message.Phase == llm.MessagePhaseFinal {
				finals++
				if finals == 2 {
					gate.FailNext(observerErr)
				}
			}
		},
	})
	if _, err := engine.SubmitUserMessage(context.Background(), "turn"); !errors.Is(err, observerErr) {
		t.Fatalf("reviewer status observer error = %v", err)
	}
	for _, event := range events {
		if event.Kind == EventReviewerCompleted {
			t.Fatalf("observer-failed reviewer status published completion: %+v", event)
		}
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded records: %v", err)
	}
	statuses := 0
	for _, record := range window.Records {
		if entry, ok := mustSessionEventPayload(record).(session.LocalEntryRecord); ok && entry.Role == string(transcript.EntryRoleReviewerStatus) {
			statuses++
		}
	}
	if statuses != 1 {
		t.Fatalf("committed reviewer statuses = %d, want one", statuses)
	}
}

func TestAppendCommittedEntryRecordDoesNotMutateChatOnAppendFailure(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
