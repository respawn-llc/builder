package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"core/shared/sessioncontract"
	"core/shared/textutil"
)

func ptrMeta(meta Meta) *Meta {
	return &meta
}

func TestOpenAcceptsLegacySessionWithoutCategory(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "legacy-session")
	now := time.Now().UTC()
	meta := Meta{
		SessionID:          "legacy-session",
		WorkspaceRoot:      "/tmp/work",
		WorkspaceContainer: "workspace",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	writeSessionFixtureEvents(t, sessionDir, nil)

	store, err := Open(sessionDir, WithPersistedSessionResolver(stubPersistedSessionResolver{
		record: PersistedSessionRecord{SessionDir: sessionDir, Meta: &meta},
	}))
	if err != nil {
		t.Fatalf("open legacy session: %v", err)
	}
	if got := store.Meta().Category; got != nil {
		t.Fatalf("legacy category = %v, want absent", got)
	}
}

func TestOpenRejectsMalformedPersistedSessionCategory(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "malformed-category-session")
	now := time.Now().UTC()
	meta := Meta{
		SessionID:          "malformed-category-session",
		WorkspaceRoot:      "/tmp/work",
		WorkspaceContainer: "workspace",
		CreatedAt:          now,
		UpdatedAt:          now,
		Category:           sessionCategoryTestPointer(sessioncontract.SessionCategory("worker")),
	}
	writeSessionFixtureEvents(t, sessionDir, nil)

	_, err := Open(sessionDir, WithPersistedSessionResolver(stubPersistedSessionResolver{
		record: PersistedSessionRecord{SessionDir: sessionDir, Meta: &meta},
	}))
	if err == nil {
		t.Fatal("open malformed category session succeeded")
	}
	var invalid InvalidSessionCategoryError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %T, want InvalidSessionCategoryError", err)
	}
	if invalid.SessionID != meta.SessionID || invalid.Category != *meta.Category {
		t.Fatalf("invalid category error = %+v, want session/category from persisted metadata", invalid)
	}
}

func sessionCategoryTestPointer(category sessioncontract.SessionCategory) *sessioncontract.SessionCategory {
	return &category
}

func TestOpenByIDUsesPersistedSessionResolver(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "projects", "project-b", "sessions", "session-b")
	target, err := Create(sessionDir, "sessions", "/tmp/work-b", testSessionCategory, sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := target.SetContinuationContext(ContinuationContext{OpenAIBaseURL: textutil.Value("http://target.local/v1")}); err != nil {
		t.Fatalf("set continuation context: %v", err)
	}

	opened, err := OpenByID(root, target.Meta().SessionID, WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
		SessionDir: target.Dir(),
		Meta:       ptrMeta(target.metaSnapshot().meta),
	}}))
	if err != nil {
		t.Fatalf("open by id: %v", err)
	}
	meta := opened.Meta()
	if meta.SessionID != target.Meta().SessionID {
		t.Fatalf("expected session id %q, got %q", target.Meta().SessionID, meta.SessionID)
	}
	if meta.WorkspaceRoot != "/tmp/work-b" {
		t.Fatalf("expected workspace root from target session, got %q", meta.WorkspaceRoot)
	}
	if meta.Continuation == nil || meta.Continuation.OpenAIBaseURL == nil || *meta.Continuation.OpenAIBaseURL != "http://target.local/v1" {
		t.Fatalf("expected continuation context from target session, got %+v", meta.Continuation)
	}
}

func TestOpenByIDRejectsWithoutPersistedSessionResolver(t *testing.T) {
	root := t.TempDir()
	if _, err := OpenByID(root, "missing-session"); err == nil || !errors.Is(err, errPersistedSessionResolverRequired) {
		t.Fatalf("expected missing resolver error, got %v", err)
	}
}

func TestSetWorkspaceRootPreservesWorkspaceContainer(t *testing.T) {
	root := t.TempDir()
	store, err := Create(root, "workspace-container", "/tmp/work-a", testSessionCategory, sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist store: %v", err)
	}

	if err := store.SetWorkspaceRoot("/tmp/work-b"); err != nil {
		t.Fatalf("SetWorkspaceRoot: %v", err)
	}
	if got := store.Meta().WorkspaceContainer; got != "workspace-container" {
		t.Fatalf("workspace container = %q, want workspace-container", got)
	}
	if got := store.Meta().WorkspaceRoot; got != "/tmp/work-b" {
		t.Fatalf("workspace root = %q, want /tmp/work-b", got)
	}

	reopened, err := openSessionTestStore(store)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if got := reopened.Meta().WorkspaceContainer; got != "workspace-container" {
		t.Fatalf("persisted workspace container = %q, want workspace-container", got)
	}
	if got := reopened.Meta().WorkspaceRoot; got != "/tmp/work-b" {
		t.Fatalf("persisted workspace root = %q, want /tmp/work-b", got)
	}
}

func TestRunArtifactRelocationUpdatesPathsAndWorkspaceAfterCallback(t *testing.T) {
	container := t.TempDir()
	store, err := Create(container, "source", "/workspace/source", testSessionCategory, sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
	log := mustMaterializeSessionTestEventLog(t, store)
	oldDir := store.Dir()
	targetContainer := t.TempDir()
	targetDir := filepath.Join(targetContainer, store.Meta().SessionID)

	err = store.RunArtifactRelocation(ArtifactRelocationTarget{
		SessionDir:         targetDir,
		WorkspaceRoot:      "/workspace/target",
		WorkspaceContainer: "target",
		UpdatedAt:          time.Now().UTC(),
	}, func() error {
		return os.Rename(oldDir, targetDir)
	})
	if err != nil {
		t.Fatalf("RunArtifactRelocation: %v", err)
	}

	if got := store.Dir(); got != targetDir {
		t.Fatalf("store dir = %q, want %q", got, targetDir)
	}
	meta := storeTestMeta(store)
	if meta.WorkspaceRoot != "/workspace/target" || meta.WorkspaceContainer != "target" {
		t.Fatalf("workspace metadata = %+v", meta)
	}
	if meta.WorktreeReminder != nil {
		t.Fatalf("worktree reminder = %+v, want nil", meta.WorktreeReminder)
	}
	if _, _, err := log.AppendRecord(stringPointer("step-1"), sessionTestMessage(MessageRoleUser, "after move")); err != nil {
		t.Fatalf("AppendEvent after move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, eventsFile)); err != nil {
		t.Fatalf("target events file: %v", err)
	}
}

func TestRunArtifactRelocationRejectsZeroCommitTimeBeforeCallback(t *testing.T) {
	container := t.TempDir()
	store, err := Create(container, "source", "/workspace/source", testSessionCategory, sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	oldDir := store.Dir()
	targetDir := filepath.Join(t.TempDir(), store.Meta().SessionID)
	callbackCalled := false
	err = store.RunArtifactRelocation(ArtifactRelocationTarget{
		SessionDir:         targetDir,
		WorkspaceRoot:      "/workspace/target",
		WorkspaceContainer: "target",
	}, func() error {
		callbackCalled = true
		return nil
	})
	if err == nil {
		t.Fatal("RunArtifactRelocation accepted a zero update time")
	}
	if callbackCalled {
		t.Fatal("RunArtifactRelocation called relocation before validating update time")
	}
	if store.Dir() != oldDir || store.Meta().WorkspaceRoot != "/workspace/source" {
		t.Fatalf("failed relocation mutated store: dir=%q meta=%+v", store.Dir(), store.Meta())
	}
}

func TestRunArtifactRelocationDoesNotMutateStoreWhenCallbackFails(t *testing.T) {
	store, err := Create(t.TempDir(), "source", "/workspace/source", testSessionCategory, sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	oldDir := store.Dir()
	callbackErr := errors.New("relocation failed")
	err = store.RunArtifactRelocation(ArtifactRelocationTarget{
		SessionDir:         filepath.Join(t.TempDir(), store.Meta().SessionID),
		WorkspaceRoot:      "/workspace/target",
		WorkspaceContainer: "target",
		UpdatedAt:          time.Now().UTC(),
	}, func() error {
		return callbackErr
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("RunArtifactRelocation error = %v, want %v", err, callbackErr)
	}
	if store.Dir() != oldDir || store.Meta().WorkspaceRoot != "/workspace/source" {
		t.Fatalf("failed relocation mutated store: dir=%q meta=%+v", store.Dir(), store.Meta())
	}
}

func TestEventUseRejectsSymlinkedEventsFileAfterMetadataOnlyOpen(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "target-session")
	writeSessionFixtureEvents(t, targetDir, []legacyTestEvent{{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Kind:      "message",
		StepID:    "target-step",
		Payload:   mustFixtureJSON(t, map[string]any{"role": "user", "content": "hello"}),
	}})
	sessionDir := filepath.Join(root, "bad-session")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	meta := Meta{
		SessionID:          "bad-session",
		WorkspaceRoot:      "/tmp/work-bad",
		WorkspaceContainer: "workspace-bad",
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}
	if err := os.Symlink(filepath.Join(targetDir, eventsFile), filepath.Join(sessionDir, eventsFile)); err != nil {
		t.Fatalf("symlink events file: %v", err)
	}

	opened, err := Open(sessionDir, WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta:       &meta,
	}}))
	if err != nil {
		t.Fatalf("metadata-only open: %v", err)
	}
	if _, err := opened.MaterializeEventLog(); err == nil || !errors.Is(err, ErrSessionFileSymlink) {
		t.Fatalf("expected event use to reject symlinked events file, got %v", err)
	}
}

func TestEventUseRejectsMissingEventsFileWithoutMutation(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-without-events")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	meta := Meta{
		SessionID:          "session-without-events",
		WorkspaceRoot:      "/tmp/work",
		WorkspaceContainer: "workspace-x",
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
		LastSequence:       3,
	}
	observer := &recordingPersistenceObserver{}

	opened, err := Open(
		sessionDir,
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta:       &meta,
		}}),
		WithPersistenceObserver(observer),
	)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, eventsFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata-only open created events file: %v", err)
	}
	if _, err := opened.MaterializeEventLog(); err == nil {
		t.Fatal("expected missing published event log error")
	}
	if _, err := os.Stat(filepath.Join(sessionDir, eventsFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing event log was mutated: %v", err)
	}
}

func TestMaterializeMissingEventsFileWithoutObserverFailsUnchanged(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-without-events")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	meta := Meta{
		SessionID:          "session-without-events",
		WorkspaceRoot:      "/tmp/work",
		WorkspaceContainer: "workspace-x",
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}

	opened, err := Open(
		sessionDir,
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta:       &meta,
		}}),
	)
	if err != nil {
		t.Fatalf("metadata-only open: %v", err)
	}
	_, materializeErr := opened.MaterializeEventLog()
	var typedErr *EventLogMaterializationError
	if !errors.As(materializeErr, &typedErr) {
		t.Fatalf("materialization error = %v, want typed materialization error", materializeErr)
	}
	if typedErr.Committed || typedErr.PendingRepair {
		t.Fatalf("materialization error facts = %+v", typedErr)
	}
	if _, statErr := os.Stat(filepath.Join(sessionDir, eventsFile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("missing event log was mutated: %v", statErr)
	}
}

func TestReadEventsIgnoresTrailingTruncatedEOFLine(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	if _, _, err := log.AppendRecord(stringPointer("s1"), sessionTestMessage(MessageRoleUser, "u1")); err != nil {
		t.Fatalf("append event: %v", err)
	}

	fp, err := os.OpenFile(filepath.Join(store.Dir(), eventsFile), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open events for append: %v", err)
	}
	if _, err := fp.WriteString("{\"seq\":2"); err != nil {
		_ = fp.Close()
		t.Fatalf("append truncated line: %v", err)
	}
	if err := fp.Close(); err != nil {
		t.Fatalf("close events file: %v", err)
	}

	window, err := log.ReadRecentRecords(10)
	if err != nil {
		t.Fatalf("read typed records: %v", err)
	}
	if len(window.Records) != 1 || window.Records[0].Seq() != 1 {
		t.Fatalf("typed records = %#v, want one sequence-1 record", window.Records)
	}
}

func TestAppendEventRepairsTruncatedTailBeforeAppend(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	if _, _, err := log.AppendRecord(stringPointer("s1"), sessionTestMessage(MessageRoleUser, "u1")); err != nil {
		t.Fatalf("append event 1: %v", err)
	}

	fp, err := os.OpenFile(filepath.Join(store.Dir(), eventsFile), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open events for append: %v", err)
	}
	if _, err := fp.WriteString("{\"seq\":2"); err != nil {
		_ = fp.Close()
		t.Fatalf("append truncated tail: %v", err)
	}
	if err := fp.Close(); err != nil {
		t.Fatalf("close events file: %v", err)
	}

	e2, _, err := log.AppendRecord(stringPointer("s2"), sessionTestMessage(MessageRoleAssistant, "a2"))
	if err != nil {
		t.Fatalf("append event 2: %v", err)
	}
	if e2.Seq() != 2 {
		t.Fatalf("expected seq=2, got %d", e2.Seq())
	}

	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Seq() != 1 || events[1].Seq() != 2 {
		t.Fatalf("unexpected event sequence: %+v", events)
	}
}

func TestEventUseReconcilesMetaLastSequenceFromEventLog(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	if _, _, err := log.AppendRecord(stringPointer("s1"), sessionTestMessage(MessageRoleUser, "u1")); err != nil {
		t.Fatalf("append event 1: %v", err)
	}
	if _, _, err := log.AppendRecord(stringPointer("s2"), sessionTestMessage(MessageRoleAssistant, "a1")); err != nil {
		t.Fatalf("append event 2: %v", err)
	}

	meta := store.metaSnapshot().meta
	meta.LastSequence = 0
	persistence := &testSessionMetadata{records: map[string]PersistedSessionRecord{
		meta.SessionID: {
			SessionDir: store.Dir(),
			Meta:       &meta,
		},
	}}

	reopened, err := Open(
		store.Dir(),
		WithPersistedSessionResolver(persistence),
		WithPersistenceObserver(persistence),
	)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if got := storeTestMeta(reopened).LastSequence; got != 0 {
		t.Fatalf("metadata-only open changed last sequence to %d", got)
	}
	reopenedLog, err := reopened.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	if mustMaterializedRevision(reopenedLog) != 2 {
		t.Fatalf("expected reconciled last sequence 2, got %d", mustMaterializedRevision(reopenedLog))
	}
	next, _, err := reopenedLog.AppendRecord(stringPointer("s3"), sessionTestMessage(MessageRoleUser, "u2"))
	if err != nil {
		t.Fatalf("append event after reconcile: %v", err)
	}
	if next.Seq() != 3 {
		t.Fatalf("expected seq=3 after reopen reconciliation, got %d", next.Seq())
	}
}

type legacyTestEvent struct {
	Seq       int64           `json:"seq"`
	Timestamp time.Time       `json:"timestamp"`
	Kind      string          `json:"kind"`
	StepID    string          `json:"step_id,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

func writeSessionFixtureEvents(t *testing.T, sessionDir string, events []legacyTestEvent) {
	t.Helper()
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	var lines []byte
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encode legacy event: %v", err)
		}
		lines = append(lines, line...)
		lines = append(lines, '\n')
	}
	if err := os.WriteFile(filepath.Join(sessionDir, eventsFile), lines, 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}
}

func mustFixtureJSON(t *testing.T, payload any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal fixture payload: %v", err)
	}
	return data
}
