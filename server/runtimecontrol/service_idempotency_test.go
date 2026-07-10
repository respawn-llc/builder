package runtimecontrol

import (
	"context"
	"encoding/json"
	"testing"

	"core/server/llm"
	"core/server/runtime"
	"core/server/runtimeactivity"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/shared/clientui"
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

type sequenceRuntimeActivityResolver struct {
	snapshots []runtimeactivity.ResponseSnapshot
	calls     int
}

func (r *sequenceRuntimeActivityResolver) Snapshot(context.Context, string, []clientui.RuntimeOperationRef) (runtimeactivity.ResponseSnapshot, error) {
	if r.calls >= len(r.snapshots) {
		return r.snapshots[len(r.snapshots)-1], nil
	}
	snapshot := r.snapshots[r.calls]
	r.calls++
	return snapshot, nil
}

func TestServiceInterruptRetryReturnsFreshActivitySnapshot(t *testing.T) {
	runningVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 1}
	idleVersion := clientui.ReadModelVersion{Epoch: "epoch-1", Generation: 1, Sequence: 2}
	resolver := &sequenceRuntimeActivityResolver{snapshots: []runtimeactivity.ResponseSnapshot{
		{
			Version: runningVersion,
			Activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityRunning, clientui.RuntimeActivityOptions{
				ActiveKind: clientui.RuntimeActivityActiveKindGoalLoop,
				RunID:      "run-1",
				StepID:     "step-1",
			}),
			InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(runningVersion),
		},
		{
			Version: idleVersion,
			Activity: clientui.MustRuntimeActivity(clientui.RuntimeActivityRegisteredIdle, clientui.RuntimeActivityOptions{
				QueueAccepting: true,
			}),
			InputReconciliation: clientui.NewEmptyRuntimeInputReconciliationSnapshot(idleVersion),
		},
	}}
	service := NewService(stubRuntimeResolver{}).WithRuntimeActivityResolver(resolver)
	req := serverapi.RuntimeInterruptRequest{ClientRequestID: "interrupt-retry", SessionID: "session-1"}

	first, err := service.Interrupt(context.Background(), req)
	if err != nil {
		t.Fatalf("Interrupt first: %v", err)
	}
	second, err := service.Interrupt(context.Background(), req)
	if err != nil {
		t.Fatalf("Interrupt retry: %v", err)
	}
	if !first.Activity.ActiveForControl() {
		t.Fatalf("first activity = %+v, want active", first.Activity)
	}
	if second.Activity.ActiveForControl() || second.Version != idleVersion {
		t.Fatalf("retry activity/version = %+v/%+v, want fresh idle %+v", second.Activity, second.Version, idleVersion)
	}
	if resolver.calls != 2 {
		t.Fatalf("snapshot calls = %d, want fresh composition on retry", resolver.calls)
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
	req := serverapi.RuntimeCompactContextRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, Args: "compact now", OperationRef: clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindCompact, ClientRequestID: "req-1"}}

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
	req := serverapi.RuntimeCompactContextForPreSubmitRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, OperationRef: clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindPreSubmitCompact, ClientRequestID: "req-1"}}

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

	if _, err := service.Interrupt(context.Background(), req); err != nil {
		t.Fatalf("Interrupt first: %v", err)
	}
	if _, err := service.Interrupt(context.Background(), req); err != nil {
		t.Fatalf("Interrupt replay: %v", err)
	}
}

func newRuntimeControlCompactionFixture(t *testing.T) (*session.Store, *runtime.Engine, *runtimeControlFakeClient) {
	t.Helper()
	trimmed := 1
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
			TrimmedItemsCount: &trimmed,
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

func localEntryEvents(t *testing.T, store *session.Store) []runtime.ChatEntry {
	t.Helper()
	events, err := sessiontest.CollectEvents(store)
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	entries := make([]runtime.ChatEntry, 0)
	for _, evt := range events {
		if evt.Kind != "local_entry" {
			continue
		}
		var entry runtime.ChatEntry
		if err := json.Unmarshal(evt.Payload, &entry); err != nil {
			t.Fatalf("decode local_entry: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
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
	for _, entry := range localEntryEvents(t, store) {
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
	for _, entry := range localEntryEvents(t, store) {
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
	req := serverapi.RuntimeSubmitQueuedUserMessagesRequest{ClientRequestID: "req-1", SessionID: store.Meta().SessionID, OperationRef: clientui.RuntimeOperationRef{Kind: clientui.RuntimeOperationKindSubmitQueued, ClientRequestID: "req-1"}}

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
