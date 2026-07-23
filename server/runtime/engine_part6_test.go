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

func TestReviewerCompletedEventReflectsPersistedReviewerStatusStateWithoutTranscriptAdvance(t *testing.T) {
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
		eng                        *Engine
	)
	eng = mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
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
	assistantIndex := len(snapshotAtCompletion.Entries) - 2
	suggestionsIndex := -1
	for index, entry := range snapshotAtCompletion.Entries[:assistantIndex] {
		if entry.Role == "reviewer_suggestions" {
			suggestionsIndex = index
		}
	}
	if suggestionsIndex < 0 {
		t.Fatalf("expected reviewer suggestions before follow-up assistant, got %+v", snapshotAtCompletion.Entries)
	}
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
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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

	result, err := eng.runReviewerFollowUp(context.Background(), "step-1", llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("original final")}, -1, false, reviewerClient)
	if err != nil {
		t.Fatalf("run reviewer follow-up: %v", err)
	}
	if messageContent(result.Message) != "original final" {
		t.Fatalf("follow-up result message = %q, want original final", messageContent(result.Message))
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
	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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

	_, err := eng.runStepLoop(context.Background(), "step-1")
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
			t.Fatalf("did not expect reviewer completed event after reviewer status persistence failure, got %+v", deferredEvents)
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
	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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
	for _, evt := range deferredEvents {
		if evt.Kind == EventReviewerCompleted {
			t.Fatalf("did not expect reviewer completed event after reviewer status persistence failure, got %+v", deferredEvents)
		}
	}
	if !errors.Is(err, localEntryErr) {
		t.Fatalf("expected injected reviewer status failure, got %v", err)
	}
	snapshot := eng.ChatSnapshot()
	reviewerStatuses := 0
	for _, entry := range snapshot.Entries {
		if entry.Role == string(transcript.EntryRoleReviewerStatus) {
			reviewerStatuses++
		}
	}
	if reviewerStatuses != 1 {
		t.Fatalf("committed reviewer status was not projected after observer failure: %+v", snapshot.Entries)
	}
}

func TestRestoreMessagesKeepsStoredReviewerEntriesVerbatim(t *testing.T) {
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
	if _, _, err := appendTestEvent(t, store, "legacy-step", storedLocalEntry{
		Role:     "system",
		Text:     "Mirrored notice",
		NoticeID: textutil.Value("notice-1"),
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
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

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

func TestReviewerSuggestionPresentation(t *testing.T) {
	tests := []struct {
		name            string
		verbose         bool
		wantSuggestions int
	}{
		{name: "default"},
		{name: "verbose", verbose: true, wantSuggestions: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
					VerboseOutput: tt.verbose,
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
			assertReviewerPresentation(t, eng.ChatSnapshot(), tt.wantSuggestions)

			restored := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
			assertReviewerPresentation(t, restored.ChatSnapshot(), tt.wantSuggestions)
		})
	}
}

func assertReviewerPresentation(t *testing.T, snapshot ChatSnapshot, wantSuggestions int) {
	t.Helper()
	suggestions := 0
	statuses := 0
	suggestionsIndex := -1
	statusIndex := -1
	for index, entry := range snapshot.Entries {
		switch transcript.EntryRole(entry.Role) {
		case transcript.EntryRoleReviewerSuggestions:
			suggestions++
			suggestionsIndex = index
			if strings.TrimSpace(entry.CondensedText) == "" {
				t.Fatalf("reviewer suggestions condensed text is blank: %+v", entry)
			}
		case transcript.EntryRoleReviewerStatus:
			statuses++
			statusIndex = index
		}
	}
	if suggestions != wantSuggestions || statuses != 1 {
		t.Fatalf("reviewer suggestions/status counts = %d/%d, want %d/1; entries=%+v", suggestions, statuses, wantSuggestions, snapshot.Entries)
	}
	if suggestionsIndex >= 0 && suggestionsIndex >= statusIndex {
		t.Fatalf("reviewer suggestions/status indexes = %d/%d, want suggestions before status", suggestionsIndex, statusIndex)
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
