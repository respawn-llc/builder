package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ptrMeta(meta Meta) *Meta {
	return &meta
}

func TestOpenByIDUsesPersistedSessionResolver(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "projects", "project-b", "sessions", "session-b")
	target, err := Create(sessionDir, "sessions", "/tmp/work-b", sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := target.SetContinuationContext(ContinuationContext{OpenAIBaseURL: "http://target.local/v1"}); err != nil {
		t.Fatalf("set continuation context: %v", err)
	}

	opened, err := OpenByID(root, target.Meta().SessionID, WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
		SessionDir: target.Dir(),
		Meta:       ptrMeta(target.Meta()),
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
	if meta.Continuation == nil || meta.Continuation.OpenAIBaseURL != "http://target.local/v1" {
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
	store, err := Create(root, "workspace-container", "/tmp/work-a", sessionTestPersistence.options()...)
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
	store, err := Create(container, "source", "/workspace/source", sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("EnsureDurable: %v", err)
	}
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
	meta := store.Meta()
	if meta.WorkspaceRoot != "/workspace/target" || meta.WorkspaceContainer != "target" {
		t.Fatalf("workspace metadata = %+v", meta)
	}
	if meta.WorktreeReminder != nil {
		t.Fatalf("worktree reminder = %+v, want nil", meta.WorktreeReminder)
	}
	if _, _, err := store.AppendEvent("step-1", "message", map[string]string{"text": "after move"}); err != nil {
		t.Fatalf("AppendEvent after move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, eventsFile)); err != nil {
		t.Fatalf("target events file: %v", err)
	}
}

func TestRunArtifactRelocationRejectsZeroCommitTimeBeforeCallback(t *testing.T) {
	container := t.TempDir()
	store, err := Create(container, "source", "/workspace/source", sessionTestPersistence.options()...)
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
	store, err := Create(t.TempDir(), "source", "/workspace/source", sessionTestPersistence.options()...)
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

func TestOpenRejectsSymlinkedEventsFile(t *testing.T) {
	root := t.TempDir()
	targetDir := filepath.Join(root, "target-session")
	writeSessionFixtureEvents(t, targetDir, []Event{{
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

	if _, err := Open(sessionDir, WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta:       &meta,
	}})); err == nil || !errors.Is(err, ErrSessionFileSymlink) {
		t.Fatalf("expected open to reject symlinked events file, got %v", err)
	}
}

func TestOpenMissingEventsFileWithObserverRepairsAndPublishes(t *testing.T) {
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
	if _, err := os.Stat(filepath.Join(sessionDir, eventsFile)); err != nil {
		t.Fatalf("expected missing events file to be recreated: %v", err)
	}
	if opened.Meta().LastSequence != 0 {
		t.Fatalf("expected reopened last sequence to reconcile to zero, got %d", opened.Meta().LastSequence)
	}
	if !observer.reconciled || observer.reconciliation.LastSequence != 0 {
		t.Fatalf("observer reconciliation = %+v, called = %t", observer.reconciliation, observer.reconciled)
	}
	events, err := collectEvents(opened)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected recreated events file to be empty, got %+v", events)
	}
}

func TestOpenMissingEventsFileWithoutObserverFailsBeforeCreatingArtifact(t *testing.T) {
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

	_, err := Open(
		sessionDir,
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta:       &meta,
		}}),
	)
	if !errors.Is(err, errPersistenceObserverRequired) {
		t.Fatalf("Open error = %v, want persistence observer required", err)
	}
	if _, statErr := os.Stat(filepath.Join(sessionDir, eventsFile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("events artifact exists after rejected open: %v", statErr)
	}
}

func TestFilelessOpenMissingEventsFileFailsWithoutCreatingArtifact(t *testing.T) {
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

	_, err := Open(
		sessionDir,
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta:       &meta,
		}}),
		WithFilelessEventPersistence(),
	)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open error = %v, want missing events artifact", err)
	}
	if _, statErr := os.Stat(filepath.Join(sessionDir, eventsFile)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("events artifact exists after fileless open: %v", statErr)
	}
}

func TestReadEventsIgnoresTrailingTruncatedEOFLine(t *testing.T) {
	store := newSessionTestStore(t)
	if _, _, err := store.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "u1"}); err != nil {
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

	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	if events[0].Seq != 1 {
		t.Fatalf("expected seq=1, got %d", events[0].Seq)
	}
}

func TestAppendEventRepairsTruncatedTailBeforeAppend(t *testing.T) {
	store := newSessionTestStore(t)
	if _, _, err := store.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "u1"}); err != nil {
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

	e2, _, err := store.AppendEvent("s2", "message", map[string]any{"role": "assistant", "content": "a2"})
	if err != nil {
		t.Fatalf("append event 2: %v", err)
	}
	if e2.Seq != 2 {
		t.Fatalf("expected seq=2, got %d", e2.Seq)
	}

	events, err := collectEvents(store)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("unexpected event sequence: %+v", events)
	}
}

func TestOpenReconcilesMetaLastSequenceFromEventLog(t *testing.T) {
	store := newSessionTestStore(t)
	if _, _, err := store.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "u1"}); err != nil {
		t.Fatalf("append event 1: %v", err)
	}
	if _, _, err := store.AppendEvent("s2", "message", map[string]any{"role": "assistant", "content": "a1"}); err != nil {
		t.Fatalf("append event 2: %v", err)
	}

	meta := store.Meta()
	meta.LastSequence = 0

	reopened, err := Open(
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
	if reopened.Meta().LastSequence != 2 {
		t.Fatalf("expected reconciled last sequence 2, got %d", reopened.Meta().LastSequence)
	}
	next, _, err := reopened.AppendEvent("s3", "message", map[string]any{"role": "user", "content": "u2"})
	if err != nil {
		t.Fatalf("append event after reconcile: %v", err)
	}
	if next.Seq != 3 {
		t.Fatalf("expected seq=3 after reopen reconciliation, got %d", next.Seq)
	}
}

func writeSessionFixtureEvents(t *testing.T, sessionDir string, events []Event) {
	t.Helper()
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	lines, err := encodeEventLines(events, false)
	if err != nil {
		t.Fatalf("encode events: %v", err)
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
