package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/toolspec"
)

func TestQueuedUserMessageFlushesWhenAssistantReturnsWithoutTools(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "after flush"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	var seenFlushed bool
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		OnEvent: func(evt Event) {
			if evt.Kind == EventUserMessageFlushed && evt.UserMessage == "steer now" {
				seenFlushed = true
			}
		},
	})

	eng.QueueUserMessage("steer now")
	msg, err := eng.SubmitUserMessage(context.Background(), "start")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "after flush" {
		t.Fatalf("assistant content = %q, want after flush", msg.Content)
	}
	if !seenFlushed {
		t.Fatal("expected user_message_flushed event")
	}
	if len(client.calls) < 2 {
		t.Fatalf("expected at least 2 model calls, got %d", len(client.calls))
	}
	second := client.calls[1]
	hasInjected := false
	for _, m := range requestMessages(second) {
		if m.Role == llm.RoleUser && m.Content == "steer now" {
			hasInjected = true
			break
		}
	}
	if !hasInjected {
		t.Fatalf("expected flushed user message in second request, messages=%+v", requestMessages(second))
	}
}

func TestQueuedUserMessageFlushAfterFinalAssistantPublishesCommittedAssistantFirst(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first final", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "after flush", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	events := make([]Event, 0, 12)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	eng.QueueUserMessage("steer now")
	msg, err := eng.SubmitUserMessage(context.Background(), "start")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "after flush" {
		t.Fatalf("assistant content = %q, want after flush", msg.Content)
	}

	assistantIndex := -1
	flushIndex := -1
	for idx, evt := range events {
		if evt.Kind == EventAssistantMessage && evt.Message.Content == "first final" {
			assistantIndex = idx
		}
		if evt.Kind == EventUserMessageFlushed && evt.UserMessage == "steer now" {
			flushIndex = idx
		}
	}
	if assistantIndex < 0 {
		t.Fatalf("expected first final assistant event before queued flush, got %+v", events)
	}
	if flushIndex < 0 {
		t.Fatalf("expected queued user flush event, got %+v", events)
	}
	if assistantIndex > flushIndex {
		t.Fatalf("first final assistant event index %d must be before queued flush index %d; events=%+v", assistantIndex, flushIndex, events)
	}

	assistant := events[assistantIndex]
	flushed := events[flushIndex]
	assistantEntries := TranscriptEntriesFromEvent(assistant)
	if len(assistantEntries) == 0 {
		t.Fatalf("assistant event carried no transcript entries: %+v", assistant)
	}
	wantFlushStart := assistant.CommittedEntryStart + len(assistantEntries)
	if flushed.CommittedEntryStart != wantFlushStart {
		t.Fatalf("queued user flush start = %d, want %d after assistant event; assistant=%+v flush=%+v", flushed.CommittedEntryStart, wantFlushStart, assistant, flushed)
	}
}

func TestQueuedUserMessageFlushAfterFinalAssistantWithReasoningPublishesAssistantFirst(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first final", Phase: llm.MessagePhaseFinal},
			Reasoning: []llm.ReasoningEntry{
				{Role: "reasoning", Text: "Plan summary"},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "after flush", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	events := make([]Event, 0, 12)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	eng.QueueUserMessage("steer now")
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
	if assistant.Kind != EventAssistantMessage || assistant.Message.Content != "first final" {
		t.Fatalf("committed event after initial user = %+v, want first final assistant before reasoning/queued rows; all=%+v", assistant, committedEvents)
	}
	assertRuntimeEventsAdvanceCommittedFrontierContiguously(t, committedEvents)
}

func TestDirectUserMessageWithQueuedInjectionPublishesUserBeforeToolEvents(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "working", Phase: llm.MessagePhaseCommentary},
			ToolCalls: []llm.ToolCall{{
				ID:    "call_shell_1",
				Name:  string(toolspec.ToolExecCommand),
				Input: json.RawMessage(`{"command":"pwd"}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	events := make([]Event, 0, 16)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		OnEvent: func(evt Event) {
			events = append(events, evt)
		},
	})

	eng.QueueUserMessage("queued steer")
	if _, err := eng.SubmitUserMessage(context.Background(), "start"); err != nil {
		t.Fatalf("submit: %v", err)
	}

	committedEvents := committedTranscriptEventsWithEntries(events)
	assertRuntimeEventsAdvanceCommittedFrontierContiguously(t, committedEvents)
	startIdx := -1
	toolCompleteIdx := -1
	for idx, evt := range committedEvents {
		if evt.Kind == EventUserMessageFlushed && evt.UserMessage == "start" {
			startIdx = idx
		}
		if evt.Kind == EventToolCallCompleted {
			toolCompleteIdx = idx
		}
	}
	if startIdx < 0 {
		t.Fatalf("expected direct user message committed event, got %+v", committedEvents)
	}
	if toolCompleteIdx < 0 {
		t.Fatalf("expected tool completion committed event, got %+v", committedEvents)
	}
	if startIdx > toolCompleteIdx {
		t.Fatalf("direct user message must publish before tool completion, user_idx=%d tool_complete_idx=%d events=%+v", startIdx, toolCompleteIdx, committedEvents)
	}
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
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
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

func TestQueuedUserMessageFlushDoesNotEmitConversationUpdatedForInjectedMessage(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "after flush"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	var (
		events     []Event
		eventIndex int
		flushIndex = -1
	)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		OnEvent: func(evt Event) {
			events = append(events, evt)
			eventIndex++
			if evt.Kind == EventUserMessageFlushed && evt.UserMessage == "steer now" && flushIndex < 0 {
				flushIndex = eventIndex
			}
		},
	})

	eng.QueueUserMessage("steer now")
	if _, err := eng.SubmitUserMessage(context.Background(), "start"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if flushIndex < 0 {
		t.Fatal("expected user_message_flushed event")
	}
	if got := committedConversationUpdatedCountAfterLastUserFlush(events); got != 0 {
		t.Fatalf("committed conversation_updated count after injected user flush = %d, want 0; events=%+v", got, events)
	}
}

func TestDirectUserMessageFlushDoesNotEmitConversationUpdated(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done"},
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

func TestQueuedUserMessagesCoalesceIntoSingleFlush(t *testing.T) {
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "after flush"},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}

	var (
		queuedFlushCount int
		flushed          Event
	)
	eng := mustNewTestEngine(t, mustCreateTestSession(t), client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		OnEvent: func(evt Event) {
			if evt.Kind == EventUserMessageFlushed && len(evt.UserMessageBatch) == 2 {
				queuedFlushCount++
				flushed = evt
			}
		},
	})

	eng.QueueUserMessage("steer now")
	eng.QueueUserMessage("and keep tests focused")
	msg, err := eng.SubmitUserMessage(context.Background(), "start")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if msg.Content != "after flush" {
		t.Fatalf("assistant content = %q, want after flush", msg.Content)
	}
	if flushed.UserMessage != "steer now\n\nand keep tests focused" {
		t.Fatalf("unexpected flushed user message %q", flushed.UserMessage)
	}
	if len(flushed.UserMessageBatch) != 2 {
		t.Fatalf("expected two flushed user messages in batch, got %+v", flushed.UserMessageBatch)
	}
	if queuedFlushCount != 1 {
		t.Fatalf("expected one coalesced queued flush event, got %d", queuedFlushCount)
	}
	if len(client.calls) < 2 {
		t.Fatalf("expected at least 2 model calls, got %d", len(client.calls))
	}
	second := client.calls[1]
	userMessages := make([]llm.Message, 0, len(requestMessages(second)))
	for _, m := range requestMessages(second) {
		if m.Role == llm.RoleUser {
			userMessages = append(userMessages, m)
		}
	}
	if len(userMessages) < 2 {
		t.Fatalf("expected initial and flushed user messages, got %+v", requestMessages(second))
	}
	last := userMessages[len(userMessages)-1]
	if last.Content != "steer now\n\nand keep tests focused" {
		t.Fatalf("expected coalesced flushed user message, got %+v", userMessages)
	}
}

func TestRequestMessagesPreserveANSIEscapes(t *testing.T) {
	seedContent := "raw \x1b[31mansi\x1b[0m"
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "ok"},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}

	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: seedContent}})); err != nil {
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
			if msg.Role == llm.RoleUser && msg.Content == seedContent {
				foundSeed = true
			}
		}
		if !foundSeed {
			t.Fatalf("expected request messages to preserve exact seeded ANSI message %q, messages=%+v", seedContent, requestMessages(req))
		}
	}
}

func TestReasoningSummaryVisibleAndEncryptedReasoningRoundTrips(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "first"},
			Reasoning: []llm.ReasoningEntry{
				{Role: "reasoning", Text: "Plan summary"},
			},
			ReasoningItems: []llm.ReasoningItem{
				{ID: "rs_1", EncryptedContent: "enc_1"},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "second"},
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
		if msg.Role != llm.RoleAssistant || msg.Content != "first" {
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
		if strings.Contains(msg.Content, "Plan summary") {
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

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	sawLocal := false
	for _, evt := range events {
		if evt.Kind != "local_entry" {
			continue
		}
		var entry storedLocalEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			t.Fatalf("decode local_entry: %v", err)
		}
		if entry.Role == "reasoning" && entry.Text == "Plan summary" {
			sawLocal = true
		}
	}
	if !sawLocal {
		t.Fatalf("expected persisted local_entry for reasoning summary, events=%+v", events)
	}
}

func TestDiscardQueuedUserMessageRemovesExactQueuedEntry(t *testing.T) {
	eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{})

	first := eng.QueueUserMessage("same")
	eng.QueueUserMessage("other")
	duplicate := eng.QueueUserMessage("same")

	if removed := eng.DiscardQueuedUserMessage(duplicate.ID); !removed {
		t.Fatal("expected duplicate queued item removed")
	}

	messageFlow := eng.messageFlow.(*defaultMessageLifecycle)
	var messages []QueuedUserMessage
	if messageFlow != nil && messageFlow.queue != nil {
		messages = messageFlow.queue.Snapshot()
	}
	if len(messages) != 2 || messages[0].ID != first.ID || messages[0].Text != "same" || messages[1].Text != "other" {
		t.Fatalf("unexpected pending queue after discard: %+v", messages)
	}
}

func TestContextUsageUsesLastUsageWhenAvailable(t *testing.T) {
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
	eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "estimate me"}})); err != nil {
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
	eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})

	if usage := eng.ContextUsage(); usage.HasCacheHitPercentage {
		t.Fatalf("expected cache hit percentage to be unavailable before model usage, got %+v", usage)
	}

	eng.setLastUsage(llm.Usage{InputTokens: 100, CachedInputTokens: 40, HasCachedInputTokens: true})
	eng.setLastUsage(llm.Usage{InputTokens: 300, CachedInputTokens: 60, HasCachedInputTokens: true})
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
	eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})
	eng.setLastUsage(llm.Usage{InputTokens: 100, OutputTokens: 0, WindowTokens: 410_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("x", 1600)}})); err != nil {
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
	eng := mustNewTestEngine(t, mustCreateTestSession(t), &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("seed-", 100)}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	checkpointEstimate := estimateItemsTokens(eng.transcriptRuntimeState().SnapshotItems())
	eng.setLastUsage(llm.Usage{InputTokens: 900, OutputTokens: 120, WindowTokens: 410_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("delta-", 40)}})); err != nil {
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

func TestReopenedSessionRestoresUsageCheckpointDeltaAccounting(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("seed-", 100)}})); err != nil {
		t.Fatalf("append seed message: %v", err)
	}
	checkpointEstimate := estimateItemsTokens(eng.transcriptRuntimeState().SnapshotItems())
	if err := eng.recordLastUsage(llm.Usage{InputTokens: 900, OutputTokens: 120, WindowTokens: 410_000, CachedInputTokens: 45, HasCachedInputTokens: true}); err != nil {
		t.Fatalf("record last usage: %v", err)
	}
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("delta-", 40)}})); err != nil {
		t.Fatalf("append delta message: %v", err)
	}

	reopenedStore := mustOpenTestSession(t, store.Dir())
	restored := mustNewTestEngine(t, reopenedStore, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})

	currentEstimate := estimateItemsTokens(restored.transcriptRuntimeState().SnapshotItems())
	deltaEstimate := currentEstimate - checkpointEstimate
	if deltaEstimate <= 0 {
		t.Fatalf("expected positive estimated delta after reopen, got checkpoint=%d current=%d", checkpointEstimate, currentEstimate)
	}
	usage := restored.ContextUsage()
	want := 900 + deltaEstimate
	if usage.UsedTokens != want {
		t.Fatalf("used tokens after reopen=%d, want baseline+delta %d", usage.UsedTokens, want)
	}
	if !usage.HasCacheHitPercentage || usage.CacheHitPercent != 5 {
		t.Fatalf("cache hit metadata after reopen=%+v, want 5%%", usage)
	}
}

func TestReopenedSessionRestoresLastAssistantFinalAnswerAcrossCompaction(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{
		{Role: llm.RoleUser, Content: "do the thing"},
		{Role: llm.RoleAssistant, Phase: llm.MessagePhaseFinal, Content: "the final answer"},
	})); err != nil {
		t.Fatalf("append messages: %v", err)
	}
	if got := eng.LastCommittedAssistantFinalAnswer(); got != "the final answer" {
		t.Fatalf("precondition: live final answer = %q, want %q", got, "the final answer")
	}

	if err := newCompactionPersistence(eng).replaceHistory("step-compact", "local", compactionModeManual, llm.ItemsFromMessages([]llm.Message{
		{Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary so far"},
	})); err != nil {
		t.Fatalf("replace history: %v", err)
	}
	if got := eng.LastCommittedAssistantFinalAnswer(); got != "the final answer" {
		t.Fatalf("post-compaction live final answer = %q, want preserved %q", got, "the final answer")
	}

	reopenedStore := mustOpenTestSession(t, store.Dir())
	restored := mustNewTestEngine(t, reopenedStore, &fakeClient{}, tools.NewRegistry(), Config{})
	if got := restored.LastCommittedAssistantFinalAnswer(); got != "the final answer" {
		t.Fatalf("restored final answer after compaction = %q, want %q", got, "the final answer")
	}
}

func TestHistoryReplacementResetsDiagnosticDedupe(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})
	if err := eng.steerPersistedDiagnosticEntry("step-1", preciseTokenCountFailureDiagnostic, "error", "first fallback"); err != nil {
		t.Fatalf("append first diagnostic: %v", err)
	}
	if err := newCompactionPersistence(eng).replaceHistory("step-compact", "local", compactionModeManual, llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleAssistant, Content: "summary"}})); err != nil {
		t.Fatalf("replace history: %v", err)
	}
	if err := eng.steerPersistedDiagnosticEntry("step-2", preciseTokenCountFailureDiagnostic, "error", "second fallback"); err != nil {
		t.Fatalf("append second diagnostic: %v", err)
	}

	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	count := 0
	for _, evt := range events {
		if evt.Kind != "local_entry" {
			continue
		}
		var entry storedLocalEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			t.Fatalf("decode local entry: %v", err)
		}
		if entry.DiagnosticKey == preciseTokenCountFailureDiagnostic {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("diagnostic entry count=%d, want 2", count)
	}
}

func TestReopenedSessionHistoryReplacementResetsDiagnosticDedupe(t *testing.T) {
	store := mustCreateTestSession(t)
	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})
	if err := eng.steerPersistedDiagnosticEntry("step-1", preciseTokenCountFailureDiagnostic, "error", "first fallback"); err != nil {
		t.Fatalf("append first diagnostic: %v", err)
	}
	if err := newCompactionPersistence(eng).replaceHistory("step-compact", "local", compactionModeManual, llm.ItemsFromMessages([]llm.Message{{Role: llm.RoleAssistant, Content: "summary"}})); err != nil {
		t.Fatalf("replace history: %v", err)
	}

	reopenedStore := mustOpenTestSession(t, store.Dir())
	restored := mustNewTestEngine(t, reopenedStore, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{ContextWindowTokens: 410_000})
	if err := restored.steerPersistedDiagnosticEntry("step-2", preciseTokenCountFailureDiagnostic, "error", "second fallback"); err != nil {
		t.Fatalf("append second diagnostic after reopen: %v", err)
	}

	events, err := sessiontest.CollectEvents(reopenedStore)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	count := 0
	for _, evt := range events {
		if evt.Kind != "local_entry" {
			continue
		}
		var entry storedLocalEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			t.Fatalf("decode local entry: %v", err)
		}
		if entry.DiagnosticKey == preciseTokenCountFailureDiagnostic {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("diagnostic entry count after reopen=%d, want 2", count)
	}
}

func TestEstimateItemsTokensDoesNotTreatInlineImagePayloadAsPlainText(t *testing.T) {
	base64Payload := strings.Repeat("A", 24_000)
	item := llm.ResponseItem{
		Type:   llm.ResponseItemTypeFunctionCallOutput,
		Name:   string(toolspec.ToolViewImage),
		CallID: "call-1",
		Output: json.RawMessage(`[{"type":"input_image","image_url":"data:image/png;base64,` + base64Payload + `"}]`),
	}

	estimated := estimateItemsTokens([]llm.ResponseItem{item})
	naive := (len(item.Name) + len(item.CallID) + len(item.Output) + 3) / 4
	if estimated <= 0 {
		t.Fatalf("expected multimodal estimate > 0, got %d", estimated)
	}
	if estimated >= naive/4 {
		t.Fatalf("expected multimodal estimate to stay well below plain-text estimate, got estimated=%d naive=%d", estimated, naive)
	}
}

func TestContextUsageDoesNotInflateInlineImagePayloadByBase64Length(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{Model: "gpt-5", ContextWindowTokens: 410_000})
	eng.setLastUsage(llm.Usage{InputTokens: 100, OutputTokens: 0, WindowTokens: 410_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{
		Role:       llm.RoleTool,
		ToolCallID: "call-1",
		Name:       string(toolspec.ToolViewImage),
		Content:    `[{"type":"input_image","image_url":"data:image/png;base64,` + strings.Repeat("A", 24_000) + `"}]`,
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

func TestShouldAutoCompactAccountsForMessagesAppendedAfterLastUsage(t *testing.T) {
	store := mustCreateTestSession(t)

	eng := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   410_000,
		AutoCompactTokenLimit: 300,
	})
	eng.setLastUsage(llm.Usage{InputTokens: 120, OutputTokens: 0, WindowTokens: 410_000})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("stale-usage-gap-", 120)}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	if !eng.shouldAutoCompactWithContext(context.Background()) {
		t.Fatalf("expected auto compaction to trigger from appended message growth")
	}
}

func TestShouldAutoCompactUsesPreciseRequestInputTokenCountWhenAvailable(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{inputTokenCount: 960, contextWindow: 1000}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   400_000,
		AutoCompactTokenLimit: 900,
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: "short"}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	if !eng.shouldAutoCompactWithContext(context.Background()) {
		t.Fatalf("expected auto compaction to trigger from precise input token count")
	}
}

func TestPreSubmitCompactionTokenLimitUsesFixedRunwayReserve(t *testing.T) {
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

func TestShouldCompactBeforeUserMessageUsesPromptGrowthBelowPreSubmitBand(t *testing.T) {
	store := mustCreateTestSession(t)

	client := &preciseCompactionClient{inputTokenCount: 960, contextWindow: 1000}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:                         "gpt-5",
		AutoCompactTokenLimit:         950,
		ContextWindowTokens:           1000,
		PreSubmitCompactionLeadTokens: 50,
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("a", 3400)}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	shouldCompact, err := eng.ShouldCompactBeforeUserMessage(context.Background(), strings.Repeat("b", 400))
	if err != nil {
		t.Fatalf("ShouldCompactBeforeUserMessage: %v", err)
	}
	if !shouldCompact {
		t.Fatal("expected pre-submit compaction when prompt growth would cross the real threshold")
	}
	if client.countCalls == 0 {
		t.Fatal("expected precise request token count to be used for prompt-growth check")
	}
}

func TestShouldCompactBeforeUserMessageFallsBackWhenExactCountUnsupported(t *testing.T) {
	store := mustCreateTestSession(t)

	supported := false
	client := &preciseCompactionClient{inputTokenCount: 960, contextWindow: 1000, countSupported: &supported}
	eng := mustNewTestEngine(t, store, client, tools.NewRegistry(), Config{
		Model:                         "gpt-5",
		AutoCompactTokenLimit:         950,
		ContextWindowTokens:           1000,
		PreSubmitCompactionLeadTokens: 50,
	})
	if err := eng.steer("", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventDefault, true, []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("a", 3400)}})); err != nil {
		t.Fatalf("append message: %v", err)
	}

	shouldCompact, err := eng.ShouldCompactBeforeUserMessage(context.Background(), strings.Repeat("b", 400))
	if err != nil {
		t.Fatalf("ShouldCompactBeforeUserMessage: %v", err)
	}
	if !shouldCompact {
		t.Fatal("expected fallback estimator to trigger pre-submit compaction when exact counting is unsupported")
	}
	if client.countCalls != 0 {
		t.Fatalf("count calls=%d, want 0 when exact counting is unsupported", client.countCalls)
	}
}
