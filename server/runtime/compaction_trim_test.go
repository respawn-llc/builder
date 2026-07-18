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
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestCompactionCacheObservationRequestAppendsPromptToConversationReplica(t *testing.T) {
	seedContent := "seed \x1b[31mansi\x1b[0m"
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
	})
	if err := eng.steerBaseMetaContextIfNeeded("seed-step"); err != nil {
		t.Fatalf("inject agents: %v", err)
	}

	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: seedContent}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}}}})); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleTool, ToolCallID: "call-1", Name: string(toolspec.ToolExecCommand), Content: `{"output":"/tmp"}`}})); err != nil {
		t.Fatalf("append tool output message: %v", err)
	}

	args := "keep API details"
	request, ok, err := eng.compactionCacheObservationRequest(context.Background(), llm.CompactionRequest{
		Model:        "gpt-5",
		Instructions: compactionInstructions(args),
		InputItems:   eng.transcriptRuntimeState().SnapshotItems(),
	})
	if err != nil {
		t.Fatalf("build compaction cache observation request: %v", err)
	}
	if !ok {
		t.Fatal("expected compaction cache observation request")
	}

	wantItems := compactionConversationWithPromptItems(
		eng.transcriptRuntimeState().SnapshotItems(),
		compactionInstructions(args),
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
	foundSeed := false
	for _, msg := range requestMessages(request) {
		if msg.Role == llm.RoleUser && msg.Content == seedContent {
			foundSeed = true
		}
	}
	if !foundSeed {
		t.Fatalf("expected compaction cache request to preserve exact ANSI message %q, messages=%+v", seedContent, requestMessages(request))
	}
	if got, want := request.PromptCacheKey, conversationPromptCacheKey(eng.SessionID(), eng.compactionRuntimeState().Count()); got != want {
		t.Fatalf("PromptCacheKey = %q, want %q", got, want)
	}
	if got, want := request.PromptCacheScope, transcript.CacheWarningScopeConversation; got != want {
		t.Fatalf("PromptCacheScope = %q, want %q", got, want)
	}
}

func TestRemoteCompactionCollapsesToolPayloadAfterOverflowAndWarnsOnCacheBreak(t *testing.T) {
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
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "seed"},
				{Type: llm.ResponseItemTypeCompaction, ID: "cmp_1", EncryptedContent: "enc_1"},
			},
			Usage: llm.Usage{InputTokens: 1000, OutputTokens: 10, WindowTokens: 2500},
		}},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:               "gpt-5",
		ContextWindowTokens: 2500,
	})
	if err := eng.steerBaseMetaContextIfNeeded("seed-step"); err != nil {
		t.Fatalf("inject agents: %v", err)
	}

	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	reasoningPayload := strings.Repeat("encrypted-reasoning", 4_000)
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{{
		ID:               "rs-preserve",
		EncryptedContent: reasoningPayload,
	}}}})); err != nil {
		t.Fatalf("append reasoning message: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}}}})); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleTool, ToolCallID: "call-1", Name: string(toolspec.ToolExecCommand), Content: `{"output":"` + strings.Repeat("x", 120_000) + `"}`}})); err != nil {
		t.Fatalf("append tool output message: %v", err)
	}

	initialSnapshot := eng.transcriptRuntimeState().SnapshotItems()
	initialJSON, err := json.Marshal(initialSnapshot)
	if err != nil {
		t.Fatalf("marshal initial snapshot: %v", err)
	}
	seedRequest, err := eng.buildRequest(context.Background(), "", true)
	if err != nil {
		t.Fatalf("build seed request: %v", err)
	}
	seedClient := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "seeded"},
		Usage: llm.Usage{
			HasCachedInputTokens: true,
			CachedInputTokens:    512,
		},
	}}}
	if _, err := eng.generateWithRetryClient(context.Background(), "seed-cache", seedClient, seedRequest, nil, nil, nil); err != nil {
		t.Fatalf("seed cache lineage: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(client.compactionCalls) != 2 {
		t.Fatalf("expected overflow retry to issue two compact calls, got %d", len(client.compactionCalls))
	}

	firstJSON, err := json.Marshal(client.compactionCalls[0].InputItems)
	if err != nil {
		t.Fatalf("marshal first compact call input: %v", err)
	}
	if string(firstJSON) != string(initialJSON) {
		t.Fatalf("expected first compaction attempt to use an exact conversation replica\nwant=%s\n got=%s", initialJSON, firstJSON)
	}
	if got, want := client.compactionCalls[0].Instructions, compactionInstructions(""); got != want {
		t.Fatalf("first compaction instructions mismatch\nwant=%q\n got=%q", want, got)
	}

	secondInput := client.compactionCalls[1].InputItems
	hasCall := false
	hasOutput := false
	outputCollapsed := false
	reasoningPreserved := false
	for _, item := range secondInput {
		switch item.Type {
		case llm.ResponseItemTypeFunctionCall:
			if item.CallID == "call-1" || item.ID == "call-1" {
				hasCall = true
				if string(item.Arguments) != `{"command":"pwd"}` {
					t.Fatalf("expected shell input to be preserved, got %s", item.Arguments)
				}
			}
		case llm.ResponseItemTypeFunctionCallOutput:
			if item.CallID == "call-1" {
				hasOutput = true
				outputCollapsed = isCollapsedCompactionOverflowShellOutput(item.Output)
			}
		case llm.ResponseItemTypeReasoning:
			if item.ID == "rs-preserve" {
				reasoningPreserved = item.EncryptedContent == reasoningPayload
			}
		}
	}
	if !hasCall || !hasOutput {
		t.Fatalf("expected retry repair to preserve function_call/function_call_output pair, got %+v", secondInput)
	}
	if !reasoningPreserved {
		t.Fatalf("expected retry repair to preserve encrypted reasoning item byte-for-byte, got %+v", secondInput)
	}
	if !outputCollapsed {
		t.Fatalf("expected retry repair to collapse shell output, got %+v", secondInput)
	}

	warnings := persistedCacheWarnings(t, store)
	if len(warnings) != 1 {
		t.Fatalf("expected one cache warning for repaired overflow retry, got %+v", warnings)
	}
	if got, want := warnings[0].Reason, transcript.CacheWarningReasonNonPostfix; got != want {
		t.Fatalf("warning reason = %q, want %q", got, want)
	}
	if got, want := warnings[0].CacheKey, conversationPromptCacheKey(store.Meta().SessionID, 0); got != want {
		t.Fatalf("warning cache key = %q, want %q", got, want)
	}
}

func TestRemoteCompactionDoesNotRepairUnsupportedViewImagePayload(t *testing.T) {
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
					total += 1000
				}
			}
			return total
		},
		compactionErrors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolViewImage, Handler: fakeTool{name: toolspec.ToolViewImage}}), Config{
		Model:               "gpt-5",
		ContextWindowTokens: 2500,
	})
	if err := eng.steerBaseMetaContextIfNeeded("seed-step"); err != nil {
		t.Fatalf("inject agents: %v", err)
	}

	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{
		ID:    "call-view-image-1",
		Name:  string(toolspec.ToolViewImage),
		Input: json.RawMessage(`{"path":"doc.pdf"}`),
	}}}})); err != nil {
		t.Fatalf("append assistant tool call: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:       llm.RoleTool,
		ToolCallID: "call-view-image-1",
		Name:       string(toolspec.ToolViewImage),
		Content:    `[{"type":"input_file","file_data":"data:application/pdf;base64,Zm9v","filename":"doc.pdf"}]`,
	}})); err != nil {
		t.Fatalf("append tool output message: %v", err)
	}

	initialSnapshot := eng.transcriptRuntimeState().SnapshotItems()
	initialCall, initialOutput, initialPromoted := viewImageProviderUnitPresence(initialSnapshot, "call-view-image-1")
	if !initialCall || !initialOutput || !initialPromoted {
		t.Fatalf("expected initial snapshot to include complete promoted view_image unit, got call=%v output=%v promoted=%v items=%+v", initialCall, initialOutput, initialPromoted, initialSnapshot)
	}
	initialJSON, err := json.Marshal(initialSnapshot)
	if err != nil {
		t.Fatalf("marshal initial snapshot: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err == nil {
		t.Fatal("expected unsupported view_image payload to fail repair")
	}
	if len(client.compactionCalls) != 1 {
		t.Fatalf("expected no retry when unsupported payload cannot be collapsed, got %d compact calls", len(client.compactionCalls))
	}

	firstJSON, err := json.Marshal(client.compactionCalls[0].InputItems)
	if err != nil {
		t.Fatalf("marshal first compact call input: %v", err)
	}
	if string(firstJSON) != string(initialJSON) {
		t.Fatalf("expected first compaction attempt to use an exact conversation replica\nwant=%s\n got=%s", initialJSON, firstJSON)
	}
}

func TestRemoteCompactionFailsFastWhenOverflowHasNoCollapsibleToolPayload(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		compactionErrors: []error{
			&llm.ProviderAPIError{ProviderID: "openai", StatusCode: 400, Code: llm.UnifiedErrorCodeContextLengthOverflow, ProviderCode: "context_length_exceeded", Message: "prompt exceeded"},
			nil,
		},
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "unexpected retry"},
				{Type: llm.ResponseItemTypeCompaction, ID: "cmp_1", EncryptedContent: "enc_1"},
			},
		}},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:               "gpt-5",
		ContextWindowTokens: 2500,
	})
	if err := eng.steerBaseMetaContextIfNeeded("seed-step"); err != nil {
		t.Fatalf("inject agents: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("chat-heavy-history", 12_000)}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{{
		ID:               "rs-heavy",
		EncryptedContent: strings.Repeat("reasoning-heavy-history", 12_000),
	}}}})); err != nil {
		t.Fatalf("append reasoning message: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err == nil {
		t.Fatal("expected ordinary-history overflow to fail without retry")
	} else if !llm.IsContextLengthOverflowError(err) {
		t.Fatalf("expected context overflow error, got %v", err)
	}
	if len(client.compactionCalls) != 1 {
		t.Fatalf("expected no retry without collapsible tool payloads, got %d compact calls", len(client.compactionCalls))
	}
}

func viewImageProviderUnitPresence(items []llm.ResponseItem, callID string) (bool, bool, bool) {
	hasCall := false
	hasOutput := false
	hasPromoted := false
	for _, item := range items {
		switch item.Type {
		case llm.ResponseItemTypeFunctionCall:
			if item.CallID == callID || item.ID == callID {
				hasCall = true
			}
		case llm.ResponseItemTypeFunctionCallOutput:
			if item.CallID == callID {
				hasOutput = true
			}
		case llm.ResponseItemTypeOther:
			if item.CallID == callID && item.Name == string(toolspec.ToolViewImage) {
				hasPromoted = true
			}
		}
	}
	return hasCall, hasOutput, hasPromoted
}

func TestCompactionTransientRetryObservesCacheLineageOnce(t *testing.T) {
	withCompactionRetryDelays(t, []time.Duration{time.Millisecond})

	store := mustCreateTestSession(t)

	client := &fakeCompactionClient{
		compactionErrors: []error{errors.New("temporary upstream failure"), nil},
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, Content: "seed"},
				{Type: llm.ResponseItemTypeCompaction, ID: "cmp_1", EncryptedContent: "enc_1"},
			},
			Usage: llm.Usage{HasCachedInputTokens: true, CachedInputTokens: 123, InputTokens: 1000, WindowTokens: 200000},
		}},
	}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5"})
	if err := eng.steerBaseMetaContextIfNeeded("seed-step"); err != nil {
		t.Fatalf("inject agents: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "seed"}})); err != nil {
		t.Fatalf("append user message: %v", err)
	}

	if err := eng.CompactContext(context.Background(), ""); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(client.compactionCalls) != 2 {
		t.Fatalf("expected one transient retry, got %d compaction calls", len(client.compactionCalls))
	}

	requestObserved := 0
	responseObserved := 0
	if err := store.WalkEvents(func(evt session.Event) error {
		switch evt.Kind {
		case sessionEventCacheRequestObserved:
			requestObserved++
		case sessionEventCacheResponseObserved:
			responseObserved++
		}
		return nil
	}); err != nil {
		t.Fatalf("walk events: %v", err)
	}
	if requestObserved != 1 {
		t.Fatalf("cache_request_observed count = %d, want 1", requestObserved)
	}
	if responseObserved != 1 {
		t.Fatalf("cache_response_observed count = %d, want 1", responseObserved)
	}
}
