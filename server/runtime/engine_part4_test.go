package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"core/server/llm"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestSubmitUserMessageCommentaryWithToolCallsPublishesCommittedEntryStartMetadata(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "working",
				Phase:   llm.MessagePhaseCommentary,
			},
			ToolCalls: []llm.ToolCall{{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "done",
				Phase:   llm.MessagePhaseFinal,
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
	assistantContents := make([]string, 0, 2)
	commentaryToolCalls := -1
	for idx, evt := range eventsSnapshot {
		if evt.Kind == EventAssistantMessage {
			assistantContents = append(assistantContents, evt.Message.Content)
			if evt.Message.Content == "working" {
				assistantIdx = idx
				commentaryToolCalls = len(evt.Message.ToolCalls)
			}
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
	if len(assistantContents) != 2 || assistantContents[0] != "working" || assistantContents[1] != "done" {
		t.Fatalf("assistant realtime events = %+v, want [working done]", assistantContents)
	}
	if commentaryToolCalls != 1 {
		t.Fatalf("expected commentary assistant event to carry one persisted tool call, got %d", commentaryToolCalls)
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

func TestAutoCompactionStatusEventDoesNotPublishCommittedEntryStart(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "u1"},
				{Type: llm.ResponseItemTypeCompaction, ID: "cmp_1", EncryptedContent: "enc_1"},
			},
			Usage: llm.Usage{InputTokens: 190000, OutputTokens: 1000, WindowTokens: 200000},
		}},
	}

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
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	eng.setLastUsage(llm.Usage{InputTokens: 190000, OutputTokens: 0, WindowTokens: 200000})

	if err := eng.autoCompactIfNeeded(context.Background(), "step-1", compactionModeAuto); err != nil {
		t.Fatalf("auto compact failed: %v", err)
	}

	eventsMu.Lock()
	eventsSnapshot := append([]Event(nil), events...)
	eventsMu.Unlock()
	compactionIdx := -1
	for idx, evt := range eventsSnapshot {
		if evt.Kind == EventCompactionCompleted {
			compactionIdx = idx
		}
		if evt.Kind == EventLocalEntryAdded && evt.LocalEntry != nil && evt.LocalEntry.Role == "compaction_notice" {
			t.Fatalf("did not expect separate compaction notice local entry event, got %+v", eventsSnapshot)
		}
	}
	if compactionIdx < 0 {
		t.Fatalf("expected compaction completed event, got %+v", eventsSnapshot)
	}
	compactionEvt := eventsSnapshot[compactionIdx]
	if compactionEvt.CommittedEntryStartSet {
		t.Fatalf("expected compaction status event to stay pre-commit, got %+v", compactionEvt)
	}
	if compactionEvt.Compaction == nil || compactionEvt.Compaction.Initiator != CompactionInitiatorAutomatic {
		t.Fatalf("auto compaction initiator = %+v, want automatic", compactionEvt.Compaction)
	}
}

func TestCompactionInitiatorDistinguishesUserAndPreSubmitManualMode(t *testing.T) {
	tests := []struct {
		name          string
		start         func(*Engine) error
		wantInitiator CompactionInitiator
	}{
		{
			name: "explicit compact",
			start: func(eng *Engine) error {
				return eng.CompactContext(context.Background(), "")
			},
			wantInitiator: CompactionInitiatorUserRequested,
		},
		{
			name: "pre-submit",
			start: func(eng *Engine) error {
				return eng.CompactContextForPreSubmit(context.Background())
			},
			wantInitiator: CompactionInitiatorAutomatic,
		},
		{
			name: "workflow continuation",
			start: func(eng *Engine) error {
				return eng.CompactContextForWorkflowContinuation(context.Background())
			},
			wantInitiator: CompactionInitiatorAutomatic,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			var events []Event
			eng := mustNewTestEngine(t, store, &fakeClient{responses: []llm.Response{{
				Assistant: llm.Message{Role: llm.RoleAssistant, Content: "summary"},
			}}}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
				Model:          "gpt-5",
				CompactionMode: "local",
				OnEvent:        func(evt Event) { events = append(events, evt) },
			})
			if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
				t.Fatalf("append seed message: %v", err)
			}

			if err := test.start(eng); err != nil {
				t.Fatalf("start compaction: %v", err)
			}

			for _, kind := range []EventKind{EventCompactionStarted, EventCompactionCompleted} {
				var status *CompactionStatus
				for _, event := range events {
					if event.Kind == kind {
						status = event.Compaction
						break
					}
				}
				if status == nil || status.Mode != string(compactionModeManual) || status.Initiator != test.wantInitiator {
					t.Fatalf("%s compaction status = %+v, want manual mode and %q initiator", kind, status, test.wantInitiator)
				}
			}
		})
	}
}

func TestReplaceHistoryPublishesProjectedTranscriptEntriesBeforeCompactionStatus(t *testing.T) {
	store := mustCreateTestSession(t)

	var events []Event
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "before compaction"}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	replacement := llm.ItemsFromMessages([]llm.Message{
		{Role: llm.RoleDeveloper, MessageType: llm.MessageTypeEnvironment, Content: "environment info"},
		{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "condensed summary"},
	})
	if _, err := newCompactionPersistence(eng).replaceHistory("step-1", "local", compactionModeManual, replacement); err != nil {
		t.Fatalf("replace history: %v", err)
	}
	trimmed := 2
	if err := newCompactionPersistence(eng).emitStatus("step-1", EventCompactionCompleted, compactionModeManual, CompactionInitiatorUserRequested, "local", "", &trimmed, 1, ""); err != nil {
		t.Fatalf("emit compaction status: %v", err)
	}

	var projected []Event
	for idx := range events {
		evt := events[idx]
		if evt.Kind != EventLocalEntryAdded || evt.LocalEntry == nil {
			continue
		}
		if evt.LocalEntry.Role == "compaction_notice" {
			t.Fatalf("did not expect separate compaction notice event, got %+v", events)
		}
		projected = append(projected, evt)
	}
	if len(projected) != 2 {
		t.Fatalf("expected 2 projected replacement entry events, got %+v", events)
	}
	if projected[0].LocalEntry.Role != string(transcript.EntryRoleDeveloperContext) || projected[0].LocalEntry.Text != "environment info" {
		t.Fatalf("unexpected first projected event: %+v", projected[0])
	}
	if !projected[0].CommittedEntryStartSet || projected[0].CommittedEntryStart != 1 {
		t.Fatalf("unexpected first projected committed start: %+v", projected[0])
	}
	if projected[1].LocalEntry.Role != string(transcript.EntryRoleCompactionSummary) || projected[1].LocalEntry.Text != "condensed summary" {
		t.Fatalf("unexpected second projected event: %+v", projected[1])
	}
	if !projected[1].CommittedEntryStartSet || projected[1].CommittedEntryStart != 2 {
		t.Fatalf("unexpected second projected committed start: %+v", projected[1])
	}
	conversationUpdatedCount := 0
	for _, evt := range events {
		if evt.Kind != EventConversationUpdated || evt.StepID != "step-1" {
			continue
		}
		conversationUpdatedCount++
	}
	if conversationUpdatedCount != 1 {
		t.Fatalf("expected one compaction conversation update, got %+v", events)
	}
}

func TestSubmitUserMessageLegacyClientKeepsResponsesTerminalWithoutPhaseWarnings(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "progress update",
				Phase:   llm.MessagePhaseCommentary,
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "done",
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "working #+#+#+#+#+ malformed",
				Phase:   llm.MessagePhaseFinal,
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = llm.ProviderCapabilities{ProviderID: "anthropic", SupportsResponsesAPI: false, IsOpenAIFirstParty: false}

	eng := mustNewExecTestEngine(t, store, client, Config{Model: "gpt-5"})

	msg, err := eng.SubmitUserMessage(context.Background(), "show progress")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "progress update" {
		t.Fatalf("assistant content = %q, want progress update", msg.Content)
	}
	msg, err = eng.SubmitUserMessage(context.Background(), "finish")
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}
	msg, err = eng.SubmitUserMessage(context.Background(), "preserve final artifact")
	if err != nil {
		t.Fatalf("third submit: %v", err)
	}
	if msg.Content != "working #+#+#+#+#+ malformed" {
		t.Fatalf("assistant content = %q", msg.Content)
	}
	assertModelCallCount(t, client, 3)

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	progressPersisted := false
	donePersisted := false
	persistedAsFinal := false
	for _, evt := range events {
		if evt.Kind != "message" {
			continue
		}
		var persisted llm.Message
		if err := json.Unmarshal(evt.Payload, &persisted); err != nil {
			t.Fatalf("decode message event: %v", err)
		}
		if persisted.Role == llm.RoleDeveloper &&
			(strings.Contains(persisted.Content, missingAssistantPhaseWarning) ||
				strings.Contains(persisted.Content, commentaryWithoutToolCallsWarning) ||
				strings.Contains(persisted.Content, finalWithoutContentWarning)) {
			t.Fatalf("did not expect phase warning for legacy client response")
		}
		if persisted.Role == llm.RoleAssistant && persisted.Content == "progress update" {
			progressPersisted = true
		}
		if persisted.Role == llm.RoleAssistant && persisted.Content == "done" {
			donePersisted = true
		}
		if persisted.Role == llm.RoleAssistant && persisted.Content == "working #+#+#+#+#+ malformed" {
			persistedAsFinal = persisted.Phase == llm.MessagePhaseFinal
		}
	}
	if !progressPersisted || !donePersisted || !persistedAsFinal {
		t.Fatalf("legacy response persistence = progress:%t done:%t artifact_final:%t", progressPersisted, donePersisted, persistedAsFinal)
	}
}

func TestSubmitUserMessageFinalAnswerWithMixedToolCallsMaterializesAllToolsBeforeSingleFinal(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "hosted final", Phase: llm.MessagePhaseFinal},
			OutputItems: []llm.ResponseItem{
				{
					Type: llm.ResponseItemTypeOther,
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"ws_hosted_only","status":"completed","action":{"type":"search","query":"kent cli"}}`),
				},
				{
					Type:    llm.ResponseItemTypeMessage,
					Role:    llm.RoleAssistant,
					Phase:   llm.MessagePhaseFinal,
					Content: "hosted final",
				},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{
				Role:    llm.RoleAssistant,
				Content: "final response",
				Phase:   llm.MessagePhaseFinal,
			},
			ToolCalls: []llm.ToolCall{
				{ID: "call_shell_1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)},
			},
			OutputItems: []llm.ResponseItem{
				{
					Type: llm.ResponseItemTypeOther,
					Raw:  json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kent cli"}}`),
				},
				{
					Type:    llm.ResponseItemTypeMessage,
					Role:    llm.RoleAssistant,
					Phase:   llm.MessagePhaseFinal,
					Content: "final response",
				},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = openAIFirstPartyNativeWebSearchCaps()

	var emittedMu sync.Mutex
	var emitted []Event
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:         "gpt-5",
		WebSearchMode: "native",
		EnabledTools:  []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolWebSearch},
		OnEvent: func(evt Event) {
			emittedMu.Lock()
			defer emittedMu.Unlock()
			emitted = append(emitted, evt)
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "find latest")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "hosted final" {
		t.Fatalf("assistant content = %q, want hosted final", msg.Content)
	}
	if len(client.calls) != 1 {
		t.Fatalf("expected hosted-only final to finish in 1 model call, got %d", len(client.calls))
	}
	if _, ok := eng.toolCallStarts.Lookup("ws_hosted_only"); ok {
		t.Fatal("did not expect hosted-only tool call retained in pending starts")
	}
	hostedEvents, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read hosted-only events: %v", err)
	}
	hostedCallBeforeFinal := false
	hostedResultBeforeFinal := false
	hostedFinalSeen := false
	for _, evt := range hostedEvents {
		if evt.Kind != "message" {
			continue
		}
		var persisted llm.Message
		if err := json.Unmarshal(evt.Payload, &persisted); err != nil {
			t.Fatalf("decode hosted-only message event: %v", err)
		}
		if persisted.Role == llm.RoleAssistant && len(persisted.ToolCalls) == 1 && persisted.ToolCalls[0].ID == "ws_hosted_only" {
			if hostedFinalSeen {
				t.Fatal("hosted-only tool call persisted after final answer")
			}
			hostedCallBeforeFinal = true
		}
		if persisted.Role == llm.RoleTool && persisted.ToolCallID == "ws_hosted_only" {
			if hostedFinalSeen {
				t.Fatal("hosted-only tool result persisted after final answer")
			}
			hostedResultBeforeFinal = true
		}
		if persisted.Role == llm.RoleAssistant && persisted.Phase == llm.MessagePhaseFinal && strings.TrimSpace(persisted.Content) == "hosted final" {
			hostedFinalSeen = true
			if len(persisted.ToolCalls) != 0 {
				t.Fatalf("hosted-only final retained tool calls: %+v", persisted.ToolCalls)
			}
		}
	}
	if !hostedCallBeforeFinal || !hostedResultBeforeFinal || !hostedFinalSeen {
		t.Fatalf("expected hosted-only call, result, and final in order; call=%t result=%t final=%t", hostedCallBeforeFinal, hostedResultBeforeFinal, hostedFinalSeen)
	}

	msg, err = eng.SubmitUserMessage(context.Background(), "do the task")
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if msg.Content != "final response" {
		t.Fatalf("assistant content = %q, want final response", msg.Content)
	}
	if len(client.calls) != 2 {
		t.Fatalf("expected 2 model calls, got %d", len(client.calls))
	}
	if got := eng.toolCallStarts.Len(); got != 0 {
		t.Fatalf("expected pending tool call starts drained after final mixed tool calls, got %d", got)
	}
	if _, ok := eng.toolCallStarts.Lookup("ws_1"); ok {
		t.Fatal("did not expect hosted tool call id retained in pending starts")
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	order := make([]string, 0, 4)
	finalCount := 0
	mixedTurn := false
	recoverySeen := false
	recoveryBeforeCalls := false
	for _, evt := range events {
		if evt.Kind == "model_recovery_pending" && mixedTurn {
			recoverySeen = true
		}
		if evt.Kind != "message" {
			continue
		}
		var persisted llm.Message
		if err := json.Unmarshal(evt.Payload, &persisted); err != nil {
			t.Fatalf("decode message event: %v", err)
		}
		if persisted.Role == llm.RoleUser && persisted.Content == "do the task" {
			mixedTurn = true
			recoverySeen = false
			continue
		}
		if !mixedTurn {
			continue
		}
		if persisted.Role == llm.RoleDeveloper && persisted.MessageType == llm.MessageTypeErrorFeedback {
			t.Fatalf("did not expect developer warning for final answer with mixed tool calls: %+v", persisted)
		}
		if persisted.Role == llm.RoleAssistant && len(persisted.ToolCalls) == 2 {
			if persisted.ToolCalls[0].ID != "call_shell_1" || persisted.ToolCalls[1].ID != "ws_1" {
				t.Fatalf("unexpected mixed tool call order: %+v", persisted.ToolCalls)
			}
			recoveryBeforeCalls = recoverySeen
			order = append(order, "calls")
		}
		if persisted.Role == llm.RoleTool && persisted.ToolCallID == "call_shell_1" {
			order = append(order, "local_result")
		}
		if persisted.Role == llm.RoleTool && persisted.ToolCallID == "ws_1" {
			order = append(order, "hosted_result")
		}
		if persisted.Role == llm.RoleAssistant && persisted.Phase == llm.MessagePhaseFinal && strings.TrimSpace(persisted.Content) == "final response" {
			finalCount++
			if len(persisted.ToolCalls) != 0 {
				t.Fatalf("final assistant message retained tool calls: %+v", persisted.ToolCalls)
			}
			order = append(order, "final")
		}
	}
	wantOrder := []string{"calls", "local_result", "hosted_result", "final"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("message order = %+v, want %+v", order, wantOrder)
	}
	if finalCount != 1 {
		t.Fatalf("final answer count = %d, want 1", finalCount)
	}
	if !recoveryBeforeCalls {
		t.Fatalf("model recovery marker did not precede final-answer tool calls: %+v", events)
	}

	emittedMu.Lock()
	defer emittedMu.Unlock()
	localStarted := -1
	localCompleted := -1
	finalAssistant := -1
	for idx, evt := range emitted {
		if evt.Kind == EventToolCallStarted && evt.ToolCall != nil && evt.ToolCall.ID == "call_shell_1" {
			localStarted = idx
		}
		if evt.Kind == EventToolCallCompleted && evt.ToolResult != nil && evt.ToolResult.CallID == "call_shell_1" {
			localCompleted = idx
		}
		if evt.Kind == EventAssistantMessage && evt.Message.Phase == llm.MessagePhaseFinal && strings.TrimSpace(evt.Message.Content) == "final response" {
			finalAssistant = idx
		}
	}
	if localStarted < 0 || localCompleted < 0 || finalAssistant < 0 {
		t.Fatalf("expected local tool start/completion and final assistant events, got %+v", emitted)
	}
	if !(localStarted < localCompleted && localCompleted < finalAssistant) {
		t.Fatalf("event order invalid: started=%d completed=%d final=%d events=%+v", localStarted, localCompleted, finalAssistant, emitted)
	}
}

func TestReviewerSkippedWhenNoToolCalls(t *testing.T) {
	store := mustCreateTestSession(t)

	mainClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	reviewerClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: `{"suggestions":["x"]}`},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, mainClient, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "edits",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			Client:        reviewerClient,
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "done" {
		t.Fatalf("assistant content = %q, want done", msg.Content)
	}
	if len(reviewerClient.calls) != 0 {
		t.Fatalf("expected reviewer not to be called, got %d calls", len(reviewerClient.calls))
	}
}
