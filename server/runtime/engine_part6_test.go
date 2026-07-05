package runtime

import (
	"context"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReviewerCompletedEventReflectsPersistedReviewerStatusStateWithoutTranscriptAdvance(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "original final", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "updated final after review", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":["Add final verification notes."]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	var (
		eventsMu                   sync.Mutex
		assistantEvent             *Event
		assistantEventCount        int
		reviewerCompletedEvent     *Event
		snapshotAtReviewerComplete ChatSnapshot
		eng                        *Engine
	)
	eng = mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.Kind == EventAssistantMessage && evt.Message.Content == "updated final after review" {
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
	if msg.Content != "updated final after review" {
		t.Fatalf("assistant content = %q, want updated final after review", msg.Content)
	}

	eventsMu.Lock()
	assistant := assistantEvent
	assistantCount := assistantEventCount
	completed := reviewerCompletedEvent
	snapshotAtCompletion := snapshotAtReviewerComplete
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
	if completed.CommittedTranscriptChanged {
		t.Fatalf("expected reviewer completed event to avoid committed transcript advancement, got %+v", *completed)
	}
	if len(snapshotAtCompletion.Entries) < 2 {
		t.Fatalf("expected follow-up assistant and reviewer status in completion snapshot, got %+v", snapshotAtCompletion.Entries)
	}
	assistantEntry := snapshotAtCompletion.Entries[len(snapshotAtCompletion.Entries)-2]
	if assistantEntry.Role != "assistant" || assistantEntry.Text != "updated final after review" {
		t.Fatalf("expected completion snapshot penultimate entry to be follow-up assistant, got %+v", assistantEntry)
	}
	if !assistant.CommittedEntryStartSet {
		t.Fatalf("expected follow-up assistant event committed start metadata, got %+v", *assistant)
	}
	if got, want := assistant.CommittedEntryStart, len(snapshotAtCompletion.Entries)-2; got != want {
		t.Fatalf("follow-up assistant committed start = %d, want %d; snapshot=%+v", got, want, snapshotAtCompletion.Entries)
	}
	statusEntry := snapshotAtCompletion.Entries[len(snapshotAtCompletion.Entries)-1]
	if statusEntry.Role != "reviewer_status" || statusEntry.Text != "Supervisor ran: 1 suggestion, applied." {
		t.Fatalf("expected completion snapshot to end with reviewer status, got %+v", statusEntry)
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
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	if err := eng.steer("step-1", steerLocalEntryIntent(storedLocalEntry{Visibility: transcript.EntryVisibilityAuto, Role: "reviewer_suggestions", Text: "Supervisor suggested:\n1. Add verification notes.", CondensedText: "Supervisor made 1 suggestion."})); err != nil {
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
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:    "gpt-5",
		Reviewer: ReviewerConfig{Model: "gpt-5"},
	})
	if err := eng.steer("prep-1", steerMessagesWithPersistenceIntent(steeringPriorityUser, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "first request"}})); err != nil {
		t.Fatalf("append first message: %v", err)
	}

	reviewerClient := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true}, responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":["Add final verification notes."]}`},
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

	result, err := eng.runReviewerFollowUp(context.Background(), "step-1", llm.Message{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "original final"}, -1, false, reviewerClient)
	if err != nil {
		t.Fatalf("run reviewer follow-up: %v", err)
	}
	if result.Message.Content != "original final" {
		t.Fatalf("follow-up result message = %q, want original final", result.Message.Content)
	}
	if result.Completion == nil {
		t.Fatal("expected reviewer completion after follow-up append failure")
	}
	if result.Completion.Outcome != "followup_failed" {
		t.Fatalf("reviewer completion outcome = %q, want followup_failed", result.Completion.Outcome)
	}
	if result.Completion.SuggestionsCount != 1 {
		t.Fatalf("reviewer completion suggestions = %d, want 1", result.Completion.SuggestionsCount)
	}
	if strings.TrimSpace(result.Completion.Error) == "" {
		t.Fatal("expected reviewer completion to include append failure error")
	}
}

func TestRunStepLoopFailsWhenReviewerStatusPersistenceFailsAfterReviewerInstructionAppendFailure(t *testing.T) {
	reviewerInstructionErr := errors.New("injected reviewer instruction persistence failure")
	localEntryErr := errors.New("injected reviewer status persistence failure")
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "original final", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	reviewerClient := &fakeClient{caps: llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true}, responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":["Add final verification notes."]}`},
		Usage:     llm.Usage{InputTokens: 10, WindowTokens: 200000},
	}}}

	var (
		eventsMu sync.Mutex
		events   []Event
	)
	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		AutoCompactTokenLimit: 1_000_000,
		OnEvent: func(evt Event) {
			eventsMu.Lock()
			defer eventsMu.Unlock()
			events = append(events, evt)
		},
		Reviewer: ReviewerConfig{
			Frequency: "all",
			Model:     "gpt-5",
			Client:    reviewerClient,
		},
	})
	eng.beforePersistMessage = func(msg llm.Message) error {
		if msg.MessageType == llm.MessageTypeReviewerFeedback {
			return reviewerInstructionErr
		}
		return nil
	}
	eng.beforePersistLocalEntry = func(entry storedLocalEntry) error {
		if entry.Role == "reviewer_status" {
			return localEntryErr
		}
		return nil
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "do task"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	_, err := eng.runStepLoop(context.Background(), "step-1")
	if err == nil {
		t.Fatal("expected runStepLoop to fail when reviewer status persistence fails")
	}

	eventsMu.Lock()
	deferredEvents := append([]Event(nil), events...)
	eventsMu.Unlock()
	assistantEventIdx := -1
	for idx, evt := range deferredEvents {
		if evt.Kind == EventAssistantMessage && evt.Message.Content == "original final" {
			assistantEventIdx = idx
		}
		if evt.Kind == EventReviewerCompleted {
			t.Fatalf("did not expect reviewer completed event after reviewer status persistence failure, got %+v", deferredEvents)
		}
	}
	if assistantEventIdx < 0 {
		t.Fatalf("expected assistant message event, got %+v", deferredEvents)
	}
	if !errors.Is(err, localEntryErr) {
		t.Fatalf("expected injected reviewer status failure, got %v", err)
	}

	snapshot := eng.ChatSnapshot()
	if len(snapshot.Entries) != 2 {
		t.Fatalf("expected append failure to leave transcript at persisted assistant entries only, got %+v", snapshot.Entries)
	}
	for _, entry := range snapshot.Entries {
		if entry.Role == "reviewer_status" {
			t.Fatalf("did not expect in-memory reviewer status after append failure, got %+v", snapshot.Entries)
		}
	}
}

func TestSubmitUserMessageFailsWhenReviewerStatusPersistenceFailsAfterAssistantEvent(t *testing.T) {
	localEntryErr := errors.New("injected reviewer status persistence failure")
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "original final", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "updated final after review", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":["Add final verification notes."]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	var (
		eventsMu sync.Mutex
		events   []Event
	)
	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			eventsMu.Lock()
			defer eventsMu.Unlock()
			events = append(events, evt)
		},
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			VerboseOutput: true,
			Client:        reviewerClient,
		},
	})
	eng.beforePersistLocalEntry = func(entry storedLocalEntry) error {
		if entry.Role == "reviewer_status" {
			return localEntryErr
		}
		return nil
	}

	_, err := eng.SubmitUserMessage(context.Background(), "do task")
	if err == nil {
		t.Fatal("expected submit to fail when reviewer status persistence fails")
	}

	eventsMu.Lock()
	deferredEvents := append([]Event(nil), events...)
	eventsMu.Unlock()
	for _, evt := range deferredEvents {
		if evt.Kind == EventReviewerCompleted {
			t.Fatalf("did not expect reviewer completed event after reviewer status persistence failure, got %+v", deferredEvents)
		}
	}
	if !errors.Is(err, localEntryErr) {
		t.Fatalf("expected injected reviewer status failure, got %v", err)
	}
}

func TestRestoreMessagesKeepsStoredReviewerEntriesVerbatim(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := store.AppendEvent("legacy-step", "local_entry", storedLocalEntry{
		Role:          "reviewer_suggestions",
		Text:          "Supervisor suggested:\n1. Add final verification notes.",
		CondensedText: "Supervisor made 1 suggestion.",
	}); err != nil {
		t.Fatalf("append legacy reviewer_suggestions: %v", err)
	}
	if _, _, err := store.AppendEvent("legacy-step", "local_entry", storedLocalEntry{
		Role: "reviewer_status",
		Text: "Supervisor ran, applied 1 suggestion:\n1. Add final verification notes.",
	}); err != nil {
		t.Fatalf("append legacy reviewer_status: %v", err)
	}

	restored := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	snapshot := restored.ChatSnapshot()
	if len(snapshot.Entries) != 2 {
		t.Fatalf("expected 2 restored entries, got %+v", snapshot.Entries)
	}
	if snapshot.Entries[0].Role != "reviewer_suggestions" || snapshot.Entries[0].CondensedText != "Supervisor made 1 suggestion." {
		t.Fatalf("expected stored reviewer_suggestions entry, got %+v", snapshot.Entries[0])
	}
	if snapshot.Entries[1].Role != "reviewer_status" || snapshot.Entries[1].Text != "Supervisor ran, applied 1 suggestion:\n1. Add final verification notes." {
		t.Fatalf("expected stored reviewer_status entry, got %+v", snapshot.Entries[1])
	}
}

func TestRestoreMessagesPreservesStoredLocalEntryNoticeID(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := store.AppendEvent("legacy-step", "local_entry", storedLocalEntry{
		Role:     "system",
		Text:     "Mirrored notice",
		NoticeID: "notice-1",
	}); err != nil {
		t.Fatalf("append local entry: %v", err)
	}

	restored := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	snapshot := restored.ChatSnapshot()
	if len(snapshot.Entries) != 1 {
		t.Fatalf("expected 1 restored entry, got %+v", snapshot.Entries)
	}
	if snapshot.Entries[0].NoticeID != "notice-1" {
		t.Fatalf("notice id = %q, want notice-1", snapshot.Entries[0].NoticeID)
	}
}

func TestAppendCommittedEntryRecordDoesNotMutateChatOnAppendFailure(t *testing.T) {
	localEntryErr := errors.New("injected local entry persistence failure")
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	eng.beforePersistLocalEntry = func(entry storedLocalEntry) error {
		return localEntryErr
	}

	err := eng.steer("step-1", steerLocalEntryIntent(storedLocalEntry{
		Visibility: transcript.EntryVisibilityOngoing,
		Role:       "reviewer_status",
		Text:       "Supervisor ran, applied 1 suggestion.",
	}))

	if !errors.Is(err, localEntryErr) {
		t.Fatalf("expected injected local entry failure, got %v", err)
	}
	if snapshot := eng.ChatSnapshot(); len(snapshot.Entries) != 0 {
		t.Fatalf("expected no in-memory local entries after append failure, got %+v", snapshot.Entries)
	}
}

func TestAppendCommittedEntryWithCondensedTextSkipsBlankEntries(t *testing.T) {
	store := mustCreateTestSession(t)
	var events []Event
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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
	if _, _, err := store.AppendEvent("legacy-step", "message", llm.Message{
		Role:    llm.RoleAssistant,
		Content: "working",
		ToolCalls: []llm.ToolCall{{
			ID:           "call_1",
			Name:         string(toolspec.ToolExecCommand),
			Input:        json.RawMessage(`{"command":"pwd"}`),
			Presentation: presentation,
		}},
	}); err != nil {
		t.Fatalf("append assistant tool call message: %v", err)
	}

	restored := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	snapshot := restored.ChatSnapshot()
	if len(snapshot.Entries) != 2 {
		t.Fatalf("expected assistant and tool call entries, got %+v", snapshot.Entries)
	}
	toolEntry := snapshot.Entries[1]
	if toolEntry.Role != "tool_call" {
		t.Fatalf("expected tool_call entry, got %+v", toolEntry)
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

func TestRestoreMessagesIgnoresLegacyReviewerRollbackHistoryReplacement(t *testing.T) {
	store := mustCreateTestSession(t)
	presentation := transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
		ToolName:       string(toolspec.ToolExecCommand),
		Presentation:   transcript.ToolPresentationShell,
		RenderBehavior: transcript.ToolCallRenderBehaviorShell,
		IsShell:        true,
		Command:        "pwd",
	})
	legacyItems := []llm.ResponseItem{
		{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "before"},
		{
			Type:             llm.ResponseItemTypeFunctionCall,
			CallID:           "call_1",
			Name:             string(toolspec.ToolExecCommand),
			ToolPresentation: presentation,
			Arguments:        json.RawMessage(`{"command":"pwd"}`),
		},
	}
	if _, _, err := store.AppendEvent("legacy-step", "history_replaced", historyReplacementPayload{
		Engine: "reviewer_rollback",
		Mode:   "manual",
		Items:  legacyItems,
	}); err != nil {
		t.Fatalf("append history replacement: %v", err)
	}

	type restoreResult struct {
		engine *Engine
		err    error
	}
	resultCh := make(chan restoreResult, 1)
	go func() {
		restored, err := New(store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
		resultCh <- restoreResult{engine: restored, err: err}
	}()
	var restored *Engine
	select {
	case result := <-resultCh:
		restored = result.engine
		if result.err != nil {
			t.Fatalf("restore engine: %v", result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restore engine timed out while ignoring legacy reviewer_rollback history replacement")
	}
	items := restored.transcriptRuntimeState().SnapshotItems()
	if len(items) != 0 {
		t.Fatalf("expected legacy reviewer rollback replacement to be ignored, got %+v", items)
	}
	snapshot := restored.ChatSnapshot()
	if len(snapshot.Entries) != 0 {
		t.Fatalf("expected ignored legacy reviewer rollback to produce no transcript entries, got %+v", snapshot.Entries)
	}
}

func TestRestoreMessagesFailsOnMalformedHistoryReplacementPayload(t *testing.T) {
	t.Run("non-legacy payload still fails", func(t *testing.T) {
		store := mustCreateTestSession(t)
		if _, err := store.AppendReplayEvents([]session.ReplayEvent{{
			StepID:  "legacy-step",
			Kind:    "history_replaced",
			Payload: json.RawMessage(`{"engine":"local","items":"not-an-array"}`),
		}}); err != nil {
			t.Fatalf("append malformed replay event: %v", err)
		}

		if _, err := New(store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"}); !errors.Is(err, errDecodeHistoryReplacedEvent) {
			t.Fatalf("expected errDecodeHistoryReplacedEvent, got %v", err)
		}
	})

	t.Run("legacy reviewer rollback payload is ignored", func(t *testing.T) {
		store := mustCreateTestSession(t)
		if _, err := store.AppendReplayEvents([]session.ReplayEvent{{
			StepID:  "legacy-step",
			Kind:    "history_replaced",
			Payload: json.RawMessage(`{"engine":"reviewer_rollback","items":"not-an-array"}`),
		}}); err != nil {
			t.Fatalf("append malformed replay event: %v", err)
		}

		if _, err := New(store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"}); err != nil {
			t.Fatalf("expected malformed legacy reviewer rollback payload to be ignored, got %v", err)
		}
	})
}

func TestReviewerDefaultOutputOmitsReviewerSuggestionsEntry(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "original final"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, {
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "updated final after review"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":["Add final verification notes."]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "do task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "updated final after review" {
		t.Fatalf("assistant content = %q, want updated final after review", msg.Content)
	}

	snapshot := eng.ChatSnapshot()
	for _, entry := range snapshot.Entries {
		if entry.Role == "reviewer_suggestions" {
			t.Fatalf("expected reviewer_suggestions entry to be omitted by default, got %+v", snapshot.Entries)
		}
		if entry.Role == "reviewer_status" && strings.Contains(entry.Text, "Supervisor suggested:") {
			t.Fatalf("expected concise reviewer status by default, got %+v", entry)
		}
	}
}

func TestReviewerVerboseOutputShowsSuggestionsWhenIssuedAndKeepsFinalStatusConcise(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "original final"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}, {
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "updated final after review"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":["Add final verification notes."]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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
	if msg.Content != "updated final after review" {
		t.Fatalf("assistant content = %q, want updated final after review", msg.Content)
	}

	snapshot := eng.ChatSnapshot()
	foundVerboseSuggestions := false
	foundConciseStatus := false
	wantSuggestionsCondensedText := "Supervisor suggested:\n1. Add final verification notes."
	for _, entry := range snapshot.Entries {
		if entry.Role == "reviewer_suggestions" && entry.CondensedText == wantSuggestionsCondensedText {
			foundVerboseSuggestions = true
		}
		if entry.Role == "reviewer_status" && entry.Text == "Supervisor ran: 1 suggestion, applied." {
			foundConciseStatus = true
		}
	}
	if !foundVerboseSuggestions {
		t.Fatalf("expected verbose reviewer suggestions entry in snapshot, got %+v", snapshot.Entries)
	}
	if !foundConciseStatus {
		t.Fatalf("expected concise reviewer status entry in snapshot, got %+v", snapshot.Entries)
	}

	restored := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	restoredSnapshot := restored.ChatSnapshot()
	foundRestoredVerboseSuggestions := false
	foundRestoredConciseStatus := false
	for _, entry := range restoredSnapshot.Entries {
		if entry.Role == "reviewer_suggestions" && entry.CondensedText == wantSuggestionsCondensedText {
			foundRestoredVerboseSuggestions = true
		}
		if entry.Role == "reviewer_status" && entry.Text == "Supervisor ran: 1 suggestion, applied." {
			foundRestoredConciseStatus = true
		}
	}
	if !foundRestoredVerboseSuggestions {
		t.Fatalf("expected restored verbose reviewer suggestions entry, got %+v", restoredSnapshot.Entries)
	}
	if !foundRestoredConciseStatus {
		t.Fatalf("expected restored concise reviewer status entry, got %+v", restoredSnapshot.Entries)
	}
}

func TestParseReviewerSuggestionsObjectSupportsStructuredPayload(t *testing.T) {
	suggestions := parseReviewerSuggestionsObject(`{"suggestions":["one"," two ","one"," ","NO_OP","no_op"]}`)
	if len(suggestions) != 3 || suggestions[0] != "one" || suggestions[1] != "two" || suggestions[2] != "one" {
		t.Fatalf("unexpected suggestions from object payload: %+v", suggestions)
	}

	suggestions = parseReviewerSuggestionsObject(`[" ","NO_OP"]`)
	if len(suggestions) != 0 {
		t.Fatalf("expected invalid non-object payload to be ignored, got %+v", suggestions)
	}

	suggestions = parseReviewerSuggestionsObject("")
	if len(suggestions) != 0 {
		t.Fatalf("expected empty payload to be ignored, got %+v", suggestions)
	}

	suggestions = parseReviewerSuggestionsObject(`not-json`)
	if len(suggestions) != 0 {
		t.Fatalf("expected invalid payload to be ignored, got %+v", suggestions)
	}
}

func TestBuildReviewerTranscriptMessagesIncludesConversationAndToolCalls(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleAssistant, Phase: llm.MessagePhaseCommentary, Content: "I’ll inspect quickly."},
		{Role: llm.RoleUser, Content: "user request"},
		{Role: llm.RoleAssistant, Content: "Running command now.", Phase: llm.MessagePhaseCommentary, ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "exec_command", Input: json.RawMessage(`{"command":"pwd"}`)}}},
		{Role: llm.RoleAssistant, Content: "assistant response", Phase: llm.MessagePhaseFinal},
		{Role: llm.RoleTool, Name: "exec_command", ToolCallID: "call_1", Content: "{\"output\":\"ok\"}"},
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeEnvironment, Content: environmentInjectedHeader + "\nOS: darwin"},
	}

	reviewerMessages := buildReviewerTranscriptMessages(messages)
	if len(reviewerMessages) != 6 {
		t.Fatalf("expected 6 reviewer transcript messages after filtering, got %d", len(reviewerMessages))
	}
	if reviewerMessages[0].Role != llm.RoleUser {
		t.Fatalf("expected reviewer transcript messages to use user role, got %q", reviewerMessages[0].Role)
	}
	if !strings.Contains(reviewerMessages[0].Content, "I’ll inspect quickly.") {
		t.Fatalf("expected short commentary preamble to be preserved, message=%q", reviewerMessages[0].Content)
	}
	if !strings.Contains(reviewerMessages[2].Content, "Running command now.") {
		t.Fatalf("expected short commentary preamble text to be preserved when tool calls exist, message=%q", reviewerMessages[2].Content)
	}
	if !strings.Contains(reviewerMessages[3].Content, "Tool call:") || !strings.Contains(reviewerMessages[3].Content, "pwd") {
		t.Fatalf("expected separate tool call transcript entry, message=%q", reviewerMessages[3].Content)
	}
	if strings.Contains(reviewerMessages[3].Content, "(id=") {
		t.Fatalf("did not expect tool call id in reviewer transcript, message=%q", reviewerMessages[3].Content)
	}
	if !strings.Contains(reviewerMessages[4].Content, "Agent:") {
		t.Fatalf("expected assistant final answer entry to use agent label, message=%q", reviewerMessages[4].Content)
	}
	if !strings.Contains(reviewerMessages[5].Content, "Tool result:") || !strings.Contains(reviewerMessages[5].Content, "ok") {
		t.Fatalf("expected separate tool result transcript entry, message=%q", reviewerMessages[5].Content)
	}
}

func TestBuildReviewerTranscriptMessagesKeepsOrphanToolOutputEntry(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleTool, Name: "exec_command", ToolCallID: "orphan_call", Content: "{\"output\":\"orphan\"}"},
	}

	reviewerMessages := buildReviewerTranscriptMessages(messages)
	if len(reviewerMessages) != 1 {
		t.Fatalf("expected one reviewer message for orphan tool output, got %d", len(reviewerMessages))
	}
	if !strings.Contains(reviewerMessages[0].Content, "Tool result:") || !strings.Contains(reviewerMessages[0].Content, "orphan") {
		t.Fatalf("expected orphan tool output to remain as tool entry, message=%q", reviewerMessages[0].Content)
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

func TestBuildReviewerTranscriptMessagesIncludesSupervisorControlDeveloperMessage(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleDeveloper, Content: "Supervisor agent gave you suggestions:\n1. run tests"},
	}

	reviewerMessages := buildReviewerTranscriptMessages(messages)
	if len(reviewerMessages) != 1 {
		t.Fatalf("expected one reviewer message, got %d", len(reviewerMessages))
	}
	if !strings.Contains(reviewerMessages[0].Content, "Supervisor agent gave you suggestions:") {
		t.Fatalf("expected supervisor control message to be included, got %q", reviewerMessages[0].Content)
	}
	if !strings.Contains(reviewerMessages[0].Content, "Developer context:") {
		t.Fatalf("expected developer-context label in reviewer message, got %q", reviewerMessages[0].Content)
	}
}

func TestAppendMissingReviewerMetaContextPrependsAgentsAndEnvironmentWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	globalDir := filepath.Join(home, agentsGlobalDirName)
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global agents dir: %v", err)
	}
	globalPath := filepath.Join(globalDir, agentsFileName)
	if err := os.WriteFile(globalPath, []byte("global rule"), 0o644); err != nil {
		t.Fatalf("write global AGENTS: %v", err)
	}

	workspace := t.TempDir()
	workspacePath := filepath.Join(workspace, agentsFileName)
	if err := os.WriteFile(workspacePath, []byte("workspace rule"), 0o644); err != nil {
		t.Fatalf("write workspace AGENTS: %v", err)
	}

	in := []llm.Message{{Role: llm.RoleUser, Content: "request"}}
	got, err := appendMissingReviewerMetaContext(in, workspace, "gpt-5", "high", "", false, nil)
	if err != nil {
		t.Fatalf("appendMissingReviewerMetaContext: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 2 prepended agents + 1 environment message plus original, got %d", len(got))
	}
	if got[0].Role != llm.RoleDeveloper || got[0].MessageType != llm.MessageTypeEnvironment || !strings.Contains(got[0].Content, environmentInjectedHeader) {
		t.Fatalf("expected prepended environment developer message first, got %+v", got[0])
	}
	if got[1].Role != llm.RoleDeveloper || got[1].MessageType != llm.MessageTypeAgentsMD || !strings.Contains(got[1].Content, "source: "+globalPath) {
		t.Fatalf("expected global AGENTS developer message after environment, got %+v", got[1])
	}
	if got[2].Role != llm.RoleDeveloper || got[2].MessageType != llm.MessageTypeAgentsMD || !strings.Contains(got[2].Content, "source: "+workspacePath) {
		t.Fatalf("expected workspace AGENTS developer message last in base context, got %+v", got[2])
	}
	if got[3].Role != llm.RoleUser || got[3].Content != "request" {
		t.Fatalf("expected original message at tail, got %+v", got[3])
	}
}
