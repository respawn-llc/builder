package runtime

import (
	"context"
	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/toolspec"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type webSearchProbeTool struct {
	mu    sync.Mutex
	calls int
	name  toolspec.ID
}

func (t *webSearchProbeTool) Call(_ context.Context, c tools.Call) (tools.Result, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	out, _ := json.Marshal(map[string]any{"tool": string(t.name)})
	return tools.Result{CallID: c.ID, Name: c.Name, Output: out}, nil
}

func (t *webSearchProbeTool) Calls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

func TestMultiRowCompactionEmitsPerRowCommittedCounts(t *testing.T) {
	store := mustCreateTestSession(t)
	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})
	if err := newCompactionPersistence(eng).replaceHistory("step-compact", "local", compactionModeManual, llm.ItemsFromMessages([]llm.Message{
		{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary one"},
		{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "summary two"},
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

func TestExecuteToolCallsRejectsWhitespaceWebSearchQuery(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})

	results, err := eng.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
		ID:    "call-web",
		Name:  string(toolspec.ToolWebSearch),
		Input: json.RawMessage(`{"query":"   "}`),
	}})
	if err != nil {
		t.Fatalf("execute tool calls: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatalf("expected invalid web search query to fail, got %+v", results[0])
	}
	var output map[string]string
	if err := json.Unmarshal(results[0].Output, &output); err != nil {
		t.Fatalf("decode result output: %v", err)
	}
	if output["error"] != tools.InvalidWebSearchQueryMessage {
		t.Fatalf("expected invalid query error, got %+v", output)
	}
	if completion, ok := eng.transcriptRuntimeState().ToolCompletionSnapshot("call-web"); !ok {
		t.Fatal("expected tool completion to be recorded")
	} else if !completion.IsError {
		t.Fatalf("expected persisted completion to be error, got %+v", completion)
	}
}

func TestExecuteToolCallsRejectsHallucinatedWebSearchQueryBeforeHandler(t *testing.T) {
	store := mustCreateTestSession(t)

	probe := &webSearchProbeTool{name: toolspec.ToolWebSearch}
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{
		ID:      toolspec.ToolWebSearch,
		Handler: probe,
	}), Config{Model: "gpt-5"})

	results, err := eng.executeToolCalls(context.Background(), "step", []llm.ToolCall{{
		ID:    "call-web",
		Name:  string(toolspec.ToolWebSearch),
		Input: json.RawMessage(`{"query":"web search"}`),
	}})
	if err != nil {
		t.Fatalf("execute tool calls: %v", err)
	}
	if probe.Calls() != 0 {
		t.Fatalf("expected validation to reject before handler execution, got %d handler calls", probe.Calls())
	}
	if len(results) != 1 {
		t.Fatalf("expected one result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatalf("expected hallucinated web search query to fail, got %+v", results[0])
	}
	var output map[string]string
	if err := json.Unmarshal(results[0].Output, &output); err != nil {
		t.Fatalf("decode result output: %v", err)
	}
	if output["error"] != tools.InvalidWebSearchQueryMessage {
		t.Fatalf("expected invalid query error, got %+v", output)
	}
	if completion, ok := eng.transcriptRuntimeState().ToolCompletionSnapshot("call-web"); !ok {
		t.Fatal("expected tool completion to be recorded")
	} else if !completion.IsError {
		t.Fatalf("expected persisted completion to be error, got %+v", completion)
	}
}

func TestCriticalExactRecountsAfterToolCompletionBeforeToolMessageAppend(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{inputTokenCountFn: func(req llm.Request) int {
		for _, item := range req.Items {
			if item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == "call-1" {
				return 200
			}
		}
		return 100
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	call := llm.ToolCall{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}
	if err := eng.steer("step", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call}}})); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	if precise, ok := eng.currentInputTokensPrecisely(context.Background()); !ok || precise != 100 {
		t.Fatalf("initial exact count = (%d, %v), want (100, true)", precise, ok)
	}
	if client.countInputTokenCalls != 1 {
		t.Fatalf("count calls=%d, want 1", client.countInputTokenCalls)
	}
	results, err := eng.executeToolCalls(context.Background(), "step", []llm.ToolCall{call})
	if err != nil {
		t.Fatalf("execute tool calls: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected one tool result, got %d", len(results))
	}
	req, err := eng.buildRequest(context.Background(), "", true)
	if err != nil {
		t.Fatalf("build request after tool completion: %v", err)
	}
	foundOutput := false
	for _, item := range req.Items {
		if item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == call.ID {
			foundOutput = true
			break
		}
	}
	if !foundOutput {
		t.Fatalf("expected synthesized function_call_output before tool message append, items=%+v", req.Items)
	}
	if precise, ok := eng.currentInputTokensPreciselyIfDueWithPriority(context.Background(), 1_000, true); !ok || precise != 200 {
		t.Fatalf("critical exact recount = (%d, %v), want (200, true)", precise, ok)
	}
	if client.countInputTokenCalls != 2 {
		t.Fatalf("expected critical recount after tool completion, got %d count calls", client.countInputTokenCalls)
	}
}

func TestCustomToolResultPersistsAsCustomToolCallOutput(t *testing.T) {
	store := mustCreateTestSession(t)

	patchInput := "*** Begin Patch\n*** Add File: a.txt\n+hi\n*** End Patch\n"
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "patching", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{
				ID:          "call_patch",
				Name:        string(toolspec.ToolPatch),
				Custom:      true,
				CustomInput: patchInput,
				Input:       json.RawMessage(`{}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch}}), Config{Model: "gpt-5", EnabledTools: []toolspec.ID{toolspec.ToolPatch}})

	msg, err := eng.SubmitUserMessage(context.Background(), "apply patch")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "done" {
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
		case item.Type == llm.ResponseItemTypeCustomToolCall && item.CallID == "call_patch":
			foundCustomCall = true
		case item.Type == llm.ResponseItemTypeCustomToolOutput && item.CallID == "call_patch":
			foundCustomOutput = true
		case item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == "call_patch":
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
			eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch}}), Config{Model: "gpt-5", EnabledTools: []toolspec.ID{toolspec.ToolPatch}})
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
			if !tt.wantCustom && len(requestTools[0].Schema) == 0 {
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
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch}}), Config{
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
	if len(requestTools[0].Schema) == 0 {
		t.Fatalf("expected function-tool schema fallback for active compatible provider, got %+v", requestTools[0])
	}
}

func TestFailedCustomToolResultPersistsAsCustomToolCallOutput(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "patching", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{
				ID:          "call_patch",
				Name:        string(toolspec.ToolPatch),
				Custom:      true,
				CustomInput: "*** Begin Patch\n*** Add File: a.txt\n+hi\n*** End Patch\n",
				Input:       json.RawMessage(`{}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: failingTool{name: toolspec.ToolPatch}}), Config{Model: "gpt-5", EnabledTools: []toolspec.ID{toolspec.ToolPatch}})

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
		case item.Type == llm.ResponseItemTypeCustomToolOutput && item.CallID == "call_patch":
			foundCustomOutput = true
		case item.Type == llm.ResponseItemTypeFunctionCallOutput && item.CallID == "call_patch":
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
	if _, _, err := store.AppendEvent("step", "message", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call1, call2}}); err != nil {
		t.Fatalf("append assistant tool calls: %v", err)
	}
	if _, _, err := store.AppendEvent("step", "tool_completed", map[string]any{"call_id": call1.ID, "name": string(toolspec.ToolExecCommand), "is_error": false, "output": json.RawMessage(`{"output":"/tmp"}`)}); err != nil {
		t.Fatalf("append first tool completion: %v", err)
	}
	if _, _, err := store.AppendEvent("step", "tool_completed", map[string]any{"call_id": call2.ID, "name": string(toolspec.ToolExecCommand), "is_error": false, "output": json.RawMessage(`{"output":"a.txt"}`)}); err != nil {
		t.Fatalf("append second tool completion: %v", err)
	}
	restored := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	items := restored.transcriptRuntimeState().SnapshotItems()
	if len(items) != 4 {
		t.Fatalf("expected 4 restored items, got %d (%+v)", len(items), items)
	}
	if items[0].Type != llm.ResponseItemTypeFunctionCall || items[0].CallID != call1.ID {
		t.Fatalf("unexpected restored item[0]: %+v", items[0])
	}
	if items[1].Type != llm.ResponseItemTypeFunctionCall || items[1].CallID != call2.ID {
		t.Fatalf("unexpected restored item[1]: %+v", items[1])
	}
	if items[2].Type != llm.ResponseItemTypeFunctionCallOutput || items[2].CallID != call1.ID {
		t.Fatalf("unexpected restored item[2]: %+v", items[2])
	}
	if items[3].Type != llm.ResponseItemTypeFunctionCallOutput || items[3].CallID != call2.ID {
		t.Fatalf("unexpected restored item[3]: %+v", items[3])
	}
}

func TestRestoreMessagesPreservesRecoveredMultiToolExactTokenParity(t *testing.T) {
	dir := t.TempDir()
	liveStore := mustCreateNamedTestSessionAt(t, filepath.Join(dir, "live"), "ws", dir)
	restoredStore := mustCreateNamedTestSessionAt(t, filepath.Join(dir, "restored"), "ws", dir)
	countForRequest := func(req llm.Request) int {
		count := 0
		for i, item := range req.Items {
			switch item.Type {
			case llm.ResponseItemTypeFunctionCall:
				count += 100 + (i * 7)
			case llm.ResponseItemTypeFunctionCallOutput:
				count += 1_000 + (i * 11)
			default:
				count += 10 + i
			}
		}
		return count
	}
	client := &fakeCompactionClient{inputTokenCountFn: countForRequest}
	live := mustNewTestEngine(t, liveStore, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
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
	liveCount, ok := live.requestInputTokensPreciselyTracked(context.Background(), liveReq, false)
	if !ok {
		t.Fatal("expected live precise token count")
	}
	if _, _, err := restoredStore.AppendEvent("step", "message", llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{call1, call2}}); err != nil {
		t.Fatalf("append restored assistant tool calls: %v", err)
	}
	if _, _, err := restoredStore.AppendEvent("step", "tool_completed", map[string]any{"call_id": call1.ID, "name": string(toolspec.ToolExecCommand), "is_error": false, "output": json.RawMessage(`{"tool":"exec_command"}`)}); err != nil {
		t.Fatalf("append restored tool completion 1: %v", err)
	}
	if _, _, err := restoredStore.AppendEvent("step", "tool_completed", map[string]any{"call_id": call2.ID, "name": string(toolspec.ToolExecCommand), "is_error": false, "output": json.RawMessage(`{"tool":"exec_command"}`)}); err != nil {
		t.Fatalf("append restored tool completion 2: %v", err)
	}
	restored := mustNewTestEngine(t, restoredStore, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	restoredReq, err := restored.buildRequest(context.Background(), "", true)
	if err != nil {
		t.Fatalf("build restored request: %v", err)
	}
	restoredCount, ok := restored.requestInputTokensPreciselyTracked(context.Background(), restoredReq, false)
	if !ok {
		t.Fatal("expected restored precise token count")
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
	if liveCount != restoredCount {
		t.Fatalf("precise token count mismatch: live=%d restored=%d", liveCount, restoredCount)
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
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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
	if msg.Content != "final" {
		t.Fatalf("assistant content = %q, want final", msg.Content)
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

func TestStreamingEmitsReasoningSummaryDeltaEvents(t *testing.T) {
	store := mustCreateTestSession(t)

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, fakeReasoningStreamClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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
	eng := mustNewTestEngine(t, store, fakeAsyncLateDeltaClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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
	if msg.Content != "final" {
		t.Fatalf("assistant content = %q, want final", msg.Content)
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

func TestStreamingNoopFinalClearsLiveAssistantDelta(t *testing.T) {
	store := mustCreateTestSession(t)

	var (
		mu     sync.Mutex
		events []Event
	)
	eng := mustNewTestEngine(t, store, fakeNoopStreamClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			events = append(events, evt)
			mu.Unlock()
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "stream noop")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "" {
		t.Fatalf("assistant content = %q, want empty", msg.Content)
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
			if evt.AssistantDelta == reviewerNoopToken {
				hasDelta = true
			}
		case EventAssistantDeltaReset:
			hasReset = true
		case EventAssistantMessage:
			hasAssistantMessage = true
		case EventModelResponse:
			hasModelResponse = true
		}
	}
	if !hasDelta {
		t.Fatalf("expected streamed noop delta event, got %+v", events)
	}
	if !hasReset {
		t.Fatalf("expected assistant delta reset for noop final, got %+v", events)
	}
	if hasAssistantMessage {
		t.Fatalf("did not expect assistant_message event for noop final, got %+v", events)
	}
	if hasModelResponse {
		t.Fatalf("did not expect model_response_received event for noop final, got %+v", events)
	}
}

func TestStreamingDeltasDoNotEmitConversationSnapshotEvents(t *testing.T) {
	store := mustCreateTestSession(t)

	var (
		mu                   sync.Mutex
		events               []Event
		conversationWithLive int
	)
	var eng *Engine
	eng = mustNewTestEngine(t, store, fakeSimpleStreamClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, evt)
			if evt.Kind == EventConversationUpdated && eng != nil {
				if strings.TrimSpace(eng.ChatSnapshot().Streaming) != "" {
					conversationWithLive++
				}
			}
		},
	})

	msg, err := eng.SubmitUserMessage(context.Background(), "stream")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "ab" {
		t.Fatalf("assistant content = %q, want ab", msg.Content)
	}

	mu.Lock()
	defer mu.Unlock()
	if conversationWithLive != 0 {
		t.Fatalf("expected no conversation_updated events carrying live ongoing snapshot, got %d events: %+v", conversationWithLive, events)
	}
}

func TestChatSnapshotOngoingTracksStreamingAndClearsOnCommit(t *testing.T) {
	store := mustCreateTestSession(t)

	var (
		mu             sync.Mutex
		deltaSnapshots []string
	)
	var eng *Engine
	eng = mustNewTestEngine(t, store, fakeSimpleStreamClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		OnEvent: func(evt Event) {
			if evt.Kind != EventAssistantDelta || eng == nil {
				return
			}
			mu.Lock()
			deltaSnapshots = append(deltaSnapshots, eng.ChatSnapshot().Streaming)
			mu.Unlock()
		},
	})

	_, err := eng.SubmitUserMessage(context.Background(), "stream")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	mu.Lock()
	if len(deltaSnapshots) != 2 {
		mu.Unlock()
		t.Fatalf("expected two assistant delta snapshots, got %d", len(deltaSnapshots))
	}
	if deltaSnapshots[0] != "a" || deltaSnapshots[1] != "ab" {
		mu.Unlock()
		t.Fatalf("unexpected ongoing snapshots during streaming: %+v", deltaSnapshots)
	}
	mu.Unlock()

	if ongoing := strings.TrimSpace(eng.ChatSnapshot().Streaming); ongoing != "" {
		t.Fatalf("expected ongoing cleared after commit, got %q", ongoing)
	}
}

func TestAuthErrorsAreNotRetried(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &authFailClient{}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
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
	for _, status := range []int{400, 401, 403, 404} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			store := mustCreateTestSession(t)

			client := &statusFailClient{status: status}
			eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
				Model: "gpt-5",
			})

			_, err := eng.SubmitUserMessage(context.Background(), "trigger status error")
			if err == nil {
				t.Fatalf("expected status %d failure", status)
			}
			if client.Calls() != 1 {
				t.Fatalf("expected single model attempt on status %d, got %d", status, client.Calls())
			}
		})
	}
}

func TestProviderContractErrorsAreNotRetried(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &providerContractFailClient{}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})

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
