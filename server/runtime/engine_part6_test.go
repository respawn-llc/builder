package runtime

import (
	"context"
	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestReviewerSuggestionsPrecedeFollowUpAndCompletionReportsApplication(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("original final"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("updated final after review"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":["Add final verification notes."]}`)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	var (
		eventsMu                   sync.Mutex
		assistantEvent             *Event
		assistantEventCount        int
		reviewerCompletedEvent     *Event
		snapshotAtReviewerComplete ChatSnapshot
		reviewerEventOrder         []Event
		eng                        *Engine
	)
	eng = mustNewTestEngine(t, store, mainClient, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.Kind == EventLocalEntryAdded || evt.Kind == EventAssistantMessage || evt.Kind == EventReviewerCompleted {
				eventsMu.Lock()
				reviewerEventOrder = append(reviewerEventOrder, evt)
				eventsMu.Unlock()
			}
			if evt.Kind == EventAssistantMessage && messageContent(evt.Message) == "updated final after review" {
				eventsMu.Lock()
				captured := evt
				assistantEvent = &captured
				assistantEventCount++
				eventsMu.Unlock()
				return
			}
			if evt.Kind != EventReviewerCompleted || evt.Reviewer == nil || evt.Reviewer.Outcome != "applied" {
				return
			}
			eventsMu.Lock()
			defer eventsMu.Unlock()
			captured := evt
			reviewerCompletedEvent = &captured
			snapshotAtReviewerComplete = eng.ChatSnapshot()
		},
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			VerboseOutput: true,
			Client:        reviewerClient,
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "do task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "updated final after review" {
		t.Fatalf("assistant content = %q, want updated final after review", messageContent(msg))
	}
	if got := fakeClientCallCount(reviewerClient); got != 1 {
		t.Fatalf("reviewer calls = %d, want 1 for an all-frequency no-tool final", got)
	}

	eventsMu.Lock()
	assistant := assistantEvent
	assistantCount := assistantEventCount
	completed := reviewerCompletedEvent
	snapshotAtCompletion := snapshotAtReviewerComplete
	eventOrder := append([]Event(nil), reviewerEventOrder...)
	eventsMu.Unlock()
	if assistant == nil {
		t.Fatal("expected follow-up assistant event")
	}
	if assistantCount != 1 {
		t.Fatalf("follow-up assistant event count = %d, want 1", assistantCount)
	}
	if completed == nil {
		t.Fatal("expected reviewer completed event")
	}
	if len(snapshotAtCompletion.Entries) < 3 {
		t.Fatalf("expected feedback, follow-up assistant, and reviewer status in completion snapshot, got %+v", snapshotAtCompletion.Entries)
	}
	assistantIndex := len(snapshotAtCompletion.Entries) - 2
	assistantEntry := snapshotAtCompletion.Entries[assistantIndex]
	if assistantEntry.Role != "assistant" || assistantEntry.Text != "updated final after review" {
		t.Fatalf("expected completion snapshot penultimate entry to be follow-up assistant, got %+v", assistantEntry)
	}
	if !assistant.CommittedEntryStartSet {
		t.Fatalf("expected follow-up assistant event committed start metadata, got %+v", *assistant)
	}
	if got, want := assistant.CommittedEntryStart, assistantIndex; got != want {
		t.Fatalf("follow-up assistant committed start = %d, want %d; snapshot=%+v", got, want, snapshotAtCompletion.Entries)
	}
	statusEntry := snapshotAtCompletion.Entries[len(snapshotAtCompletion.Entries)-1]
	if statusEntry.Role != string(transcript.EntryRoleReviewerStatus) ||
		statusEntry.Text != reviewerStatusText(*completed.Reviewer, nil) {
		t.Fatalf("expected completion snapshot to end with reviewer application status, got %+v", statusEntry)
	}
	feedbackEntry := snapshotAtCompletion.Entries[len(snapshotAtCompletion.Entries)-3]
	if feedbackEntry.ReviewerFeedback == nil || len(feedbackEntry.ReviewerFeedback.Suggestions) != 1 {
		t.Fatalf("expected typed reviewer feedback before follow-up assistant, got %+v", feedbackEntry)
	}

	feedbackEventIndex, assistantEventIndex, statusEventIndex, completedEventIndex := -1, -1, -1, -1
	for index := range eventOrder {
		event := eventOrder[index]
		switch {
		case event.Kind == EventLocalEntryAdded && event.LocalEntry != nil && event.LocalEntry.ReviewerFeedback != nil:
			feedbackEventIndex = index
		case event.Kind == EventAssistantMessage && messageContent(event.Message) == "updated final after review":
			assistantEventIndex = index
		case event.Kind == EventLocalEntryAdded && event.LocalEntry != nil && event.LocalEntry.Role == string(transcript.EntryRoleReviewerStatus):
			statusEventIndex = index
		case event.Kind == EventReviewerCompleted:
			completedEventIndex = index
		}
	}
	if !(feedbackEventIndex >= 0 &&
		feedbackEventIndex < assistantEventIndex &&
		assistantEventIndex < statusEventIndex &&
		statusEventIndex < completedEventIndex) {
		t.Fatalf(
			"reviewer event order = feedback:%d assistant:%d status:%d completed:%d events:%+v",
			feedbackEventIndex,
			assistantEventIndex,
			statusEventIndex,
			completedEventIndex,
			eventOrder,
		)
	}
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded reviewer records: %v", err)
	}
	feedbackRecords, statusRecords, finalRows := 0, 0, 0
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.ReviewerFeedbackRecord:
			feedbackRecords++
		case session.LocalEntryRecord:
			if payload.Role == string(transcript.EntryRoleReviewerStatus) {
				statusRecords++
			}
		case session.MessageRecord:
			message := llmMessageFromSessionRecord(payload)
			if message.Role == llm.RoleAssistant && message.Phase != nil && *message.Phase == llm.MessagePhaseFinal {
				finalRows++
			}
		}
	}
	if feedbackRecords != 1 || statusRecords != 1 || finalRows != 2 {
		t.Fatalf(
			"reviewer records = feedback:%d status:%d finals:%d records:%+v",
			feedbackRecords,
			statusRecords,
			finalRows,
			window.Records,
		)
	}

	eng.AppendCommittedEntry("warning", "later unrelated note")
	finalSnapshot := eng.ChatSnapshot()
	if got, want := len(finalSnapshot.Entries), len(snapshotAtCompletion.Entries)+1; got != want {
		t.Fatalf("expected later note after reviewer completion snapshot, got %d entries want %d", got, want)
	}
	if finalSnapshot.Entries[len(finalSnapshot.Entries)-1].Text != "later unrelated note" {
		t.Fatalf("expected later unrelated note at transcript tail, got %+v", finalSnapshot.Entries[len(finalSnapshot.Entries)-1])
	}
}

func TestAppendCommittedEntryEmitsRealtimeLocalEntryEvent(t *testing.T) {
	store := mustCreateTestSession(t)
	var events []Event
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	if err := eng.steer("step-1", steerLocalEntryIntent(storedLocalEntry{Visibility: transcript.EntryVisibilityAuto, Role: "reviewer_suggestions", Text: "Supervisor suggested:\n1. Add verification notes.", CondensedText: textutil.Value("Supervisor made 1 suggestion.")})); err != nil {
		t.Fatalf("append persisted local entry: %v", err)
	}
	if got := len(events); got != 1 {
		t.Fatalf("event count = %d, want 1", got)
	}
	if got := events[0].Kind; got != EventLocalEntryAdded {
		t.Fatalf("first event kind = %q, want %q", got, EventLocalEntryAdded)
	}
	if events[0].LocalEntry == nil {
		t.Fatal("expected local entry payload on realtime local entry event")
	}
	if got := events[0].LocalEntry.Role; got != "reviewer_suggestions" {
		t.Fatalf("local entry role = %q, want reviewer_suggestions", got)
	}
	if got := events[0].LocalEntry.CondensedText; got != "Supervisor made 1 suggestion." {
		t.Fatalf("local entry ongoing text = %q, want supervisor summary", got)
	}
}

func TestRunReviewerFollowUpReturnsCompletionWhenReviewerInstructionAppendFails(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:    "gpt-5",
		Reviewer: ReviewerConfig{Model: "gpt-5"},
	})
	if err := eng.steer("prep-1", steerMessagesWithPersistenceIntent(steeringPriorityUser, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("first request")}})); err != nil {
		t.Fatalf("append first message: %v", err)
	}

	reviewerClient := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true}, responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":["Add final verification notes."]}`)},
		Usage:     llm.Usage{InputTokens: 10},
	}}}

	eventsPath := filepath.Join(store.Dir(), "events.jsonl")
	info, err := os.Stat(eventsPath)
	if err != nil {
		t.Fatalf("stat events log: %v", err)
	}
	if err := os.Chmod(eventsPath, 0o400); err != nil {
		t.Fatalf("chmod events log readonly: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(eventsPath, info.Mode()) })

	_, err = eng.runReviewerFollowUp(context.Background(), "step-1", llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("original final")}, -1, false, reviewerClient)
	if err == nil {
		t.Fatal("expected Reviewer instruction append failure")
	}
}

func TestRunStepLoopFailsWhenReviewerStatusPersistenceFailsAfterReviewerInstructionAppendFailure(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("original final"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	reviewerClient := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true}, responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":["Add final verification notes."]}`)},
		Usage:     llm.Usage{InputTokens: 10, WindowTokens: 200000},
	}}}

	var (
		eventsMu      sync.Mutex
		events        []Event
		blockReviewer bool
		blocker       *testEventLogAppendBlocker
		blockErr      error
	)
	eng := mustNewTestEngine(t, store, mainClient, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		AutoCompactTokenLimit: 1_000_000,
		OnEvent: func(evt Event) {
			eventsMu.Lock()
			defer eventsMu.Unlock()
			events = append(events, evt)
			if blockReviewer && evt.Kind == EventAssistantMessage {
				blockReviewer = false
				blocker, blockErr = blockTestEventLogAppends(store)
			}
		},
		Reviewer: ReviewerConfig{
			Frequency: "all",
			Model:     "gpt-5",
			Client:    reviewerClient,
		},
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("do task")}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	blockReviewer = true

	_, err := runStepLoopInActiveTestRun(t, context.Background(), eng)
	if err == nil {
		t.Fatal("expected runStepLoop to fail when reviewer status persistence fails")
	}
	if blockErr != nil || blocker == nil {
		t.Fatalf("block reviewer persistence: blocker=%v error=%v", blocker, blockErr)
	}
	if err := blocker.Restore(); err != nil {
		t.Fatalf("restore event log: %v", err)
	}

	eventsMu.Lock()
	deferredEvents := append([]Event(nil), events...)
	eventsMu.Unlock()
	assistantEventIdx := -1
	for idx, evt := range deferredEvents {
		if evt.Kind == EventAssistantMessage && messageContent(evt.Message) == "original final" {
			assistantEventIdx = idx
		}
		if evt.Kind == EventReviewerCompleted {
			assistantEventIdx = idx
		}
	}
	if assistantEventIdx < 0 {
		t.Fatalf("expected assistant message event, got %+v", deferredEvents)
	}
	if err == nil {
		t.Fatal("expected reviewer status event-log append failure")
	}

	snapshot := eng.ChatSnapshot()
	if len(snapshot.Entries) != 2 {
		t.Fatalf("expected append failure to leave transcript at persisted assistant entries only, got %+v", snapshot.Entries)
	}
	for _, entry := range snapshot.Entries {
		if entry.Role == string(transcript.EntryRoleReviewerStatus) || entry.Role == string(transcript.EntryRoleReviewerError) {
			t.Fatalf("did not expect in-memory reviewer status after append failure, got %+v", snapshot.Entries)
		}
	}
}

func TestSubmitUserMessageFailsWhenReviewerStatusPersistenceFailsAfterAssistantEvent(t *testing.T) {
	localEntryErr := errors.New("injected reviewer status persistence failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("original final"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("updated final after review"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":["Add final verification notes."]}`)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	var (
		eventsMu        sync.Mutex
		events          []Event
		assistantEvents int
	)
	eng := mustNewTestEngine(t, store, mainClient, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			eventsMu.Lock()
			defer eventsMu.Unlock()
			events = append(events, evt)
			if evt.Kind == EventAssistantMessage {
				assistantEvents++
				if assistantEvents == 2 {
					gate.FailNext(localEntryErr)
				}
			}
		},
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			VerboseOutput: true,
			Client:        reviewerClient,
		},
	})
	_, err := eng.SubmitUserMessage(context.Background(), "do task")
	if err == nil {
		t.Fatal("expected submit to fail when reviewer status persistence fails")
	}

	eventsMu.Lock()
	deferredEvents := append([]Event(nil), events...)
	eventsMu.Unlock()
	foundCompletion := false
	for _, evt := range deferredEvents {
		if evt.Kind == EventReviewerCompleted {
			foundCompletion = true
		}
	}
	if !foundCompletion {
		t.Fatalf("expected Reviewer completion after feedback persistence failure, got %+v", deferredEvents)
	}
	if !errors.Is(err, localEntryErr) {
		t.Fatalf("expected injected reviewer status failure, got %v", err)
	}
	snapshot := eng.ChatSnapshot()
	reviewerFeedback := 0
	for _, entry := range snapshot.Entries {
		if entry.ReviewerFeedback != nil {
			reviewerFeedback++
		}
	}
	if reviewerFeedback != 1 {
		t.Fatalf("committed reviewer feedback was not projected after observer failure: %+v", snapshot.Entries)
	}
	window, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if readErr != nil {
		t.Fatalf("read bounded records: %v", readErr)
	}
	persistedFeedback := 0
	for _, record := range window.Records {
		if _, ok := mustSessionEventPayload(record).(session.ReviewerFeedbackRecord); ok {
			persistedFeedback++
		}
	}
	if persistedFeedback != 1 {
		t.Fatalf("committed reviewer feedback = %d, want one", persistedFeedback)
	}
}

func TestRestoreMessagesKeepsStoredReviewerEntriesVerbatim(t *testing.T) {
	// TODO(KENT-405): delete this compatibility fixture with the legacy reader in 2.7.0.
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "legacy-step", storedLocalEntry{
		Role:          "reviewer_suggestions",
		Text:          "Supervisor suggested:\n1. Add final verification notes.",
		CondensedText: textutil.Value("Supervisor made 1 suggestion."),
	}); err != nil {
		t.Fatalf("append legacy reviewer_suggestions: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, "legacy-step", storedLocalEntry{
		Role: "reviewer_status",
		Text: "Supervisor ran, applied 1 suggestion:\n1. Add final verification notes.",
	}); err != nil {
		t.Fatalf("append legacy reviewer_status: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, "legacy-step", storedLocalEntry{
		Role: "reviewer_error",
		Text: "legacy Reviewer error",
	}); err != nil {
		t.Fatalf("append legacy reviewer_error: %v", err)
	}

	restored := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	snapshot := restored.ChatSnapshot()
	if len(snapshot.Entries) != 3 {
		t.Fatalf("expected 3 restored legacy entries, got %+v", snapshot.Entries)
	}
	if snapshot.Entries[0].Role != "reviewer_suggestions" || snapshot.Entries[0].CondensedText != "Supervisor made 1 suggestion." {
		t.Fatalf("expected stored reviewer_suggestions entry, got %+v", snapshot.Entries[0])
	}
	if snapshot.Entries[1].Role != "reviewer_status" {
		t.Fatalf("expected stored reviewer_status entry, got %+v", snapshot.Entries[1])
	}
	if snapshot.Entries[2].Role != "reviewer_error" || snapshot.Entries[2].Text != "legacy Reviewer error" {
		t.Fatalf("expected stored reviewer_error entry, got %+v", snapshot.Entries[2])
	}
}

func TestRestoreMessagesPreservesStoredLocalEntryNoticeID(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "legacy-step", storedLocalEntry{
		Role:     "system",
		Text:     "Mirrored notice",
		NoticeID: textutil.Value("notice-1"),
	}); err != nil {
		t.Fatalf("append local entry: %v", err)
	}

	restored := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	snapshot := restored.ChatSnapshot()
	if len(snapshot.Entries) != 1 {
		t.Fatalf("expected 1 restored entry, got %+v", snapshot.Entries)
	}
	if snapshot.Entries[0].NoticeID != "notice-1" {
		t.Fatalf("notice id = %q, want notice-1", snapshot.Entries[0].NoticeID)
	}
}

func TestAppendPersistedLocalEntryRejectsInvalidRecords(t *testing.T) {
	blankCallID := " "
	tests := []struct {
		name  string
		entry storedLocalEntry
	}{
		{
			name:  "missing role",
			entry: storedLocalEntry{Text: "feedback"},
		},
		{
			name:  "missing text",
			entry: storedLocalEntry{Role: "system"},
		},
		{
			name: "empty tool attachment",
			entry: storedLocalEntry{
				Role:            "system",
				Text:            "feedback",
				AfterToolCallID: &blankCallID,
			},
		},
	}
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{Model: "gpt-5"})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := eng.steer("step-1", steerLocalEntryIntent(test.entry)); err == nil {
				t.Fatal("invalid local entry persistence succeeded")
			}
			events, err := collectTestEventRecords(store)
			if err != nil {
				t.Fatalf("collect events: %v", err)
			}
			if len(events) != 0 {
				t.Fatalf("invalid local entry persisted events: %+v", events)
			}
			if snapshot := eng.ChatSnapshot(); len(snapshot.Entries) != 0 {
				t.Fatalf("invalid local entry mutated chat: %+v", snapshot.Entries)
			}
		})
	}
}

func TestAppendCommittedEntryWithCondensedTextSkipsBlankEntries(t *testing.T) {
	store := mustCreateTestSession(t)
	var events []Event
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})

	eng.AppendCommittedEntryWithCondensedText("user", "   ", "ignored")
	if len(events) != 0 {
		t.Fatalf("expected blank local entry to emit no events, got %+v", events)
	}
	if snapshot := eng.ChatSnapshot(); len(snapshot.Entries) != 0 {
		t.Fatalf("expected blank local entry to skip chat append, got %+v", snapshot.Entries)
	}
}

func TestRestoreMessagesKeepsStoredToolCallPresentationPayload(t *testing.T) {
	store := mustCreateTestSession(t)
	presentation := transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
		ToolName:       string(toolspec.ToolExecCommand),
		Presentation:   transcript.ToolPresentationShell,
		RenderBehavior: transcript.ToolCallRenderBehaviorShell,
		IsShell:        true,
		Command:        "pwd",
		TimeoutLabel:   "",
	})
	if _, _, err := appendTestEvent(t, store, "legacy-step", llm.Message{
		Role:    llm.RoleAssistant,
		Content: textutil.Value("working"),
		ToolCalls: []llm.ToolCall{{
			ID:           "call_1",
			Name:         string(toolspec.ToolExecCommand),
			Input:        json.RawMessage(`{"command":"pwd"}`),
			Presentation: presentation,
		}},
	}); err != nil {
		t.Fatalf("append assistant tool call message: %v", err)
	}

	restored := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	snapshot := restored.ChatSnapshot()
	var toolEntry *ChatEntry
	for index := range snapshot.Entries {
		if snapshot.Entries[index].Role == "tool_call" &&
			snapshot.Entries[index].ToolCallID == "call_1" {
			toolEntry = &snapshot.Entries[index]
			break
		}
	}
	if toolEntry == nil {
		t.Fatalf("expected restored tool_call entry, got %+v", snapshot.Entries)
	}
	if toolEntry.ToolCall == nil || !toolEntry.ToolCall.IsShell {
		t.Fatalf("expected restored shell tool metadata, got %+v", toolEntry.ToolCall)
	}
	if toolEntry.ToolCall.Command != "pwd" {
		t.Fatalf("expected restored shell command, got %+v", toolEntry.ToolCall)
	}
	if toolEntry.ToolCall.TimeoutLabel != "" {
		t.Fatalf("expected restored timeout label, got %+v", toolEntry.ToolCall)
	}
}

func TestReviewerSuggestionPresentation(t *testing.T) {
	store := mustCreateTestSession(t)
	mainClient := &fakeClient{responses: []llm.Response{
		finalTextResponse("original final"),
		finalTextResponse("updated final after review"),
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value(`{"suggestions":["Add final verification notes."]}`)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	eng := mustNewExecTestEngine(t, store, mainClient, Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			VerboseOutput: true,
			Client:        reviewerClient,
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "do task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "updated final after review" {
		t.Fatalf("assistant content = %q, want updated final after review", messageContent(msg))
	}
	assertReviewerPresentation(t, eng.ChatSnapshot(), 1)

	restored := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
	assertReviewerPresentation(t, restored.ChatSnapshot(), 1)
}

func assertReviewerPresentation(t *testing.T, snapshot ChatSnapshot, wantSuggestions int) {
	t.Helper()
	suggestions := 0
	for _, entry := range snapshot.Entries {
		if entry.ReviewerFeedback != nil {
			suggestions++
		}
	}
	if suggestions != wantSuggestions {
		t.Fatalf("reviewer feedback counts = %d, want %d; entries=%+v", suggestions, wantSuggestions, snapshot.Entries)
	}
}

func TestParseReviewerSuggestionsObjectSupportsStructuredPayload(t *testing.T) {
	contract := mustReviewerSuggestionsContract(t)
	suggestions := parseReviewerSuggestionsObject(contract, `{"suggestions":["one"," two ","one"," ","NO_OP","no_op"]}`)
	if len(suggestions) != 5 || suggestions[0] != "one" || suggestions[1] != " two " || suggestions[2] != "one" || suggestions[3] != "NO_OP" || suggestions[4] != "no_op" {
		t.Fatalf("unexpected suggestions from object payload: %+v", suggestions)
	}

	suggestions = parseReviewerSuggestionsObject(contract, `[" ","NO_OP"]`)
	if len(suggestions) != 0 {
		t.Fatalf("expected invalid non-object payload to be ignored, got %+v", suggestions)
	}

	suggestions = parseReviewerSuggestionsObject(contract, "")
	if len(suggestions) != 0 {
		t.Fatalf("expected empty payload to be ignored, got %+v", suggestions)
	}

	suggestions = parseReviewerSuggestionsObject(contract, `not-json`)
	if len(suggestions) != 0 {
		t.Fatalf("expected invalid payload to be ignored, got %+v", suggestions)
	}
}

func TestParseReviewerSuggestionsObjectPreservesAcceptedMarkdownBytes(t *testing.T) {
	const first = "  - keep indentation  \n    ```go\n    fmt.Println(\"x\")\n    ```  "
	const second = "\n> quoted feedback\n"
	payload, err := json.Marshal(struct {
		Suggestions []string `json:"suggestions"`
	}{Suggestions: []string{first, " ", "NO_OP", second}})
	if err != nil {
		t.Fatalf("marshal Reviewer payload: %v", err)
	}
	suggestions := parseReviewerSuggestionsObject(mustReviewerSuggestionsContract(t), string(payload))
	if len(suggestions) != 3 || suggestions[0] != first || suggestions[1] != "NO_OP" || suggestions[2] != second {
		t.Fatalf("suggestions lost exact Markdown bytes: %#v", suggestions)
	}
	instruction := formatReviewerDeveloperInstruction(suggestions)
	if !strings.Contains(instruction, first) || !strings.Contains(instruction, second) {
		t.Fatalf("developer instruction lost exact Markdown bytes: %q", instruction)
	}
}

func TestBuildReviewerTranscriptMessagesIncludesConversationAndToolCalls(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseCommentary), Content: textutil.Value("I’ll inspect quickly.")},
		{Role: llm.RoleUser, Content: textutil.Value("user request")},
		{Role: llm.RoleAssistant, Content: textutil.Value("Running command now."), Phase: textutil.Value(llm.MessagePhaseCommentary), ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "exec_command", Input: json.RawMessage(`{"command":"pwd"}`)}}},
		{Role: llm.RoleAssistant, Content: textutil.Value("assistant response"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		{Role: llm.RoleTool, Name: textutil.Value("exec_command"), ToolCallID: textutil.Value("call_1"), Content: textutil.Value("{\"output\":\"ok\"}")},
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment), Content: textutil.Value(environmentInjectedHeader + "\nOS: darwin")},
	}

	reviewerMessages := buildReviewerTranscriptMessages(messages)
	if len(reviewerMessages) != 6 {
		t.Fatalf("expected 6 reviewer transcript messages after filtering, got %d", len(reviewerMessages))
	}
	if reviewerMessages[0].Role != llm.RoleUser {
		t.Fatalf("expected reviewer transcript messages to use user role, got %q", reviewerMessages[0].Role)
	}
	if !strings.Contains(messageContent(reviewerMessages[0]), "I’ll inspect quickly.") {
		t.Fatalf("expected short commentary preamble to be preserved, message=%q", messageContent(reviewerMessages[0]))
	}
	if !strings.Contains(messageContent(reviewerMessages[2]), "Running command now.") {
		t.Fatalf("expected short commentary preamble text to be preserved when tool calls exist, message=%q", messageContent(reviewerMessages[2]))
	}
	if !strings.Contains(messageContent(reviewerMessages[3]), "Tool call:") || !strings.Contains(messageContent(reviewerMessages[3]), "pwd") {
		t.Fatalf("expected separate tool call transcript entry, message=%q", messageContent(reviewerMessages[3]))
	}
	if strings.Contains(messageContent(reviewerMessages[3]), "(id=") {
		t.Fatalf("did not expect tool call id in reviewer transcript, message=%q", messageContent(reviewerMessages[3]))
	}
	if !strings.Contains(messageContent(reviewerMessages[4]), "Agent:") {
		t.Fatalf("expected assistant final answer entry to use agent label, message=%q", messageContent(reviewerMessages[4]))
	}
	if !strings.Contains(messageContent(reviewerMessages[5]), "Tool result:") || !strings.Contains(messageContent(reviewerMessages[5]), "ok") {
		t.Fatalf("expected separate tool result transcript entry, message=%q", messageContent(reviewerMessages[5]))
	}
}

func TestReviewerStatusTextIncludesReviewerCacheHitMetadata(t *testing.T) {
	text := reviewerStatusText(ReviewerStatus{
		Outcome:               "applied",
		SuggestionsCount:      2,
		CacheHitPercent:       85,
		HasCacheHitPercentage: true,
	}, []string{"one", "two"})
	if strings.Contains(text, "Supervisor suggested:") || strings.Contains(text, "1. one") {
		t.Fatalf("expected reviewer status text to stay concise even when suggestions are provided, got %q", text)
	}
	if !strings.Contains(text, "85% cache hit") {
		t.Fatalf("expected reviewer cache hit metadata in reviewer status text, got %q", text)
	}

	text = reviewerStatusText(ReviewerStatus{
		Outcome:               "applied",
		SuggestionsCount:      2,
		CacheHitPercent:       85,
		HasCacheHitPercentage: true,
	}, nil)
	if !strings.Contains(text, "85% cache hit") {
		t.Fatalf("expected reviewer cache hit metadata even without suggestions, got %q", text)
	}

	text = reviewerStatusText(ReviewerStatus{
		Outcome:          "followup_failed",
		SuggestionsCount: 2,
		Error:            "tool crashed",
	}, []string{"one", "two"})
	if text != "Supervisor ran: 2 suggestions, but follow-up failed: tool crashed" {
		t.Fatalf("expected concise follow-up failure status, got %q", text)
	}
}

func TestReviewerStatusEntryRoleMarksErrors(t *testing.T) {
	cases := []struct {
		outcome string
		want    string
	}{
		{outcome: "failed", want: string(transcript.EntryRoleReviewerError)},
		{outcome: "followup_failed", want: string(transcript.EntryRoleReviewerError)},
		{outcome: "applied", want: string(transcript.EntryRoleReviewerStatus)},
		{outcome: "no_suggestions", want: string(transcript.EntryRoleReviewerStatus)},
	}

	for _, tt := range cases {
		t.Run(tt.outcome, func(t *testing.T) {
			if got := reviewerStatusEntryRole(ReviewerStatus{Outcome: tt.outcome}); got != tt.want {
				t.Fatalf("reviewerStatusEntryRole = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildReviewerTranscriptMessagesIncludesSupervisorControlDeveloperMessage(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleDeveloper, Content: textutil.Value("Supervisor agent gave you suggestions:\n1. run tests")},
	}

	reviewerMessages := buildReviewerTranscriptMessages(messages)
	if len(reviewerMessages) != 1 {
		t.Fatalf("expected one reviewer message, got %d", len(reviewerMessages))
	}
	if !strings.Contains(messageContent(reviewerMessages[0]), "Supervisor agent gave you suggestions:") {
		t.Fatalf("expected supervisor control message to be included, got %q", messageContent(reviewerMessages[0]))
	}
	if !strings.Contains(messageContent(reviewerMessages[0]), "Developer context:") {
		t.Fatalf("expected developer-context label in reviewer message, got %q", messageContent(reviewerMessages[0]))
	}
}
