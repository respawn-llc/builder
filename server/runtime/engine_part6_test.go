package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestAppendCommittedEntryEmitsRealtimeLocalEntryEvent(t *testing.T) {
	store := mustCreateTestSession(t)
	var events []Event
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})

	restoreStep := setTestActiveStep(eng, "step-1")
	defer restoreStep()
	entry := storedLocalEntry{
		Visibility:    transcript.EntryVisibilityAuto,
		Role:          "reviewer_suggestions",
		Text:          "Supervisor suggested:\n1. Add verification notes.",
		CondensedText: textutil.Value("Supervisor made 1 suggestion."),
	}
	if err := eng.steer(runtimeTestStepID("step-1"), steerLocalEntryIntent(entry)); err != nil {
		t.Fatalf("append persisted local entry: %v", err)
	}
	if len(events) != 1 || events[0].Kind != EventLocalEntryAdded || events[0].LocalEntry == nil {
		t.Fatalf("local-entry events = %+v, want one typed event", events)
	}
	if got := events[0].LocalEntry; got.Role != entry.Role || got.CondensedText != *entry.CondensedText {
		t.Fatalf("local-entry payload = %+v, want role and condensed text preserved", got)
	}
}

func TestRestoreMessagesPreservesStoredLocalEntryNoticeID(t *testing.T) {
	store := mustCreateTestSession(t)
	if _, _, err := appendTestEvent(t, store, "step-1", storedLocalEntry{
		Role:     "system",
		Text:     "Mirrored notice",
		NoticeID: textutil.Value("notice-1"),
	}); err != nil {
		t.Fatalf("append local entry: %v", err)
	}

	restored := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{Model: "gpt-5"})
	snapshot := restored.ChatSnapshot()
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].NoticeID != "notice-1" {
		t.Fatalf("restored notice entry = %+v, want notice-1", snapshot.Entries)
	}
}

func TestAppendPersistedLocalEntryRejectsInvalidRecords(t *testing.T) {
	blankCallID := " "
	tests := []storedLocalEntry{
		{Text: "feedback"},
		{Role: "system"},
		{Role: "system", Text: "feedback", AfterToolCallID: &blankCallID},
	}
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	for _, entry := range tests {
		if err := eng.steer(runtimeTestStepID("step-1"), steerLocalEntryIntent(entry)); err == nil {
			t.Fatalf("invalid local entry persistence succeeded: %+v", entry)
		}
		if snapshot := eng.ChatSnapshot(); len(snapshot.Entries) != 0 {
			t.Fatalf("invalid local entry mutated chat: %+v", snapshot.Entries)
		}
	}
	records, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("collect events: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("invalid local entries persisted events: %+v", records)
	}
}

func TestAppendCommittedEntryWithCondensedTextSkipsBlankEntries(t *testing.T) {
	store := mustCreateTestSession(t)
	var events []Event
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})

	eng.AppendCommittedEntryWithCondensedText("user", "   ", "ignored")
	if len(events) != 0 || len(eng.ChatSnapshot().Entries) != 0 {
		t.Fatalf("blank local entry changed state: events=%+v snapshot=%+v", events, eng.ChatSnapshot())
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
	})
	if _, _, err := appendTestEvent(t, store, "step-1", llm.Message{
		Role:    llm.RoleAssistant,
		Content: textutil.Value("working"),
		ToolCalls: []llm.ToolCall{{
			ID:           "call-1",
			Name:         string(toolspec.ToolExecCommand),
			Input:        json.RawMessage(`{"command":"pwd"}`),
			Presentation: presentation,
		}},
	}); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}

	restored := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{Model: "gpt-5"})
	for _, entry := range restored.ChatSnapshot().Entries {
		if entry.Role != "tool_call" || entry.ToolCallID != "call-1" {
			continue
		}
		if entry.ToolCall == nil || !entry.ToolCall.IsShell || entry.ToolCall.Command != "pwd" {
			t.Fatalf("restored tool presentation = %+v", entry.ToolCall)
		}
		return
	}
	t.Fatalf("restored tool call missing: %+v", restored.ChatSnapshot().Entries)
}

func TestParseReviewerSuggestionsObjectSupportsStructuredPayload(t *testing.T) {
	contract := mustReviewerSuggestionsContract(t)
	suggestions := parseReviewerSuggestionsObject(contract, `{"suggestions":["one"," two ","one"," ","NO_OP","no_op"]}`)
	want := []string{"one", " two ", "one", "NO_OP", "no_op"}
	if len(suggestions) != len(want) {
		t.Fatalf("suggestions = %#v, want %#v", suggestions, want)
	}
	for index := range want {
		if suggestions[index] != want[index] {
			t.Fatalf("suggestions = %#v, want %#v", suggestions, want)
		}
	}
	if got := parseReviewerSuggestionsObject(contract, `["not-an-object"]`); len(got) != 0 {
		t.Fatalf("non-object Reviewer payload = %#v, want ignored", got)
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
		{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("Running command now."),
			Phase:   textutil.Value(llm.MessagePhaseCommentary),
			ToolCalls: []llm.ToolCall{{
				ID: "call-1", Name: "exec_command", Input: json.RawMessage(`{"command":"pwd"}`),
			}},
		},
		{Role: llm.RoleAssistant, Content: textutil.Value("assistant response"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		{Role: llm.RoleTool, Name: textutil.Value("exec_command"), ToolCallID: textutil.Value("call-1"), Content: textutil.Value(`{"output":"ok"}`)},
		{Role: llm.RoleDeveloper, MessageType: textutil.Value(llm.MessageTypeEnvironment), Content: textutil.Value(environmentInjectedHeader + "\nOS: darwin")},
	}

	reviewerMessages := buildReviewerTranscriptMessages(messages)
	if len(reviewerMessages) != 6 {
		t.Fatalf("Reviewer transcript messages = %d, want 6", len(reviewerMessages))
	}
	if !strings.Contains(messageContent(reviewerMessages[3]), "Tool call:") ||
		!strings.Contains(messageContent(reviewerMessages[3]), "pwd") ||
		!strings.Contains(messageContent(reviewerMessages[5]), "Tool result:") {
		t.Fatalf("Reviewer transcript lost structured tool context: %+v", reviewerMessages)
	}
}
