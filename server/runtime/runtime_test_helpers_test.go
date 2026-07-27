package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/filemode"
	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	shelltool "core/server/tools/shell"
	"core/server/workflowruntime"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/toolspec"
	"core/shared/transcript"
)

type testPersistedEvent struct {
	Kind   string
	Record session.EventRecord
}

func withGenerateRetryDelays(t *testing.T, delays []time.Duration) {
	t.Helper()
	previous := generateRetryDelays
	generateRetryDelays = append([]time.Duration(nil), delays...)
	t.Cleanup(func() {
		generateRetryDelays = previous
	})
}

type blockingStepLifecycleSink struct {
	endedStarted chan StepLifecycleSnapshot
	releaseEnded chan struct{}
}

func newBlockingStepLifecycleSink() *blockingStepLifecycleSink {
	return &blockingStepLifecycleSink{
		endedStarted: make(chan StepLifecycleSnapshot, 1),
		releaseEnded: make(chan struct{}),
	}
}

func (s *blockingStepLifecycleSink) StepBegan(context.Context, StepLifecycleSnapshot) error {
	return nil
}

func (s *blockingStepLifecycleSink) StepEnded(_ context.Context, snapshot StepLifecycleSnapshot) error {
	s.endedStarted <- snapshot
	<-s.releaseEnded
	return nil
}

func fakeClientCallCount(client *fakeClient) int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return len(client.calls)
}

func waitEngineLifecycleTasks(t *testing.T, eng *Engine) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		eng.lifecycleWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for engine lifecycle tasks")
	}
}

func backgroundShellEventTypeForTest(eventType shelltool.EventType) BackgroundShellEventType {
	switch eventType {
	case shelltool.EventBackgrounded:
		return BackgroundShellEventBackgrounded
	case shelltool.EventCompleted:
		return BackgroundShellEventCompleted
	case shelltool.EventKilled:
		return BackgroundShellEventKilled
	default:
		panic("unknown shell event type in runtime test")
	}
}

func userMessageSeqAt(t *testing.T, store *session.Store, n int) int64 {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(10_000)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	visible := 0
	for _, evt := range window.Records {
		message, ok := mustSessionEventPayload(evt).(session.MessageRecord)
		if !ok || message.Role != session.MessageRoleUser {
			continue
		}
		visible++
		if visible == n {
			return evt.Seq()
		}
	}
	t.Fatalf("user message %d not found among %d events", n, len(window.Records))
	return 0
}

func mustCreateTestSession(t *testing.T, workspaceRoot ...string) *session.Store {
	t.Helper()
	root := t.TempDir()
	workspace := root
	if len(workspaceRoot) > 0 {
		workspace = workspaceRoot[0]
	}
	return mustCreateNamedTestSessionAt(t, root, "ws", workspace)
}

var runtimeTestSessionPersistence = sessiontest.NewPersistence()

type testPersistenceObserver struct {
	observer   session.PersistenceObserver
	reconciler *sessiontest.Persistence
}

func (o testPersistenceObserver) ObservePersistedStore(
	ctx context.Context,
	snapshot session.PersistedStoreSnapshot,
) error {
	return errors.Join(
		o.reconciler.ObservePersistedStore(ctx, snapshot),
		o.observer.ObservePersistedStore(ctx, snapshot),
	)
}

func (o testPersistenceObserver) ObserveEventLogReconciliation(
	ctx context.Context,
	reconciliation session.PersistedEventLogReconciliation,
) error {
	return o.reconciler.ObserveEventLogReconciliation(ctx, reconciliation)
}

func withRuntimeTestPersistenceObserver(
	observer session.PersistenceObserver,
) session.StoreOption {
	return session.WithPersistenceObserver(testPersistenceObserver{
		observer:   observer,
		reconciler: runtimeTestSessionPersistence,
	})
}

type testEventLogAppendBlocker = filemode.EventLogAppendBlocker

func blockTestEventLogAppends(store *session.Store) (*testEventLogAppendBlocker, error) {
	if store == nil {
		return nil, errors.New("event-log append blocker requires a session store")
	}
	return filemode.BlockEventLogAppends(filepath.Join(store.Dir(), "events.jsonl"))
}

func mustBlockTestEventLogAppends(t *testing.T, store *session.Store) *testEventLogAppendBlocker {
	t.Helper()
	if store == nil {
		t.Fatal("event-log append blocker requires a session store")
	}
	return filemode.MustBlockEventLogAppends(t, filepath.Join(store.Dir(), "events.jsonl"))
}

func appendRawCurrentEventLine(t *testing.T, store *session.Store, line []byte) {
	t.Helper()
	path := filepath.Join(store.Dir(), "events.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open persisted event log for raw append: %v", err)
	}
	if _, err := file.Write(append(append([]byte(nil), line...), '\n')); err != nil {
		_ = file.Close()
		t.Fatalf("append raw persisted event: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close persisted event log after raw append: %v", err)
	}
}

func mustCreateTestSessionAt(t *testing.T, root string, options ...session.StoreOption) *session.Store {
	t.Helper()
	return mustCreateNamedTestSessionAt(t, root, "ws", root, options...)
}

func mustCreateNamedTestSession(t *testing.T, workspaceContainerName string, workspaceRoot string, options ...session.StoreOption) *session.Store {
	t.Helper()
	return mustCreateNamedTestSessionAt(t, t.TempDir(), workspaceContainerName, workspaceRoot, options...)
}

func mustCreateNamedTestSessionAt(t *testing.T, root string, workspaceContainerName string, workspaceRoot string, options ...session.StoreOption) *session.Store {
	t.Helper()
	store, err := session.Create(root, workspaceContainerName, workspaceRoot, sessioncontract.SessionCategoryMain, append(runtimeTestSessionPersistence.Options(), options...)...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	initializeTestEventLog(t, store)
	return store
}

func initializeTestEventLog(t *testing.T, store *session.Store) {
	t.Helper()
	// Fresh runtime fixtures always need a materializable current log. Writing
	// its stable header here avoids repeating header initialization per engine.
	sessiontest.WriteEventLogHeaderFixture(t, store, session.EventLogHeader{
		Contract: session.EventLogContract,
		Version:  session.EventLogVersionV1,
	})
}

func mustOpenTestSession(t *testing.T, dir string) *session.Store {
	t.Helper()
	store, err := runtimeTestSessionPersistence.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return store
}

func mustNewTestEngine(t *testing.T, store *session.Store, client llm.Client, registry *tools.Registry, cfg Config) *Engine {
	t.Helper()
	if cfg.Model == "" {
		cfg.Model = "gpt-5"
	}
	eventLog := mustMaterializeTestEventLog(t, store)
	engine, err := New(store, eventLog, client, registry, cfg)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(); closeErr != nil && !errors.Is(closeErr, ErrEngineClosed) {
			t.Errorf("close engine: %v", closeErr)
		}
	})
	return engine
}

func mustMaterializeTestEventLog(
	t *testing.T,
	store *session.Store,
) session.MaterializedEventLog {
	t.Helper()
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	return eventLog
}

func collectTestEventRecords(store *session.Store) ([]testPersistedEvent, error) {
	if store == nil {
		return nil, errors.New("session store is required")
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		return nil, err
	}
	records := make([]testPersistedEvent, 0)
	err = eventLog.WalkRecords(func(record session.EventRecord) error {
		records = append(records, testPersistedEvent{
			Kind: string(mustSessionEventKind(record)), Record: record,
		})
		return nil
	})
	return records, err
}

func persistedMessageForTest(t *testing.T, event testPersistedEvent) llm.Message {
	t.Helper()
	record, ok := mustSessionEventPayload(event.Record).(session.MessageRecord)
	if !ok {
		t.Fatalf("event %q payload type = %T, want session.MessageRecord", event.Kind, mustSessionEventPayload(event.Record))
	}
	message, err := llmMessageFromSessionRecord(record)
	if err != nil {
		t.Fatalf("restore message record: %v", err)
	}
	return message
}

func persistedLocalEntryForTest(t *testing.T, event testPersistedEvent) storedLocalEntry {
	t.Helper()
	record, ok := mustSessionEventPayload(event.Record).(session.LocalEntryRecord)
	if !ok {
		t.Fatalf("event %q payload type = %T, want session.LocalEntryRecord", event.Kind, mustSessionEventPayload(event.Record))
	}
	entry, err := storedLocalEntryFromSessionRecord(record)
	if err != nil {
		t.Fatalf("restore local entry record: %v", err)
	}
	return entry
}

func persistedToolCompletionForTest(t *testing.T, event testPersistedEvent) storedToolCompletion {
	t.Helper()
	record, ok := mustSessionEventPayload(event.Record).(session.ToolCompletionRecord)
	if !ok {
		t.Fatalf("event %q payload type = %T, want session.ToolCompletionRecord", event.Kind, mustSessionEventPayload(event.Record))
	}
	completion, err := storedToolCompletionFromSessionRecord(record)
	if err != nil {
		t.Fatalf("restore tool completion record: %v", err)
	}
	return completion
}

func persistedHistoryReplacementForTest(t *testing.T, event testPersistedEvent) historyReplacementPayload {
	t.Helper()
	record, ok := mustSessionEventPayload(event.Record).(session.HistoryReplacementRecord)
	if !ok {
		t.Fatalf("event %q payload type = %T, want session.HistoryReplacementRecord", event.Kind, mustSessionEventPayload(event.Record))
	}
	replacement, err := historyReplacementPayloadFromSessionRecord(record)
	if err != nil {
		t.Fatalf("restore history replacement record: %v", err)
	}
	return replacement
}

func mustAppendTestEvent(t *testing.T, store *session.Store, stepID string, payload any) session.EventRecord {
	t.Helper()
	event, _, err := appendTestEvent(t, store, stepID, payload)
	if err != nil {
		t.Fatalf("append typed test event: %v", err)
	}
	return event
}

func appendTestEvent(
	t *testing.T,
	store *session.Store,
	stepID string,
	payload any,
) (session.EventRecord, session.CommitReceipt, error) {
	t.Helper()
	var record session.EventRecordPayload
	switch value := payload.(type) {
	case llm.Message:
		adapted, err := sessionMessageRecordFromLLM(value)
		if err != nil {
			return session.EventRecord{}, session.CommitReceipt{}, err
		}
		record = adapted
	case historyReplacementPayload:
		if value.Engine == "compaction" {
			value.Engine = "local"
		}
		if value.Mode == "" {
			value.Mode = string(compactionModeAuto)
		}
		adapted, err := sessionHistoryReplacementRecordFromRuntime(value)
		if err != nil {
			return session.EventRecord{}, session.CommitReceipt{}, err
		}
		record = adapted
	case storedLocalEntry:
		adapted, err := sessionLocalEntryRecordFromRuntime(value)
		if err != nil {
			return session.EventRecord{}, session.CommitReceipt{}, err
		}
		record = adapted
	case persistedCacheRequestObserved:
		adapted, err := sessionCacheRequestRecordFromRuntime(value)
		if err != nil {
			return session.EventRecord{}, session.CommitReceipt{}, err
		}
		record = adapted
	case persistedCacheResponseObserved:
		adapted, err := sessionCacheResponseRecordFromRuntime(value)
		if err != nil {
			return session.EventRecord{}, session.CommitReceipt{}, err
		}
		record = adapted
	case transcript.CacheWarning:
		adapted, err := sessionCacheWarningRecordFromRuntime(value)
		if err != nil {
			return session.EventRecord{}, session.CommitReceipt{}, err
		}
		record = adapted
	case storedToolCompletion:
		result := tools.Result{
			CallID:        value.CallID,
			Name:          toolspec.ID(value.Name),
			Output:        value.Output,
			IsError:       value.IsError,
			Summary:       textutil.Pointer(value.Summary),
			CondensedText: textutil.Pointer(value.CondensedText),
			Presentation:  value.Presentation,
		}
		adapted, err := sessionToolCompletionRecordFromRuntime(result, value.ProviderItems)
		if err != nil {
			return session.EventRecord{}, session.CommitReceipt{}, err
		}
		record = adapted
	case map[string]any:
		body, err := json.Marshal(value)
		if err != nil {
			return session.EventRecord{}, session.CommitReceipt{}, err
		}
		var completion storedToolCompletion
		if err := json.Unmarshal(body, &completion); err != nil {
			return session.EventRecord{}, session.CommitReceipt{}, err
		}
		if len(completion.ProviderItems) == 0 {
			completion.ProviderItems = []llm.ResponseItem{{
				Type:   llm.ResponseItemTypeFunctionCallOutput,
				CallID: textutil.Value(completion.CallID),
				Name:   textutil.Value(completion.Name),
				Output: completion.Output,
			}}
		}
		return appendTestEvent(t, store, stepID, completion)
	default:
		return session.EventRecord{}, session.CommitReceipt{},
			fmt.Errorf("unsupported typed test event payload %T", payload)
	}
	event, receipt, err := mustMaterializeTestEventLog(t, store).AppendRecord(&stepID, record)
	return event, receipt, err
}

func appendTestCompactionHistoryReplacement(
	t *testing.T,
	store *session.Store,
	stepID string,
	payload historyReplacementPayload,
) (session.EventRecord, session.CommitReceipt, error) {
	t.Helper()
	record, err := sessionHistoryReplacementRecordFromRuntime(payload)
	if err != nil {
		return session.EventRecord{}, session.CommitReceipt{}, err
	}
	return mustMaterializeTestEventLog(t, store).AppendCompactionHistoryReplacement(
		&stepID,
		record,
	)
}

func mustNewFakeToolEngine(t *testing.T, store *session.Store, client llm.Client, cfg Config, toolIDs ...toolspec.ID) *Engine {
	t.Helper()
	handlers := make([]tools.HandlerRegistration, 0, len(toolIDs))
	for _, id := range toolIDs {
		handlers = append(handlers, tools.HandlerRegistration{ID: id, Handler: fakeTool{name: id}})
	}
	return mustNewTestEngine(t, store, client, tools.NewRegistry(handlers...), cfg)
}

func mustNewExecTestEngine(t *testing.T, store *session.Store, client llm.Client, cfg Config) *Engine {
	t.Helper()
	return mustNewFakeToolEngine(t, store, client, cfg, toolspec.ToolExecCommand)
}

func mustNewHandoffTestEngine(t *testing.T, store *session.Store, client llm.Client, cfg Config) *Engine {
	t.Helper()
	if cfg.CompactionMode == "" {
		cfg.CompactionMode = "local"
	}
	cfg.EnabledTools = []toolspec.ID{toolspec.ToolExecCommand, toolspec.ToolTriggerHandoff}
	return mustNewExecTestEngine(t, store, client, cfg)
}

func mustNewWorkflowTestEngine(t *testing.T, store *session.Store, client llm.Client, workflowCfg *workflowruntime.Config, cfg Config) *Engine {
	t.Helper()
	cfg.WorkflowRun = workflowCfg
	return mustNewExecTestEngine(t, store, client, cfg)
}

func mustSetWorktreeReminderState(t *testing.T, store *session.Store, state session.WorktreeReminderState) session.WorktreeReminderState {
	t.Helper()
	if err := store.SetWorktreeReminderState(&state); err != nil {
		t.Fatalf("SetWorktreeReminderState: %v", err)
	}
	persisted := store.Meta().WorktreeReminder
	if persisted == nil {
		t.Fatal("worktree reminder state was not persisted")
	}
	return *session.CloneWorktreeReminderState(persisted)
}

func testWorktreeReminderState(mode session.WorktreeReminderMode, branch, worktreePath, workspaceRoot, effectiveCWD string) session.WorktreeReminderState {
	return session.WorktreeReminderState{
		Mode: mode,
		WorktreeContext: session.WorktreeContext{
			Branch:        session.OptionalWorktreeBranch(branch),
			WorktreePath:  worktreePath,
			WorkspaceRoot: workspaceRoot,
			EffectiveCwd:  effectiveCWD,
		},
	}
}

func finalTextResponse(content string) llm.Response {
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value(content)},
		Usage:     llm.Usage{WindowTokens: 200000},
	}
}

func finalOutputItemResponse(content string) llm.Response {
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Phase: textutil.Value(llm.MessagePhaseFinal), Content: textutil.Value(content)},
		OutputItems: []llm.ResponseItem{{
			Type:    llm.ResponseItemTypeMessage,
			Role:    textutil.Value(llm.RoleAssistant),
			Phase:   textutil.Value(llm.MessagePhaseFinal),
			Content: textutil.Value(content),
		}},
		Usage: llm.Usage{WindowTokens: 200000},
	}
}

func commentaryResponse(content string, toolCalls ...llm.ToolCall) llm.Response {
	return llm.Response{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.OptionalTrimmedString(content), Phase: textutil.Value(llm.MessagePhaseCommentary), ToolCalls: toolCalls},
		ToolCalls: toolCalls,
		Usage:     llm.Usage{WindowTokens: 200000},
	}
}

func assertModelCallCount(t *testing.T, client *fakeClient, want int) {
	t.Helper()
	if len(client.calls) != want {
		t.Fatalf("model calls = %d, want %d", len(client.calls), want)
	}
}

func messageContent(message llm.Message) string {
	if message.Content == nil {
		panic("test expected message content to be present")
	}
	return *message.Content
}
