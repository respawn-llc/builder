package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/filemode"
	"core/shared/sessioncontract"
)

func appendSessionTestEvent(t *testing.T, store *Store, stepID, kind string, payload any) Event {
	t.Helper()
	event, _, err := store.AppendEvent(stepID, kind, payload)
	if err != nil {
		t.Fatalf("append %s event: %v", kind, err)
	}
	return event
}

func sessionTestLockedContract() LockedContract {
	toolPreambles := true
	return LockedContract{
		Model:             "gpt-5",
		SystemPrompt:      "prompt",
		HasSystemPrompt:   true,
		ReviewerPrompt:    "reviewer",
		HasReviewerPrompt: true,
		EnabledTools:      []string{"shell"},
		HasEnabledTools:   true,
		WebSearchMode:     "native",
		ToolPreambles:     &toolPreambles,
	}
}

func markSessionTestLocked(t *testing.T, store *Store, locked LockedContract) {
	t.Helper()
	if err := store.MarkModelDispatchLocked(locked); err != nil {
		t.Fatalf("mark model dispatch locked: %v", err)
	}
}

func TestNewLazyDoesNotPersistUntilFirstWrite(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if _, err := os.Stat(store.Dir()); !os.IsNotExist(err) {
		t.Fatalf("expected no session dir before first write, stat err=%v", err)
	}

	appendSessionTestEvent(t, store, "step1", "message", map[string]any{"a": 1})
	if _, err := os.Stat(filepath.Join(store.Dir(), eventsFile)); err != nil {
		t.Fatalf("expected events file after first write: %v", err)
	}
}

func TestSessionCategoryRequiredForFreshAndLazyStores(t *testing.T) {
	root := t.TempDir()
	mainStore, err := Create(root, "workspace-main", "/tmp/main", sessioncontract.SessionCategoryMain, sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("create main store: %v", err)
	}
	if got := mainStore.Meta().Category; got == nil || *got != sessioncontract.SessionCategoryMain {
		t.Fatalf("main category = %v", got)
	}

	subagentStore, err := NewLazy(root, "workspace-subagent", "/tmp/subagent", sessioncontract.SessionCategorySubagent)
	if err != nil {
		t.Fatalf("create lazy subagent store: %v", err)
	}
	if got := subagentStore.Meta().Category; got == nil || *got != sessioncontract.SessionCategorySubagent {
		t.Fatalf("subagent category = %v", got)
	}
}

func TestSessionCategoryUnaffectedByUnrelatedMetadataMutations(t *testing.T) {
	store, err := Create(t.TempDir(), "workspace", "/tmp/work", sessioncontract.SessionCategorySubagent, sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	assertSubagent := func(operation string) {
		t.Helper()
		got := store.Meta().Category
		if got == nil || *got != sessioncontract.SessionCategorySubagent {
			t.Fatalf("category after %s = %v, want subagent", operation, got)
		}
	}

	if err := store.SetName("renamed session"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	assertSubagent("rename")
	parentSessionID := "legacy-parent"
	if err := store.SetParentSessionID(&parentSessionID); err != nil {
		t.Fatalf("set parent: %v", err)
	}
	assertSubagent("parent assignment")
	if err := store.SetHeadlessActive(true); err != nil {
		t.Fatalf("set headless active: %v", err)
	}
	assertSubagent("headless activation")
}

func TestSessionCategoryPromotionIsOneWayAndAdvancesRecencyOnce(t *testing.T) {
	now := time.Date(2026, time.July, 14, 10, 0, 0, 0, time.UTC)
	store, err := Create(
		t.TempDir(),
		"workspace",
		"/tmp/work",
		sessioncontract.SessionCategorySubagent,
		append(sessionTestPersistence.options(), WithClock(func() time.Time { return now }))...,
	)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	createdAt := store.Meta().UpdatedAt

	now = now.Add(time.Hour)
	changed, err := store.PromoteSubagentToMain()
	if err != nil {
		t.Fatalf("promote subagent: %v", err)
	}
	if !changed {
		t.Fatal("first promotion reported no change")
	}
	promoted := store.Meta()
	if promoted.Category == nil || *promoted.Category != sessioncontract.SessionCategoryMain {
		t.Fatalf("promoted category = %v, want main", promoted.Category)
	}
	if !promoted.UpdatedAt.Equal(now) || !promoted.UpdatedAt.After(createdAt) {
		t.Fatalf("promoted updated_at = %v, want %v after %v", promoted.UpdatedAt, now, createdAt)
	}

	now = now.Add(time.Hour)
	changed, err = store.PromoteSubagentToMain()
	if err != nil {
		t.Fatalf("repeat promotion: %v", err)
	}
	if changed {
		t.Fatal("repeat promotion reported a change")
	}
	if got := store.Meta().UpdatedAt; !got.Equal(promoted.UpdatedAt) {
		t.Fatalf("repeat promotion advanced recency from %v to %v", promoted.UpdatedAt, got)
	}
}

func TestNewLazyReadEventsBeforePersistReturnsEmpty(t *testing.T) {
	store := newSessionTestLazyStore(t)
	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events len = %d, want 0", len(events))
	}
}

func TestBackfillLockedContextBudgetWithoutLockedContractDoesNotPersistLazyStore(t *testing.T) {
	store := newSessionTestLazyStore(t)

	if err := store.BackfillLockedContextBudget(1000, 50); err != nil {
		t.Fatalf("BackfillLockedContextBudget: %v", err)
	}
	if _, err := os.Stat(store.Dir()); !os.IsNotExist(err) {
		t.Fatalf("expected no session dir after no-op backfill, stat err=%v", err)
	}
}

func TestSetWorkflowSessionStateNormalizesEmptyStateToNil(t *testing.T) {
	store := newSessionTestStore(t)

	if err := store.SetWorkflowSessionState(&WorkflowSessionState{
		RunID:      "   ",
		TaskID:     "\t",
		WorkflowID: "\n",
	}); err != nil {
		t.Fatalf("SetWorkflowSessionState: %v", err)
	}
	if store.Meta().WorkflowSession != nil {
		t.Fatalf("workflow session = %+v, want nil", store.Meta().WorkflowSession)
	}
}

func TestAppendEventMonotonicSequence(t *testing.T) {
	store := newSessionTestStore(t)

	e1 := appendSessionTestEvent(t, store, "step1", "message", map[string]any{"a": 1})
	e2 := appendSessionTestEvent(t, store, "step1", "message", map[string]any{"b": 2})

	if e1.Seq != 1 || e2.Seq != 2 {
		t.Fatalf("unexpected sequence values: %d, %d", e1.Seq, e2.Seq)
	}

	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("persisted sequence mismatch: %+v", events)
	}
}

func TestInputDraftAndRecoveryPersistAcrossReopenAndClearTogether(t *testing.T) {
	store := newSessionTestLazyStore(t)
	want := "draft line one\nline two"
	if err := store.SetInputDraft(want); err != nil {
		t.Fatalf("set input draft: %v", err)
	}
	reopened := mustOpenSessionTestStore(t, store)
	if reopened.Meta().InputDraft != want {
		t.Fatalf("expected persisted draft %q, got %q", want, reopened.Meta().InputDraft)
	}

	if err := reopened.SetInputDraftRecovery("visible", []InputDraftRecoveryBuffer{{
		Kind:                     "active_submit",
		ID:                       "queued-1",
		Text:                     "submitted before forced exit",
		OperationKind:            "submit",
		OperationClientRequestID: "submit-1",
	}}); err != nil {
		t.Fatalf("set input draft recovery: %v", err)
	}
	recovered := mustOpenSessionTestStore(t, store)
	if recovered.Meta().InputDraft != "visible" || len(recovered.Meta().InputDraftRecoveryBuffers) != 1 {
		t.Fatalf("reopened draft metadata = %+v", recovered.Meta())
	}
	if recovered.Meta().InputDraftRecoveryBuffers[0].OperationClientRequestID != "submit-1" {
		t.Fatalf("recovery buffer = %+v, want operation client request id", recovered.Meta().InputDraftRecoveryBuffers[0])
	}
	if err := recovered.SetInputDraft(""); err != nil {
		t.Fatalf("clear input draft: %v", err)
	}
	cleared := mustOpenSessionTestStore(t, store)
	if cleared.Meta().InputDraft != "" || len(cleared.Meta().InputDraftRecoveryBuffers) != 0 {
		t.Fatalf("cleared draft metadata = %+v, want no draft recovery", cleared.Meta())
	}
}

func TestSetUsageStatePersistsAcrossReopen(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if _, err := store.SetUsageState(&UsageState{
		InputTokens:             900,
		OutputTokens:            120,
		WindowTokens:            400_000,
		CachedInputTokens:       50,
		HasCachedInputTokens:    true,
		EstimatedProviderTokens: 180,
		TotalInputTokens:        1_200,
		TotalCachedInputTokens:  60,
	}); err != nil {
		t.Fatalf("set usage state: %v", err)
	}
	reopened := mustOpenSessionTestStore(t, store)
	if reopened.Meta().UsageState == nil {
		t.Fatal("expected persisted usage state")
	}
	if got := reopened.Meta().UsageState; got.InputTokens != 900 || got.EstimatedProviderTokens != 180 || got.TotalInputTokens != 1_200 {
		t.Fatalf("unexpected usage state after reopen: %+v", got)
	}
}

func TestLockedContractPersistenceIncludesPromptAndRequestSnapshots(t *testing.T) {
	store := newSessionTestStore(t)
	contract := sessionTestLockedContract()
	contract.EnabledTools = nil
	markSessionTestLocked(t, store, contract)
	opened := mustOpenSessionTestStore(t, store)
	locked := opened.Meta().Locked
	if locked == nil || locked.SystemPrompt != "prompt" || !locked.HasSystemPrompt {
		t.Fatalf("locked system prompt = %+v, want persisted snapshot marker", locked)
	}
	if locked.ReviewerPrompt != "reviewer" || !locked.HasReviewerPrompt {
		t.Fatalf("locked reviewer prompt = %+v, want persisted snapshot marker", locked)
	}
	if !locked.HasEnabledTools || len(locked.EnabledTools) != 0 || locked.WebSearchMode != "native" {
		t.Fatalf("locked request shape = %+v, want explicit zero tools and native web search", locked)
	}
}

func TestResetLockedContractForCompactionBoundaryPersistsFreshContractBoundary(t *testing.T) {
	store := newSessionTestStore(t)
	markSessionTestLocked(t, store, sessionTestLockedContract())

	if err := store.ResetLockedContractForCompactionBoundary(); err != nil {
		t.Fatalf("reset locked contract for compaction boundary: %v", err)
	}
	if locked := store.Meta().Locked; locked != nil {
		t.Fatalf("in-memory locked contract = %+v, want absent", locked)
	}
	if got := store.Meta().PromptCacheLineageGeneration; got != 1 {
		t.Fatalf("prompt cache lineage generation = %d, want 1", got)
	}
	opened := mustOpenSessionTestStore(t, store)
	if locked := opened.Meta().Locked; locked != nil {
		t.Fatalf("persisted locked contract = %+v, want absent", locked)
	}
	if got := opened.Meta().PromptCacheLineageGeneration; got != 1 {
		t.Fatalf("persisted prompt cache lineage generation = %d, want 1", got)
	}
}

func TestLockedPromptFacingMutationsPreserveLifetimeFields(t *testing.T) {
	store := newSessionTestStore(t)
	toolPreambles := true
	markSessionTestLocked(t, store, sessionTestLockedContract())
	stale, err := store.MarkLockedPromptFacingSnapshotsStale()
	if err != nil {
		t.Fatalf("mark stale: %v", err)
	}
	if !stale.Committed || stale.Locked == nil {
		t.Fatalf("stale result = %+v, want committed lock", stale)
	}
	if stale.Locked.SystemPrompt != "" || stale.Locked.HasSystemPrompt || stale.Locked.ReviewerPrompt != "" || stale.Locked.HasReviewerPrompt {
		t.Fatalf("stale locked prompts = %+v, want cleared", stale.Locked)
	}
	if stale.Locked.Model != "gpt-5" || stale.Locked.WebSearchMode != "native" || len(stale.Locked.EnabledTools) != 1 || !stale.Locked.HasEnabledTools {
		t.Fatalf("stale lifetime fields = %+v", stale.Locked)
	}
	refreshed, err := store.RefreshLockedMainPromptSnapshot(LockedMainPromptSnapshot{
		SystemPrompt:    "prompt B",
		HasSystemPrompt: true,
		ToolPreambles:   &toolPreambles,
		ContextWindow:   200,
		ContextPercent:  60,
	})
	if err != nil {
		t.Fatalf("refresh main: %v", err)
	}
	if refreshed.Locked.SystemPrompt != "prompt B" || !refreshed.Locked.HasSystemPrompt || refreshed.Locked.ReviewerPrompt != "" {
		t.Fatalf("main refresh lock = %+v", refreshed.Locked)
	}
	reviewer, err := store.RefreshLockedReviewerPromptSnapshot(LockedReviewerPromptSnapshot{ReviewerPrompt: "reviewer B", HasReviewerPrompt: true})
	if err != nil {
		t.Fatalf("refresh reviewer: %v", err)
	}
	if reviewer.Locked.ReviewerPrompt != "reviewer B" || !reviewer.Locked.HasReviewerPrompt || reviewer.Locked.SystemPrompt != "prompt B" {
		t.Fatalf("reviewer refresh lock = %+v", reviewer.Locked)
	}
}

func TestLockedRequestShapeBackfillPersistsTogether(t *testing.T) {
	store := newSessionTestStore(t)
	if err := store.MarkModelDispatchLocked(LockedContract{Model: "gpt-5", SystemPrompt: "prompt", HasSystemPrompt: true}); err != nil {
		t.Fatalf("mark model dispatch locked: %v", err)
	}
	result, err := store.BackfillLockedRequestShape(LockedRequestShapeBackfill{
		EnabledTools:    []string{"shell", "patch"},
		HasEnabledTools: true,
		WebSearchMode:   "native",
	})
	if err != nil {
		t.Fatalf("backfill request shape: %v", err)
	}
	if !result.Committed || result.Locked == nil || !result.Locked.HasEnabledTools || strings.Join(result.Locked.EnabledTools, ",") != "shell,patch" || result.Locked.WebSearchMode != "native" {
		t.Fatalf("request shape result = %+v", result)
	}
	opened := mustOpenSessionTestStore(t, store)
	if locked := opened.Meta().Locked; locked == nil || !locked.HasEnabledTools || locked.WebSearchMode != "native" || len(locked.EnabledTools) != 2 {
		t.Fatalf("persisted request shape = %+v", locked)
	}
}

func TestLockedPromptFacingContractStaleClearsRequestShape(t *testing.T) {
	store := newSessionTestStore(t)
	markSessionTestLocked(t, store, sessionTestLockedContract())

	result, err := store.MarkLockedPromptFacingContractStale()
	if err != nil {
		t.Fatalf("mark contract stale: %v", err)
	}
	if !result.Committed || result.Locked == nil {
		t.Fatalf("stale contract result = %+v, want committed lock", result)
	}
	locked := result.Locked
	if locked.SystemPrompt != "" || locked.HasSystemPrompt || locked.ReviewerPrompt != "" || locked.HasReviewerPrompt {
		t.Fatalf("stale contract prompts = %+v, want cleared", locked)
	}
	if len(locked.EnabledTools) != 0 || locked.HasEnabledTools || locked.WebSearchMode != "" || locked.ToolPreambles != nil {
		t.Fatalf("stale contract request shape = %+v, want cleared", locked)
	}
	if locked.Model != "gpt-5" {
		t.Fatalf("stale contract model = %q, want preserved", locked.Model)
	}
}

func TestSetContinuationContextAndLockedPromptFacingContractStalePersistsTogether(t *testing.T) {
	store := newSessionTestStore(t)
	markSessionTestLocked(t, store, sessionTestLockedContract())

	role := "reviewer"
	result, err := store.SetContinuationContextAndMarkLockedPromptFacingContractStale(ContinuationContext{AgentRole: &role})
	if err != nil {
		t.Fatalf("set continuation and stale contract: %v", err)
	}
	if !result.Committed || result.Locked == nil {
		t.Fatalf("mutation result = %+v, want committed lock", result)
	}
	opened := mustOpenSessionTestStore(t, store)
	meta := opened.Meta()
	if meta.Continuation == nil || meta.Continuation.AgentRole == nil || *meta.Continuation.AgentRole != "reviewer" {
		t.Fatalf("continuation = %+v, want reviewer", meta.Continuation)
	}
	if locked := meta.Locked; locked == nil || locked.HasSystemPrompt || locked.HasEnabledTools || len(locked.EnabledTools) != 0 || locked.WebSearchMode != "" {
		t.Fatalf("locked contract = %+v, want prompt-facing fields cleared", locked)
	}
}

func TestLockedContractMutationObserverCommitSemantics(t *testing.T) {
	observer := &recordingPersistenceObserver{}
	store, err := Create(t.TempDir(), "ws", t.TempDir(), testSessionCategory, WithPersistenceObserver(observer))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	observer.err = os.ErrPermission
	if err := store.MarkModelDispatchLocked(LockedContract{Model: "gpt-5", SystemPrompt: "prompt A", HasSystemPrompt: true}); err == nil {
		t.Fatal("expected observer error on initial lock")
	}
	before := store.Meta().Locked
	result, err := store.MarkLockedPromptFacingSnapshotsStale()
	if err == nil || result.Committed {
		t.Fatalf("observer result=%+v err=%v, want uncommitted failure", result, err)
	}
	if after := store.Meta().Locked; before == nil || after == nil || after.SystemPrompt != before.SystemPrompt || !after.HasSystemPrompt {
		t.Fatalf("lock after failed mutation = %+v, before %+v", after, before)
	}
}

func TestReadEventsHandlesLargeJSONLines(t *testing.T) {
	store := newSessionTestStore(t)

	const payloadSize = 128 * 1024
	large := strings.Repeat("x", payloadSize)
	appendSessionTestEvent(t, store, "step1", "message", map[string]any{"blob": large})

	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}

	var payload map[string]string
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got := len(payload["blob"]); got != payloadSize {
		t.Fatalf("payload blob size = %d, want %d", got, payloadSize)
	}
}

func TestAppendEventPersistsFirstPromptPreview(t *testing.T) {
	store := newSessionTestStore(t)
	appendSessionTestEvent(t, store, "s1", "message", map[string]any{"role": "assistant", "content": "hello"})
	appendSessionTestEvent(t, store, "s1", "message", map[string]any{"role": "developer", "message_type": "compaction_summary", "content": "summary"})
	if got := store.Meta().FirstPromptPreview; got != "" {
		t.Fatalf("non-user messages set preview %q", got)
	}
	appendSessionTestEvent(t, store, "s2", "message", map[string]any{"role": "user", "content": "\n  Investigate config load failures\nsecond line"})
	if got := store.Meta().FirstPromptPreview; got != "Investigate config load failures" {
		t.Fatalf("preview = %q, want normalized first user line", got)
	}

	opened := mustOpenSessionTestStore(t, store)
	if got := opened.Meta().FirstPromptPreview; got != "Investigate config load failures" {
		t.Fatalf("reopened preview = %q, want persisted first user line", got)
	}
}

func TestSetListingMetadataPersistsNameAndFirstPromptPreview(t *testing.T) {
	root := t.TempDir()
	observer := &recordingPersistenceObserver{}
	store, err := Create(root, "workspace-x", "/tmp/work", testSessionCategory, WithPersistenceObserver(observer))
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.SetListingMetadata("  Workflow Session  ", "\n  Rendered workflow prompt\nsecond line"); err != nil {
		t.Fatalf("SetListingMetadata: %v", err)
	}
	meta := store.Meta()
	if meta.Name != "Workflow Session" || meta.FirstPromptPreview != "Rendered workflow prompt" {
		t.Fatalf("metadata = name %q preview %q, want trimmed name and normalized preview", meta.Name, meta.FirstPromptPreview)
	}
	if !observer.called || observer.snapshot.Meta.Name != "Workflow Session" || observer.snapshot.Meta.FirstPromptPreview != "Rendered workflow prompt" {
		t.Fatalf("observer snapshot = %+v, called %v", observer.snapshot.Meta, observer.called)
	}

	appendSessionTestEvent(t, store, "s1", "message", map[string]any{"role": "user", "content": "event prompt"})
	if got := store.Meta().FirstPromptPreview; got != "Rendered workflow prompt" {
		t.Fatalf("event capture overwrote explicit preview: %q", got)
	}

	longPreview := strings.Repeat("x", firstPromptPreviewMaxChars+5)
	if err := store.SetListingMetadata("Updated", longPreview); err != nil {
		t.Fatalf("SetListingMetadata overwrite: %v", err)
	}
	wantTruncated := strings.Repeat("x", firstPromptPreviewMaxChars-1) + "…"
	if got := store.Meta().FirstPromptPreview; got != wantTruncated {
		t.Fatalf("truncated preview = %q, want %q", got, wantTruncated)
	}

	if err := store.SetListingMetadata("  ", " \n\t "); err != nil {
		t.Fatalf("SetListingMetadata clear: %v", err)
	}
	if store.Meta().Name != "" || store.Meta().FirstPromptPreview != "" {
		t.Fatalf("cleared metadata = %+v, want empty name and preview", store.Meta())
	}

	reopened, err := Open(store.Dir(), WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
		SessionDir: observer.snapshot.SessionDir,
		Meta:       &observer.snapshot.Meta,
	}}))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if reopened.Meta().Name != "" || reopened.Meta().FirstPromptPreview != "" {
		t.Fatalf("reopened metadata = %+v, want empty name and preview", reopened.Meta())
	}
}

func TestParentSessionIDValidationClearingAndSnapshotIsolation(t *testing.T) {
	store := newSessionTestStore(t)
	parentSessionID := "parent-session"
	if err := store.SetParentSessionID(&parentSessionID); err != nil {
		t.Fatalf("SetParentSessionID: %v", err)
	}

	snapshot := store.Meta()
	if snapshot.ParentSessionID == nil {
		t.Fatal("snapshot parent session id is nil")
	}
	*snapshot.ParentSessionID = "mutated-snapshot"
	if got := store.Meta().ParentSessionID; got == nil || *got != parentSessionID {
		t.Fatalf("store parent session id = %v after snapshot mutation", got)
	}

	blankParentSessionID := " \t "
	if err := store.SetParentSessionID(&blankParentSessionID); err == nil {
		t.Fatal("SetParentSessionID blank value unexpectedly succeeded")
	}
	if got := store.Meta().ParentSessionID; got == nil || *got != parentSessionID {
		t.Fatalf("parent session id changed after rejected write: %v", got)
	}
	if err := store.SetParentSessionID(nil); err != nil {
		t.Fatalf("SetParentSessionID(nil): %v", err)
	}
	if got := store.Meta().ParentSessionID; got != nil {
		t.Fatalf("parent session id = %v, want nil", got)
	}
}

func TestConversationFreshnessAdvancesOnlyForVisibleUserMessages(t *testing.T) {
	store := newSessionTestStore(t)
	if got := store.ConversationFreshness(); got != ConversationFreshnessFresh {
		t.Fatalf("freshness = %v, want fresh", got)
	}
	appendSessionTestEvent(t, store, "s1", "message", map[string]any{"role": "assistant", "content": "hello"})
	if got := store.ConversationFreshness(); got != ConversationFreshnessFresh {
		t.Fatalf("freshness after assistant = %v, want fresh", got)
	}
	appendSessionTestEvent(t, store, "s2", "message", map[string]any{"role": "developer", "message_type": "compaction_summary", "content": "summary"})
	if got := store.ConversationFreshness(); got != ConversationFreshnessFresh {
		t.Fatalf("freshness after compaction summary = %v, want fresh", got)
	}
	appendSessionTestEvent(t, store, "s3", "message", map[string]any{"role": "user", "content": "Investigate config load failures"})
	if got := store.ConversationFreshness(); got != ConversationFreshnessEstablished {
		t.Fatalf("freshness after visible user message = %v, want established", got)
	}
	opened := mustOpenSessionTestStore(t, store)
	if got := opened.ConversationFreshness(); got != ConversationFreshnessEstablished {
		t.Fatalf("reopened freshness = %v, want established", got)
	}
}

func TestOpenBackfillsConversationFreshnessFromTail(t *testing.T) {
	store := newSessionTestStore(t)
	appendSessionTestEvent(t, store, "s1", "message", map[string]any{"role": "user", "content": "established session"})

	meta := store.Meta()
	meta.ConversationEstablished = false

	opened, err := Open(
		store.Dir(),
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: store.Dir(),
			Meta:       &meta,
		}}),
		WithPersistenceObserver(sessionTestPersistence),
	)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if got := opened.ConversationFreshness(); got != ConversationFreshnessEstablished {
		t.Fatalf("backfilled freshness = %v, want established", got)
	}
	if !opened.Meta().ConversationEstablished {
		t.Fatalf("expected backfill to persist conversation_established flag")
	}
}

func TestOpenRecoversLastSequenceFromTailWhenMetaStale(t *testing.T) {
	store := newSessionTestStore(t)
	for i := 0; i < 3; i++ {
		appendSessionTestEvent(t, store, "s1", "message", map[string]any{"role": "assistant", "content": "reply"})
	}
	trueLastSeq := store.Meta().LastSequence

	meta := store.Meta()
	meta.LastSequence = 0
	persistence := &testSessionMetadata{records: map[string]PersistedSessionRecord{
		meta.SessionID: {
			SessionDir: store.Dir(),
			Meta:       &meta,
		},
	}}

	opened, err := Open(
		store.Dir(),
		WithPersistedSessionResolver(persistence),
		WithPersistenceObserver(persistence),
	)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if got := opened.Meta().LastSequence; got != trueLastSeq {
		t.Fatalf("recovered last sequence = %d, want %d", got, trueLastSeq)
	}
}

func TestAppendTurnAtomicPersistsFirstPromptPreview(t *testing.T) {
	store := newSessionTestStore(t)
	if _, receipt, err := store.AppendTurnAtomic("s1", []EventInput{{Kind: "message", Payload: map[string]any{"role": "assistant", "content": "hello"}}, {Kind: "message", Payload: map[string]any{"role": "user", "content": "Atomic preview source\nmore"}}}); err != nil {
		t.Fatalf("append turn: %v", err)
	} else if !receipt.Committed {
		t.Fatal("append turn returned an uncommitted receipt")
	}
	if got := store.Meta().FirstPromptPreview; got != "Atomic preview source" {
		t.Fatalf("preview = %q, want %q", got, "Atomic preview source")
	}
}

func TestAppendTurnAtomicReportsUncommittedEventLogFailure(t *testing.T) {
	store := newSessionTestStore(t)
	filemode.MustBlockEventLogAppends(t, store.eventsFP)

	events, receipt, err := store.AppendTurnAtomic("s1", []EventInput{{
		Kind:    "message",
		Payload: map[string]any{"role": "user", "content": "must not commit"},
	}})
	if err == nil {
		t.Fatal("append turn did not surface the event-log failure")
	}
	if receipt.Committed {
		t.Fatalf("append turn receipt = %+v, want uncommitted", receipt)
	}
	if len(events) != 1 {
		t.Fatalf("built events = %+v, want the attempted event", events)
	}
	if meta := store.Meta(); meta.LastSequence != 0 || meta.FirstPromptPreview != "" {
		t.Fatalf("metadata mutated after uncommitted append: %+v", meta)
	}
}

func TestAppendTurnAtomicReportsCommittedObserverFailure(t *testing.T) {
	observer := &recordingPersistenceObserver{}
	store, err := Create(
		t.TempDir(),
		"workspace",
		t.TempDir(),
		testSessionCategory,
		WithPersistenceObserver(observer),
	)
	if err != nil {
		t.Fatalf("create observed store: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist observed store: %v", err)
	}
	observer.err = os.ErrPermission

	events, receipt, err := store.AppendTurnAtomic("s1", []EventInput{{
		Kind:    "message",
		Payload: map[string]any{"role": "user", "content": "committed despite observer failure"},
	}})
	if err == nil {
		t.Fatal("append turn did not surface the observer failure")
	}
	if !receipt.Committed {
		t.Fatalf("append turn receipt = %+v, want committed", receipt)
	}
	if len(events) != 1 || events[0].Seq != 1 {
		t.Fatalf("committed events = %+v, want one sequence-1 event", events)
	}
	if meta := store.Meta(); meta.LastSequence != 1 || meta.FirstPromptPreview != "committed despite observer failure" {
		t.Fatalf("metadata after committed observer failure = %+v", meta)
	}
}

func userMessageSeqAt(t *testing.T, store *Store, n int) int64 {
	t.Helper()
	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("collect events: %v", err)
	}
	visible := 0
	for _, evt := range events {
		if hasVisibleUserMessageEvent(evt.Kind, evt.Payload) {
			visible++
			if visible == n {
				return evt.Seq
			}
		}
	}
	t.Fatalf("user message %d not found among %d events", n, len(events))
	return 0
}

func TestForkAtUserMessageCopiesPrefixBeforeSelectedMessage(t *testing.T) {
	root := t.TempDir()
	parent := newSessionTestStoreAt(t, root)
	contract := sessionTestLockedContract()
	contract.Model = "locked-parent"
	contract.SystemPrompt = "parent prompt snapshot"
	contract.ReviewerPrompt = "parent reviewer prompt snapshot"
	markSessionTestLocked(t, parent, contract)
	appendSessionTestEvent(t, parent, "s1", "message", map[string]any{"role": "user", "content": "u1"})
	appendSessionTestEvent(t, parent, "s1", "message", map[string]any{"role": "assistant", "content": "a1"})
	appendSessionTestEvent(t, parent, "s2", "message", map[string]any{"role": "user", "content": "u2"})
	appendSessionTestEvent(t, parent, "s2", "message", map[string]any{"role": "assistant", "content": "a2"})

	forked, _, err := ForkAtUserMessage(parent, userMessageSeqAt(t, parent, 2), "Parent → edit u2", testSessionCategory)
	if err != nil {
		t.Fatalf("fork at user message: %v", err)
	}
	forkEvents, err := collectEvents(forked)
	if err != nil {
		t.Fatalf("read fork events: %v", err)
	}
	if len(forkEvents) != 2 {
		t.Fatalf("expected two replayed events, got %d", len(forkEvents))
	}
	var first struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(forkEvents[0].Payload, &first); err != nil {
		t.Fatalf("decode first message: %v", err)
	}
	if first.Role != "user" || first.Content != "u1" {
		t.Fatalf("unexpected first message in fork: %+v", first)
	}
	meta := forked.Meta()
	if meta.ParentSessionID == nil || *meta.ParentSessionID != parent.Meta().SessionID {
		t.Fatalf("expected fork parent session id, got %v", meta.ParentSessionID)
	}
	if meta.Name != "Parent → edit u2" {
		t.Fatalf("expected fork name, got %q", meta.Name)
	}
	if meta.FirstPromptPreview != "u1" {
		t.Fatalf("expected fork preview to persist first user message, got %q", meta.FirstPromptPreview)
	}
	if meta.Locked == nil || meta.Locked.SystemPrompt != "parent prompt snapshot" || !meta.Locked.HasSystemPrompt {
		t.Fatalf("fork locked system prompt = %+v, want replay fork to preserve parent prompt snapshot", meta.Locked)
	}
	if meta.Locked.ReviewerPrompt != "parent reviewer prompt snapshot" || !meta.Locked.HasReviewerPrompt {
		t.Fatalf("fork locked reviewer prompt = %+v, want replay fork to preserve parent reviewer prompt snapshot", meta.Locked)
	}
}

func TestForkAtUserMessageDerivesReminderIssuedFromReplayedHistory(t *testing.T) {
	user := func(content string) EventInput {
		return EventInput{Kind: "message", Payload: map[string]any{"role": "user", "content": content}}
	}
	reminder := EventInput{Kind: "message", Payload: map[string]any{
		"role": "developer", "message_type": "compaction_soon_reminder", "content": "compact soon",
	}}
	replacement := func(engine string, items []map[string]any) EventInput {
		return EventInput{Kind: "history_replaced", Payload: map[string]any{"engine": engine, "items": items}}
	}
	cases := []struct {
		name              string
		events            []EventInput
		forkAtUser        int
		persistedReminder bool
		wantReminder      bool
	}{
		{"before reminder", []EventInput{user("u1"), reminder, user("u2")}, 1, true, false},
		{"after reminder", []EventInput{user("u1"), reminder, user("u2")}, 2, true, true},
		{"legacy reviewer rollback injected reminder ignored", []EventInput{
			user("u1"),
			replacement("reviewer_rollback", []map[string]any{{
				"type": "message", "role": "developer", "message_type": "compaction_soon_reminder", "content": "compact soon",
			}}),
			user("u2"),
		}, 2, false, false},
		{"compaction clears reminder", []EventInput{
			user("u1"), reminder, replacement("compaction", []map[string]any{}), user("u2"),
		}, 2, false, false},
		{"legacy reviewer rollback preserves earlier reminder", []EventInput{
			user("u1"), reminder, replacement("reviewer_rollback", []map[string]any{}), user("u2"),
		}, 2, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := newSessionTestStore(t)
			if _, receipt, err := parent.AppendTurnAtomic("replay", tc.events); err != nil {
				t.Fatalf("append replay fixture: %v", err)
			} else if !receipt.Committed {
				t.Fatal("replay fixture was not committed")
			}
			if tc.persistedReminder {
				if err := parent.SetCompactionSoonReminderIssued(true); err != nil {
					t.Fatalf("persist reminder state: %v", err)
				}
			}
			forked, _, err := ForkAtUserMessage(parent, userMessageSeqAt(t, parent, tc.forkAtUser), tc.name, testSessionCategory)
			if err != nil {
				t.Fatalf("fork: %v", err)
			}
			if got := forked.Meta().CompactionSoonReminderIssued; got != tc.wantReminder {
				t.Fatalf("reminder-issued = %v, want %v", got, tc.wantReminder)
			}
		})
	}
}

func TestForkAtUserMessageCopiesWorktreeReminderTarget(t *testing.T) {
	parent := newSessionTestStore(t)
	appendSessionTestEvent(t, parent, "s1", "message", map[string]any{"role": "user", "content": "u1"})
	if err := parent.SetWorktreeReminderState(&WorktreeReminderState{
		Mode: WorktreeReminderModeEnter,
		WorktreeContext: WorktreeContext{
			Branch:        OptionalWorktreeBranch("feature/fork"),
			WorktreePath:  "/tmp/wt-fork",
			WorkspaceRoot: "/tmp/workspace",
			EffectiveCwd:  "/tmp/wt-fork",
		},
	}); err != nil {
		t.Fatalf("persist worktree reminder state: %v", err)
	}
	appendSessionTestEvent(t, parent, "s2", "message", map[string]any{"role": "user", "content": "u2"})

	forked, _, err := ForkAtUserMessage(parent, userMessageSeqAt(t, parent, 2), "forked", testSessionCategory)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	state := forked.Meta().WorktreeReminder
	if state == nil {
		t.Fatal("expected forked worktree reminder state")
	}
	if state.Mode != WorktreeReminderModeEnter ||
		state.Branch == nil ||
		*state.Branch != "feature/fork" ||
		state.WorktreePath != "/tmp/wt-fork" ||
		state.EffectiveCwd != "/tmp/wt-fork" ||
		state.ContextID == nil {
		t.Fatalf("unexpected forked reminder payload: %+v", state)
	}
	parentState := parent.Meta().WorktreeReminder
	if parentState == nil || !WorktreeReminderStateEqual(*parentState, *state) {
		t.Fatalf("expected parent reminder state unchanged, got %+v", parentState)
	}
	if parentState.ContextID == state.ContextID || parentState.Branch == state.Branch {
		t.Fatal("expected forked reminder pointers to be deep-copied")
	}
}

func TestSetWorktreeReminderStateOwnsStableTargetContextID(t *testing.T) {
	store := newSessionTestStore(t)
	firstTarget := WorktreeReminderState{
		Mode: WorktreeReminderModeEnter,
		WorktreeContext: WorktreeContext{
			Branch:        OptionalWorktreeBranch("feature/first"),
			WorktreePath:  "/tmp/worktree-first",
			WorkspaceRoot: "/tmp/workspace",
			EffectiveCwd:  "/tmp/shared-cwd",
		},
	}
	if err := store.SetWorktreeReminderState(&firstTarget); err != nil {
		t.Fatalf("set first target: %v", err)
	}
	first := CloneWorktreeReminderState(store.Meta().WorktreeReminder)
	if first == nil || first.ContextID == nil || first.ContextID.Version() != 4 {
		t.Fatalf("first worktree context id = %v, want UUID v4", first)
	}

	if err := store.SetWorktreeReminderState(&firstTarget); err != nil {
		t.Fatalf("reapply first target: %v", err)
	}
	reapplied := store.Meta().WorktreeReminder
	if reapplied == nil || !WorktreeReminderStateEqual(*first, *reapplied) {
		t.Fatalf("reapplied target = %+v, want stable identity %+v", reapplied, first)
	}

	changedTarget := firstTarget
	changedTarget.Branch = OptionalWorktreeBranch("feature/second")
	if err := store.SetWorktreeReminderState(&changedTarget); err != nil {
		t.Fatalf("set changed target: %v", err)
	}
	changed := store.Meta().WorktreeReminder
	if changed == nil || changed.ContextID == nil || *changed.ContextID == *first.ContextID {
		t.Fatalf("changed target context id = %v, want new identity after target change", changed)
	}
	if changed.EffectiveCwd != first.EffectiveCwd {
		t.Fatalf("changed target effective cwd = %q, want same cwd %q", changed.EffectiveCwd, first.EffectiveCwd)
	}
}

func TestSetWorktreeReminderStateRejectsEmptyPresentBranch(t *testing.T) {
	store := newSessionTestStore(t)
	emptyBranch := " "
	err := store.SetWorktreeReminderState(&WorktreeReminderState{
		Mode: WorktreeReminderModeEnter,
		WorktreeContext: WorktreeContext{
			Branch:        &emptyBranch,
			WorktreePath:  "/tmp/worktree",
			WorkspaceRoot: "/tmp/workspace",
			EffectiveCwd:  "/tmp/worktree",
		},
	})
	if err == nil {
		t.Fatal("expected empty present worktree branch to be rejected")
	}
}

func TestInitializeChildFromParentCopiesContextWithoutConversationState(t *testing.T) {
	root := t.TempDir()
	parent, err := Create(root, "workspace-parent", "/tmp/work-parent", testSessionCategory, sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	contract := sessionTestLockedContract()
	contract.Model = "locked-parent"
	contract.EnabledTools = []string{"shell", "patch"}
	contract.SystemPrompt = "parent system prompt snapshot"
	contract.ReviewerPrompt = "parent reviewer prompt snapshot"
	markSessionTestLocked(t, parent, contract)
	if err := parent.SetContinuationContext(ContinuationContext{OpenAIBaseURL: "http://parent.local/v1"}); err != nil {
		t.Fatalf("SetContinuationContext parent: %v", err)
	}
	if _, err := parent.SetUsageState(&UsageState{InputTokens: 123}); err != nil {
		t.Fatalf("SetUsageState parent: %v", err)
	}
	if err := parent.SetWorktreeReminderState(&WorktreeReminderState{
		Mode: WorktreeReminderModeEnter,
		WorktreeContext: WorktreeContext{
			Branch:        OptionalWorktreeBranch("feature/child-context"),
			WorktreePath:  "/tmp/work-parent-wt",
			WorkspaceRoot: "/tmp/work-parent",
			EffectiveCwd:  "/tmp/work-parent-wt/pkg",
		},
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState parent: %v", err)
	}
	child, err := NewLazy(root, "workspace-child", "/tmp/work-child", testSessionCategory)
	if err != nil {
		t.Fatalf("new child: %v", err)
	}

	if err := InitializeChildFromParent(child, parent); err != nil {
		t.Fatalf("InitializeChildFromParent: %v", err)
	}
	meta := child.Meta()
	if meta.ParentSessionID == nil || *meta.ParentSessionID != parent.Meta().SessionID {
		t.Fatalf("parent session id = %v, want %q", meta.ParentSessionID, parent.Meta().SessionID)
	}
	if meta.WorkspaceRoot != "/tmp/work-parent" || meta.WorkspaceContainer != "workspace-parent" {
		t.Fatalf("workspace context = root %q container %q, want parent", meta.WorkspaceRoot, meta.WorkspaceContainer)
	}
	if meta.Locked == nil || meta.Locked.Model != "locked-parent" || len(meta.Locked.EnabledTools) != 2 {
		t.Fatalf("locked contract = %+v, want parent lock", meta.Locked)
	}
	if meta.Locked.SystemPrompt != "parent system prompt snapshot" || !meta.Locked.HasSystemPrompt {
		t.Fatalf("locked system prompt = %+v, want parent prompt snapshot", meta.Locked)
	}
	if meta.Locked.ReviewerPrompt != "parent reviewer prompt snapshot" || !meta.Locked.HasReviewerPrompt {
		t.Fatalf("locked reviewer prompt = %+v, want parent reviewer prompt snapshot", meta.Locked)
	}
	if meta.Locked.ToolPreambles == nil || !*meta.Locked.ToolPreambles {
		t.Fatalf("locked tool preambles = %+v, want copied true", meta.Locked.ToolPreambles)
	}
	if meta.Locked.ToolPreambles == parent.Meta().Locked.ToolPreambles {
		t.Fatal("expected locked tool preambles pointer to be deep-copied")
	}
	if meta.Continuation == nil || meta.Continuation.OpenAIBaseURL != "http://parent.local/v1" {
		t.Fatalf("continuation = %+v, want parent continuation", meta.Continuation)
	}
	if meta.UsageState != nil {
		t.Fatalf("usage state = %+v, want nil for fresh child", meta.UsageState)
	}
	if meta.FirstPromptPreview != "" || meta.ModelRequestCount != 0 {
		t.Fatalf("conversation state leaked into child: %+v", meta)
	}
	if meta.WorktreeReminder == nil {
		t.Fatal("expected worktree reminder")
	}
	if meta.WorktreeReminder.Branch == nil || *meta.WorktreeReminder.Branch != "feature/child-context" {
		t.Fatalf("worktree reminder = %+v, want parent branch", meta.WorktreeReminder)
	}
}

func TestSetContinuationContextStaysLazyUntilFirstWrite(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if err := store.SetContinuationContext(ContinuationContext{OpenAIBaseURL: "http://example.local/v1"}); err != nil {
		t.Fatalf("set continuation context: %v", err)
	}
	if store.Meta().Continuation == nil || store.Meta().Continuation.OpenAIBaseURL != "http://example.local/v1" {
		t.Fatalf("expected in-memory continuation context, got %+v", store.Meta().Continuation)
	}
	if _, err := os.Stat(store.Dir()); !os.IsNotExist(err) {
		t.Fatalf("expected lazy session to remain unpersisted, stat err=%v", err)
	}
	appendSessionTestEvent(t, store, "step1", "message", map[string]any{"a": 1})
	opened := mustOpenSessionTestStore(t, store)
	if opened.Meta().Continuation == nil || opened.Meta().Continuation.OpenAIBaseURL != "http://example.local/v1" {
		t.Fatalf("expected persisted continuation context, got %+v", opened.Meta().Continuation)
	}
}

func TestPendingModelRecoveryPersistsMetadataAndEvents(t *testing.T) {
	store := newSessionTestStore(t)
	recovery := PendingModelRecovery{
		RecoveryID:             "recovery-1",
		StepID:                 "step-1",
		Reason:                 "interrupted_or_crashed_step",
		OutstandingToolCallIDs: []string{"call-1"},
	}
	if err := store.SetPendingModelRecovery(recovery); err != nil {
		t.Fatalf("SetPendingModelRecovery: %v", err)
	}
	if got := store.Meta().PendingModelRecovery; got == nil || got.RecoveryID != recovery.RecoveryID || got.StepID != recovery.StepID {
		t.Fatalf("pending model recovery metadata = %+v", got)
	}
	if err := store.ClearPendingModelRecovery(); err != nil {
		t.Fatalf("ClearPendingModelRecovery: %v", err)
	}
	if got := store.Meta().PendingModelRecovery; got != nil {
		t.Fatalf("pending model recovery after clear = %+v, want nil", got)
	}
	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("collect events: %v", err)
	}
	if len(events) != 2 || events[0].Kind != eventModelRecoveryPending || events[1].Kind != eventModelRecoveryConsumed {
		t.Fatalf("recovery event sequence = %+v", events)
	}
	if events[0].StepID != recovery.StepID || events[1].StepID != recovery.StepID {
		t.Fatalf("recovery events should carry step ID, got %+v", events)
	}
}

func TestHeadlessActiveFromReplayEvents(t *testing.T) {
	msg := func(messageType string) ReplayEvent {
		return ReplayEvent{Kind: "message", Payload: []byte(`{"role":"developer","message_type":"` + messageType + `","content":"x"}`)}
	}
	cases := []struct {
		name   string
		events []ReplayEvent
		want   bool
	}{
		{"empty", nil, false},
		{"enter", []ReplayEvent{msg("headless_mode")}, true},
		{"enter then exit", []ReplayEvent{msg("headless_mode"), msg("headless_mode_exit")}, false},
		{"exit then enter", []ReplayEvent{msg("headless_mode_exit"), msg("headless_mode")}, true},
		{"non-developer ignored", []ReplayEvent{{Kind: "message", Payload: []byte(`{"role":"user","message_type":"headless_mode","content":"x"}`)}}, false},
	}
	for _, tc := range cases {
		derived := replayDerivedState{}
		for _, evt := range tc.events {
			derived.apply(evt)
		}
		if derived.headlessActive != tc.want {
			t.Fatalf("%s: derived.headlessActive = %v, want %v", tc.name, derived.headlessActive, tc.want)
		}
	}
}
