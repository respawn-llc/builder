package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func TestSubmitUserMessageMissingPhaseOpenAILegacyResponseRemainsTerminal(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(client.calls))
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		persisted := persistedMessageForTest(t, evt)
		if persisted.Role == llm.RoleDeveloper && strings.Contains(messageContent(persisted), commentaryWithoutToolCallsWarning) {
			t.Fatalf("did not expect commentary-without-tools warning for legacy OpenAI response")
		}
		if persisted.Role == llm.RoleDeveloper && strings.Contains(messageContent(persisted), finalWithoutContentWarning) {
			t.Fatalf("did not expect final-without-content warning for legacy OpenAI response")
		}
	}
}

func TestSubmitUserMessageCommentaryWithoutToolsNonOpenAIRemainsTerminal(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("progress update"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = llm.ProviderCapabilities{ProviderID: "anthropic", SupportsResponsesAPI: false, IsOpenAIFirstParty: false}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "claude-3"})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "progress update" {
		t.Fatalf("assistant content = %q, want progress update", messageContent(msg))
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected 1 model call, got %d", len(client.calls))
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		persisted := persistedMessageForTest(t, evt)
		if persisted.Role == llm.RoleDeveloper && strings.Contains(messageContent(persisted), commentaryWithoutToolCallsWarning) {
			t.Fatalf("did not expect commentary-phase warning for non-openai provider")
		}
	}
}

func TestSubmitUserMessageCommentaryWithoutToolsEmitsRealtimeAssistantEvent(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("progress update"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, evt)
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(client.calls))
	}

	mu.Lock()
	defer mu.Unlock()
	assistantContents := make([]string, 0, 2)
	for _, evt := range events {
		if evt.Kind != EventAssistantMessage {
			continue
		}
		assistantContents = append(assistantContents, messageContent(evt.Message))
	}
	if len(assistantContents) != 2 || assistantContents[0] != "progress update" || assistantContents[1] != "done" {
		t.Fatalf("assistant realtime events = %+v, want [progress update done]", assistantContents)
	}
}

func TestSubmitUserMessageCommentaryWithToolCallsEmitsContiguousAssistantEvent(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("working"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, evt)
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}

	mu.Lock()
	defer mu.Unlock()
	assistantContents := make([]string, 0, 2)
	commentaryToolCalls := -1
	for _, evt := range events {
		if evt.Kind != EventAssistantMessage {
			continue
		}
		assistantContents = append(assistantContents, messageContent(evt.Message))
		if messageContent(evt.Message) == "working" {
			commentaryToolCalls = len(evt.Message.ToolCalls)
		}
	}
	if len(assistantContents) != 2 || assistantContents[0] != "working" || assistantContents[1] != "done" {
		t.Fatalf("assistant realtime events = %+v, want [working done]", assistantContents)
	}
	if commentaryToolCalls != 1 {
		t.Fatalf("expected commentary assistant event to carry persisted tool call entries, got %d", commentaryToolCalls)
	}
}

func TestSubmitUserMessageCommentaryWithToolCallsPublishesCommittedEntryStartMetadata(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("working"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}

	var (
		eventsMu sync.Mutex
		events   []Event
	)
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			eventsMu.Lock()
			events = append(events, evt)
			eventsMu.Unlock()
		},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "do the task"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	snapshot := eng.ChatSnapshot()
	assistantEntryIndex := -1
	toolCallEntryIndex := -1
	toolResultEntryIndex := -1
	for idx, entry := range snapshot.Entries {
		if assistantEntryIndex < 0 && entry.Role == "assistant" && entry.Text == "working" {
			assistantEntryIndex = idx
		}
		if toolCallEntryIndex < 0 && entry.Role == "tool_call" && entry.ToolCallID == "call_shell_1" {
			toolCallEntryIndex = idx
		}
		if toolResultEntryIndex < 0 && entry.ToolCallID == "call_shell_1" && (entry.Role == "tool_result_ok" || entry.Role == "tool_result_error") {
			toolResultEntryIndex = idx
		}
	}
	if assistantEntryIndex < 0 || toolCallEntryIndex < 0 || toolResultEntryIndex < 0 {
		t.Fatalf("expected authoritative snapshot to contain commentary assistant + tool call/result, snapshot=%+v", snapshot.Entries)
	}

	eventsMu.Lock()
	eventsSnapshot := append([]Event(nil), events...)
	eventsMu.Unlock()
	assistantIdx := -1
	toolStartIdx := -1
	toolCompleteIdx := -1
	for idx, evt := range eventsSnapshot {
		if evt.Kind == EventAssistantMessage && messageContent(evt.Message) == "working" {
			assistantIdx = idx
		}
		if evt.Kind == EventToolCallStarted && evt.ToolCall != nil && evt.ToolCall.ID == "call_shell_1" {
			toolStartIdx = idx
		}
		if evt.Kind == EventToolCallCompleted && evt.ToolResult != nil && evt.ToolResult.CallID == "call_shell_1" {
			toolCompleteIdx = idx
		}
	}
	if assistantIdx < 0 {
		t.Fatalf("expected commentary assistant event, got %+v", eventsSnapshot)
	}
	if toolStartIdx < 0 {
		t.Fatalf("expected tool_call_started event, got %+v", eventsSnapshot)
	}
	if toolCompleteIdx < 0 {
		t.Fatalf("expected tool_call_completed event, got %+v", eventsSnapshot)
	}
	assistantEvt := eventsSnapshot[assistantIdx]
	if !assistantEvt.CommittedEntryStartSet {
		t.Fatalf("expected commentary assistant event committed start set, got %+v", assistantEvt)
	}
	if got, want := assistantEvt.CommittedEntryStart, assistantEntryIndex; got != want {
		t.Fatalf("commentary assistant committed start = %d, want %d", got, want)
	}
	toolStartEvt := eventsSnapshot[toolStartIdx]
	if !toolStartEvt.CommittedEntryStartSet {
		t.Fatalf("expected tool_call_started committed start set, got %+v", toolStartEvt)
	}
	if got, want := toolStartEvt.CommittedEntryStart, toolCallEntryIndex; got != want {
		t.Fatalf("tool_call_started committed start = %d, want %d", got, want)
	}
	toolCompleteEvt := eventsSnapshot[toolCompleteIdx]
	if !toolCompleteEvt.CommittedEntryStartSet {
		t.Fatalf("expected tool_call_completed committed start set, got %+v", toolCompleteEvt)
	}
	if got, want := toolCompleteEvt.CommittedEntryStart, toolResultEntryIndex; got != want {
		t.Fatalf("tool_call_completed committed start = %d, want %d", got, want)
	}
	if toolStartEvt.CommittedEntryCount < toolStartEvt.CommittedEntryStart+1 {
		t.Fatalf("tool_call_started committed count/start inconsistent: %+v", toolStartEvt)
	}
	if toolCompleteEvt.CommittedEntryCount < toolCompleteEvt.CommittedEntryStart+1 {
		t.Fatalf("tool_call_completed committed count/start inconsistent: %+v", toolCompleteEvt)
	}
	if assistantEvt.CommittedEntryCount < assistantEvt.CommittedEntryStart+1 {
		t.Fatalf("assistant committed count/start inconsistent: %+v", assistantEvt)
	}
	if toolStartIdx <= assistantIdx {
		t.Fatalf("expected tool_call_started after commentary assistant event, assistant_idx=%d tool_idx=%d events=%+v", assistantIdx, toolStartIdx, eventsSnapshot)
	}
	if toolCompleteIdx <= toolStartIdx {
		t.Fatalf("expected tool_call_completed after tool_call_started, start_idx=%d complete_idx=%d events=%+v", toolStartIdx, toolCompleteIdx, eventsSnapshot)
	}
	if assistantEvt.CommittedEntryStart >= toolStartEvt.CommittedEntryStart {
		t.Fatalf("expected commentary assistant before tool call in committed order, assistant=%+v tool=%+v", assistantEvt, toolStartEvt)
	}
	if toolStartEvt.CommittedEntryStart >= toolCompleteEvt.CommittedEntryStart {
		t.Fatalf("expected tool call before tool result in committed order, start=%+v complete=%+v", toolStartEvt, toolCompleteEvt)
	}
	assistantEntries := TranscriptEntriesFromEvent(assistantEvt)
	if len(assistantEntries) < 2 {
		t.Fatalf("commentary assistant event must carry persisted tool-call entries to avoid sparse committed frontier, got %+v", assistantEntries)
	}
	if assistantEntries[0].Role != "assistant" || assistantEntries[1].Role != "tool_call" {
		t.Fatalf("unexpected commentary assistant event entries: %+v", assistantEntries)
	}
}

func TestSubmitUserMessageMissingPhaseWithToolCallsPublishesContiguousAssistantEvent(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("working without phase"),
			},
			ProviderPhase: llm.AbsentProviderPhase(),
			ToolCalls:     []llm.ToolCall{{ID: "call_shell_missing_phase", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:         llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}

	events := make([]Event, 0, 16)
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "do the task"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	assistantIdx := -1
	toolCompleteIdx := -1
	for idx, evt := range events {
		if evt.Kind == EventAssistantMessage && messageContent(evt.Message) == "working without phase" {
			assistantIdx = idx
		}
		if evt.Kind == EventToolCallCompleted && evt.ToolResult != nil && evt.ToolResult.CallID == "call_shell_missing_phase" {
			toolCompleteIdx = idx
		}
	}
	if assistantIdx < 0 {
		t.Fatalf("expected missing-phase assistant event before tool completion, got %+v", events)
	}
	if toolCompleteIdx < 0 {
		t.Fatalf("expected tool_call_completed event, got %+v", events)
	}
	if assistantIdx > toolCompleteIdx {
		t.Fatalf("assistant event index %d must be before tool completion %d; events=%+v", assistantIdx, toolCompleteIdx, events)
	}
	assistantEntries := TranscriptEntriesFromEvent(events[assistantIdx])
	if len(assistantEntries) < 2 || assistantEntries[0].Role != "assistant" || assistantEntries[1].Role != "tool_call" {
		t.Fatalf("missing-phase assistant event must carry assistant + tool-call rows, got %+v", assistantEntries)
	}
	committedEvents := committedTranscriptEventsWithEntries(events)
	assertRuntimeEventsAdvanceCommittedFrontierContiguously(t, committedEvents)
}

func TestSubmitUserMessageDoesNotRetainPendingToolStartForHostedExecutions(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("working"),
				Phase:   textutil.Value(llm.MessagePhaseCommentary),
			},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			OutputItems: []llm.ResponseItem{
				{
					Type:   llm.ResponseItemTypeFunctionCall,
					ID:     textutil.Value("call_shell_1"),
					CallID: textutil.Value("call_shell_1"),
				},
				{
					Type: llm.ResponseItemTypeOther,
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kent cli"}}`),
				},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:        "gpt-5",
		EnabledTools: []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolWebSearch},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "do the task"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := eng.toolCallStarts.Len(); got != 0 {
		t.Fatalf("expected pending tool call starts drained after submit, got %d", got)
	}
	if _, ok := eng.toolCallStarts.Lookup("ws_1"); ok {
		t.Fatal("did not expect hosted tool call id retained in pending starts")
	}
}

func TestSubmitUserMessageLegacyArtifactContentRemainsTerminal(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("working #+#+#+#+#+ malformed"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewExecTestEngine(t, store, client, Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "working #+#+#+#+#+ malformed" {
		t.Fatalf("assistant content = %q", messageContent(msg))
	}
	assertModelCallCount(t, client, 1)

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	persistedAsFinal := false
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		persisted := persistedMessageForTest(t, evt)
		if persisted.Role == llm.RoleAssistant && messageContent(persisted) == "working #+#+#+#+#+ malformed" {
			persistedAsFinal = persisted.Phase != nil && *persisted.Phase == llm.MessagePhaseFinal
		}
	}
	if !persistedAsFinal {
		t.Fatalf("expected garbage-token assistant message to remain final")
	}
}

func TestSubmitUserMessageFinalAnswerWithoutContentForcesNextLoop(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role: llm.RoleAssistant,

				Phase: textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: textutil.Value("done"),
				Phase:   textutil.Value(llm.MessagePhaseFinal),
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(client.calls))
	}

	secondReq := client.calls[1]
	foundWarning := false
	for _, reqMsg := range requestMessages(secondReq) {
		if reqMsg.Role == llm.RoleDeveloper && strings.Contains(messageContent(reqMsg), finalWithoutContentWarning) {
			if reqMsg.MessageType == nil || *reqMsg.MessageType != llm.MessageTypeErrorFeedback {
				t.Fatalf("expected final-without-content warning message type error_feedback, got %+v", reqMsg)
			}
			foundWarning = true
			break
		}
	}
	if !foundWarning {
		t.Fatalf("expected final-without-content warning in next request, got %+v", requestMessages(secondReq))
	}
}
