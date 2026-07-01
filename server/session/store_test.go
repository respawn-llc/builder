package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewLazyDoesNotPersistUntilFirstWrite(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if _, err := os.Stat(store.Dir()); !os.IsNotExist(err) {
		t.Fatalf("expected no session dir before first write, stat err=%v", err)
	}

	if _, _, err := store.AppendEvent("step1", "message", map[string]any{"a": 1}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), sessionFile)); err != nil {
		t.Fatalf("expected session metadata after first write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir(), eventsFile)); err != nil {
		t.Fatalf("expected events file after first write: %v", err)
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

	e1, _, err := store.AppendEvent("step1", "message", map[string]any{"a": 1})
	if err != nil {
		t.Fatalf("append event1: %v", err)
	}
	e2, _, err := store.AppendEvent("step1", "message", map[string]any{"b": 2})
	if err != nil {
		t.Fatalf("append event2: %v", err)
	}

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

func TestSetInputDraftPersistsAcrossReopen(t *testing.T) {
	store := newSessionTestLazyStore(t)
	want := "draft line one\nline two"
	if err := store.SetInputDraft(want); err != nil {
		t.Fatalf("set input draft: %v", err)
	}
	reopened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if reopened.Meta().InputDraft != want {
		t.Fatalf("expected persisted draft %q, got %q", want, reopened.Meta().InputDraft)
	}
}

func TestSetInputDraftClearsPersistedValue(t *testing.T) {
	store := newSessionTestStore(t)
	if err := store.SetInputDraft("draft"); err != nil {
		t.Fatalf("set draft: %v", err)
	}
	if err := store.SetInputDraft(""); err != nil {
		t.Fatalf("clear draft: %v", err)
	}
	reopened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if reopened.Meta().InputDraft != "" {
		t.Fatalf("expected cleared draft, got %q", reopened.Meta().InputDraft)
	}
}

func TestSetUsageStatePersistsAcrossReopen(t *testing.T) {
	store := newSessionTestLazyStore(t)
	if err := store.SetUsageState(&UsageState{
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
	reopened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if reopened.Meta().UsageState == nil {
		t.Fatal("expected persisted usage state")
	}
	if got := reopened.Meta().UsageState; got.InputTokens != 900 || got.EstimatedProviderTokens != 180 || got.TotalInputTokens != 1_200 {
		t.Fatalf("unexpected usage state after reopen: %+v", got)
	}
}

func TestListSessionsSortedByUpdatedAt(t *testing.T) {
	root := t.TempDir()
	s1 := newSessionTestStoreAt(t, root)
	if _, _, err := s1.AppendEvent("step1", "message", map[string]any{"a": 1}); err != nil {
		t.Fatalf("append event1: %v", err)
	}

	s2 := newSessionTestStoreAt(t, root)
	if _, _, err := s2.AppendEvent("step1", "message", map[string]any{"b": 2}); err != nil {
		t.Fatalf("append event2: %v", err)
	}

	items, err := ListSessions(root)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(items))
	}
	if filepath.Base(items[0].Path) != s2.Meta().SessionID {
		t.Fatalf("latest session expected first")
	}
}

func TestLockedContractPersistenceIncludesSystemPromptButNotToolSchema(t *testing.T) {
	store := newSessionTestStore(t)
	if err := store.MarkModelDispatchLocked(LockedContract{
		Model:             "gpt-5",
		Temperature:       1,
		MaxOutputToken:    0,
		SystemPrompt:      "locked system prompt",
		HasSystemPrompt:   true,
		ReviewerPrompt:    "locked reviewer prompt",
		HasReviewerPrompt: true,
	}); err != nil {
		t.Fatalf("mark model dispatch locked: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(store.Dir(), sessionFile))
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "tools_json") {
		t.Fatalf("session metadata must not persist tools_json: %s", text)
	}
	opened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	locked := opened.Meta().Locked
	if locked == nil || locked.SystemPrompt != "locked system prompt" || !locked.HasSystemPrompt {
		t.Fatalf("locked system prompt = %+v, want persisted snapshot marker", locked)
	}
	if locked.ReviewerPrompt != "locked reviewer prompt" || !locked.HasReviewerPrompt {
		t.Fatalf("locked reviewer prompt = %+v, want persisted snapshot marker", locked)
	}
}

func TestLockedContractPersistenceIncludesExplicitZeroToolsAndWebSearchMode(t *testing.T) {
	store := newSessionTestStore(t)
	if err := store.MarkModelDispatchLocked(LockedContract{
		Model:             "gpt-5",
		SystemPrompt:      "prompt",
		HasSystemPrompt:   true,
		EnabledTools:      nil,
		HasEnabledTools:   true,
		WebSearchMode:     "native",
		ReviewerPrompt:    "reviewer",
		HasReviewerPrompt: true,
	}); err != nil {
		t.Fatalf("mark model dispatch locked: %v", err)
	}
	opened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	locked := opened.Meta().Locked
	if locked == nil || !locked.HasEnabledTools || len(locked.EnabledTools) != 0 || locked.WebSearchMode != "native" {
		t.Fatalf("locked request shape = %+v, want explicit zero tools and native web search", locked)
	}
}

func TestLockedPromptFacingMutationsPreserveLifetimeFields(t *testing.T) {
	store := newSessionTestStore(t)
	toolPreambles := true
	lockedAt := store.Meta().CreatedAt
	if err := store.MarkModelDispatchLocked(LockedContract{
		Model:             "gpt-5",
		Temperature:       0.7,
		MaxOutputToken:    1000,
		SystemPrompt:      "prompt A",
		HasSystemPrompt:   true,
		ReviewerPrompt:    "reviewer A",
		HasReviewerPrompt: true,
		ContextWindow:     100,
		ContextPercent:    50,
		EnabledTools:      []string{"shell"},
		HasEnabledTools:   true,
		WebSearchMode:     "native",
		ToolPreambles:     &toolPreambles,
		LockedAt:          lockedAt,
	}); err != nil {
		t.Fatalf("mark model dispatch locked: %v", err)
	}
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
	opened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if locked := opened.Meta().Locked; locked == nil || !locked.HasEnabledTools || locked.WebSearchMode != "native" || len(locked.EnabledTools) != 2 {
		t.Fatalf("persisted request shape = %+v", locked)
	}
}

func TestLockedContractMutationObserverCommitSemantics(t *testing.T) {
	fileObserver := &recordingPersistenceObserver{err: os.ErrPermission}
	fileStore, err := Create(t.TempDir(), "ws", t.TempDir(), WithPersistenceObserver(fileObserver))
	if err != nil {
		t.Fatalf("create file store: %v", err)
	}
	if err := fileStore.MarkModelDispatchLocked(LockedContract{Model: "gpt-5", SystemPrompt: "prompt A", HasSystemPrompt: true}); err == nil {
		t.Fatal("expected observer error on initial lock")
	}
	result, err := fileStore.MarkLockedPromptFacingSnapshotsStale()
	if err == nil || !result.Committed || result.Locked == nil || result.Locked.HasSystemPrompt {
		t.Fatalf("file-backed observer result=%+v err=%v, want committed observer warning", result, err)
	}

	filelessObserver := &recordingPersistenceObserver{err: os.ErrPermission}
	filelessStore, err := Create(t.TempDir(), "ws", t.TempDir(), WithFilelessMetadataPersistence(), WithPersistenceObserver(filelessObserver))
	if err != nil {
		t.Fatalf("create fileless store: %v", err)
	}
	if err := filelessStore.MarkModelDispatchLocked(LockedContract{Model: "gpt-5", SystemPrompt: "prompt A", HasSystemPrompt: true}); err == nil {
		t.Fatal("expected observer error on initial fileless lock")
	}
	before := filelessStore.Meta().Locked
	result, err = filelessStore.MarkLockedPromptFacingSnapshotsStale()
	if err == nil || result.Committed {
		t.Fatalf("fileless observer result=%+v err=%v, want uncommitted failure", result, err)
	}
	if after := filelessStore.Meta().Locked; before == nil || after == nil || after.SystemPrompt != before.SystemPrompt || !after.HasSystemPrompt {
		t.Fatalf("fileless lock after failed mutation = %+v, before %+v", after, before)
	}
}

func TestReadEventsHandlesLargeJSONLines(t *testing.T) {
	store := newSessionTestStore(t)

	const payloadSize = 128 * 1024
	large := strings.Repeat("x", payloadSize)
	if _, _, err := store.AppendEvent("step1", "message", map[string]any{"blob": large}); err != nil {
		t.Fatalf("append large event: %v", err)
	}

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
	root := t.TempDir()
	store := newSessionTestStoreAt(t, root)
	if _, _, err := store.AppendEvent("s1", "message", map[string]any{"role": "assistant", "content": "hello"}); err != nil {
		t.Fatalf("append assistant event: %v", err)
	}
	if got := store.Meta().FirstPromptPreview; got != "" {
		t.Fatalf("expected assistant event to leave preview empty, got %q", got)
	}
	if _, _, err := store.AppendEvent("s2", "message", map[string]any{"role": "user", "content": "Investigate config load failures\nsecond line"}); err != nil {
		t.Fatalf("append user event: %v", err)
	}
	if got := store.Meta().FirstPromptPreview; got != "Investigate config load failures" {
		t.Fatalf("preview = %q, want %q", got, "Investigate config load failures")
	}

	opened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if got := opened.Meta().FirstPromptPreview; got != "Investigate config load failures" {
		t.Fatalf("reopened preview = %q, want %q", got, "Investigate config load failures")
	}

	items, err := ListSessions(root)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one session, got %d", len(items))
	}
	if items[0].FirstPromptPreview != "Investigate config load failures" {
		t.Fatalf("list preview = %q, want %q", items[0].FirstPromptPreview, "Investigate config load failures")
	}
}

func TestSetListingMetadataPersistsNameAndFirstPromptPreview(t *testing.T) {
	root := t.TempDir()
	observer := &recordingPersistenceObserver{}
	store, err := Create(root, "workspace-x", "/tmp/work", WithPersistenceObserver(observer))
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

	if _, _, err := store.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "event prompt"}); err != nil {
		t.Fatalf("append user event: %v", err)
	}
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

	reopened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if reopened.Meta().Name != "" || reopened.Meta().FirstPromptPreview != "" {
		t.Fatalf("reopened metadata = %+v, want empty name and preview", reopened.Meta())
	}
	items, err := ListSessions(root)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != 1 || items[0].Name != "" || items[0].FirstPromptPreview != "" {
		t.Fatalf("listed sessions = %+v, want cleared metadata persisted", items)
	}
}

func TestConversationFreshnessAdvancesOnlyForVisibleUserMessages(t *testing.T) {
	store := newSessionTestStore(t)
	if got := store.ConversationFreshness(); got != ConversationFreshnessFresh {
		t.Fatalf("freshness = %v, want fresh", got)
	}
	if _, _, err := store.AppendEvent("s1", "message", map[string]any{"role": "assistant", "content": "hello"}); err != nil {
		t.Fatalf("append assistant event: %v", err)
	}
	if got := store.ConversationFreshness(); got != ConversationFreshnessFresh {
		t.Fatalf("freshness after assistant = %v, want fresh", got)
	}
	if _, _, err := store.AppendEvent("s2", "message", map[string]any{"role": "developer", "message_type": "compaction_summary", "content": "summary"}); err != nil {
		t.Fatalf("append compaction summary event: %v", err)
	}
	if got := store.ConversationFreshness(); got != ConversationFreshnessFresh {
		t.Fatalf("freshness after compaction summary = %v, want fresh", got)
	}
	if _, _, err := store.AppendEvent("s3", "message", map[string]any{"role": "user", "content": "Investigate config load failures"}); err != nil {
		t.Fatalf("append user event: %v", err)
	}
	if got := store.ConversationFreshness(); got != ConversationFreshnessEstablished {
		t.Fatalf("freshness after visible user message = %v, want established", got)
	}
}

func TestOpenRehydratesConversationFreshnessFromEvents(t *testing.T) {
	store := newSessionTestStore(t)
	if _, _, err := store.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "Investigate config load failures"}); err != nil {
		t.Fatalf("append user event: %v", err)
	}

	opened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if got := opened.ConversationFreshness(); got != ConversationFreshnessEstablished {
		t.Fatalf("reopened freshness = %v, want established", got)
	}
}

func TestOpenBackfillsConversationFreshnessForLegacyMetaFromTail(t *testing.T) {
	store := newSessionTestStore(t)
	if _, _, err := store.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "legacy established session"}); err != nil {
		t.Fatalf("append user event: %v", err)
	}

	metaPath := filepath.Join(store.Dir(), sessionFile)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	meta.ConversationEstablished = false
	rewritten, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(metaPath, rewritten, 0o644); err != nil {
		t.Fatalf("write legacy meta: %v", err)
	}

	opened, err := Open(store.Dir())
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
		if _, _, err := store.AppendEvent("s1", "message", map[string]any{"role": "assistant", "content": "reply"}); err != nil {
			t.Fatalf("append event %d: %v", i, err)
		}
	}
	trueLastSeq := store.Meta().LastSequence

	metaPath := filepath.Join(store.Dir(), sessionFile)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("unmarshal meta: %v", err)
	}
	meta.LastSequence = 0
	rewritten, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(metaPath, rewritten, 0o644); err != nil {
		t.Fatalf("write stale meta: %v", err)
	}

	opened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if got := opened.Meta().LastSequence; got != trueLastSeq {
		t.Fatalf("recovered last sequence = %d, want %d", got, trueLastSeq)
	}
}

func TestFirstPromptPreviewSkipsCompactionSummaryMessages(t *testing.T) {
	store := newSessionTestStore(t)
	if _, _, err := store.AppendEvent("s1", "message", map[string]any{"role": "developer", "message_type": "compaction_summary", "content": "summary"}); err != nil {
		t.Fatalf("append compaction summary event: %v", err)
	}
	if got := store.Meta().FirstPromptPreview; got != "" {
		t.Fatalf("expected compaction summary to be ignored, got %q", got)
	}
	if _, _, err := store.AppendEvent("s2", "message", map[string]any{"role": "user", "content": "\n  Fix config registry boot path\nmore details"}); err != nil {
		t.Fatalf("append visible user event: %v", err)
	}
	if got := store.Meta().FirstPromptPreview; got != "Fix config registry boot path" {
		t.Fatalf("preview = %q, want %q", got, "Fix config registry boot path")
	}
}

func TestAppendTurnAtomicPersistsFirstPromptPreview(t *testing.T) {
	store := newSessionTestStore(t)
	if _, err := store.AppendTurnAtomic("s1", []EventInput{{Kind: "message", Payload: map[string]any{"role": "assistant", "content": "hello"}}, {Kind: "message", Payload: map[string]any{"role": "user", "content": "Atomic preview source\nmore"}}}); err != nil {
		t.Fatalf("append turn: %v", err)
	}
	if got := store.Meta().FirstPromptPreview; got != "Atomic preview source" {
		t.Fatalf("preview = %q, want %q", got, "Atomic preview source")
	}
}

func TestListSessionsUsesPersistedFirstPromptPreviewOnly(t *testing.T) {
	root := t.TempDir()
	store := newSessionTestStoreAt(t, root)
	if _, _, err := store.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "Preview source\nsecond line"}); err != nil {
		t.Fatalf("append user event: %v", err)
	}

	metaPath := filepath.Join(store.Dir(), sessionFile)
	data, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	var meta Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("decode session meta: %v", err)
	}
	meta.FirstPromptPreview = ""
	rewritten, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		t.Fatalf("encode session meta: %v", err)
	}
	if err := os.WriteFile(metaPath, rewritten, 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	items, err := ListSessions(root)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one session, got %d", len(items))
	}
	if items[0].FirstPromptPreview != "" {
		t.Fatalf("expected listed session preview to remain empty, got %q", items[0].FirstPromptPreview)
	}

	reloaded, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if reloaded.Meta().FirstPromptPreview != "" {
		t.Fatalf("expected persisted metadata preview to remain empty after list, got %q", reloaded.Meta().FirstPromptPreview)
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
	parent, err := Create(root, "workspace-x", "/tmp/work")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := parent.MarkModelDispatchLocked(LockedContract{
		Model:             "locked-parent",
		SystemPrompt:      "parent prompt snapshot",
		HasSystemPrompt:   true,
		ReviewerPrompt:    "parent reviewer prompt snapshot",
		HasReviewerPrompt: true,
	}); err != nil {
		t.Fatalf("MarkModelDispatchLocked parent: %v", err)
	}
	if _, _, err := parent.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "u1"}); err != nil {
		t.Fatalf("append u1: %v", err)
	}
	if _, _, err := parent.AppendEvent("s1", "message", map[string]any{"role": "assistant", "content": "a1"}); err != nil {
		t.Fatalf("append a1: %v", err)
	}
	if _, _, err := parent.AppendEvent("s2", "message", map[string]any{"role": "user", "content": "u2"}); err != nil {
		t.Fatalf("append u2: %v", err)
	}
	if _, _, err := parent.AppendEvent("s2", "message", map[string]any{"role": "assistant", "content": "a2"}); err != nil {
		t.Fatalf("append a2: %v", err)
	}

	forked, _, err := ForkAtUserMessage(parent, userMessageSeqAt(t, parent, 2), "Parent → edit u2")
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
	if meta.ParentSessionID != parent.Meta().SessionID {
		t.Fatalf("expected fork parent session id, got %q", meta.ParentSessionID)
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
	parent, err := Create(t.TempDir(), "ws", t.TempDir())
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, _, err := parent.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "u1"}); err != nil {
		t.Fatalf("append first user: %v", err)
	}
	if _, _, err := parent.AppendEvent("s1", "message", map[string]any{"role": "developer", "message_type": "compaction_soon_reminder", "content": "compact soon"}); err != nil {
		t.Fatalf("append reminder: %v", err)
	}
	if err := parent.SetCompactionSoonReminderIssued(true); err != nil {
		t.Fatalf("persist reminder state: %v", err)
	}
	if _, _, err := parent.AppendEvent("s2", "message", map[string]any{"role": "user", "content": "u2"}); err != nil {
		t.Fatalf("append second user: %v", err)
	}

	beforeReminder, _, err := ForkAtUserMessage(parent, userMessageSeqAt(t, parent, 1), "before reminder")
	if err != nil {
		t.Fatalf("fork before reminder: %v", err)
	}
	if beforeReminder.Meta().CompactionSoonReminderIssued {
		t.Fatal("expected fork before reminder to clear reminder-issued state")
	}

	afterReminder, _, err := ForkAtUserMessage(parent, userMessageSeqAt(t, parent, 2), "after reminder")
	if err != nil {
		t.Fatalf("fork after reminder: %v", err)
	}
	if !afterReminder.Meta().CompactionSoonReminderIssued {
		t.Fatal("expected fork after reminder to preserve reminder-issued state")
	}

	t.Run("legacy reviewer rollback history replacement is ignored", func(t *testing.T) {
		parent, err := Create(t.TempDir(), "ws", t.TempDir())
		if err != nil {
			t.Fatalf("create parent: %v", err)
		}
		if _, _, err := parent.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "u1"}); err != nil {
			t.Fatalf("append first user: %v", err)
		}
		if _, _, err := parent.AppendEvent("s1", "history_replaced", map[string]any{
			"engine": "reviewer_rollback",
			"items": []map[string]any{{
				"type":         "message",
				"role":         "developer",
				"message_type": "compaction_soon_reminder",
				"content":      "compact soon",
			}},
		}); err != nil {
			t.Fatalf("append reviewer rollback history replacement: %v", err)
		}
		if _, _, err := parent.AppendEvent("s2", "message", map[string]any{"role": "user", "content": "u2"}); err != nil {
			t.Fatalf("append second user: %v", err)
		}

		forked, _, err := ForkAtUserMessage(parent, userMessageSeqAt(t, parent, 2), "after legacy reviewer rollback")
		if err != nil {
			t.Fatalf("fork: %v", err)
		}
		if forked.Meta().CompactionSoonReminderIssued {
			t.Fatal("expected legacy reviewer rollback history replacement to be ignored for reminder-issued state")
		}
	})

	t.Run("non-reviewer history replacement clears reminder state", func(t *testing.T) {
		parent, err := Create(t.TempDir(), "ws", t.TempDir())
		if err != nil {
			t.Fatalf("create parent: %v", err)
		}
		if _, _, err := parent.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "u1"}); err != nil {
			t.Fatalf("append first user: %v", err)
		}
		if _, _, err := parent.AppendEvent("s1", "message", map[string]any{"role": "developer", "message_type": "compaction_soon_reminder", "content": "compact soon"}); err != nil {
			t.Fatalf("append reminder: %v", err)
		}
		if _, _, err := parent.AppendEvent("s1", "history_replaced", map[string]any{
			"engine": "compaction",
			"items":  []map[string]any{},
		}); err != nil {
			t.Fatalf("append compaction history replacement: %v", err)
		}
		if _, _, err := parent.AppendEvent("s2", "message", map[string]any{"role": "user", "content": "u2"}); err != nil {
			t.Fatalf("append second user: %v", err)
		}

		forked, _, err := ForkAtUserMessage(parent, userMessageSeqAt(t, parent, 2), "after compaction")
		if err != nil {
			t.Fatalf("fork: %v", err)
		}
		if forked.Meta().CompactionSoonReminderIssued {
			t.Fatal("expected reminder-issued state to clear after non-reviewer history replacement")
		}
	})

	t.Run("legacy reviewer rollback preserves earlier reminder-issued state", func(t *testing.T) {
		parent, err := Create(t.TempDir(), "ws", t.TempDir())
		if err != nil {
			t.Fatalf("create parent: %v", err)
		}
		if _, _, err := parent.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "u1"}); err != nil {
			t.Fatalf("append first user: %v", err)
		}
		if _, _, err := parent.AppendEvent("s1", "message", map[string]any{"role": "developer", "message_type": "compaction_soon_reminder", "content": "compact soon"}); err != nil {
			t.Fatalf("append reminder: %v", err)
		}
		if _, _, err := parent.AppendEvent("s1", "history_replaced", map[string]any{
			"engine": "reviewer_rollback",
			"items":  []map[string]any{},
		}); err != nil {
			t.Fatalf("append reviewer rollback history replacement: %v", err)
		}
		if _, _, err := parent.AppendEvent("s2", "message", map[string]any{"role": "user", "content": "u2"}); err != nil {
			t.Fatalf("append second user: %v", err)
		}

		forked, _, err := ForkAtUserMessage(parent, userMessageSeqAt(t, parent, 2), "after legacy rollback")
		if err != nil {
			t.Fatalf("fork: %v", err)
		}
		if !forked.Meta().CompactionSoonReminderIssued {
			t.Fatal("expected legacy reviewer rollback to preserve earlier reminder-issued state")
		}
	})
}

func TestForkAtUserMessageResetsWorktreeReminderGenerationFlags(t *testing.T) {
	parent, err := Create(t.TempDir(), "ws", t.TempDir())
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if _, _, err := parent.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "u1"}); err != nil {
		t.Fatalf("append first user: %v", err)
	}
	if err := parent.SetWorktreeReminderState(&WorktreeReminderState{
		Mode:                  WorktreeReminderModeEnter,
		Branch:                "feature/fork",
		WorktreePath:          "/tmp/wt-fork",
		WorkspaceRoot:         "/tmp/workspace",
		EffectiveCwd:          "/tmp/wt-fork",
		HasIssuedInGeneration: true,
		IssuedCompactionCount: 7,
	}); err != nil {
		t.Fatalf("persist worktree reminder state: %v", err)
	}
	if _, _, err := parent.AppendEvent("s2", "message", map[string]any{"role": "user", "content": "u2"}); err != nil {
		t.Fatalf("append second user: %v", err)
	}

	forked, _, err := ForkAtUserMessage(parent, userMessageSeqAt(t, parent, 2), "forked")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	state := forked.Meta().WorktreeReminder
	if state == nil {
		t.Fatal("expected forked worktree reminder state")
	}
	if state.Mode != WorktreeReminderModeEnter || state.Branch != "feature/fork" {
		t.Fatalf("unexpected forked reminder payload: %+v", state)
	}
	if state.HasIssuedInGeneration || state.IssuedCompactionCount != 0 {
		t.Fatalf("expected forked reminder generation flags reset, got %+v", state)
	}
	parentState := parent.Meta().WorktreeReminder
	if parentState == nil || !parentState.HasIssuedInGeneration || parentState.IssuedCompactionCount != 7 {
		t.Fatalf("expected parent reminder state unchanged, got %+v", parentState)
	}
}

func TestInitializeChildFromParentCopiesContextWithoutConversationState(t *testing.T) {
	root := t.TempDir()
	parent, err := Create(root, "workspace-parent", "/tmp/work-parent")
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	toolPreambles := true
	if err := parent.MarkModelDispatchLocked(LockedContract{
		Model:             "locked-parent",
		EnabledTools:      []string{"shell", "patch"},
		ToolPreambles:     &toolPreambles,
		SystemPrompt:      "parent system prompt snapshot",
		HasSystemPrompt:   true,
		ReviewerPrompt:    "parent reviewer prompt snapshot",
		HasReviewerPrompt: true,
	}); err != nil {
		t.Fatalf("MarkModelDispatchLocked parent: %v", err)
	}
	if err := parent.SetContinuationContext(ContinuationContext{OpenAIBaseURL: "http://parent.local/v1"}); err != nil {
		t.Fatalf("SetContinuationContext parent: %v", err)
	}
	if err := parent.SetUsageState(&UsageState{InputTokens: 123}); err != nil {
		t.Fatalf("SetUsageState parent: %v", err)
	}
	if err := parent.SetWorktreeReminderState(&WorktreeReminderState{
		Mode:                  WorktreeReminderModeEnter,
		Branch:                "feature/child-context",
		WorktreePath:          "/tmp/work-parent-wt",
		WorkspaceRoot:         "/tmp/work-parent",
		EffectiveCwd:          "/tmp/work-parent-wt/pkg",
		HasIssuedInGeneration: true,
		IssuedCompactionCount: 2,
	}); err != nil {
		t.Fatalf("SetWorktreeReminderState parent: %v", err)
	}
	child, err := NewLazy(root, "workspace-child", "/tmp/work-child")
	if err != nil {
		t.Fatalf("new child: %v", err)
	}

	if err := InitializeChildFromParent(child, parent); err != nil {
		t.Fatalf("InitializeChildFromParent: %v", err)
	}
	meta := child.Meta()
	if meta.ParentSessionID != parent.Meta().SessionID {
		t.Fatalf("parent session id = %q, want %q", meta.ParentSessionID, parent.Meta().SessionID)
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
	if meta.WorktreeReminder.Branch != "feature/child-context" {
		t.Fatalf("worktree reminder = %+v, want parent branch", meta.WorktreeReminder)
	}
	if meta.WorktreeReminder.HasIssuedInGeneration || meta.WorktreeReminder.IssuedCompactionCount != 0 {
		t.Fatalf("worktree reminder generation flags = %+v, want reset", meta.WorktreeReminder)
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
	if _, _, err := store.AppendEvent("step1", "message", map[string]any{"a": 1}); err != nil {
		t.Fatalf("append event: %v", err)
	}
	opened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if opened.Meta().Continuation == nil || opened.Meta().Continuation.OpenAIBaseURL != "http://example.local/v1" {
		t.Fatalf("expected persisted continuation context, got %+v", opened.Meta().Continuation)
	}
}

func TestSessionMetadataDoesNotPersistModelVerbosityState(t *testing.T) {
	store := newSessionTestStore(t)
	if err := store.MarkModelDispatchLocked(LockedContract{
		Model:          "gpt-5",
		Temperature:    1,
		MaxOutputToken: 0,
	}); err != nil {
		t.Fatalf("mark model dispatch locked: %v", err)
	}
	if err := store.SetContinuationContext(ContinuationContext{OpenAIBaseURL: "http://example.local/v1"}); err != nil {
		t.Fatalf("set continuation context: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(store.Dir(), sessionFile))
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "openai_base_url") {
		t.Fatalf("expected continuation openai_base_url to persist, got %q", text)
	}
	if strings.Contains(text, "model_verbosity") {
		t.Fatalf("session metadata must not persist model_verbosity: %s", text)
	}

	opened, err := Open(store.Dir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if opened.Meta().Continuation == nil || opened.Meta().Continuation.OpenAIBaseURL != "http://example.local/v1" {
		t.Fatalf("expected persisted continuation context, got %+v", opened.Meta().Continuation)
	}
	reopenedMetaJSON, err := json.Marshal(opened.Meta())
	if err != nil {
		t.Fatalf("marshal reopened meta: %v", err)
	}
	if strings.Contains(string(reopenedMetaJSON), "model_verbosity") {
		t.Fatal("expected reopened session metadata to remain free of model_verbosity")
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
