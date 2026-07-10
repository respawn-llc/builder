package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type stubPersistedSessionResolver struct {
	record PersistedSessionRecord
	err    error
}

func (s stubPersistedSessionResolver) ResolvePersistedSession(context.Context, string) (PersistedSessionRecord, error) {
	if s.err != nil {
		return PersistedSessionRecord{}, s.err
	}
	return s.record, nil
}

type recordingPersistenceObserver struct {
	snapshot PersistedStoreSnapshot
	called   bool
	err      error
}

func (r *recordingPersistenceObserver) ObservePersistedStore(_ context.Context, snapshot PersistedStoreSnapshot) error {
	r.called = true
	r.snapshot = snapshot
	return r.err
}

type flakyPersistenceObserver struct {
	failuresRemaining int
	callCount         int
	lastSnapshot      PersistedStoreSnapshot
}

func (o *flakyPersistenceObserver) ObservePersistedStore(_ context.Context, snapshot PersistedStoreSnapshot) error {
	o.callCount++
	o.lastSnapshot = snapshot
	if o.failuresRemaining > 0 {
		o.failuresRemaining--
		return context.DeadlineExceeded
	}
	return nil
}

type reentrantPersistenceObserver struct {
	store *Store
	ch    chan Meta
}

func (o *reentrantPersistenceObserver) ObservePersistedStore(_ context.Context, _ PersistedStoreSnapshot) error {
	o.ch <- o.store.Meta()
	return nil
}

func TestOpenByIDUsesResolverWhenSessionMetaFileIsMissing(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "projects", "project-1", "sessions", "session-1")
	writeSessionFixtureEvents(t, sessionDir, nil)
	now := time.Now().UTC()
	store, err := OpenByID(
		root,
		"session-1",
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta: &Meta{
				SessionID:     "session-1",
				WorkspaceRoot: "/tmp/workspace-a",
				CreatedAt:     now,
				UpdatedAt:     now,
			},
		}}),
		WithFilelessMetadataPersistence(),
	)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	if got := store.Meta().SessionID; got != "session-1" {
		t.Fatalf("session id = %q, want session-1", got)
	}
	if got := store.Meta().WorkspaceRoot; got != "/tmp/workspace-a" {
		t.Fatalf("workspace root = %q", got)
	}
}

func TestFilelessMetadataPersistenceSkipsSessionFileAndPublishesObserver(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "projects", "project-1", "sessions", "session-1")
	writeSessionFixtureEvents(t, sessionDir, nil)
	now := time.Now().UTC()
	observer := &recordingPersistenceObserver{}
	store, err := Open(
		sessionDir,
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta: &Meta{
				SessionID:     "session-1",
				WorkspaceRoot: "/tmp/workspace-a",
				CreatedAt:     now,
				UpdatedAt:     now,
			},
		}}),
		WithFilelessMetadataPersistence(),
		WithPersistenceObserver(observer),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetName("incident triage"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, sessionFile)); !os.IsNotExist(err) {
		t.Fatalf("expected no session meta file, got %v", err)
	}
	if !observer.called {
		t.Fatal("expected persistence observer to be called")
	}
	if observer.snapshot.Meta.Name != "incident triage" {
		t.Fatalf("observer name = %q", observer.snapshot.Meta.Name)
	}
}

func TestFilelessEventPersistenceDoesNotAppendToEventLog(t *testing.T) {
	persisted := newSessionTestStore(t)
	if _, _, err := persisted.AppendEvent("live", "message", map[string]any{"role": "user", "content": "before inspection"}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	eventsPath := filepath.Join(persisted.Dir(), eventsFile)
	sessionPath := filepath.Join(persisted.Dir(), sessionFile)
	before, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read event log before inspection: %v", err)
	}
	metaBefore, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read session metadata before inspection: %v", err)
	}

	inspection, err := Open(
		persisted.Dir(),
		WithFilelessEventPersistence(),
	)
	if err != nil {
		t.Fatalf("open inspection store: %v", err)
	}
	event, committed, err := inspection.AppendEvent("inspect", "message", map[string]any{"role": "developer", "content": "ephemeral context"})
	if err != nil {
		t.Fatalf("append inspection event: %v", err)
	}
	if !committed {
		t.Fatal("expected inspection event to apply to the in-memory store")
	}
	if event.Seq != 2 || inspection.Meta().LastSequence != 2 {
		t.Fatalf("inspection sequence = %d / %d, want 2", event.Seq, inspection.Meta().LastSequence)
	}

	after, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read event log after inspection: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("inspection appended to the durable event log")
	}
	metaAfter, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read session metadata after inspection: %v", err)
	}
	if string(metaAfter) != string(metaBefore) {
		t.Fatal("inspection advanced durable session metadata")
	}
}

func TestForkAtUserMessagePreservesPersistenceOptions(t *testing.T) {
	root := t.TempDir()
	observer := &recordingPersistenceObserver{}
	parent, err := Create(
		root,
		"workspace-x",
		"/tmp/work",
		WithPersistenceObserver(observer),
		WithFilelessMetadataPersistence(),
	)
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	userEvt, _, err := parent.AppendEvent("s1", "message", map[string]any{"role": "user", "content": "u1"})
	if err != nil {
		t.Fatalf("append user message: %v", err)
	}
	observer.called = false

	forked, _, err := ForkAtUserMessage(parent, userEvt.Seq, "Parent -> edit u1")
	if err != nil {
		t.Fatalf("fork at user message: %v", err)
	}
	if !observer.called {
		t.Fatal("expected forked child persistence to publish observer snapshot")
	}
	if observer.snapshot.Meta.SessionID != forked.Meta().SessionID {
		t.Fatalf("observer session id = %q, want %q", observer.snapshot.Meta.SessionID, forked.Meta().SessionID)
	}
	if _, err := os.Stat(filepath.Join(forked.Dir(), sessionFile)); !os.IsNotExist(err) {
		t.Fatalf("expected forked child to preserve fileless metadata persistence, stat err=%v", err)
	}
}

func TestOpenByIDRejectsResolverRecordWithoutMetadata(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "projects", "project-1", "sessions", "session-1")
	writeSessionFixtureEvents(t, sessionDir, nil)
	_, err := OpenByID(
		root,
		"session-1",
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{SessionDir: sessionDir}}),
		WithFilelessMetadataPersistence(),
	)
	if err == nil || !errors.Is(err, errResolverRecordMissingMetadata) {
		t.Fatalf("expected missing metadata validation error, got %v", err)
	}
}

func TestOpenByIDRejectsResolverRecordWithRelativeSessionDir(t *testing.T) {
	root := t.TempDir()
	_, err := OpenByID(
		root,
		"session-1",
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: "relative/session-1",
			Meta:       &Meta{SessionID: "session-1"},
		}}),
		WithFilelessMetadataPersistence(),
	)
	if err == nil || !errors.Is(err, errResolverRecordRelativeSessionDir) {
		t.Fatalf("expected absolute clean path validation error, got %v", err)
	}
}

func TestOpenByIDRequiresPersistedSessionResolver(t *testing.T) {
	root := t.TempDir()
	_, err := OpenByID(root, "session-1")
	if err == nil || !errors.Is(err, errPersistedSessionResolverRequired) {
		t.Fatalf("expected resolver-required error, got %v", err)
	}
}

func TestFilelessMetadataRetriesSameValueUntilObserverSucceeds(t *testing.T) {
	store := newSessionTestLazyStore(t)
	observer := &flakyPersistenceObserver{failuresRemaining: 1}
	store.options.filelessMeta = true
	store.options.observer = observer
	store.options.observerTimeout = time.Second

	err := store.SetInputDraft("draft")
	if err == nil {
		t.Fatal("expected first SetInputDraft call to surface observer failure")
	}
	if observer.callCount != 1 {
		t.Fatalf("observer call count after failure = %d, want 1", observer.callCount)
	}
	err = store.SetInputDraft("draft")
	if err != nil {
		t.Fatalf("second SetInputDraft should retry same value successfully: %v", err)
	}
	if observer.callCount != 2 {
		t.Fatalf("observer call count after retry = %d, want 2", observer.callCount)
	}
	if observer.lastSnapshot.Meta.InputDraft != "draft" {
		t.Fatalf("persisted draft = %q, want draft", observer.lastSnapshot.Meta.InputDraft)
	}
}

func TestFilelessPersistenceObserverRunsOutsideStoreLock(t *testing.T) {
	store := newSessionTestLazyStore(t)
	observer := &reentrantPersistenceObserver{ch: make(chan Meta, 1)}
	observer.store = store
	store.options.filelessMeta = true
	store.options.observer = observer
	store.options.observerTimeout = time.Second

	errCh := make(chan error, 1)
	go func() {
		errCh <- store.SetName("incident triage")
	}()

	select {
	case meta := <-observer.ch:
		if meta.Name != "incident triage" {
			t.Fatalf("observer reentrant read name = %q, want incident triage", meta.Name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer did not complete; possible store lock reentrancy deadlock")
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("SetName: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SetName did not return; possible store lock reentrancy deadlock")
	}
}
