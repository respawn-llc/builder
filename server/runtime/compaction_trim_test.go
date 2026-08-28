package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestCompactionInstructionsInputRejectsPresentBlankValue(t *testing.T) {
	t.Parallel()
	if _, err := newCompactionInstructionsInput(" \t "); err == nil {
		t.Fatal("present blank compaction instructions succeeded")
	}
}

func TestCompactionCacheObservationRequestBuildsExactConversationReplica(t *testing.T) {
	t.Parallel()
	seedContent := "seed \x1b[31mansi\x1b[0m"
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeCompactionClient{}, newTestToolRegistry(t, tools.HandlerRegistration{
		ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand},
	}), Config{Model: "gpt-5"})
	stepID := runtimeTestStepID("seed-step")
	if err := runTestActiveStep(eng, stepID, func() error {
		return eng.steerBaseMetaContextIfNeeded(stepID)
	}); err != nil {
		t.Fatalf("inject meta context: %v", err)
	}
	for _, message := range []llm.Message{
		{Role: llm.RoleUser, Content: textutil.Value(seedContent)},
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}}},
		{Role: llm.RoleTool, ToolCallID: textutil.Value("call-1"), Name: textutil.Value(string(toolspec.ToolExecCommand)), Content: textutil.Value(`{"output":"/tmp"}`)},
	} {
		if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{message})); err != nil {
			t.Fatalf("append transcript message: %v", err)
		}
	}

	instructionsInput, err := newCompactionInstructionsInput("keep API details")
	if err != nil {
		t.Fatalf("build compaction instructions input: %v", err)
	}
	instructions := compactionInstructions(instructionsInput)
	input := eng.transcriptRuntimeState().SnapshotItems()
	request, err := eng.compactionRequest(context.Background(), input, instructions)
	if err != nil {
		t.Fatalf("build compaction cache observation request: %v", err)
	}

	wantItems := append(
		llm.CloneResponseItems(input),
		llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleDeveloper, Content: textutil.Value(instructions)}})...,
	)
	gotJSON, err := json.Marshal(request.Items)
	if err != nil {
		t.Fatalf("marshal observed items: %v", err)
	}
	wantJSON, err := json.Marshal(wantItems)
	if err != nil {
		t.Fatalf("marshal expected items: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("observed compaction cache request mismatch\nwant=%s\n got=%s", wantJSON, gotJSON)
	}
	if got, want := request.PromptCacheKey, conversationPromptCacheKey(eng.SessionID()); got != want {
		t.Fatalf("prompt cache key = %q, want %q", got, want)
	}
	if got, want := request.PromptCacheScope, transcript.CacheWarningScopeConversation; got != want {
		t.Fatalf("prompt cache scope = %q, want %q", got, want)
	}
}

func TestRemoteCompactionCollapsesToolPayloadAfterOverflowAndPersistsCacheWarning(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		inputTokenCountFn: func(req llm.Request) int {
			total := 0
			for _, item := range req.Items {
				switch item.Type {
				case llm.ResponseItemTypeMessage:
					total += 1000
				case llm.ResponseItemTypeFunctionCall:
					total += 3000
				case llm.ResponseItemTypeFunctionCallOutput:
					total += 1000
				default:
					total += 500
				}
			}
			return total
		},
		compactionErrors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
		compactionResponses: []llm.CompactionResponse{{
			Checkpoint: llm.ResponseItem{
				Type:             llm.ResponseItemTypeCompaction,
				ID:               textutil.Value("cmp_1"),
				EncryptedContent: textutil.Value("enc_1"),
			},
			Usage: llm.Usage{InputTokens: 1000, OutputTokens: 10, WindowTokens: 2500},
		}},
	}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{
		ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand},
	}), Config{Model: "gpt-5", ContextWindowTokens: 2500})
	stepID := runtimeTestStepID("seed-step")
	if err := runTestActiveStep(eng, stepID, func() error {
		return eng.steerBaseMetaContextIfNeeded(stepID)
	}); err != nil {
		t.Fatalf("inject meta context: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	reasoningPayload := strings.Repeat("encrypted-reasoning", 4_000)
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{{
		ID: "rs-preserve", EncryptedContent: reasoningPayload,
	}}}})); err != nil {
		t.Fatalf("append reasoning message: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}}}})); err != nil {
		t.Fatalf("append tool call: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleTool, ToolCallID: textutil.Value("call-1"), Name: textutil.Value(string(toolspec.ToolExecCommand)), Content: textutil.Value(`{"output":"` + strings.Repeat("x", 120_000) + `"}`)}})); err != nil {
		t.Fatalf("append tool output: %v", err)
	}

	initialInput := eng.transcriptRuntimeState().SnapshotItems()
	initialJSON, err := json.Marshal(initialInput)
	if err != nil {
		t.Fatalf("marshal initial input: %v", err)
	}
	seedRequest, err := eng.buildRequest(context.Background(), "", true)
	if err != nil {
		t.Fatalf("build seed request: %v", err)
	}
	cacheStepID := runtimeTestStepID("seed-cache")
	if err := runTestActiveStep(eng, cacheStepID, func() error {
		_, err := eng.generateWithRetryClient(context.Background(), cacheStepID, newObservedModelClient(&fakeClient{responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("seeded")},
			Usage:     llm.Usage{CachedInputTokens: textutil.Value(512)},
		}}}), seedRequest, nil, nil, nil)
		return err
	}); err != nil {
		t.Fatalf("seed cache lineage: %v", err)
	}

	scheduleManualCompactionAndWait(t, eng)
	if len(client.compactionCalls) != 2 {
		t.Fatalf("compaction calls = %d, want overflow repair retry", len(client.compactionCalls))
	}
	firstJSON, err := json.Marshal(client.compactionCalls[0].Items[:len(initialInput)])
	if err != nil {
		t.Fatalf("marshal first compaction input: %v", err)
	}
	if string(firstJSON) != string(initialJSON) {
		t.Fatalf("first compaction input changed before overflow repair\nwant=%s\n got=%s", initialJSON, firstJSON)
	}

	var callPreserved, outputCollapsed, reasoningPreserved bool
	for _, item := range client.compactionCalls[1].Items {
		switch item.Type {
		case llm.ResponseItemTypeFunctionCall:
			if item.CallID != nil && *item.CallID == "call-1" {
				callPreserved = string(item.Arguments) == `{"command":"pwd"}`
			}
		case llm.ResponseItemTypeFunctionCallOutput:
			if item.CallID != nil && *item.CallID == "call-1" {
				outputCollapsed = isCollapsedCompactionOverflowShellOutput(item.Output)
			}
		case llm.ResponseItemTypeReasoning:
			if item.ID != nil && *item.ID == "rs-preserve" {
				reasoningPreserved = item.EncryptedContent != nil && *item.EncryptedContent == reasoningPayload
			}
		}
	}
	if !callPreserved || !outputCollapsed || !reasoningPreserved {
		t.Fatalf("repaired remote compaction input lost provider contract: call=%t collapsed=%t reasoning=%t", callPreserved, outputCollapsed, reasoningPreserved)
	}

	var warning *session.CacheWarningRecord
	for _, record := range mustRecentCompactionRecords(t, store) {
		if value, ok := mustSessionEventPayload(record).(session.CacheWarningRecord); ok {
			copied := value
			warning = &copied
		}
	}
	if warning == nil {
		t.Fatal("expected persisted cache warning after repaired compaction")
	}
	if got, want := warning.Reason, session.CacheWarningReason(transcript.CacheWarningReasonNonPostfix); got != want {
		t.Fatalf("cache warning reason = %q, want %q", got, want)
	}
	if warning.CacheKey == nil || *warning.CacheKey != conversationPromptCacheKey(store.Meta().SessionID) {
		t.Fatalf("cache warning key = %v, want conversation cache key", warning.CacheKey)
	}
}

func TestRemoteCompactionDoesNotRepairUnsupportedViewImagePayload(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionErrors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
	}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{
		ID: toolspec.ToolViewImage, Handler: fakeTool{name: toolspec.ToolViewImage},
	}), Config{Model: "gpt-5", ContextWindowTokens: 2500})
	stepID := runtimeTestStepID("seed-step")
	if err := runTestActiveStep(eng, stepID, func() error {
		return eng.steerBaseMetaContextIfNeeded(stepID)
	}); err != nil {
		t.Fatalf("inject meta context: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID: "call-view-image-1", Name: string(toolspec.ToolViewImage), Input: json.RawMessage(`{"path":"doc.pdf"}`),
	}}}})); err != nil {
		t.Fatalf("append view-image tool call: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role: llm.RoleTool, ToolCallID: textutil.Value("call-view-image-1"), Name: textutil.Value(string(toolspec.ToolViewImage)),
		Content: textutil.Value(`[{"type":"input_file","file_data":"data:application/pdf;base64,Zm9v","filename":"doc.pdf"}]`),
	}})); err != nil {
		t.Fatalf("append view-image tool output: %v", err)
	}

	initialInput := eng.transcriptRuntimeState().SnapshotItems()
	hasCall, hasOutput, hasPromoted := viewImageProviderUnitPresence(initialInput, "call-view-image-1")
	if !hasCall || !hasOutput || !hasPromoted {
		t.Fatalf(
			"expected complete promoted view-image provider unit, got call=%t output=%t promoted=%t",
			hasCall,
			hasOutput,
			hasPromoted,
		)
	}
	initialJSON, err := json.Marshal(initialInput)
	if err != nil {
		t.Fatalf("marshal initial input: %v", err)
	}
	var events []Event
	eng.cfg.OnEvent = func(event Event) {
		events = append(events, event)
	}
	scheduleManualCompactionAndWait(t, eng)
	if !hasEventKind(events, EventCompactionFailed) {
		t.Fatalf("unsupported view-image compaction events = %+v, want failed event", events)
	}
	if len(client.compactionCalls) != 1 {
		t.Fatalf("compaction calls = %d, want no retry for unsupported payload", len(client.compactionCalls))
	}
	gotJSON, err := json.Marshal(client.compactionCalls[0].Items[:len(initialInput)])
	if err != nil {
		t.Fatalf("marshal sent input: %v", err)
	}
	if string(gotJSON) != string(initialJSON) {
		t.Fatalf("unsupported payload changed before rejected repair\nwant=%s\n got=%s", initialJSON, gotJSON)
	}
}

func TestRemoteCompactionFailsFastWhenOverflowHasNoCollapsibleToolPayload(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionErrors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
	}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{
		ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand},
	}), Config{Model: "gpt-5", ContextWindowTokens: 2500})
	stepID := runtimeTestStepID("seed-step")
	if err := runTestActiveStep(eng, stepID, func() error {
		return eng.steerBaseMetaContextIfNeeded(stepID)
	}); err != nil {
		t.Fatalf("inject meta context: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value(strings.Repeat("chat-heavy-history", 12_000))}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{{
		ID: "rs-heavy", EncryptedContent: strings.Repeat("reasoning-heavy-history", 12_000),
	}}}})); err != nil {
		t.Fatalf("append reasoning message: %v", err)
	}

	var events []Event
	eng.cfg.OnEvent = func(event Event) {
		events = append(events, event)
	}
	scheduleManualCompactionAndWait(t, eng)
	if !hasEventKind(events, EventCompactionFailed) {
		t.Fatalf("ordinary-history overflow events = %+v, want failed event", events)
	}
	if len(client.compactionCalls) != 1 {
		t.Fatalf("compaction calls = %d, want no retry without supported payload", len(client.compactionCalls))
	}
}

func TestCompactionTransientRetryObservesCacheLineageOnce(t *testing.T) {
	withCompactionRetryDelays(t, []time.Duration{0})
	store := mustCreateTestSession(t)
	client := &fakeCompactionClient{
		compactionErrors: []error{errors.New("temporary upstream failure"), nil},
		compactionResponses: []llm.CompactionResponse{{
			Checkpoint: llm.ResponseItem{
				Type:             llm.ResponseItemTypeCompaction,
				ID:               textutil.Value("cmp_1"),
				EncryptedContent: textutil.Value("enc_1"),
			},
			Usage: llm.Usage{CachedInputTokens: textutil.Value(123), InputTokens: 1000, WindowTokens: 200000},
		}},
	}
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{
		ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand},
	}), Config{Model: "gpt-5"})
	stepID := runtimeTestStepID("seed-step")
	restoreStep := setTestActiveStep(eng, stepID)
	if err := eng.steerBaseMetaContextIfNeeded(stepID); err != nil {
		t.Fatalf("inject meta context: %v", err)
	}
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("seed")}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	restoreStep()

	scheduleManualCompactionAndWait(t, eng)
	if len(client.compactionCalls) != 2 {
		t.Fatalf("compaction calls = %d, want one transient retry", len(client.compactionCalls))
	}

	requestObserved := 0
	responseObserved := 0
	for _, record := range mustRecentCompactionRecords(t, store) {
		switch mustSessionEventKind(record) {
		case sessionEventCacheRequestObserved:
			requestObserved++
		case sessionEventCacheResponseObserved:
			responseObserved++
		}
	}
	if requestObserved != 1 || responseObserved != 1 {
		t.Fatalf("cache observation records = request:%d response:%d, want exactly one each", requestObserved, responseObserved)
	}
}

func mustRecentCompactionRecords(t *testing.T, store *session.Store) []session.EventRecord {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(64)
	if err != nil {
		t.Fatalf("read bounded recent compaction records: %v", err)
	}
	return window.Records
}

func viewImageProviderUnitPresence(items []llm.ResponseItem, callID string) (bool, bool, bool) {
	var hasCall, hasOutput, hasPromoted bool
	for _, item := range items {
		switch item.Type {
		case llm.ResponseItemTypeFunctionCall:
			if (item.CallID != nil && *item.CallID == callID) || (item.ID != nil && *item.ID == callID) {
				hasCall = true
			}
		case llm.ResponseItemTypeFunctionCallOutput:
			if item.CallID != nil && *item.CallID == callID {
				hasOutput = true
			}
		case llm.ResponseItemTypeOther:
			if item.CallID != nil && *item.CallID == callID && item.Name != nil && *item.Name == string(toolspec.ToolViewImage) {
				hasPromoted = true
			}
		}
	}
	return hasCall, hasOutput, hasPromoted
}

func withCompactionRetryDelays(t *testing.T, delays []time.Duration) {
	t.Helper()
	previous := compactionRetryDelays
	compactionRetryDelays = append([]time.Duration(nil), delays...)
	t.Cleanup(func() {
		compactionRetryDelays = previous
	})
}
