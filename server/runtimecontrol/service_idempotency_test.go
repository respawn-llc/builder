package runtimecontrol

import (
	"context"
	"testing"

	"core/server/llm"
	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/serverapi"
	"core/shared/transcript"
)

var runtimeControlOpenAICapabilities = llm.ProviderCapabilities{
	ProviderID:               "openai",
	SupportsResponsesAPI:     true,
	SupportsResponsesCompact: true,
	IsOpenAIFirstParty:       true,
}

func TestServiceSetThinkingLevelDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeSetThinkingLevelRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Level: "high"}

	if err := service.SetThinkingLevel(context.Background(), req); err != nil {
		t.Fatalf("SetThinkingLevel first: %v", err)
	}
	if err := service.SetThinkingLevel(context.Background(), req); err != nil {
		t.Fatalf("SetThinkingLevel replay: %v", err)
	}
	if got := engine.ThinkingLevel(); got != "high" {
		t.Fatalf("thinking level = %q, want high", got)
	}
}

func TestServiceSetFastModeEnabledDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5", ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeSetFastModeEnabledRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Enabled: true}

	first, err := service.SetFastModeEnabled(context.Background(), req)
	if err != nil {
		t.Fatalf("SetFastModeEnabled first: %v", err)
	}
	second, err := service.SetFastModeEnabled(context.Background(), req)
	if err != nil {
		t.Fatalf("SetFastModeEnabled replay: %v", err)
	}
	if first != second {
		t.Fatalf("responses = (%+v, %+v), want identical replay", first, second)
	}
	if !engine.FastModeEnabled() {
		t.Fatal("expected fast mode to remain enabled")
	}
}

func TestServiceSetReviewerEnabledDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5", Reviewer: runtime.ReviewerConfig{Model: "gpt-5", ClientFactory: func() (llm.Client, error) { return &runtimeControlFakeClient{}, nil }}})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeSetReviewerEnabledRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Enabled: true}

	first, err := service.SetReviewerEnabled(context.Background(), req)
	if err != nil {
		t.Fatalf("SetReviewerEnabled first: %v", err)
	}
	second, err := service.SetReviewerEnabled(context.Background(), req)
	if err != nil {
		t.Fatalf("SetReviewerEnabled replay: %v", err)
	}
	if first != second {
		t.Fatalf("responses = (%+v, %+v), want identical replay", first, second)
	}
	if got := engine.ReviewerFrequency(); got != "edits" {
		t.Fatalf("reviewer frequency = %q, want edits", got)
	}
}

func TestServiceSetAutoCompactionEnabledDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeSetAutoCompactionEnabledRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Enabled: false}

	first, err := service.SetAutoCompactionEnabled(context.Background(), req)
	if err != nil {
		t.Fatalf("SetAutoCompactionEnabled first: %v", err)
	}
	second, err := service.SetAutoCompactionEnabled(context.Background(), req)
	if err != nil {
		t.Fatalf("SetAutoCompactionEnabled replay: %v", err)
	}
	if first != second {
		t.Fatalf("responses = (%+v, %+v), want identical replay", first, second)
	}
	if engine.AutoCompactionEnabled() {
		t.Fatal("expected auto compaction to remain disabled")
	}
}

func TestServiceCompactContextDedupesSuccessfulRetry(t *testing.T) {
	store, engine, client := newRuntimeControlCompactionFixture(t)
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeCompactContextRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Args: "compact now"}

	if err := service.CompactContext(context.Background(), req); err != nil {
		t.Fatalf("CompactContext first: %v", err)
	}
	if err := service.CompactContext(context.Background(), req); err != nil {
		t.Fatalf("CompactContext replay: %v", err)
	}
	if client.compactionCalls != 1 {
		t.Fatalf("compaction call count = %d, want 1", client.compactionCalls)
	}
	if got := countEventsByKind(t, store, "history_replaced"); got != 1 {
		t.Fatalf("history_replaced event count = %d, want 1", got)
	}
}

func TestServiceCompactContextForPreSubmitDedupesSuccessfulRetry(t *testing.T) {
	store, engine, client := newRuntimeControlCompactionFixture(t)
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeCompactContextForPreSubmitRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID}

	if err := service.CompactContextForPreSubmit(context.Background(), req); err != nil {
		t.Fatalf("CompactContextForPreSubmit first: %v", err)
	}
	if err := service.CompactContextForPreSubmit(context.Background(), req); err != nil {
		t.Fatalf("CompactContextForPreSubmit replay: %v", err)
	}
	if client.compactionCalls != 1 {
		t.Fatalf("compaction call count = %d, want 1", client.compactionCalls)
	}
	if got := countEventsByKind(t, store, "history_replaced"); got != 1 {
		t.Fatalf("history_replaced event count = %d, want 1", got)
	}
}

func TestServiceInterruptDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeInterruptRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID}

	if err := service.Interrupt(context.Background(), req); err != nil {
		t.Fatalf("Interrupt first: %v", err)
	}
	if err := service.Interrupt(context.Background(), req); err != nil {
		t.Fatalf("Interrupt replay: %v", err)
	}
}

func newRuntimeControlCompactionFixture(t *testing.T) (*session.Store, *runtime.Engine, *runtimeControlFakeClient) {
	t.Helper()
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	client := &runtimeControlFakeClient{
		responses: []llm.Response{{
			Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
			Usage:     llm.Usage{WindowTokens: 200000},
		}},
		compactionResponses: []llm.CompactionResponse{{
			OutputItems: []llm.ResponseItem{
				{Type: llm.ResponseItemTypeMessage, Role: llm.RoleUser, MessageType: llm.MessageTypeCompactionSummary, Content: "summary"},
				{Type: llm.ResponseItemTypeCompaction, EncryptedContent: "checkpoint"},
			},
			Usage:             llm.Usage{WindowTokens: 200000},
			TrimmedItemsCount: 1,
		}},
	}
	engine, err := runtime.New(store, client, tools.NewRegistry(), runtime.Config{Model: "gpt-5", ProviderCapabilitiesOverride: &runtimeControlOpenAICapabilities})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	if _, err := engine.SubmitUserMessage(context.Background(), "hello"); err != nil {
		t.Fatalf("seed runtime transcript: %v", err)
	}
	return store, engine, client
}

func countEventsByKind(t *testing.T, store *session.Store, kind string) int {
	t.Helper()
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	count := 0
	for _, evt := range events {
		if evt.Kind == kind {
			count++
		}
	}
	return count
}

func TestServiceAppendCommittedEntryDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeAppendCommittedEntryRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Role: "warning", Text: "be careful"}

	if err := service.AppendCommittedEntry(context.Background(), req); err != nil {
		t.Fatalf("AppendCommittedEntry first: %v", err)
	}
	if err := service.AppendCommittedEntry(context.Background(), req); err != nil {
		t.Fatalf("AppendCommittedEntry replay: %v", err)
	}
	count := 0
	for _, entry := range engine.RecentTailTranscriptWindow(1 << 20).Snapshot.Entries {
		if entry.Role == "warning" && entry.Text == "be careful" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("local entry count = %d, want 1", count)
	}
}

func TestServiceAppendCommittedEntryReplaysVisibility(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeAppendCommittedEntryRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Role: "warning", Text: "visible warning", Visibility: string(transcript.EntryVisibilityAll)}

	if err := service.AppendCommittedEntry(context.Background(), req); err != nil {
		t.Fatalf("AppendCommittedEntry first: %v", err)
	}
	if err := service.AppendCommittedEntry(context.Background(), req); err != nil {
		t.Fatalf("AppendCommittedEntry replay: %v", err)
	}
	count := 0
	for _, entry := range engine.RecentTailTranscriptWindow(1 << 20).Snapshot.Entries {
		if entry.Role == "warning" && entry.Text == "visible warning" {
			count++
			if entry.Visibility != transcript.EntryVisibilityAll {
				t.Fatalf("entry visibility = %q, want all", entry.Visibility)
			}
		}
	}
	if count != 1 {
		t.Fatalf("visible warning entry count = %d, want 1", count)
	}
}

func TestServiceSubmitQueuedUserMessagesDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	client := &runtimeControlFakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: "done", Phase: llm.MessagePhaseFinal},
		Usage:     llm.Usage{WindowTokens: 200000},
	}}}
	engine, err := runtime.New(store, client, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	engine.QueueUserMessage("hello")
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeSubmitQueuedUserMessagesRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID}

	first, err := service.SubmitQueuedUserMessages(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitQueuedUserMessages first: %v", err)
	}
	second, err := service.SubmitQueuedUserMessages(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitQueuedUserMessages replay: %v", err)
	}
	if first != second {
		t.Fatalf("responses = (%+v, %+v), want identical replay", first, second)
	}
	if client.calls != 1 {
		t.Fatalf("generate call count = %d, want 1", client.calls)
	}
	if got := countUserMessagesWithContent(t, store, "hello"); got != 1 {
		t.Fatalf("queued user flush count = %d, want 1", got)
	}
}

func TestServiceDiscardQueuedUserMessageDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	firstQueued := engine.QueueUserMessage("same")
	otherQueued := engine.QueueUserMessage("other")
	duplicateQueued := engine.QueueUserMessage("same")
	service := NewService(stubRuntimeResolver{engine: engine})
	req := serverapi.RuntimeDiscardQueuedUserMessageRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, QueueItemID: duplicateQueued.ID}

	first, err := service.DiscardQueuedUserMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("DiscardQueuedUserMessage first: %v", err)
	}
	second, err := service.DiscardQueuedUserMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("DiscardQueuedUserMessage replay: %v", err)
	}
	if !first.Discarded || !second.Discarded {
		t.Fatalf("discard results = (%t, %t), want both true", first.Discarded, second.Discarded)
	}
	if !engine.DiscardQueuedUserMessage(firstQueued.ID) {
		t.Fatal("expected first duplicate text item to remain")
	}
	if !engine.DiscardQueuedUserMessage(otherQueued.ID) {
		t.Fatal("expected other queued item to remain")
	}
	if engine.DiscardQueuedUserMessage(duplicateQueued.ID) {
		t.Fatal("did not expect discarded queue item to remain")
	}
}

func TestServiceRecordPromptHistoryDedupesSuccessfulRetry(t *testing.T) {
	store, err := session.Create(t.TempDir(), "workspace-x", "/tmp/workspace-x")
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}
	engine, err := runtime.New(store, &runtimeControlFakeClient{}, tools.NewRegistry(), runtime.Config{Model: "gpt-5"})
	if err != nil {
		t.Fatalf("create runtime engine: %v", err)
	}
	history := newRuntimeControlPromptHistoryStore(store.Meta().SessionID)
	service := NewService(stubRuntimeResolver{engine: engine}).WithPromptHistoryStore(history)
	req := serverapi.RuntimeRecordPromptHistoryRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Text: "/resume"}

	if err := service.RecordPromptHistory(context.Background(), req); err != nil {
		t.Fatalf("RecordPromptHistory first: %v", err)
	}
	if err := service.RecordPromptHistory(context.Background(), req); err != nil {
		t.Fatalf("RecordPromptHistory replay: %v", err)
	}
	if got := countPromptHistoryEvents(t, store, "/resume"); got != 1 {
		t.Fatalf("prompt history count = %d, want 1", got)
	}
}

func countPromptHistoryEvents(t *testing.T, store *session.Store, text string) int {
	t.Helper()
	registered, ok := runtimeControlPromptHistoryStores.Load(store.Meta().SessionID)
	if !ok {
		return 0
	}
	history := registered.(*runtimeControlPromptHistoryStore)
	history.mu.Lock()
	defer history.mu.Unlock()
	count := 0
	for _, record := range history.records {
		if record.Text == text {
			count++
		}
	}
	return count
}
