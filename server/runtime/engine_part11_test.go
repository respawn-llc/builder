package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
)

func encryptedReasoningOnlyResponse(id string) llm.Response {
	reasoning := llm.ReasoningItem{ID: id, EncryptedContent: "encrypted-reasoning"}
	return llm.Response{
		Assistant:      llm.Message{Role: llm.RoleAssistant, ReasoningItems: []llm.ReasoningItem{reasoning}},
		ReasoningItems: []llm.ReasoningItem{reasoning},
		OutputItems: []llm.ResponseItem{{
			Type:             llm.ResponseItemTypeReasoning,
			ID:               textutil.Value(reasoning.ID),
			EncryptedContent: textutil.Value(reasoning.EncryptedContent),
		}},
		Usage: llm.Usage{WindowTokens: 200000},
	}
}

func newGatedHookClient(initial, next llm.Response) (*hookClient, <-chan struct{}, func()) {
	started := make(chan struct{})
	release := make(chan struct{}, 1)
	client := &hookClient{response: initial}
	client.beforeReturn = func() error {
		close(started)
		<-release
		client.mu.Lock()
		client.response = next
		client.beforeReturn = nil
		client.mu.Unlock()
		return nil
	}
	return client, started, func() {
		select {
		case release <- struct{}{}:
		default:
		}
	}
}

func assertRequestHasUserMessage(t *testing.T, request llm.Request, content string, want bool) {
	t.Helper()
	found := false
	for _, message := range requestMessages(request) {
		if message.Role == llm.RoleUser && messageContent(message) == content {
			found = true
			break
		}
	}
	if found != want {
		t.Fatalf("request user message %q present=%t, want %t; messages=%+v", content, found, want, requestMessages(request))
	}
}

func TestQueuedUserMessageFlushAfterFinalAssistantWithReasoningPublishesAssistantFirst(t *testing.T) {
	t.Parallel()
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("first final"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Reasoning: []llm.ReasoningEntry{
				{Role: textutil.Value("reasoning"), Text: "Plan summary"},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("after flush"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	events := make([]Event, 0, 12)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	mustQueueUserMessage(t, eng, "steer now")
	if _, err := eng.SubmitUserMessage(context.Background(), "start"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	committedEvents := committedTranscriptEventsWithEntries(events)
	if len(committedEvents) < 3 {
		t.Fatalf("expected assistant, reasoning, and queued user committed events, got %+v", committedEvents)
	}
	userIdx := -1
	for idx, evt := range committedEvents {
		if evt.Kind == EventUserMessageFlushed && evt.UserMessage == "start" {
			userIdx = idx
			break
		}
	}
	if userIdx < 0 {
		t.Fatalf("expected initial user flush event, got %+v", committedEvents)
	}
	assistant := committedEvents[userIdx+1]
	if assistant.Kind != EventAssistantMessage || messageContent(assistant.Message) != "first final" {
		t.Fatalf("committed event after initial user = %+v, want first final assistant before reasoning/queued rows; all=%+v", assistant, committedEvents)
	}
	assertRuntimeEventsAdvanceCommittedFrontierContiguously(t, committedEvents)
}

func committedTranscriptEventsWithEntries(events []Event) []Event {
	out := make([]Event, 0, len(events))
	for _, evt := range events {
		if !evt.CommittedTranscriptChanged {
			continue
		}
		if len(TranscriptEntriesFromEvent(evt)) == 0 {
			continue
		}
		out = append(out, evt)
	}
	return out
}

func assertRuntimeEventsAdvanceCommittedFrontierContiguously(t *testing.T, events []Event) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("expected committed transcript events")
	}
	frontier := -1
	for _, evt := range events {
		entries := TranscriptEntriesFromEvent(evt)
		if len(entries) == 0 {
			continue
		}
		if !evt.CommittedEntryStartSet {
			t.Fatalf("committed transcript event missing start: %+v", evt)
		}
		eventEnd := evt.CommittedEntryStart + len(entries)
		if frontier >= 0 && eventEnd <= frontier {
			if evt.Kind == EventToolCallStarted {
				continue
			}
			t.Fatalf("committed transcript event range overlaps previous emitted range: frontier=%d event=%+v entries=%+v all=%+v", frontier, evt, entries, events)
		}
		if frontier >= 0 && evt.CommittedEntryStart != frontier {
			t.Fatalf("committed transcript event range is not contiguous: frontier=%d event=%+v entries=%+v all=%+v", frontier, evt, entries, events)
		}
		frontier = eventEnd
	}
}

func TestModelResponseEventCarriesContextUsage(t *testing.T) {
	t.Parallel()
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{InputTokens: 420, WindowTokens: 1_000},
	}}}
	var usage *ContextUsage
	autoCompactionEnabled := false
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(), Config{
		ContextWindowTokens:   1_000,
		AutoCompactionEnabled: &autoCompactionEnabled,
		OnEvent: func(evt Event) {
			if evt.Kind == EventModelResponse {
				usage = evt.ContextUsage
			}
		},
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "prompt"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if usage == nil {
		t.Fatal("expected model response event to carry context usage")
	}
	if usage.UsedTokens != 420 || usage.WindowTokens != 1_000 {
		t.Fatalf("context usage = %+v, want used=420 window=1000", usage)
	}
}

func TestDirectUserMessageFlushDoesNotEmitConversationUpdated(t *testing.T) {
	t.Parallel()
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	var (
		events     []Event
		eventIndex int
		flushIndex = -1
	)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		OnEvent: func(evt Event) {
			events = append(events, evt)
			eventIndex++
			if evt.Kind == EventUserMessageFlushed && evt.UserMessage == "say hi" && flushIndex < 0 {
				flushIndex = eventIndex
			}
		},
	})

	if _, err := eng.SubmitUserMessage(context.Background(), "say hi"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if flushIndex < 0 {
		t.Fatal("expected direct user_message_flushed event")
	}
	if got := committedConversationUpdatedCountAfterLastUserFlush(events); got != 0 {
		t.Fatalf("committed conversation_updated count after direct user flush = %d, want 0; events=%+v", got, events)
	}
}

func TestRequestMessagesPreserveANSIEscapes(t *testing.T) {
	t.Parallel()
	seedContent := "raw \x1b[31mansi\x1b[0m"
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("ok")},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value(seedContent)}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}

	if _, err := eng.SubmitUserMessage(context.Background(), "plain user"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if len(client.calls) == 0 {
		t.Fatal("expected at least one model call")
	}

	for _, req := range client.calls {
		foundSeed := false
		for _, msg := range requestMessages(req) {
			if msg.Role == llm.RoleUser && messageContent(msg) == seedContent {
				foundSeed = true
			}
		}
		if !foundSeed {
			t.Fatalf("expected request messages to preserve exact seeded ANSI message %q, messages=%+v", seedContent, requestMessages(req))
		}
	}
}

func TestReasoningSummaryVisibleAndEncryptedReasoningRoundTrips(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("first")},
			Reasoning: []llm.ReasoningEntry{
				{Role: textutil.Value("reasoning"), Text: "Plan summary"},
			},
			ReasoningItems: []llm.ReasoningItem{
				{ID: "rs_1", EncryptedContent: "enc_1"},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("second")},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	if _, err := eng.SubmitUserMessage(context.Background(), "one"); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if _, err := eng.SubmitUserMessage(context.Background(), "two"); err != nil {
		t.Fatalf("second submit: %v", err)
	}

	if len(client.calls) < 2 {
		t.Fatalf("expected two model calls, got %d", len(client.calls))
	}
	secondReq := client.calls[1]
	foundReasoningItem := false
	for _, msg := range requestMessages(secondReq) {
		if msg.Role != llm.RoleAssistant || messageContent(msg) != "first" {
			continue
		}
		if len(msg.ReasoningItems) == 1 &&
			msg.ReasoningItems[0].ID == "rs_1" &&
			msg.ReasoningItems[0].EncryptedContent == "enc_1" {
			foundReasoningItem = true
		}
	}
	if !foundReasoningItem {
		t.Fatalf("expected prior assistant message to carry encrypted reasoning item, got %+v", requestMessages(secondReq))
	}
	for _, msg := range requestMessages(secondReq) {
		if strings.Contains(messageContent(msg), "Plan summary") {
			t.Fatalf("reasoning summary text should not be sent back to model input, found in %+v", requestMessages(secondReq))
		}
	}

	snap := eng.ChatSnapshot()
	sawSummary := false
	for _, entry := range snap.Entries {
		if entry.Role == "reasoning" && strings.Contains(entry.Text, "Plan summary") {
			sawSummary = true
			break
		}
	}
	if !sawSummary {
		t.Fatalf("expected reasoning summary in chat snapshot entries, got %+v", snap.Entries)
	}

	events, err := collectTestEventRecords(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	sawLocal := false
	for _, evt := range events {
		if evt.Kind != "local_entry" {
			continue
		}
		entry := persistedLocalEntryForTest(t, evt)
		if entry.Role == "reasoning" && entry.Text == "Plan summary" {
			sawLocal = true
		}
	}
	if !sawLocal {
		t.Fatalf("expected persisted local_entry for reasoning summary, events=%+v", events)
	}
}

func TestDiscardQueuedUserMessageRemovesExactQueuedEntry(t *testing.T) {
	t.Parallel()
	eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	first := mustQueueUserMessage(t, eng, "same")
	mustQueueUserMessage(t, eng, "other")
	duplicate := mustQueueUserMessage(t, eng, "same")

	if removed := mustDiscardQueuedUserMessage(t, eng, duplicate.ID); !removed {
		t.Fatal("expected duplicate queued item removed")
	}

	messageFlow := eng.messageFlow.(*defaultMessageLifecycle)
	var messages []QueuedUserMessage
	if messageFlow != nil && messageFlow.queue != nil {
		messages = messageFlow.queue.Snapshot()
	}
	if len(messages) != 2 || messages[0].ID != first.ID || mustQueuedUserMessageText(t, messages[0]) != "same" || mustQueuedUserMessageText(t, messages[1]) != "other" {
		t.Fatalf("unexpected pending queue after discard: %+v", messages)
	}
}

func TestContextUsageUsesLastUsageWhenAvailable(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 400_000})
	eng.setLastUsage(llm.Usage{InputTokens: 1234, OutputTokens: 66, WindowTokens: 399_000})

	usage := eng.ContextUsage()
	if usage.UsedTokens != 1234 {
		t.Fatalf("used tokens=%d, want 1234", usage.UsedTokens)
	}
	if usage.WindowTokens != 400_000 {
		t.Fatalf("window tokens=%d, want 400000", usage.WindowTokens)
	}
}

func TestContextUsageFallsBackToEstimatedTokens(t *testing.T) {
	t.Parallel()
	eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("estimate me")}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	usage := eng.ContextUsage()
	if usage.WindowTokens != 410_000 {
		t.Fatalf("window tokens=%d, want 410000", usage.WindowTokens)
	}
	if usage.UsedTokens <= 0 {
		t.Fatalf("expected estimated used tokens > 0, got %d", usage.UsedTokens)
	}
}

func TestContextUsageTracksWeightedCacheHitPercentageFromModelUsage(t *testing.T) {
	t.Parallel()
	eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})

	if usage := eng.ContextUsage(); usage.HasCacheHitPercentage {
		t.Fatalf("expected cache hit percentage to be unavailable before model usage, got %+v", usage)
	}

	eng.setLastUsage(llm.Usage{InputTokens: 100, CachedInputTokens: textutil.Value(40)})
	eng.setLastUsage(llm.Usage{InputTokens: 300, CachedInputTokens: textutil.Value(60)})
	eng.setLastUsage(llm.Usage{InputTokens: 999})

	usage := eng.ContextUsage()
	if !usage.HasCacheHitPercentage {
		t.Fatalf("expected cache hit percentage to be available, got %+v", usage)
	}
	if usage.CacheHitPercent != 25 {
		t.Fatalf("cache hit percent=%d, want 25", usage.CacheHitPercent)
	}
}

func TestContextUsageUsesEstimatedTokensWhenLastUsageIsStale(t *testing.T) {
	t.Parallel()
	eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})
	eng.setLastUsage(llm.Usage{InputTokens: 100, OutputTokens: 0, WindowTokens: 410_000})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value(strings.Repeat("x", 1600))}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	estimated := estimateItemsTokens(eng.transcriptRuntimeState().SnapshotItems())
	if estimated <= 100 {
		t.Fatalf("expected estimated tokens above stale usage baseline, got %d", estimated)
	}

	usage := eng.ContextUsage()
	want := 100 + estimated
	if usage.UsedTokens != want {
		t.Fatalf("used tokens=%d, want baseline+delta %d", usage.UsedTokens, want)
	}
}

func TestContextUsageAddsOnlyPostCheckpointEstimateDelta(t *testing.T) {
	t.Parallel()
	eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value(strings.Repeat("seed-", 100))}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	checkpointEstimate := estimateItemsTokens(eng.transcriptRuntimeState().SnapshotItems())
	eng.setLastUsage(llm.Usage{InputTokens: 900, OutputTokens: 120, WindowTokens: 410_000})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value(strings.Repeat("delta-", 40))}})); err != nil {
		t.Fatalf("append delta message: %v", err)
	}

	currentEstimate := estimateItemsTokens(eng.transcriptRuntimeState().SnapshotItems())
	deltaEstimate := currentEstimate - checkpointEstimate
	if deltaEstimate <= 0 {
		t.Fatalf("expected positive estimated delta, got checkpoint=%d current=%d", checkpointEstimate, currentEstimate)
	}

	usage := eng.ContextUsage()
	want := 900 + deltaEstimate
	if usage.UsedTokens != want {
		t.Fatalf("used tokens=%d, want baseline+delta %d", usage.UsedTokens, want)
	}
}

func TestEstimateItemsTokensDoesNotTreatInlineImagePayloadAsPlainText(t *testing.T) {
	t.Parallel()
	base64Payload := strings.Repeat("A", 24_000)
	item := llm.ResponseItem{
		Type:   llm.ResponseItemTypeFunctionCallOutput,
		Name:   textutil.Value(string(toolspec.ToolViewImage)),
		CallID: textutil.Value("call-1"),
		Output: json.RawMessage(`[{"type":"input_image","image_url":"data:image/png;base64,` + base64Payload + `"}]`),
	}

	estimated := estimateItemsTokens([]llm.ResponseItem{item})
	naive := (len(*item.Name) + len(*item.CallID) + len(item.Output) + 3) / 4
	if estimated <= 0 {
		t.Fatalf("expected multimodal estimate > 0, got %d", estimated)
	}
	if estimated >= naive/4 {
		t.Fatalf("expected multimodal estimate to stay well below plain-text estimate, got estimated=%d naive=%d", estimated, naive)
	}
}

func TestContextUsageDoesNotInflateInlineImagePayloadByBase64Length(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 410_000})
	eng.setLastUsage(llm.Usage{InputTokens: 100, OutputTokens: 0, WindowTokens: 410_000})
	if err := eng.steerRuntime(steerMessagesWithPersistenceIntent(steeringMessageEventDefault, true, []llm.Message{{
		Role:       llm.RoleTool,
		ToolCallID: textutil.Value("call-1"),
		Name:       textutil.Value(string(toolspec.ToolViewImage)),
		Content:    textutil.Value(`[{"type":"input_image","image_url":"data:image/png;base64,` + strings.Repeat("A", 24_000) + `"}]`),
	}})); err != nil {
		t.Fatalf("append tool message: %v", err)
	}

	usage := eng.ContextUsage()
	if usage.UsedTokens <= 100 {
		t.Fatalf("expected local estimate to exceed stale usage baseline, got %d", usage.UsedTokens)
	}
	if usage.UsedTokens >= 2_000 {
		t.Fatalf("expected inline image estimate to avoid base64 inflation, got %d", usage.UsedTokens)
	}
}

func TestPreSubmitCompactionTokenLimitUsesFixedRunwayReserve(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		limit    int
		runway   int
		expected int
	}{
		{
			name:     "subtracts fixed runway from auto threshold",
			limit:    190_000,
			runway:   35_000,
			expected: 155_000,
		},
		{
			name:     "large windows still use same fixed runway",
			limit:    950_000,
			runway:   35_000,
			expected: 915_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := mustCreateTestSession(t)

			eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{
				Model:                         "gpt-5",
				AutoCompactTokenLimit:         tt.limit,
				ContextWindowTokens:           1_000_000,
				PreSubmitCompactionLeadTokens: tt.runway,
			})

			if got := eng.compactionPlannerState().preSubmitTokenLimit(eng.compactionPlanningSnapshot()); got != tt.expected {
				t.Fatalf("unexpected pre-submit compaction threshold: got %d want %d", got, tt.expected)
			}
		})
	}
}
