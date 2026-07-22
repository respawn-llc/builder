package runtime

import (
	"context"
	"errors"
	"testing"

	"core/server/llm"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/server/tools"
	"core/server/workflow"
	"core/server/workflowruntime"
	"core/shared/textutil"
)

func TestReplaceHistoryDoesNotMutateRuntimeStateWhenEventAppendFails(t *testing.T) {
	store := mustCreateTestSession(t)
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
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

	receipt, err := newCompactionPersistence(engine).replaceHistory(
		"compact",
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			Content:     textutil.Value("summary"),
		}}),
	)
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

func TestCommittedCompactionHistoryReplacementInvalidatesUsageAcrossImmediateReopen(t *testing.T) {
	observerErr := errors.New("history replacement metadata observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{Model: "gpt-5"})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
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

	receipt, err := newCompactionPersistence(engine).replaceHistory(
		"compact",
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			Content:     textutil.Value("summary"),
		}}),
	)
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

func TestCommittedHistoryReplacementPreventsStaleUsageFromLaterMetadataPersistence(t *testing.T) {
	replacementErr := errors.New("history replacement observer failure")
	usageErr := errors.New("compacted usage observer failure")
	gate := sessiontest.NewPersistenceGate(runtimeTestSessionPersistence)
	store := mustCreateTestSessionAt(t, t.TempDir(), session.WithPersistenceObserver(gate))
	engine := mustNewTestEngine(t, store, &fakeClient{}, tools.NewRegistry(), Config{Model: "gpt-5"})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
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

	receipt, err := newCompactionPersistence(engine).replaceHistory(
		"compact",
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			Content:     textutil.Value("summary"),
		}}),
	)
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
	resetErr := errors.New("workflow protocol budget reset failure")
	runID := workflow.RunID("workflow-run")
	controller := &workflowBudgetResetFailureController{err: resetErr}
	store := mustCreateTestSession(t)
	var events []Event
	engine := mustNewExecTestEngine(t, store, &fakeClient{}, Config{
		Model: "gpt-5",
		WorkflowRun: &workflowruntime.Config{
			RunID:          runID,
			Contract:       workflowruntime.CompletionContract{RunID: runID},
			CompletionMode: workflowruntime.CompletionModeTool,
			Controller:     controller,
		},
		OnEvent: func(event Event) {
			events = append(events, event)
		},
	})
	if err := engine.steer("seed", steerMessagesWithPersistenceIntent(
		steeringPriorityNormal,
		steeringMessageEventNone,
		true,
		[]llm.Message{{Role: llm.RoleUser, Content: textutil.Value("input")}},
	)); err != nil {
		t.Fatalf("persist seed message: %v", err)
	}

	receipt, err := newCompactionPersistence(engine).replaceHistory(
		"compact",
		"local",
		compactionModeManual,
		llm.ItemsFromMessages([]llm.Message{{
			Role:        llm.RoleDeveloper,
			MessageType: textutil.Value(llm.MessageTypeCompactionSummary),
			Content:     textutil.Value("summary"),
		}}),
	)
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

func (c *workflowBudgetResetFailureController) ResetWorkflowProtocolViolationBudget(
	context.Context,
	workflowruntime.ViolationResetRequest,
) error {
	return c.err
}
