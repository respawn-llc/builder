package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
	"core/shared/textutil"
	"core/shared/transcript"
)

func TestReplaceHistoryDoesNotMutateRuntimeStateWhenEventAppendFails(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if err := steerTestActiveStep(engine, "seed", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	usage := &session.UsageState{InputTokens: 42, WindowTokens: 100}
	if _, err := store.SetUsageState(usage); err != nil {
		t.Fatalf("persist seed usage: %v", err)
	}
	engine.compactionRuntimeState().SetSoonReminderIssued(true)
	blocker := mustBlockTestEventLogAppends(t, store)
	t.Cleanup(func() {
		if err := blocker.Restore(); err != nil {
			t.Errorf("restore event-log appends: %v", err)
		}
	})

	var receipt session.CommitReceipt
	compactionStepID := runtimeTestStepID("compact")
	err := runTestActiveStep(engine, compactionStepID, func() error {
		var replaceErr error
		receipt, replaceErr = newCompactionPersistence(engine).replaceHistory(
			compactionStepID,
			"local",
			compactionModeManual,
			llm.ItemsFromMessages([]llm.Message{{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("summary"),
			}}),
		)
		return replaceErr
	})
	if err == nil || receipt.Committed {
		t.Fatalf("uncommitted history replacement outcome: receipt=%+v error=%v", receipt, err)
	}

	items := engine.transcriptRuntimeState().SnapshotItems()
	if len(items) != 1 ||
		items[0].Type != llm.ResponseItemTypeMessage ||
		items[0].Role == nil ||
		*items[0].Role != llm.RoleUser {
		t.Fatalf("uncommitted replacement changed active items: %+v", items)
	}
	if !engine.compactionRuntimeState().SoonReminderIssued() {
		t.Fatal("uncommitted replacement cleared compaction reminder")
	}
	storedUsage := store.Meta().UsageState
	if storedUsage == nil || storedUsage.InputTokens != usage.InputTokens {
		t.Fatalf("uncommitted replacement changed persisted usage: %+v", storedUsage)
	}
}

func TestRestoreMessagesFailsOnMalformedHistoryReplacementPayload(t *testing.T) {
	t.Parallel()
	t.Run("non-legacy payload fails materialization", func(t *testing.T) {
		store := mustCreateTestSession(t)
		writeMalformedLegacyHistoryReplacement(t, store, "local")

		_, err := store.MaterializeEventLog()
		var materializationErr *session.EventLogMaterializationError
		if !errors.As(err, &materializationErr) ||
			materializationErr.Stage != session.EventLogMaterializationStagePreparation ||
			materializationErr.Committed ||
			materializationErr.PendingRepair {
			t.Fatalf("malformed history replacement materialization = %+v", err)
		}
	})

	t.Run("legacy reviewer rollback is ignored", func(t *testing.T) {
		store := mustCreateTestSession(t)
		writeMalformedLegacyHistoryReplacement(
			t,
			store,
			session.LegacyReviewerRollbackHistoryReplacementEngine,
		)

		eventLog, err := store.MaterializeEventLog()
		if err != nil {
			t.Fatalf("materialize legacy reviewer rollback: %v", err)
		}
		window, err := eventLog.ReadRecentRecords(16)
		if err != nil {
			t.Fatalf("read bounded migrated records: %v", err)
		}
		if len(window.Records) != 0 {
			t.Fatalf("ignored legacy reviewer rollback records = %+v", window.Records)
		}
		engine, err := New(store, eventLog, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
		if err != nil {
			t.Fatalf("restore legacy reviewer rollback: %v", err)
		}
		t.Cleanup(func() {
			if err := engine.Close(); err != nil {
				t.Errorf("close restored engine: %v", err)
			}
		})
		if engine.CommittedTranscriptEntryCount() != 0 {
			t.Fatalf("ignored legacy reviewer rollback committed entry count = %d", engine.CommittedTranscriptEntryCount())
		}
	})
}

func TestHistoryReplacementResetsDiagnosticDedupe(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	diagnosticKey := "test_diagnostic"
	beforeStepID := runtimeTestStepID("before-compaction")
	if err := runTestActiveStep(engine, beforeStepID, func() error {
		return engine.steerPersistedDiagnosticEntry(
			beforeStepID,
			diagnosticKey,
			string(transcript.EntryRoleDeveloperErrorFeedback),
			"before",
		)
	}); err != nil {
		t.Fatalf("persist pre-compaction diagnostic: %v", err)
	}

	var receipt session.CommitReceipt
	compactionStepID := runtimeTestStepID("compaction")
	err := runTestActiveStep(engine, compactionStepID, func() error {
		var replaceErr error
		receipt, replaceErr = newCompactionPersistence(engine).replaceHistory(
			compactionStepID,
			"local",
			compactionModeManual,
			llm.ItemsFromMessages([]llm.Message{{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("summary"),
			}}),
		)
		return replaceErr
	})
	if err != nil || !receipt.Committed {
		t.Fatalf("persist compaction replacement: receipt=%+v error=%v", receipt, err)
	}

	afterStepID := runtimeTestStepID("after-compaction")
	if err := runTestActiveStep(engine, afterStepID, func() error {
		return engine.steerPersistedDiagnosticEntry(
			afterStepID,
			diagnosticKey,
			string(transcript.EntryRoleDeveloperErrorFeedback),
			"after",
		)
	}); err != nil {
		t.Fatalf("persist post-compaction diagnostic: %v", err)
	}

	replacements, diagnostics := boundedDiagnosticRecordCounts(t, store, diagnosticKey)
	if replacements != 1 || diagnostics != 2 {
		t.Fatalf(
			"bounded typed diagnostic records replacements=%d diagnostics=%d, want one and two",
			replacements,
			diagnostics,
		)
	}
}

func TestReopenedSessionHistoryReplacementResetsDiagnosticDedupe(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	diagnosticKey := "test_diagnostic"
	beforeStepID := runtimeTestStepID("before-compaction")
	if err := runTestActiveStep(engine, beforeStepID, func() error {
		return engine.steerPersistedDiagnosticEntry(
			beforeStepID,
			diagnosticKey,
			string(transcript.EntryRoleDeveloperErrorFeedback),
			"before",
		)
	}); err != nil {
		t.Fatalf("persist pre-compaction diagnostic: %v", err)
	}
	var receipt session.CommitReceipt
	compactionStepID := runtimeTestStepID("compaction")
	err := runTestActiveStep(engine, compactionStepID, func() error {
		var replaceErr error
		receipt, replaceErr = newCompactionPersistence(engine).replaceHistory(
			compactionStepID,
			"local",
			compactionModeManual,
			llm.ItemsFromMessages([]llm.Message{{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("summary"),
			}}),
		)
		return replaceErr
	})
	if err != nil || !receipt.Committed {
		t.Fatalf("persist compaction replacement: receipt=%+v error=%v", receipt, err)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	restored := mustNewTestEngine(t, reopened, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	afterStepID := runtimeTestStepID("after-reopen")
	if err := runTestActiveStep(restored, afterStepID, func() error {
		return restored.steerPersistedDiagnosticEntry(
			afterStepID,
			diagnosticKey,
			string(transcript.EntryRoleDeveloperErrorFeedback),
			"after",
		)
	}); err != nil {
		t.Fatalf("persist reopened diagnostic: %v", err)
	}

	replacements, diagnostics := boundedDiagnosticRecordCounts(t, reopened, diagnosticKey)
	if replacements != 1 || diagnostics != 2 {
		t.Fatalf(
			"reopened typed diagnostic records replacements=%d diagnostics=%d, want one and two",
			replacements,
			diagnostics,
		)
	}
}

func boundedDiagnosticRecordCounts(t *testing.T, store *session.Store, diagnosticKey string) (int, int) {
	t.Helper()
	window, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if err != nil {
		t.Fatalf("read bounded diagnostic records: %v", err)
	}
	replacements := 0
	diagnostics := 0
	for _, record := range window.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.HistoryReplacementRecord:
			replacements++
		case session.LocalEntryRecord:
			if payload.DiagnosticKey != nil && *payload.DiagnosticKey == diagnosticKey {
				diagnostics++
			}
		}
	}
	return replacements, diagnostics
}

func writeMalformedLegacyHistoryReplacement(t *testing.T, store *session.Store, engine string) {
	t.Helper()
	payload, err := json.Marshal(struct {
		Engine string `json:"engine"`
		Items  string `json:"items"`
	}{
		Engine: engine,
		Items:  "malformed",
	})
	if err != nil {
		t.Fatalf("marshal malformed legacy history replacement payload: %v", err)
	}
	record, err := json.Marshal(struct {
		Seq       int64           `json:"seq"`
		Timestamp time.Time       `json:"timestamp"`
		Kind      string          `json:"kind"`
		StepID    string          `json:"step_id"`
		Payload   json.RawMessage `json:"payload"`
	}{
		Seq:       1,
		Timestamp: time.Unix(0, 0).UTC(),
		Kind:      string(session.EventKindHistoryReplace),
		StepID:    "legacy-step",
		Payload:   payload,
	})
	if err != nil {
		t.Fatalf("marshal malformed legacy history replacement event: %v", err)
	}
	writeTestFile(t, filepath.Join(store.Dir(), "events.jsonl"), string(append(record, '\n')))
}

func TestCommittedCompactionHistoryReplacementInvalidatesUsageAcrossImmediateReopen(t *testing.T) {
	t.Parallel()
	observerErr := errors.New("history replacement metadata observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
	if err := steerTestActiveStep(engine, "seed", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	previousUsage := llm.Usage{
		InputTokens:       190_000,
		WindowTokens:      200_000,
		CachedInputTokens: textutil.Value(190_000),
	}
	if receipt, err := engine.recordLastUsage(previousUsage); err != nil || !receipt.Committed {
		t.Fatalf("persist previous usage: receipt=%+v error=%v", receipt, err)
	}
	gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.LastSequence >= 2 && snapshot.Meta.UsageState == nil
	}, observerErr)

	var receipt session.CommitReceipt
	compactionStepID := runtimeTestStepID("compact")
	err := runTestActiveStep(engine, compactionStepID, func() error {
		var replaceErr error
		receipt, replaceErr = newCompactionPersistence(engine).replaceHistory(
			compactionStepID,
			"local",
			compactionModeManual,
			llm.ItemsFromMessages([]llm.Message{{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("summary"),
			}}),
		)
		return replaceErr
	})
	if !receipt.Committed || !errors.Is(err, observerErr) {
		t.Fatalf("committed replacement outcome: receipt=%+v error=%v", receipt, err)
	}

	reopenedStore := mustOpenTestSession(t, store.Dir())
	if usage := reopenedStore.Meta().UsageState; usage != nil {
		t.Fatalf("immediate reopen restored pre-compaction usage: %+v", usage)
	}
	reopened := mustNewExecTestEngine(t, reopenedStore, &fakeClient{}, Config{Model: "gpt-5"})
	usage := reopened.ContextUsage()
	if usage.UsedTokens <= 0 || usage.UsedTokens >= previousUsage.InputTokens {
		t.Fatalf("immediately reopened context usage = %+v, want compacted active-history estimate", usage)
	}
	if usage.HasCacheHitPercentage {
		t.Fatalf("immediate reopen restored stale cache counters: %+v", usage)
	}
}

func TestHistoryReplacementAppendObserverFailureUpdatesLiveActiveListForNextTurn(t *testing.T) {
	t.Parallel()
	observerErr := errors.New("history replacement observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	client := &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{Role: llm.RoleAssistant, Content: textutil.Value("done")},
		Usage:     llm.Usage{WindowTokens: 200_000},
	}}}
	engine := mustNewExecTestEngine(t, store, client, Config{Model: "gpt-5"})
	if err := steerTestActiveStep(engine, "seed", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.LastSequence >= 2 && snapshot.Meta.UsageState == nil
	}, observerErr)

	var receipt session.CommitReceipt
	compactionStepID := runtimeTestStepID("compact")
	err := runTestActiveStep(engine, compactionStepID, func() error {
		var replaceErr error
		receipt, replaceErr = newCompactionPersistence(engine).replaceHistory(
			compactionStepID,
			"local",
			compactionModeManual,
			llm.ItemsFromMessages([]llm.Message{{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("summary"),
			}}),
		)
		return replaceErr
	})
	if !receipt.Committed || !errors.Is(err, observerErr) {
		t.Fatalf("committed replacement outcome: receipt=%+v error=%v", receipt, err)
	}

	active := engine.transcriptRuntimeState().SnapshotItems()
	if len(active) != 1 ||
		active[0].Type != llm.ResponseItemTypeMessage ||
		active[0].Role == nil ||
		*active[0].Role != llm.RoleDeveloper ||
		active[0].MessageType == nil ||
		*active[0].MessageType != llm.MessageTypeCompactionSummary {
		t.Fatalf("live active items after committed replacement observer failure = %+v", active)
	}

	if _, err := engine.SubmitUserMessage(context.Background(), "continue"); err != nil {
		t.Fatalf("submit after committed replacement observer failure: %v", err)
	}
	if len(client.calls) != 1 {
		t.Fatalf("model call count = %d, want 1", len(client.calls))
	}

	summaryItems := 0
	ordinaryUserItems := 0
	for _, item := range client.calls[0].Items {
		if item.Type != llm.ResponseItemTypeMessage || item.Role == nil {
			continue
		}
		switch *item.Role {
		case llm.RoleDeveloper:
			if item.MessageType != nil && *item.MessageType == llm.MessageTypeCompactionSummary {
				summaryItems++
			}
		case llm.RoleUser:
			if item.MessageType == nil {
				ordinaryUserItems++
			}
		}
	}
	if summaryItems != 1 || ordinaryUserItems != 1 {
		t.Fatalf(
			"next request response-item types = summary:%d ordinary-user:%d, want summary:1 ordinary-user:1",
			summaryItems,
			ordinaryUserItems,
		)
	}
}

type committedRemoteCompactionFixture struct {
	store         *session.Store
	engine        *Engine
	client        *fakeCompactionClient
	previousUsage llm.Usage
	events        []Event
}

func newCommittedRemoteCompactionFixture(
	t *testing.T,
	observer session.PersistenceObserver,
	locked *session.LockedContract,
) *committedRemoteCompactionFixture {
	t.Helper()
	fixture := &committedRemoteCompactionFixture{
		store: mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(observer)),
		client: &fakeCompactionClient{
			inputTokenCount: 2_000,
			compactionResponses: []llm.CompactionResponse{{
				Checkpoint: llm.ResponseItem{
					Type:             llm.ResponseItemTypeCompaction,
					ID:               textutil.Value("cmp-1"),
					EncryptedContent: textutil.Value("encrypted"),
				},
				Usage: llm.Usage{InputTokens: 1_000, OutputTokens: 100, WindowTokens: 200_000},
			}},
		},
		previousUsage: llm.Usage{
			InputTokens:       190_000,
			WindowTokens:      200_000,
			CachedInputTokens: textutil.Value(190_000),
		},
	}
	if locked != nil {
		if err := fixture.store.MarkModelDispatchLocked(*locked); err != nil {
			t.Fatalf("lock prompt-facing snapshots: %v", err)
		}
	}
	fixture.engine = mustNewTestEngine(t, fixture.store, fixture.client, tools.NewRegistry(), Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			fixture.events = append(fixture.events, event)
		},
	})
	if err := steerTestActiveStep(fixture.engine, "seed", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	if receipt, err := fixture.engine.recordLastUsage(fixture.previousUsage); err != nil || !receipt.Committed {
		t.Fatalf("persist pre-compaction usage: receipt=%+v error=%v", receipt, err)
	}
	return fixture
}

func TestCompactNowCompletesCommittedHistoryReplacementObserverFailure(t *testing.T) {
	t.Parallel()
	observerErr := errors.New("history replacement observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	fixture := newCommittedRemoteCompactionFixture(t, gate, nil)
	gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.LastSequence >= 2 && snapshot.Meta.UsageState == nil
	}, observerErr)

	var receipt session.CommitReceipt
	compactionStepID := runtimeTestStepID("compact")
	err := runTestActiveStep(fixture.engine, compactionStepID, func() error {
		var compactErr error
		_, receipt, compactErr = fixture.engine.compactNow(
			context.Background(),
			compactionStepID,
			compactionModeManual,
			compactionInstructionsInput{},
			false,
		)
		return compactErr
	})
	if !receipt.Committed || !errors.Is(err, observerErr) {
		t.Fatalf("compactNow outcome: receipt=%+v error=%v", receipt, err)
	}

	completed := 0
	failed := 0
	for _, event := range fixture.events {
		switch event.Kind {
		case EventCompactionCompleted:
			completed++
		case EventCompactionFailed:
			failed++
		}
	}
	if completed != 1 || failed != 0 {
		t.Fatalf("compaction terminal events: completed=%d failed=%d events=%+v", completed, failed, fixture.events)
	}
	if generation := fixture.engine.compactionRuntimeState().Count(); generation != 1 {
		t.Fatalf("committed compaction generation = %d, want 1", generation)
	}

	window, readErr := mustMaterializeTestEventLog(t, fixture.store).ReadRecentRecords(16)
	if readErr != nil {
		t.Fatalf("read bounded committed records: %v", readErr)
	}
	replacements := 0
	for _, record := range window.Records {
		if _, ok := mustSessionEventPayload(record).(session.HistoryReplacementRecord); ok {
			replacements++
		}
	}
	if replacements != 1 || len(fixture.client.compactionCalls) != 1 {
		t.Fatalf(
			"committed compaction replacements=%d compaction-calls=%d, want one each",
			replacements,
			len(fixture.client.compactionCalls),
		)
	}
}

func TestCompactNowReconcilesLiveUsageWhenFinalUsageObserverFails(t *testing.T) {
	t.Parallel()
	observerErr := errors.New("compaction usage observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	fixture := newCommittedRemoteCompactionFixture(t, gate, nil)
	gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		usage := snapshot.Meta.UsageState
		return usage != nil &&
			usage.WindowTokens == fixture.previousUsage.WindowTokens &&
			usage.InputTokens != fixture.previousUsage.InputTokens
	}, observerErr)

	var receipt session.CommitReceipt
	compactionStepID := runtimeTestStepID("compact")
	err := runTestActiveStep(fixture.engine, compactionStepID, func() error {
		var compactErr error
		_, receipt, compactErr = fixture.engine.compactNow(
			context.Background(),
			compactionStepID,
			compactionModeManual,
			compactionInstructionsInput{},
			false,
		)
		return compactErr
	})
	if !receipt.Committed || !errors.Is(err, observerErr) {
		t.Fatalf("compactNow outcome: receipt=%+v error=%v", receipt, err)
	}

	liveUsage := fixture.engine.ContextUsage()
	expectedInputTokens := estimateItemsTokens(fixture.engine.transcriptRuntimeState().SnapshotItems())
	if liveUsage.UsedTokens != expectedInputTokens {
		t.Fatalf("live compacted usage = %+v, want estimated input tokens %d", liveUsage, expectedInputTokens)
	}
	if persisted := fixture.store.Meta().UsageState; persisted == nil ||
		persisted.InputTokens != expectedInputTokens ||
		persisted.WindowTokens != fixture.previousUsage.WindowTokens {
		t.Fatalf("persisted compacted usage = %+v", persisted)
	}

	reopened := mustNewTestEngine(
		t,
		mustOpenTestSession(t, fixture.store.Dir()),
		&fakeClient{},
		tools.NewRegistry(),
		Config{Model: "gpt-5"},
	)
	reopenedUsage := reopened.ContextUsage()
	if reopenedUsage.UsedTokens <= 0 || reopenedUsage.UsedTokens >= fixture.previousUsage.InputTokens {
		t.Fatalf("reopened compacted usage = %+v", reopenedUsage)
	}
	if !reopenedUsage.HasCacheHitPercentage || reopenedUsage.CacheHitPercent != 100 {
		t.Fatalf("reopened compacted cache counters = %+v", reopenedUsage)
	}
}

func TestCompactNowInvalidatesPromptSnapshotsWhenStaleMetadataObserverFails(t *testing.T) {
	t.Parallel()
	observerErr := errors.New("prompt snapshot stale observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	fixture := newCommittedRemoteCompactionFixture(t, gate, &session.LockedContract{
		Model:             "gpt-5",
		SystemPrompt:      "persisted snapshot",
		HasSystemPrompt:   true,
		ReviewerPrompt:    "persisted reviewer snapshot",
		HasReviewerPrompt: true,
	})
	gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		locked := snapshot.Meta.Locked
		return locked != nil && !locked.HasSystemPrompt && !locked.HasReviewerPrompt
	}, observerErr)

	var receipt session.CommitReceipt
	compactionStepID := runtimeTestStepID("compact")
	err := runTestActiveStep(fixture.engine, compactionStepID, func() error {
		var compactErr error
		_, receipt, compactErr = fixture.engine.compactNow(
			context.Background(),
			compactionStepID,
			compactionModeManual,
			compactionInstructionsInput{},
			false,
		)
		return compactErr
	})
	if !receipt.Committed || !errors.Is(err, observerErr) {
		t.Fatalf("compactNow outcome: receipt=%+v error=%v", receipt, err)
	}

	locked, ok := fixture.engine.lockedContractState().Snapshot()
	if !ok || locked.HasSystemPrompt || locked.HasReviewerPrompt {
		t.Fatalf("live locked snapshot presence after committed stale mutation = %+v", locked)
	}
	if persisted := fixture.store.Meta().Locked; persisted == nil ||
		persisted.HasSystemPrompt || persisted.HasReviewerPrompt {
		t.Fatalf("persisted locked snapshot presence after committed stale mutation = %+v", persisted)
	}

	request, requestErr := fixture.engine.buildRequest(context.Background(), "next", false)
	if requestErr != nil {
		t.Fatalf("build request after committed stale mutation: %v", requestErr)
	}
	if generation := fixture.engine.compactionRuntimeState().Count(); generation != 1 {
		t.Fatalf("cache-key compaction generation = %d, want 1", generation)
	}
	expectedCacheKey := conversationPromptCacheKey(fixture.store.Meta().SessionID)
	if request.SessionID != nil ||
		request.CodexDispatch != nil ||
		request.PromptCacheKey != expectedCacheKey ||
		request.PromptCacheScope != transcript.CacheWarningScopeConversation {
		t.Fatalf(
			"post-compaction context-free request identity = session:%v cache-key:%q scope:%q",
			request.SessionID,
			request.PromptCacheKey,
			request.PromptCacheScope,
		)
	}
}

func TestRealCompactionClearsPersistedCompactionSoonReminderStateAcrossReopenAndFork(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{responses: []llm.Response{{
		Assistant: llm.Message{
			Role:    llm.RoleAssistant,
			Content: textutil.Value("summary"),
		},
		Usage: llm.Usage{InputTokens: 200, WindowTokens: 2_000},
	}}}, tools.NewRegistry(), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
	})
	if err := steerTestActiveStep(engine, "seed", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	engine.setLastUsage(llm.Usage{InputTokens: 890, WindowTokens: 2_000})
	reminderStepID := runtimeTestStepID("reminder")
	if err := runTestActiveStep(engine, reminderStepID, func() error {
		return newCompactionReminderCoordinator(engine).maybeAppend(context.Background(), reminderStepID)
	}); err != nil {
		t.Fatalf("append compaction reminder: %v", err)
	}

	beforeCompaction, err := mustMaterializeTestEventLog(t, store).ReadRecentRecords(4)
	if err != nil {
		t.Fatalf("read bounded pre-compaction records: %v", err)
	}
	reminders := 0
	for _, record := range beforeCompaction.Records {
		message, ok := mustSessionEventPayload(record).(session.MessageRecord)
		if ok &&
			message.Role == session.MessageRoleDeveloper &&
			message.MessageType != nil &&
			*message.MessageType == session.MessageTypeCompactionSoonReminder {
			reminders++
		}
	}
	if reminders != 1 || !store.Meta().CompactionSoonReminderIssued {
		t.Fatalf(
			"pre-compaction reminder records=%d persisted=%v, want one and true",
			reminders,
			store.Meta().CompactionSoonReminderIssued,
		)
	}

	scheduleManualCompactionAndWait(t, engine)
	if engine.compactionRuntimeState().SoonReminderIssued() || store.Meta().CompactionSoonReminderIssued {
		t.Fatalf(
			"committed compaction reminder state runtime=%v persisted=%v, want both false",
			engine.compactionRuntimeState().SoonReminderIssued(),
			store.Meta().CompactionSoonReminderIssued,
		)
	}
	if err := steerTestActiveStep(engine, "after-compaction", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("continue")}})); err != nil {
		t.Fatalf("persist post-compaction fork target: %v", err)
	}

	reopenedStore := mustOpenTestSession(t, store.Dir())
	reopened := mustNewTestEngine(t, reopenedStore, &fakeClient{}, tools.NewRegistry(), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
	})
	if reopened.compactionRuntimeState().SoonReminderIssued() || reopenedStore.Meta().CompactionSoonReminderIssued {
		t.Fatalf(
			"reopened compaction reminder state runtime=%v persisted=%v, want both false",
			reopened.compactionRuntimeState().SoonReminderIssued(),
			reopenedStore.Meta().CompactionSoonReminderIssued,
		)
	}

	reopenedLog := mustMaterializeTestEventLog(t, reopenedStore)
	recent, err := reopenedLog.ReadRecentRecords(4)
	if err != nil {
		t.Fatalf("read bounded reopened records: %v", err)
	}
	var forkTargetSeq int64
	replacements := 0
	for _, record := range recent.Records {
		switch payload := mustSessionEventPayload(record).(type) {
		case session.HistoryReplacementRecord:
			replacements++
		case session.MessageRecord:
			if payload.Role == session.MessageRoleUser {
				forkTargetSeq = record.Seq()
			}
		}
	}
	if replacements != 1 || forkTargetSeq <= 0 {
		t.Fatalf(
			"reopened bounded records replacements=%d fork-target-seq=%d, want one replacement and target",
			replacements,
			forkTargetSeq,
		)
	}

	forkedStore, _, err := session.ForkAtUserMessage(
		reopenedLog,
		forkTargetSeq,
		"fork",
		sessioncontract.SessionCategoryMain,
	)
	if err != nil {
		t.Fatalf("fork compacted session: %v", err)
	}
	forkedLog := mustMaterializeTestEventLog(t, forkedStore)
	forkedRecent, err := forkedLog.ReadRecentRecords(4)
	if err != nil {
		t.Fatalf("read bounded forked records: %v", err)
	}
	forkedReplacements := 0
	for _, record := range forkedRecent.Records {
		switch mustSessionEventPayload(record).(type) {
		case session.HistoryReplacementRecord:
			forkedReplacements++
		}
	}
	if forkedReplacements != 1 || forkedStore.Meta().CompactionSoonReminderIssued {
		t.Fatalf(
			"forked typed history replacements=%d persisted=%v, want one and false",
			forkedReplacements,
			forkedStore.Meta().CompactionSoonReminderIssued,
		)
	}
	forked := mustNewTestEngine(t, forkedStore, &fakeClient{}, tools.NewRegistry(), Config{
		Model:                 "gpt-5",
		ContextWindowTokens:   2_000,
		AutoCompactTokenLimit: 1_000,
		CompactionMode:        "local",
	})
	if forked.compactionRuntimeState().SoonReminderIssued() {
		t.Fatal("forked compacted session restored reminder-issued state")
	}
}

func TestRemoteCompactionTaskAwarenessErrorDoesNotReplaceHistory(t *testing.T) {
	t.Parallel()
	store := mustCreateTestSession(t)
	countErr := errors.New("workflow task comment count failure")
	scopeID := runtimeids.NewExecutionScopeID()
	client := &fakeCompactionClient{
		compactionResponses: []llm.CompactionResponse{{
			Checkpoint: llm.ResponseItem{
				Type:             llm.ResponseItemTypeCompaction,
				ID:               textutil.Value("cmp-1"),
				EncryptedContent: textutil.Value("encrypted"),
			},
			Usage: llm.Usage{InputTokens: 1_000, OutputTokens: 100, WindowTokens: 200_000},
		}},
	}
	engine := mustNewWorkflowTestEngine(t, store, client, &workflowruntime.CurrentNodeExecutionConfig{
		ScopeID:             scopeID,
		Contract:            workflowruntime.CompletionContract{},
		CompletionMode:      workflowruntime.CompletionModeTool,
		Controller:          &externallyCompletedWorkflowController{},
		TaskAwarenessSource: failingWorkflowTaskAwarenessSource{err: countErr},
		Instructions:        workflowruntime.TaskInstructions{CurrentNode: mustTestCurrentNodeReference(t, "task-1", "node-1", nil)},
	}, Config{Model: "gpt-5"})
	if err := steerTestActiveStep(engine, "seed", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	before := engine.transcriptRuntimeState().SnapshotItems()

	var receipt session.CommitReceipt
	compactionStepID := runtimeTestStepID("compact")
	err := runTestActiveStep(engine, compactionStepID, func() error {
		var compactErr error
		_, receipt, compactErr = engine.compactNow(
			context.Background(),
			compactionStepID,
			compactionModeManual,
			compactionInstructionsInput{},
			false,
		)
		return compactErr
	})
	if receipt.Committed || !errors.Is(err, countErr) {
		t.Fatalf("comment-count failure compaction outcome: receipt=%+v error=%v", receipt, err)
	}
	if after := engine.transcriptRuntimeState().SnapshotItems(); !reflect.DeepEqual(after, before) {
		t.Fatalf("comment-count failure changed active typed items: before=%+v after=%+v", before, after)
	}

	window, readErr := mustMaterializeTestEventLog(t, store).ReadRecentRecords(16)
	if readErr != nil {
		t.Fatalf("read bounded recent records: %v", readErr)
	}
	for _, record := range window.Records {
		if _, ok := mustSessionEventPayload(record).(session.HistoryReplacementRecord); ok {
			t.Fatalf("comment-count failure persisted history replacement: %+v", window.Records)
		}
	}
}

type failingWorkflowTaskAwarenessSource struct {
	err error
}

func (c failingWorkflowTaskAwarenessSource) TaskAwareness(context.Context, workflow.TaskID) (workflowruntime.TaskAwareness, error) {
	return workflowruntime.TaskAwareness{}, c.err
}

func TestCommittedHistoryReplacementPreventsStaleUsageFromLaterMetadataPersistence(t *testing.T) {
	t.Parallel()
	replacementErr := errors.New("history replacement observer failure")
	usageErr := errors.New("compacted usage observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if err := steerTestActiveStep(engine, "seed", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}
	previousUsage := llm.Usage{
		InputTokens:       190_000,
		WindowTokens:      200_000,
		CachedInputTokens: textutil.Value(190_000),
	}
	if receipt, err := engine.recordLastUsage(previousUsage); err != nil || !receipt.Committed {
		t.Fatalf("persist previous usage: receipt=%+v error=%v", receipt, err)
	}
	gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		return snapshot.Meta.LastSequence >= 2 && snapshot.Meta.UsageState == nil
	}, replacementErr)

	var receipt session.CommitReceipt
	compactionStepID := runtimeTestStepID("compact")
	err := runTestActiveStep(engine, compactionStepID, func() error {
		var replaceErr error
		receipt, replaceErr = newCompactionPersistence(engine).replaceHistory(
			compactionStepID,
			"local",
			compactionModeManual,
			llm.ItemsFromMessages([]llm.Message{{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("summary"),
			}}),
		)
		return replaceErr
	})
	if !receipt.Committed || !errors.Is(err, replacementErr) {
		t.Fatalf("committed replacement outcome: receipt=%+v error=%v", receipt, err)
	}

	compactedUsage := llm.Usage{InputTokens: 2_000, WindowTokens: previousUsage.WindowTokens}
	gate.FailWhen(func(snapshot session.PersistedStoreSnapshot) bool {
		usage := snapshot.Meta.UsageState
		return usage != nil && usage.InputTokens == compactedUsage.InputTokens
	}, usageErr)
	usageReceipt, recordErr := engine.recordLastUsage(compactedUsage)
	if !usageReceipt.Committed || !errors.Is(recordErr, usageErr) {
		t.Fatalf("committed compacted-usage outcome: receipt=%+v error=%v", usageReceipt, recordErr)
	}
	if err := store.SetName("metadata"); err != nil {
		t.Fatalf("persist later metadata: %v", err)
	}

	reopened := mustOpenTestSession(t, store.Dir())
	usage := reopened.Meta().UsageState
	if usage == nil || usage.InputTokens != compactedUsage.InputTokens || usage.WindowTokens != compactedUsage.WindowTokens {
		t.Fatalf("reopened usage after later metadata persistence: %+v", usage)
	}
}

func TestWorkflowBudgetResetFailureKeepsCommittedReplacementLive(t *testing.T) {
	t.Parallel()
	resetErr := errors.New("workflow protocol budget reset failure")
	scopeID := runtimeids.NewExecutionScopeID()
	controller := &workflowBudgetResetFailureController{err: resetErr}
	store := mustCreateTestSession(t)
	var events []Event
	engine := mustNewWorkflowTestEngine(t, store, &fakeClient{}, &workflowruntime.CurrentNodeExecutionConfig{
		ScopeID:        scopeID,
		Contract:       workflowruntime.CompletionContract{},
		CompletionMode: workflowruntime.CompletionModeTool,
		Controller:     controller,
	}, Config{
		Model: "gpt-5",
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})
	if err := steerTestActiveStep(engine, "seed", steerMessagesWithPersistenceIntent(steeringPriorityNormal, steeringMessageEventNone, true, []llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}})); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}

	var receipt session.CommitReceipt
	compactionStepID := runtimeTestStepID("compact")
	err := runTestActiveStep(engine, compactionStepID, func() error {
		var replaceErr error
		receipt, replaceErr = newCompactionPersistence(engine).replaceHistory(
			compactionStepID,
			"local",
			compactionModeManual,
			llm.ItemsFromMessages([]llm.Message{{
				Role:        llm.RoleDeveloper,
				MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
				Content:     textutil.Value("summary"),
			}}),
		)
		return replaceErr
	})
	if !receipt.Committed || !errors.Is(err, resetErr) {
		t.Fatalf("committed replacement reset-failure outcome: receipt=%+v error=%v", receipt, err)
	}
	if engine.compactionRuntimeState().Count() != 1 {
		t.Fatalf("committed replacement generation = %d, want 1", engine.compactionRuntimeState().Count())
	}

	items := engine.transcriptRuntimeState().SnapshotItems()
	if len(items) != 1 ||
		items[0].Type != llm.ResponseItemTypeMessage ||
		items[0].Role == nil ||
		*items[0].Role != llm.RoleDeveloper ||
		items[0].MessageType == nil ||
		*items[0].MessageType != llm.MessageTypeCompactionSummary {
		t.Fatalf("committed replacement active items = %+v", items)
	}
	for _, event := range events {
		if event.Kind == EventConversationUpdated {
			return
		}
	}
	t.Fatalf("committed replacement did not publish typed conversation update: %+v", events)
}

type workflowBudgetResetFailureController struct {
	externallyCompletedWorkflowController
	err error
}

func (c *workflowBudgetResetFailureController) ResetProtocolViolationBudget(
	context.Context,
	workflowruntime.ViolationResetRequest,
) error {
	return c.err
}
