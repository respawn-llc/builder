package runtime

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"core/server/llm"
	"core/server/tools"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

func TestSubmitUserMessageDoesNotEmitCommittedConversationUpdatedAfterFlushedUserTurn(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	events := make([]Event, 0, 16)
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if got := committedConversationUpdatedCountAfterLastUserFlush(events); got != 0 {
		t.Fatalf("committed conversation_updated count after user flush = %d, want 0; events=%+v", got, events)
	}
}

func TestSubmitUserMessageWithToolCallDoesNotEmitCommittedConversationUpdatedAfterUserFlush(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("working"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{ID: "call-1", Name: string(toolspec.ToolExecCommand), Input: json.RawMessage(`{"command":"pwd"}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	events := make([]Event, 0, 32)
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:   "gpt-5",
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "run tool"); err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if got := committedConversationUpdatedCountAfterLastUserFlush(events); got != 0 {
		t.Fatalf("committed conversation_updated count after user flush = %d, want 0; events=%+v", got, events)
	}
	if !hasEventKind(events, EventToolCallCompleted) {
		t.Fatalf("expected tool_call_completed event, got %+v", events)
	}
	if !hasEventKind(events, EventAssistantMessage) {
		t.Fatalf("expected assistant_message event, got %+v", events)
	}
}

func TestPatchToolCallStartedUsesTranscriptWorkingDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := mustCreateNamedTestSessionAt(t, dir, "ws", "/main")
	patchText := "*** Begin Patch\n*** Add File: probe.txt\n+hello\n*** End Patch\n"
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("patching"), Phase: textutil.Value(llm.MessagePhaseCommentary)},
			ToolCalls: []llm.ToolCall{{ID: "call-patch", Name: string(toolspec.ToolPatch), Input: json.RawMessage(`{"patch":` + strconv.Quote(patchText) + `}`)}},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	var started *transcript.ToolCallMeta
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolPatch, Handler: fakeTool{name: toolspec.ToolPatch}}), Config{
		Model:                "gpt-5",
		TranscriptWorkingDir: "/worktree",
		OnEvent: func(evt Event) {
			if evt.Kind == EventToolCallStarted && evt.ToolCall != nil {
				started = decodeToolCallMeta(*evt.ToolCall)
			}
		},
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "apply patch"); err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if started == nil || started.PatchRender == nil {
		t.Fatalf("expected patch render metadata, got %+v", started)
	}
	detail := started.PatchDetail
	if !strings.Contains(detail, "/worktree/probe.txt") {
		t.Fatalf("expected worktree path in patch detail, got %q", detail)
	}
	if strings.Contains(detail, "/main/probe.txt") {
		t.Fatalf("did not expect main workspace path in patch detail, got %q", detail)
	}
}

func TestHostedToolOnlyTurnUsesCommittedToolCompletionBeforeFollowUpAssistantMessage(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant: llm.Message{Role: llm.RoleAssistant},
			OutputItems: []llm.ResponseItem{{
				Type: llm.ResponseItemTypeOther,
				Raw:  json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kent cli"}}`),
			}},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = llm.ProviderCapabilities{
		ProviderID:                    "openai",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsNativeWebSearch:       true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}
	events := make([]Event, 0, 24)
	autoCompactionEnabled := false
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		WebSearchMode:         "native",
		EnabledTools:          []toolspec.ID{toolspec.ToolWebSearch},
		AutoCompactionEnabled: &autoCompactionEnabled,
		OnEvent:               func(evt Event) { events = append(events, evt) },
	})
	msg, err := eng.SubmitUserMessage(context.Background(), "find latest")
	if err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if got := committedConversationUpdatedCountAfterLastUserFlush(events); got != 0 {
		t.Fatalf("committed conversation_updated count after hosted-tool-only turn = %d, want 0; events=%+v", got, events)
	}
	if !hasEventKind(events, EventToolCallCompleted) {
		t.Fatalf("expected committed hosted tool completion event, got %+v", events)
	}
	if !hasEventKind(events, EventAssistantMessage) {
		t.Fatalf("expected assistant message event after hosted-tool-only turn, got %+v", events)
	}
}

func TestHostedToolOnlyMissingPhaseTurnUsesCommittedToolCompletion(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	client := &fakeClient{responses: []llm.Response{
		{
			Assistant:     llm.Message{Role: llm.RoleAssistant},
			ProviderPhase: llm.AbsentProviderPhase(),
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: textutil.Value(llm.RoleAssistant), Content: textutil.Value("working")},
				{Type: llm.ResponseItemTypeOther, Raw: json.RawMessage(`{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"kent cli"}}`)},
			},
			Usage: llm.Usage{WindowTokens: 200000},
		},
		{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done"), Phase: textutil.Value(llm.MessagePhaseFinal)},
			Usage:     llm.Usage{WindowTokens: 200000},
		},
	}}
	client.caps = llm.ProviderCapabilities{
		ProviderID:                    "openai",
		SupportsResponsesAPI:          true,
		SupportsResponsesCompact:      true,
		SupportsNativeWebSearch:       true,
		SupportsReasoningEncrypted:    true,
		SupportsServerSideContextEdit: true,
		IsOpenAIFirstParty:            true,
	}
	events := make([]Event, 0, 24)
	autoCompactionEnabled := false
	eng := mustNewTestEngine(t, store, client, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model:                 "gpt-5",
		WebSearchMode:         "native",
		EnabledTools:          []toolspec.ID{toolspec.ToolWebSearch},
		AutoCompactionEnabled: &autoCompactionEnabled,
		OnEvent:               func(evt Event) { events = append(events, evt) },
	})
	msg, err := eng.SubmitUserMessage(context.Background(), "find latest")
	if err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if messageContent(msg) != "done" {
		t.Fatalf("assistant content = %q, want done", messageContent(msg))
	}
	if got := committedConversationUpdatedCountAfterLastUserFlush(events); got != 0 {
		t.Fatalf("committed conversation_updated count after missing-phase hosted-only turn = %d, want 0; events=%+v", got, events)
	}
	if !hasEventKind(events, EventToolCallCompleted) {
		t.Fatalf("expected committed hosted tool completion event, got %+v", events)
	}
}

func TestReviewerTranscriptPathsUseRichEventsWithoutCommittedConversationUpdatedAfterUserFlush(t *testing.T) {
	t.Parallel()
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
	events := make([]Event, 0, 48)
	eng := mustNewTestEngine(t, store, mainClient, newTestToolRegistry(t, tools.HandlerRegistration{ID: toolspec.ToolExecCommand, Handler: fakeTool{name: toolspec.ToolExecCommand}}), Config{
		Model: "gpt-5",
		Reviewer: ReviewerConfig{
			Frequency:     "all",
			Model:         "gpt-5",
			ThinkingLevel: "low",
			VerboseOutput: true,
			Client:        reviewerClient,
		},
		OnEvent: func(evt Event) { events = append(events, evt) },
	})
	if _, err := eng.SubmitUserMessage(context.Background(), "do the task"); err != nil {
		t.Fatalf("submit user message: %v", err)
	}
	if got := committedConversationUpdatedCountAfterLastUserFlush(events); got != 0 {
		t.Fatalf("committed conversation_updated count after user flush = %d, want 0; events=%+v", got, events)
	}
	hasFeedback := false
	for _, event := range events {
		if event.LocalEntry != nil && event.LocalEntry.ReviewerFeedback != nil {
			hasFeedback = true
		}
	}
	if !hasFeedback {
		t.Fatalf("expected typed Reviewer feedback event, got %+v", events)
	}
	if !hasEventKind(events, EventRuntimeActivityChanged) {
		t.Fatalf("expected Runtime activity change event, got %+v", events)
	}
	for _, evt := range events {
		if evt.Kind != EventRuntimeActivityChanged {
			continue
		}
		if evt.CommittedTranscriptChanged {
			t.Fatalf("expected Runtime activity change to avoid committed transcript advancement, got %+v", evt)
		}
		if got := TranscriptEntriesFromEvent(evt); len(got) != 0 {
			t.Fatalf("expected Runtime activity change transcript entries to be empty, got %+v", got)
		}
	}
}

func committedConversationUpdatedCountAfterLastUserFlush(events []Event) int {
	start := 0
	for idx, evt := range events {
		if evt.Kind == EventUserMessageFlushed {
			start = idx
		}
	}
	count := 0
	for _, evt := range events[start:] {
		if evt.Kind == EventConversationUpdated && evt.CommittedTranscriptChanged && len(TranscriptEntriesFromEvent(evt)) == 0 {
			count++
		}
	}
	return count
}

func hasEventKind(events []Event, kind EventKind) bool {
	for _, evt := range events {
		if evt.Kind == kind {
			return true
		}
	}
	return false
}

func hasReviewerLocalEntryRole(events []Event, role string) bool {
	for _, evt := range events {
		if evt.Kind != EventLocalEntryAdded || evt.LocalEntry == nil {
			continue
		}
		if evt.LocalEntry.Role == role {
			return true
		}
	}
	return false
}
