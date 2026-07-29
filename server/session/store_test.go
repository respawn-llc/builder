package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/filemode"
	"core/shared/runtimeids"
	"core/shared/sessioncontract"
)

func appendSessionTestRecord(
	t *testing.T,
	store *Store,
	stepID string,
	payload EventRecordPayload,
) EventRecord {
	t.Helper()
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist store before materializing event log: %v", err)
	}
	log := mustMaterializeSessionTestEventLog(t, store)
	record, _, err := log.AppendRecord(&stepID, payload)
	if err != nil {
		t.Fatalf("append typed event record: %v", err)
	}
	return record
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

	appendSessionTestRecord(t, store, "step1", sessionTestMessage(MessageRoleUser, "first write"))
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

func TestNewLazyMaterializedEventLogReturnsEmpty(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist store before materializing event log: %v", err)
	}
	log := mustMaterializeSessionTestEventLog(t, store)
	events, err := log.ReadRecentRecords(1)
	if err != nil {
		t.Fatalf("read event records: %v", err)
	}
	if len(events.Records) != 0 {
		t.Fatalf("event record count = %d, want 0", len(events.Records))
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

func TestAppendTypedRecordMonotonicSequence(t *testing.T) {
	store := newSessionTestStore(t)

	e1 := appendSessionTestRecord(t, store, "step1", sessionTestMessage(MessageRoleUser, "one"))
	e2 := appendSessionTestRecord(t, store, "step1", sessionTestMessage(MessageRoleAssistant, "two"))

	if e1.Seq() != 1 || e2.Seq() != 2 {
		t.Fatalf("unexpected sequence values: %d, %d", e1.Seq(), e2.Seq())
	}

	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Seq() != 1 || events[1].Seq() != 2 {
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
		Kind: "active_submit",
		Text: "submitted before forced exit",
	}}); err != nil {
		t.Fatalf("set input draft recovery: %v", err)
	}
	recovered := mustOpenSessionTestStore(t, store)
	if recovered.Meta().InputDraft != "visible" || len(recovered.Meta().InputDraftRecoveryBuffers) != 1 {
		t.Fatalf("reopened draft metadata = %+v", recovered.Meta())
	}
	wantBuffer := InputDraftRecoveryBuffer{Kind: "active_submit", Text: "submitted before forced exit"}
	if recovered.Meta().InputDraftRecoveryBuffers[0] != wantBuffer {
		t.Fatalf("recovery buffer = %+v, want %+v", recovered.Meta().InputDraftRecoveryBuffers[0], wantBuffer)
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
	if err := store.MarkModelDispatchLocked(LockedContract{Model: "gpt-5", SystemPrompt: "prompt A", HasSystemPrompt: true}); err != nil {
		t.Fatalf("initial lock: %v", err)
	}
	before := store.Meta().Locked
	observer.err = os.ErrPermission
	result, err := store.MarkLockedPromptFacingSnapshotsStale()
	if err == nil || !result.Committed {
		t.Fatalf("observer result=%+v err=%v, want committed failure", result, err)
	}
	if after := store.Meta().Locked; before == nil || after == nil || after.SystemPrompt != "" || after.HasSystemPrompt {
		t.Fatalf("lock after committed mutation = %+v, before %+v", after, before)
	}
}

func TestReadRecordsHandlesLargeMessageContent(t *testing.T) {
	store := newSessionTestStore(t)

	const payloadSize = 128 * 1024
	large := strings.Repeat("x", payloadSize)
	appendSessionTestRecord(t, store, "step1", sessionTestMessage(MessageRoleAssistant, large))

	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}

	message, ok := mustEventRecordPayload(events[0]).(MessageRecord)
	if !ok {
		t.Fatalf("event payload = %T, want MessageRecord", mustEventRecordPayload(events[0]))
	}
	if message.Content == nil {
		t.Fatal("message content is absent")
	}
	if got := len(*message.Content); got != payloadSize {
		t.Fatalf("message content size = %d, want %d", got, payloadSize)
	}
}

func TestAppendTypedRecordPersistsFirstPromptPreview(t *testing.T) {
	store := newSessionTestStore(t)
	compactionSummary := MessageTypeCompactionSummary
	summary := "summary"
	appendSessionTestRecord(t, store, "s1", sessionTestMessage(MessageRoleAssistant, "hello"))
	appendSessionTestRecord(t, store, "s1", MessageRecord{
		Role:        MessageRoleDeveloper,
		MessageType: &compactionSummary,
		Content:     &summary,
	})
	if got := store.Meta().FirstPromptPreview; got != "" {
		t.Fatalf("non-user messages set preview %q", got)
	}
	appendSessionTestRecord(
		t,
		store,
		"s2",
		sessionTestMessage(MessageRoleUser, "\n  Investigate config load failures\nsecond line"),
	)
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
	meta := storeTestMeta(store)
	if meta.Name != "Workflow Session" || meta.FirstPromptPreview != "Rendered workflow prompt" {
		t.Fatalf("metadata = name %q preview %q, want trimmed name and normalized preview", meta.Name, meta.FirstPromptPreview)
	}
	if !observer.called || observer.snapshot.Meta.Name != "Workflow Session" || observer.snapshot.Meta.FirstPromptPreview != "Rendered workflow prompt" {
		t.Fatalf("observer snapshot = %+v, called %v", observer.snapshot.Meta, observer.called)
	}

	appendSessionTestRecord(t, store, "s1", sessionTestMessage(MessageRoleUser, "event prompt"))
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

func TestMetaSnapshotDeepCopiesSessionProvenance(t *testing.T) {
	root := t.TempDir()
	parent := newSessionTestStoreAt(t, root)
	store := newSessionTestLazyStoreAt(t, root)
	if err := InitializeCreationContext(store, parent, SessionCreationSourceParentAgent, ChildContextOptions{}); err != nil {
		t.Fatalf("InitializeCreationContext: %v", err)
	}

	snapshot := store.Meta()
	if snapshot.ParentAgentSessionID == nil {
		t.Fatal("snapshot parent-agent session id is nil")
	}
	original := *snapshot.ParentAgentSessionID
	mutated, err := runtimeids.ParseSessionID("mutated-snapshot")
	if err != nil {
		t.Fatalf("ParseSessionID mutated snapshot: %v", err)
	}
	*snapshot.ParentAgentSessionID = mutated

	if got := store.Meta().ParentAgentSessionID; got == nil || *got != original {
		t.Fatalf("store parent-agent session id = %v, want %q after snapshot mutation", got, original.String())
	}
}

func TestConversationFreshnessAdvancesOnlyForVisibleUserMessages(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	if got := mustConversationFreshness(log); got != ConversationFreshnessFresh {
		t.Fatalf("freshness = %v, want fresh", got)
	}
	appendSessionTestRecord(t, store, "s1", sessionTestMessage(MessageRoleAssistant, "hello"))
	if got := mustConversationFreshness(log); got != ConversationFreshnessFresh {
		t.Fatalf("freshness after assistant = %v, want fresh", got)
	}
	compactionSummary := MessageTypeCompactionSummary
	summary := "summary"
	appendSessionTestRecord(t, store, "s2", MessageRecord{
		Role:        MessageRoleDeveloper,
		MessageType: &compactionSummary,
		Content:     &summary,
	})
	if got := mustConversationFreshness(log); got != ConversationFreshnessFresh {
		t.Fatalf("freshness after compaction summary = %v, want fresh", got)
	}
	appendSessionTestRecord(t, store, "s3", sessionTestMessage(MessageRoleUser, "Investigate config load failures"))
	if got := mustConversationFreshness(log); got != ConversationFreshnessEstablished {
		t.Fatalf("freshness after visible user message = %v, want established", got)
	}
	opened := mustOpenSessionTestStore(t, store)
	openedLog := mustMaterializeSessionTestEventLog(t, opened)
	if got := mustConversationFreshness(openedLog); got != ConversationFreshnessEstablished {
		t.Fatalf("reopened freshness = %v, want established", got)
	}
}

func TestMaterializeEventLogBackfillsConversationFreshnessFromTail(t *testing.T) {
	store := newSessionTestStore(t)
	appendSessionTestRecord(t, store, "s1", sessionTestMessage(MessageRoleUser, "established session"))

	meta := storeTestMeta(store)
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
	log := mustMaterializeSessionTestEventLog(t, opened)
	if got := mustConversationFreshness(log); got != ConversationFreshnessEstablished {
		t.Fatalf("backfilled freshness = %v, want established", got)
	}
	if mustConversationFreshness(log) != ConversationFreshnessEstablished {
		t.Fatalf("expected backfill to persist conversation_established flag")
	}
}

func TestMaterializeEventLogRecoversLastSequenceFromTailWhenMetaStale(t *testing.T) {
	store := newSessionTestStore(t)
	for i := 0; i < 3; i++ {
		appendSessionTestRecord(t, store, "s1", sessionTestMessage(MessageRoleAssistant, "reply"))
	}
	trueLastSeq := mustMaterializedRevision(mustMaterializeSessionTestEventLog(t, store))

	meta := storeTestMeta(store)
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
	if got := storeTestMeta(opened).LastSequence; got != 0 {
		t.Fatalf("metadata-only last sequence = %d, want stale authoritative value 0", got)
	}
	mustMaterializeSessionTestEventLog(t, opened)
	if got := mustMaterializedRevision(mustMaterializeSessionTestEventLog(t, opened)); got != trueLastSeq {
		t.Fatalf("recovered last sequence = %d, want %d", got, trueLastSeq)
	}
}

func TestAppendTypedBatchPersistsFirstPromptPreview(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	stepID := "s1"
	if _, receipt, err := log.AppendRecordsAtomic(&stepID, []EventRecordPayload{
		sessionTestMessage(MessageRoleAssistant, "hello"),
		sessionTestMessage(MessageRoleUser, "Atomic preview source\nmore"),
	}); err != nil {
		t.Fatalf("append typed batch: %v", err)
	} else if !receipt.Committed {
		t.Fatal("append typed batch returned an uncommitted receipt")
	}
	if got := store.Meta().FirstPromptPreview; got != "Atomic preview source" {
		t.Fatalf("preview = %q, want %q", got, "Atomic preview source")
	}
}

func TestAppendTypedBatchReportsUncommittedEventLogFailure(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	filemode.MustBlockEventLogAppends(t, store.eventsFP)

	stepID := "s1"
	events, receipt, err := log.AppendRecordsAtomic(&stepID, []EventRecordPayload{
		sessionTestMessage(MessageRoleUser, "must not commit"),
	})
	if err == nil {
		t.Fatal("append typed batch did not surface the event-log failure")
	}
	if receipt.Committed {
		t.Fatalf("append typed batch receipt = %+v, want uncommitted", receipt)
	}
	if len(events) != 1 {
		t.Fatalf("built events = %+v, want the attempted event", events)
	}
	if meta := storeTestMeta(store); meta.LastSequence != 0 || meta.FirstPromptPreview != "" {
		t.Fatalf("metadata mutated after uncommitted append: %+v", meta)
	}
}

func TestAppendTypedBatchReportsCommittedObserverFailure(t *testing.T) {
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

	log := mustMaterializeSessionTestEventLog(t, store)
	observer.err = os.ErrPermission
	stepID := "s1"
	events, receipt, err := log.AppendRecordsAtomic(&stepID, []EventRecordPayload{
		sessionTestMessage(MessageRoleUser, "committed despite observer failure"),
	})
	if err == nil {
		t.Fatal("append typed batch did not surface the observer failure")
	}
	if !receipt.Committed {
		t.Fatalf("append typed batch receipt = %+v, want committed", receipt)
	}
	if len(events) != 1 || events[0].Seq() != 1 {
		t.Fatalf("committed events = %+v, want one sequence-1 event", events)
	}
	if meta := storeTestMeta(store); meta.LastSequence != 1 || meta.FirstPromptPreview != "committed despite observer failure" {
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
		isVisible, err := hasVisibleUserMessageRecord(evt)
		if err != nil {
			t.Fatalf("inspect visible user message: %v", err)
		}
		if isVisible {
			visible++
			if visible == n {
				return evt.Seq()
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
	appendSessionTestRecord(t, parent, "s1", sessionTestMessage(MessageRoleUser, "u1"))
	appendSessionTestRecord(t, parent, "s1", sessionTestMessage(MessageRoleAssistant, "a1"))
	appendSessionTestRecord(t, parent, "s2", sessionTestMessage(MessageRoleUser, "u2"))
	appendSessionTestRecord(t, parent, "s2", sessionTestMessage(MessageRoleAssistant, "a2"))

	parentLog := mustMaterializeSessionTestEventLog(t, parent)
	forked, _, err := ForkAtUserMessage(parentLog, userMessageSeqAt(t, parent, 2), "Parent → edit u2", testSessionCategory)
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
	first, ok := mustEventRecordPayload(forkEvents[0]).(MessageRecord)
	if !ok {
		t.Fatalf("first fork payload = %T, want MessageRecord", mustEventRecordPayload(forkEvents[0]))
	}
	if first.Role != MessageRoleUser || first.Content == nil || *first.Content != "u1" {
		t.Fatalf("unexpected first message in fork: %+v", first)
	}
	meta := forked.Meta()
	parentID, err := runtimeids.ParseSessionID(parent.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID parent: %v", err)
	}
	if meta.PreviousSessionID == nil || *meta.PreviousSessionID != parentID {
		t.Fatalf("expected fork previous session id, got %v", meta.PreviousSessionID)
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
	user := func(content string) EventRecordPayload {
		return sessionTestMessage(MessageRoleUser, content)
	}
	compactionSoonReminder := MessageTypeCompactionSoonReminder
	compactSoon := "compact soon"
	reminder := MessageRecord{
		Role:        MessageRoleDeveloper,
		MessageType: &compactionSoonReminder,
		Content:     &compactSoon,
	}
	replacement := func(engine string, mode CompactionMode) EventRecordPayload {
		return HistoryReplacementRecord{Engine: engine, Mode: mode}
	}
	cases := []struct {
		name              string
		events            []EventRecordPayload
		forkAtUser        int
		persistedReminder bool
		wantReminder      bool
	}{
		{"before reminder", []EventRecordPayload{user("u1"), reminder, user("u2")}, 1, true, false},
		{"after reminder", []EventRecordPayload{user("u1"), reminder, user("u2")}, 2, true, true},
		{"typed history replacement clears reminder", []EventRecordPayload{
			user("u1"),
			reminder,
			replacement("local", CompactionModeAuto),
			user("u2"),
		}, 2, false, false},
		{"typed handoff replacement clears reminder", []EventRecordPayload{
			user("u1"), reminder, replacement("remote", CompactionModeHandoff), user("u2"),
		}, 2, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parent := newSessionTestStore(t)
			parentLog := mustMaterializeSessionTestEventLog(t, parent)
			stepID := "replay"
			if _, receipt, err := parentLog.AppendRecordsAtomic(&stepID, tc.events); err != nil {
				t.Fatalf("append typed replay fixture: %v", err)
			} else if !receipt.Committed {
				t.Fatal("typed replay fixture was not committed")
			}
			if tc.persistedReminder {
				if err := parent.SetCompactionSoonReminderIssued(true); err != nil {
					t.Fatalf("persist reminder state: %v", err)
				}
			}
			forked, _, err := ForkAtUserMessage(parentLog, userMessageSeqAt(t, parent, tc.forkAtUser), tc.name, testSessionCategory)
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
	appendSessionTestRecord(t, parent, "s1", sessionTestMessage(MessageRoleUser, "u1"))
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
	appendSessionTestRecord(t, parent, "s2", sessionTestMessage(MessageRoleUser, "u2"))

	parentLog := mustMaterializeSessionTestEventLog(t, parent)
	forked, _, err := ForkAtUserMessage(parentLog, userMessageSeqAt(t, parent, 2), "forked", testSessionCategory)
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

	if err := InitializeCreationContext(child, parent, SessionCreationSourcePreviousSession, ChildContextOptions{
		InheritLockedContract: true,
		InheritContinuation:   true,
	}); err != nil {
		t.Fatalf("InitializeCreationContext: %v", err)
	}
	meta := child.Meta()
	parentID, err := runtimeids.ParseSessionID(parent.Meta().SessionID)
	if err != nil {
		t.Fatalf("ParseSessionID parent: %v", err)
	}
	if meta.PreviousSessionID == nil || *meta.PreviousSessionID != parentID {
		t.Fatalf("previous session id = %v, want %q", meta.PreviousSessionID, parent.Meta().SessionID)
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
	appendSessionTestRecord(t, store, "step1", sessionTestMessage(MessageRoleUser, "persist continuation"))
	opened := mustOpenSessionTestStore(t, store)
	if opened.Meta().Continuation == nil || opened.Meta().Continuation.OpenAIBaseURL != "http://example.local/v1" {
		t.Fatalf("expected persisted continuation context, got %+v", opened.Meta().Continuation)
	}
}

func TestPendingModelRecoveryPersistsOnlyMetadata(t *testing.T) {
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
	if len(events) != 0 {
		t.Fatalf("recovery metadata mutation emitted events: %+v", events)
	}
}

func TestHeadlessActiveFromTypedReplayRecords(t *testing.T) {
	message := func(role MessageRole, messageType MessageType) EventRecordPayload {
		content := "x"
		return MessageRecord{
			Role:        role,
			MessageType: &messageType,
			Content:     &content,
		}
	}
	cases := []struct {
		name   string
		events []EventRecordPayload
		want   bool
	}{
		{"empty", nil, false},
		{"enter", []EventRecordPayload{message(MessageRoleDeveloper, MessageTypeHeadlessMode)}, true},
		{"enter then exit", []EventRecordPayload{
			message(MessageRoleDeveloper, MessageTypeHeadlessMode),
			message(MessageRoleDeveloper, MessageTypeHeadlessModeExit),
		}, false},
		{"exit then enter", []EventRecordPayload{
			message(MessageRoleDeveloper, MessageTypeHeadlessModeExit),
			message(MessageRoleDeveloper, MessageTypeHeadlessMode),
		}, true},
		{"non-developer ignored", []EventRecordPayload{
			message(MessageRoleUser, MessageTypeHeadlessMode),
		}, false},
	}
	for _, tc := range cases {
		derived := replayDerivedState{}
		for index, payload := range tc.events {
			record, err := NewEventRecord(int64(index+1), nil, payload)
			if err != nil {
				t.Fatalf("%s: build typed replay record: %v", tc.name, err)
			}
			derived.apply(record)
		}
		if derived.headlessActive != tc.want {
			t.Fatalf("%s: derived.headlessActive = %v, want %v", tc.name, derived.headlessActive, tc.want)
		}
	}
}
