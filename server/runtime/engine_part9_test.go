package runtime

import (
	"context"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMultiRowCompactionEmitsPerRowCommittedCounts(t *testing.T) {
	store := mustCreateTestSession(t)
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t), Config{
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})
	if _, err := newCompactionPersistence(eng).replaceHistory("step-compact", "local", compactionModeManual, llm.ItemsFromMessages([]llm.Message{
		{Role: llm.RoleUser, MessageType: textutil.Value(llm.MessageTypeCompactionSummary), Content: textutil.Value("summary one")},
		{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value("summary two")},
	})); err != nil {
		t.Fatalf("replace history: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	rows := 0
	for _, evt := range events {
		if evt.Kind != EventLocalEntryAdded || !evt.CommittedTranscriptChanged {
			continue
		}
		rows++
		if !evt.CommittedEntryStartSet {
			t.Fatal("compaction row event missing CommittedEntryStartSet")
		}
		if got, want := evt.CommittedEntryCount, evt.CommittedEntryStart+1; got != want {
			t.Fatalf("compaction row committed count = %d, want per-row %d (start %d)", got, want, evt.CommittedEntryStart)
		}
	}
	if rows < 2 {
		t.Fatalf("expected >=2 projected compaction rows, got %d", rows)
	}
}

func TestCustomToolResultPersistsAsCustomToolCallOutput(t *testing.T) {
	store := mustCreateTestSession(t)

	patchInput := "*** Begin Patch\n*** Add File: a.txt\n+hi\n*** End Patch\n"
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("patching"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{
				ID:          "call_patch",
				Name:        string(toolspec.ToolPatch),
				Custom:      true,
				CustomInput: textutil.Value(patchInput),
				Input:       json.RawMessage(`{}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch}}), Config{Model: "gpt-5", EnabledTools: []toolspec.ID{toolspec.ToolPatch}})

	msg, err := eng.SubmitUserMessage(context.Background(), "apply patch")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("unexpected final message: %+v", msg)
	}
	if len(client.calls) < 2 {
		t.Fatalf("expected follow-up request after tool result, got %d", len(client.calls))
	}

	foundCustomCall := false
	foundCustomOutput := false
	foundFunctionOutput := false
	for _, item := range client.calls[1].Items {
		switch {
		case item.Type == llm.ResponseItemTypeCustomToolCall && item.CallID != nil && *item.CallID == "call_patch":
			foundCustomCall = true
		case item.Type == llm.ResponseItemTypeCustomToolOutput && item.CallID != nil && *item.CallID == "call_patch":
			foundCustomOutput = true
		case item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID != nil && *item.CallID == "call_patch":
			foundFunctionOutput = true
		}
	}
	if !foundCustomCall || !foundCustomOutput || foundFunctionOutput {
		t.Fatalf("expected custom call/output pair only, foundCustomCall=%v foundCustomOutput=%v foundFunctionOutput=%v items=%+v", foundCustomCall, foundCustomOutput, foundFunctionOutput, client.calls[1].Items)
	}
}

func TestRequestToolsExposePatchAsCustomToolOnlyForFirstPartyResponsesProvider(t *testing.T) {
	tests := []struct {
		name       string
		caps       llm.ProviderCapabilities
		wantCustom bool
	}{
		{
			name:       "first party OpenAI",
			caps:       llm.ProviderCapabilities{ProviderID: "openai", SupportsResponsesAPI: true, IsOpenAIFirstParty: true},
			wantCustom: true,
		},
		{
			name:       "OpenAI compatible fallback",
			caps:       llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, IsOpenAIFirstParty: false},
			wantCustom: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)
			client := &fakeClient{caps: tt.caps}
			eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch}}), Config{Model: "gpt-5", EnabledTools: []toolspec.ID{toolspec.ToolPatch}})
			if _, err := eng.ensureLocked(); err != nil {
				t.Fatalf("ensureLocked: %v", err)
			}

			requestTools, err := eng.requestTools(context.Background(), "")
			if err != nil {
				t.Fatalf("requestTools: %v", err)
			}
			if len(requestTools) != 1 {
				t.Fatalf("request tools = %+v, want one patch tool", requestTools)
			}
			gotCustom := requestTools[0].Custom != nil
			if gotCustom != tt.wantCustom {
				t.Fatalf("patch custom tool = %v, want %v; tool=%+v", gotCustom, tt.wantCustom, requestTools[0])
			}
			if !tt.wantCustom && !requestTools[0].Schema.Prepared() {
				t.Fatalf("expected function-tool schema fallback for unsupported custom tools, got %+v", requestTools[0])
			}
		})
	}
}

func TestRequestToolsUseActiveProviderCapsForCustomPatchTool(t *testing.T) {
	store := mustCreateTestSession(t)
	if err := store.MarkModelDispatchLocked(session.LockedContract{
		Model:        "gpt-5",
		EnabledTools: []string{string(toolspec.ToolPatch)},
		ProviderContract: llm.LockedProviderCapabilitiesFromContract(llm.ProviderCapabilities{
			ProviderID:           "openai",
			SupportsResponsesAPI: true,
			IsOpenAIFirstParty:   true,
		}),
	}); err != nil {
		t.Fatalf("mark locked: %v", err)
	}
	activeCaps := llm.ProviderCapabilities{ProviderID: "openai-compatible", SupportsResponsesAPI: true, IsOpenAIFirstParty: false}
	client := &fakeClient{caps: activeCaps}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch}}), Config{
		Model:                        "gpt-5",
		EnabledTools:                 []toolspec.ID{toolspec.ToolPatch},
		ProviderCapabilitiesOverride: &activeCaps,
	})

	requestTools, err := eng.requestTools(context.Background(), "")
	if err != nil {
		t.Fatalf("requestTools: %v", err)
	}
	if len(requestTools) != 1 {
		t.Fatalf("request tools = %+v, want one patch tool", requestTools)
	}
	if requestTools[0].Custom != nil {
		t.Fatalf("expected active compatible provider to use schema patch tool despite stale locked OpenAI caps, got %+v", requestTools[0])
	}
	if !requestTools[0].Schema.Prepared() {
		t.Fatalf("expected function-tool schema fallback for active compatible provider, got %+v", requestTools[0])
	}
}

func TestFailedCustomToolResultPersistsAsCustomToolCallOutput(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("patching"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{
				ID:          "call_patch",
				Name:        string(toolspec.ToolPatch),
				Custom:      true,
				CustomInput: textutil.Value("*** Begin Patch\n*** Add File: a.txt\n+hi\n*** End Patch\n"),
				Input:       json.RawMessage(`{}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: failingTool{name: toolspec.ToolPatch}}), Config{Model: "gpt-5", EnabledTools: []toolspec.ID{toolspec.ToolPatch}})

	if _, err := eng.SubmitUserMessage(context.Background(), "apply patch"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(client.calls) < 2 {
		t.Fatalf("expected follow-up request after tool result, got %d", len(client.calls))
	}

	foundCustomOutput := false
	foundFunctionOutput := false
	for _, item := range client.calls[1].Items {
		switch {
		case item.Type == llm.ResponseItemTypeCustomToolOutput && item.CallID != nil && *item.CallID == "call_patch":
			foundCustomOutput = true
		case item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID != nil && *item.CallID == "call_patch":
			foundFunctionOutput = true
		}
	}
	if !foundCustomOutput || foundFunctionOutput {
		t.Fatalf("expected failed custom output only, foundCustomOutput=%v foundFunctionOutput=%v items=%+v", foundCustomOutput, foundFunctionOutput, client.calls[1].Items)
	}
}

func TestRestoreMessagesPreservesRecoveredMultiToolProviderOrder(t *testing.T) {
	store := mustCreateTestSession(t)
	call1 := llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}
	call2 := llm.ToolCall{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"ls"}`)}
	if _, _, err := appendTestEvent(t, store, "step", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call1, call2}}); err != nil {
		t.Fatalf("append assistant tool calls: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, "step", map[string]any{"call_id": call1.ID, "name": string(toolspec.ToolExecCommand), "is_error": false, "output": json.RawMessage(`{"output":"/tmp"}`)}); err != nil {
		t.Fatalf("append first tool completion: %v", err)
	}
	if _, _, err := appendTestEvent(t, store, "step", map[string]any{"call_id": call2.ID, "name": string(toolspec.ToolExecCommand), "is_error": false, "output": json.RawMessage(`{"output":"a.txt"}`)}); err != nil {
		t.Fatalf("append second tool completion: %v", err)
	}
	restored := mustNewTestEngine(t, store, &fakeClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	items := restored.transcriptRuntimeState().SnapshotItems()
	if len(items) != 4 {
		t.Fatalf("expected 4 restored items, got %d (%+v)", len(items), items)
	}
	if items[0].Type != llm.ResponseItemTypeFunctionCall || items[0].CallID == nil || *items[0].CallID != call1.ID {
		t.Fatalf("unexpected restored item[0]: %+v", items[0])
	}
	if items[1].Type != llm.ResponseItemTypeFunctionCall || items[1].CallID == nil || *items[1].CallID != call2.ID {
		t.Fatalf("unexpected restored item[1]: %+v", items[1])
	}
	if items[2].Type != llm.ResponseItemTypeFunctionCallOutput || items[2].CallID == nil || *items[2].CallID != call1.ID {
		t.Fatalf("unexpected restored item[2]: %+v", items[2])
	}
	if items[3].Type != llm.ResponseItemTypeFunctionCallOutput || items[3].CallID == nil || *items[3].CallID != call2.ID {
		t.Fatalf("unexpected restored item[3]: %+v", items[3])
	}
}

func TestRestoreMessagesPreservesRecoveredMultiToolRequestParity(t *testing.T) {
	dir := t.TempDir()
	liveStore := mustCreateNamedTestSessionAt(t, filepath.Join(dir, "live"), "ws", dir)
	restoredStore := mustCreateNamedTestSessionAt(t, filepath.Join(dir, "restored"), "ws", dir)
	client := &fakeCompactionClient{}
	live := mustNewTestEngine(t, liveStore, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	call1 := llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}
	call2 := llm.ToolCall{ID: "call-2", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"ls"}`)}
	if err := live.steer("step", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call1, call2}}})); err != nil {
		t.Fatalf("append live assistant tool calls: %v", err)
	}
	if _, err := live.executeToolCalls(context.Background(), "step", []llm.ToolCall{call1, call2}); err != nil {
		t.Fatalf("execute live tool calls: %v", err)
	}
	liveReq, err := live.buildRequest(context.Background(), "", true)
	if err != nil {
		t.Fatalf("build live request: %v", err)
	}
	if _, _, err := appendTestEvent(t, restoredStore, "step", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call1, call2}}); err != nil {
		t.Fatalf("append restored assistant tool calls: %v", err)
	}
	if _, _, err := appendTestEvent(t, restoredStore, "step", map[string]any{"call_id": call1.ID, "name": string(toolspec.ToolExecCommand), "is_error": false, "output": json.RawMessage(`{"tool":"exec_command"}`)}); err != nil {
		t.Fatalf("append restored tool completion 1: %v", err)
	}
	if _, _, err := appendTestEvent(t, restoredStore, "step", map[string]any{"call_id": call2.ID, "name": string(toolspec.ToolExecCommand), "is_error": false, "output": json.RawMessage(`{"tool":"exec_command"}`)}); err != nil {
		t.Fatalf("append restored tool completion 2: %v", err)
	}
	restored := mustNewTestEngine(t, restoredStore, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	restoredReq, err := restored.buildRequest(context.Background(), "", true)
	if err != nil {
		t.Fatalf("build restored request: %v", err)
	}
	liveItemsJSON, err := json.Marshal(liveReq.Items)
	if err != nil {
		t.Fatalf("marshal live request items: %v", err)
	}
	restoredItemsJSON, err := json.Marshal(restoredReq.Items)
	if err != nil {
		t.Fatalf("marshal restored request items: %v", err)
	}
	if string(liveItemsJSON) != string(restoredItemsJSON) {
		t.Fatalf("request items mismatch\nlive=%s\nrestored=%s", liveItemsJSON, restoredItemsJSON)
	}
}

func TestStreamingRetryResetsAttemptDeltas(t *testing.T) {
	withGenerateRetryDelays(t, []time.Duration{time.Millisecond})

	store := mustCreateTestSession(t)

	client := &fakeStreamClient{}

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "retry stream")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "final" {
		t.Fatalf("assistant content = %q, want final", messageContent(msg))
	}

	mu.Lock()
	defer mu.Unlock()

	firstDelta := -1
	reset := -1
	secondDelta := -1
	for i, evt := range events {
		if evt.Kind == EventAssistantDelta && evt.AssistantDelta == "partial" && firstDelta == -1 {
			firstDelta = i
		}
		if evt.Kind == EventAssistantDeltaReset && reset == -1 {
			reset = i
		}
		if evt.Kind == EventAssistantDelta && evt.AssistantDelta == "final" && secondDelta == -1 {
			secondDelta = i
		}
	}

	if firstDelta == -1 {
		t.Fatalf("missing first attempt delta event: %+v", events)
	}
	if reset == -1 {
		t.Fatalf("missing reset event: %+v", events)
	}
	if secondDelta == -1 {
		t.Fatalf("missing second attempt delta event: %+v", events)
	}
	if !(firstDelta < reset && reset < secondDelta) {
		t.Fatalf("unexpected delta/reset ordering first=%d reset=%d second=%d", firstDelta, reset, secondDelta)
	}
}

type fakeNonRetriableStreamClient struct{}

func (fakeNonRetriableStreamClient) Generate(_ context.Context, _ llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("not implemented")
}

func (fakeNonRetriableStreamClient) GenerateStream(_ context.Context, _ llm.Request, onDelta func(string)) (llm.Response, error) {
	if onDelta != nil {
		onDelta("partial")
	}
	return llm.Response{}, &llm.ProviderAPIError{
		ProviderID: "openai-compatible",
		Code:       llm.UnifiedErrorCodeProviderContract,
		Message:    "stream ended before terminal event",
	}
}

func TestStreamingNonRetriableErrorResetsAttemptDeltas(t *testing.T) {
	store := mustCreateTestSession(t)

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, fakeNonRetriableStreamClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

	_, err := eng.SubmitUserMessage(context.Background(), "non-retriable stream")
	if err == nil {
		t.Fatal("expected non-retriable stream error")
	}

	mu.Lock()
	defer mu.Unlock()

	var deltaIndex int
	hasDelta := false
	var delta Event
	var terminals []Event
	assistantMessageCount := 0
	for i, evt := range events {
		switch evt.Kind {
		case EventAssistantDelta:
			if hasDelta {
				t.Fatalf("multiple assistant deltas before terminal error: %+v", events)
			}
			deltaIndex = i
			hasDelta = true
			delta = evt
		case EventAssistantDeltaReset:
			terminals = append(terminals, evt)
		case EventAssistantMessage:
			assistantMessageCount++
		}
	}
	if !hasDelta || delta.AssistantTranscriptStreamID == nil {
		t.Fatalf("missing streamed delta before terminal error: %+v", events)
	}
	if len(terminals) != 1 {
		t.Fatalf("assistant stream terminals = %+v, want exactly one: %+v", terminals, events)
	}
	terminal := terminals[0]
	if terminal.AssistantTranscriptStreamID == nil ||
		*terminal.AssistantTranscriptStreamID != *delta.AssistantTranscriptStreamID ||
		terminal.AssistantStreamAbortReason != string(AssistantStreamAbortSuperseded) {
		t.Fatalf("assistant stream terminal = %+v, delta = %+v", terminal, delta)
	}
	if assistantMessageCount != 0 {
		t.Fatalf("final assistant events = %d, want none: %+v", assistantMessageCount, events)
	}

	var cleanupKinds []EventKind
	for _, evt := range events[deltaIndex+1:] {
		switch evt.Kind {
		case EventConversationUpdated, EventAssistantDeltaReset, EventReasoningDeltaReset:
			cleanupKinds = append(cleanupKinds, evt.Kind)
		}
	}
	wantCleanupKinds := []EventKind{
		EventReasoningDeltaReset,
		EventConversationUpdated,
		EventAssistantDeltaReset,
		EventReasoningDeltaReset,
		EventConversationUpdated,
	}
	if len(cleanupKinds) != len(wantCleanupKinds) {
		t.Fatalf("cleanup event kinds = %v, want %v", cleanupKinds, wantCleanupKinds)
	}
	for i, want := range wantCleanupKinds {
		if cleanupKinds[i] != want {
			t.Fatalf("cleanup event kinds = %v, want %v", cleanupKinds, wantCleanupKinds)
		}
	}
}

func TestStreamingEmitsReasoningSummaryDeltaEvents(t *testing.T) {
	store := mustCreateTestSession(t)

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, fakeReasoningStreamClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "stream reasoning"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var reasoningTexts []string
	var assistantDeltaPhases []llm.MessagePhase
	for _, evt := range events {
		if evt.Kind == EventAssistantDelta {
			assistantDeltaPhases = append(assistantDeltaPhases, evt.AssistantDeltaPhase)
			continue
		}
		if evt.Kind != EventReasoningDelta || evt.ReasoningDelta == nil {
			continue
		}
		reasoningTexts = append(reasoningTexts, evt.ReasoningDelta.Text)
	}
	if len(reasoningTexts) != 2 || reasoningTexts[0] != "Plan" || reasoningTexts[1] != "Plan summary" {
		t.Fatalf("unexpected reasoning delta events: %+v", reasoningTexts)
	}
	if len(assistantDeltaPhases) != 1 || assistantDeltaPhases[0] != llm.MessagePhaseFinal {
		t.Fatalf("unexpected assistant delta phases: %+v", assistantDeltaPhases)
	}
}

func TestStreamingIgnoresAsyncLateDeltasAfterGenerateReturns(t *testing.T) {
	store := mustCreateTestSession(t)

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, fakeAsyncLateDeltaClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "test")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if messageContent(msg) != "final" {
		t.Fatalf("assistant content = %q, want final", messageContent(msg))
	}
	time.Sleep(40 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 {
		t.Fatal("expected runtime events")
	}
	for _, evt := range events {
		if evt.Kind == EventAssistantDelta && evt.AssistantDelta == "late" {
			t.Fatalf("expected late delta to be ignored, got events: %+v", events)
		}
	}
}

func TestStreamingBlankFinalClearsLiveAssistantDelta(t *testing.T) {
	store := mustCreateTestSession(t)

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, fakeNoopStreamClient{}, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "stream blank")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != nil {
		t.Fatalf("assistant content = %q, want absent", *msg.Content)
	}
	if ongoing := strings.TrimSpace(eng.ChatSnapshot().Streaming); ongoing != "" {
		t.Fatalf("expected ongoing cleared after noop final, got %q", ongoing)
	}

	mu.Lock()
	defer mu.Unlock()
	hasDelta := false
	hasReset := false
	hasAssistantMessage := false
	hasModelResponse := false
	for _, evt := range events {
		switch evt.Kind {
		case EventAssistantDelta:
			hasDelta = true
		case EventAssistantDeltaReset:
			hasReset = true
		case EventAssistantMessage:
			hasAssistantMessage = true
		case EventModelResponse:
			hasModelResponse = true
		}
	}
	if hasDelta {
		t.Fatalf("did not expect streamed blank delta event, got %+v", events)
	}
	if hasReset {
		t.Fatalf("did not expect assistant delta reset for blank final, got %+v", events)
	}
	if hasAssistantMessage {
		t.Fatalf("did not expect assistant_message event for blank final, got %+v", events)
	}
	if hasModelResponse {
		t.Fatalf("did not expect model_response_received event for blank final, got %+v", events)
	}
}

func TestAuthErrorsAreNotRetried(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &authFailClient{}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
	})

	_, err := eng.SubmitUserMessage(context.Background(), "trigger auth error")
	if err == nil {
		t.Fatal("expected auth failure")
	}
	if client.Calls() != 1 {
		t.Fatalf("expected single model attempt on auth error, got %d", client.Calls())
	}
}

func TestNonRetriableStatusCodesAreNotRetried(t *testing.T) {
	store := mustCreateTestSession(t)
	client := &statusFailClient{}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
	})

	for _, status := range []int{400, 401, 403, 404} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			client.mu.Lock()
			client.status = status
			callsBefore := client.calls
			client.mu.Unlock()

			_, err := eng.SubmitUserMessage(context.Background(), "trigger status error")
			if err == nil {
				t.Fatalf("expected status %d failure", status)
			}
			if calls := client.Calls() - callsBefore; calls != 1 {
				t.Fatalf("expected single model attempt on status %d, got %d", status, calls)
			}
		})
	}
}

func TestProviderContractErrorsAreNotRetried(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &providerContractFailClient{}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

	_, err := eng.SubmitUserMessage(context.Background(), "trigger provider contract error")
	if err == nil {
		t.Fatal("expected provider contract failure")
	}
	if !llm.IsNonRetriableModelError(err) {
		t.Fatalf("expected non-retriable provider contract error, got %v", err)
	}
	if client.Calls() != 1 {
		t.Fatalf("expected single model attempt on provider contract error, got %d", client.Calls())
	}
}
