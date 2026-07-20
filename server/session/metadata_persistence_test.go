package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
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
	snapshot       PersistedStoreSnapshot
	reconciliation PersistedEventLogReconciliation
	called         bool
	reconciled     bool
	err            error
}

func (r *recordingPersistenceObserver) ObservePersistedStore(_ context.Context, snapshot PersistedStoreSnapshot) error {
	r.called = true
	r.snapshot = snapshot
	return r.err
}

func (r *recordingPersistenceObserver) ObserveEventLogReconciliation(_ context.Context, reconciliation PersistedEventLogReconciliation) error {
	r.reconciled = true
	r.reconciliation = reconciliation
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
	o.ch <- storeTestMeta(o.store)
	return nil
}

type blockingFailingPersistenceObserver struct {
	downstream PersistenceObserver
	mu         sync.Mutex
	failNext   bool
	blocked    chan struct{}
	release    chan struct{}
}

func newBlockingFailingPersistenceObserver(downstream PersistenceObserver) *blockingFailingPersistenceObserver {
	return &blockingFailingPersistenceObserver{
		downstream: downstream,
		blocked:    make(chan struct{}),
		release:    make(chan struct{}),
	}
}

func (o *blockingFailingPersistenceObserver) Arm() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.failNext = true
}

func (o *blockingFailingPersistenceObserver) ObservePersistedStore(ctx context.Context, snapshot PersistedStoreSnapshot) error {
	o.mu.Lock()
	fail := o.failNext
	o.failNext = false
	o.mu.Unlock()
	if fail {
		close(o.blocked)
		select {
		case <-o.release:
			return os.ErrPermission
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return o.downstream.ObservePersistedStore(ctx, snapshot)
}

func TestOpenByIDUsesAuthoritativeResolver(t *testing.T) {
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
	)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	if got := store.Metadata().WorkspaceRoot; got != "/tmp/workspace-a" {
		t.Fatalf("workspace root = %q", got)
	}
}

func TestOpenUsesAuthoritativeResolverMetadata(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	writeSessionFixtureEvents(t, sessionDir, nil)
	now := time.Now().UTC()
	authoritative := Meta{
		SessionID:          "session-1",
		WorkspaceRoot:      "/tmp/workspace-new",
		WorkspaceContainer: "workspace-new",
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	opened, err := Open(
		sessionDir,
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta:       &authoritative,
		}}),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.Metadata().WorkspaceRoot != authoritative.WorkspaceRoot {
		t.Fatalf("workspace root = %q, want authoritative %q", opened.Metadata().WorkspaceRoot, authoritative.WorkspaceRoot)
	}
}

func TestOpenRejectsAuthoritativeResolverSessionDirMismatch(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	otherDir := filepath.Join(t.TempDir(), "session-1")
	now := time.Now().UTC()
	_, err := Open(
		sessionDir,
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: otherDir,
			Meta: &Meta{
				SessionID:     "session-1",
				WorkspaceRoot: "/tmp/workspace",
				CreatedAt:     now,
				UpdatedAt:     now,
			},
		}}),
	)
	if !errors.Is(err, errResolverRecordSessionDirMismatch) {
		t.Fatalf("Open error = %v, want session dir mismatch", err)
	}
}

func TestOpenPropagatesAuthoritativeResolverNotFound(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	_, err := Open(
		sessionDir,
		WithPersistedSessionResolver(stubPersistedSessionResolver{err: ErrSessionNotFound}),
	)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Open error = %v, want ErrSessionNotFound", err)
	}
}

func TestOpenRejectsAuthoritativeResolverSessionIDMismatch(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	now := time.Now().UTC()
	_, err := Open(
		sessionDir,
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta: &Meta{
				SessionID:     "session-2",
				WorkspaceRoot: "/tmp/workspace",
				CreatedAt:     now,
				UpdatedAt:     now,
			},
		}}),
	)
	if !errors.Is(err, errResolverRecordSessionIDMismatch) {
		t.Fatalf("Open error = %v, want session id mismatch", err)
	}
}

func TestOpenRejectsAuthoritativeResolverMissingSessionID(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	now := time.Now().UTC()
	_, err := Open(
		sessionDir,
		WithPersistedSessionResolver(stubPersistedSessionResolver{record: PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta: &Meta{
				WorkspaceRoot: "/tmp/workspace",
				CreatedAt:     now,
				UpdatedAt:     now,
			},
		}}),
	)
	if !errors.Is(err, errResolverRecordMissingSessionID) {
		t.Fatalf("Open error = %v, want missing session id", err)
	}
}

func TestMetadataPersistencePublishesObserver(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
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
		WithPersistenceObserver(observer),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.SetName("incident triage"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if !observer.called || observer.snapshot.Meta.Name != "incident triage" {
		t.Fatalf("observer snapshot = %+v, called = %t", observer.snapshot.Meta, observer.called)
	}
}

func TestFilelessEventPersistenceDoesNotAppendToEventLog(t *testing.T) {
	persisted := newSessionTestStore(t)
	persistedLog := mustMaterializeSessionTestEventLog(t, persisted)
	if _, _, err := persistedLog.AppendRecord(stringPointer(uuid.NewString()), sessionTestMessage(MessageRoleUser, "before inspection")); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	eventsPath := filepath.Join(persisted.Dir(), eventsFile)
	before, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read event log before inspection: %v", err)
	}

	inspection, err := Open(
		persisted.Dir(),
		WithPersistedSessionResolver(sessionTestPersistence),
		WithFilelessEventPersistence(),
	)
	if err != nil {
		t.Fatalf("open inspection store: %v", err)
	}
	inspectionLog, lease, err := inspection.MaterializeFilelessEventLog(context.Background())
	if err != nil {
		t.Fatalf("materialize inspection event log: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close() })
	event, committed, err := inspectionLog.AppendRecord(stringPointer(uuid.NewString()), sessionTestMessage(MessageRoleDeveloper, "ephemeral context"))
	if err != nil {
		t.Fatalf("append inspection event: %v", err)
	}
	if !committed.Committed || event.Seq() != 2 || mustMaterializedRevision(inspectionLog) != 2 {
		t.Fatalf("inspection event = %+v, receipt = %+v, revision = %d", event, committed, mustMaterializedRevision(inspectionLog))
	}

	after, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read event log after inspection: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("inspection appended to the durable event log")
	}
}

func TestForkAtUserMessagePreservesPersistenceObserver(t *testing.T) {
	observer := &recordingPersistenceObserver{}
	parent, err := Create(t.TempDir(), "workspace-x", "/tmp/work", testSessionCategory, WithPersistenceObserver(observer))
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	parentLog := mustMaterializeSessionTestEventLog(t, parent)
	userEvt, _, err := parentLog.AppendRecord(stringPointer("s1"), sessionTestMessage(MessageRoleUser, "u1"))
	if err != nil {
		t.Fatalf("append user message: %v", err)
	}
	observer.called = false

	forked, _, err := ForkAtUserMessage(parentLog, userEvt.Seq(), "Parent -> edit u1", testSessionCategory)
	if err != nil {
		t.Fatalf("fork at user message: %v", err)
	}
	if !observer.called || observer.snapshot.Meta.SessionID != forked.Metadata().SessionID {
		t.Fatalf("observer snapshot = %+v, called = %t", observer.snapshot.Meta, observer.called)
	}
}

func TestOpenByIDRejectsInvalidResolverRecords(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "projects", "project-1", "sessions", "session-1")
	writeSessionFixtureEvents(t, sessionDir, nil)
	tests := []struct {
		name   string
		record PersistedSessionRecord
		want   error
	}{
		{
			name:   "missing metadata",
			record: PersistedSessionRecord{SessionDir: sessionDir},
			want:   errResolverRecordMissingMetadata,
		},
		{
			name: "relative session dir",
			record: PersistedSessionRecord{
				SessionDir: "relative/session-1",
				Meta:       &Meta{SessionID: "session-1"},
			},
			want: errResolverRecordRelativeSessionDir,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := OpenByID(
				root,
				"session-1",
				WithPersistedSessionResolver(stubPersistedSessionResolver{record: tt.record}),
			)
			if !errors.Is(err, tt.want) {
				t.Fatalf("OpenByID error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestPersistedSessionOpenRequiresResolver(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "session-1")
	writeSessionFixtureEvents(t, sessionDir, nil)
	if _, err := Open(sessionDir); !errors.Is(err, errPersistedSessionResolverRequired) {
		t.Fatalf("Open error = %v, want resolver required", err)
	}
	if _, err := OpenByID(root, "session-1"); !errors.Is(err, errPersistedSessionResolverRequired) {
		t.Fatalf("OpenByID error = %v, want resolver required", err)
	}
}

func TestDurableSessionCreationRequiresPersistenceObserver(t *testing.T) {
	root := t.TempDir()
	if _, err := Create(root, "workspace-x", "/tmp/work", testSessionCategory); !errors.Is(err, errPersistenceObserverRequired) {
		t.Fatalf("Create error = %v, want persistence observer required", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("durable creation without authority created artifacts: %+v", entries)
	}
}

func TestMetadataMutationRequiresPersistenceObserverWithoutChangingState(t *testing.T) {
	store, err := NewLazy(t.TempDir(), "workspace-x", "/tmp/work", testSessionCategory)
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}
	if err := store.SetName("must not persist"); !errors.Is(err, errPersistenceObserverRequired) {
		t.Fatalf("SetName error = %v, want persistence observer required", err)
	}
	if store.Metadata().Name != "" {
		t.Fatalf("name changed without persistence observer: %q", store.Metadata().Name)
	}
	if _, err := os.Stat(store.Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session artifact created without persistence observer: %v", err)
	}
}

func TestEventLogMaterializationRequiresPersistenceObserverWithoutCreatingArtifact(t *testing.T) {
	store, err := NewLazy(t.TempDir(), "workspace-x", "/tmp/work", testSessionCategory)
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}
	if _, err := store.MaterializeEventLog(); err == nil {
		t.Fatal("MaterializeEventLog succeeded without durable session metadata")
	}
	if storeTestMeta(store).LastSequence != 0 {
		t.Fatalf("last sequence = %d, want unchanged", storeTestMeta(store).LastSequence)
	}
	if _, err := os.Stat(store.Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session artifact created without persistence observer: %v", err)
	}
}

func TestPersistedSessionDirectoryContainsOnlyAppendOnlyArtifacts(t *testing.T) {
	observer := &recordingPersistenceObserver{}
	store, err := Create(t.TempDir(), "workspace-x", "/tmp/work", testSessionCategory, WithPersistenceObserver(observer))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	entries, err := os.ReadDir(store.Dir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != eventsFile || entries[0].IsDir() {
		t.Fatalf("session artifacts = %+v, want only %s", entries, eventsFile)
	}
}

func TestMetadataPersistenceRetriesSameValueUntilObserverSucceeds(t *testing.T) {
	observer := &flakyPersistenceObserver{failuresRemaining: 1}
	store, err := NewLazy(t.TempDir(), "workspace-x", "/tmp/work", testSessionCategory, WithPersistenceObserver(observer))
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}

	if err := store.SetInputDraft("draft"); err == nil {
		t.Fatal("expected first SetInputDraft call to surface observer failure")
	}
	if err := store.SetInputDraft("draft"); err != nil {
		t.Fatalf("second SetInputDraft should retry same value successfully: %v", err)
	}
	if observer.callCount != 2 || observer.lastSnapshot.Meta.InputDraft != "draft" {
		t.Fatalf("observer calls = %d, snapshot = %+v", observer.callCount, observer.lastSnapshot.Meta)
	}
}

func TestSetUsageStateReportsCommittedObserverFailureAndRetries(t *testing.T) {
	observer := &flakyPersistenceObserver{failuresRemaining: 1}
	store, err := NewLazy(t.TempDir(), "workspace-x", "/tmp/work", testSessionCategory, WithPersistenceObserver(observer))
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}
	usage := &UsageState{InputTokens: 900, WindowTokens: 200_000}

	receipt, err := store.SetUsageState(usage)
	if err == nil || !receipt.Committed {
		t.Fatalf("first SetUsageState receipt=%+v error=%v, want committed observer failure", receipt, err)
	}
	if stored := store.Metadata().UsageState; stored == nil || stored.InputTokens != usage.InputTokens {
		t.Fatalf("committed usage state = %+v, want %+v", stored, usage)
	}

	receipt, err = store.SetUsageState(usage)
	if err != nil || !receipt.Committed {
		t.Fatalf("retried SetUsageState receipt=%+v error=%v, want committed success", receipt, err)
	}
	if stored := store.Metadata().UsageState; stored == nil || stored.InputTokens != usage.InputTokens {
		t.Fatalf("committed usage state = %+v, want %+v", stored, usage)
	}
}

func TestPersistenceObserverRunsOutsideStoreLock(t *testing.T) {
	observer := &reentrantPersistenceObserver{ch: make(chan Meta, 1)}
	store, err := NewLazy(t.TempDir(), "workspace-x", "/tmp/work", testSessionCategory, WithPersistenceObserver(observer))
	if err != nil {
		t.Fatalf("NewLazy: %v", err)
	}
	observer.store = store

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

func TestPersistenceSnapshotsAreImmutable(t *testing.T) {
	observer := &recordingPersistenceObserver{}
	store, err := Create(t.TempDir(), "workspace-x", "/tmp/work", testSessionCategory, WithPersistenceObserver(observer))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	verbosity := true
	if err := store.MarkModelDispatchLocked(LockedContract{
		Model:        "gpt-5",
		SystemPrompt: "original prompt",
		ProviderContract: LockedProviderCapabilities{
			SupportsProviderVerbosity: &verbosity,
		},
	}); err != nil {
		t.Fatalf("MarkModelDispatchLocked: %v", err)
	}
	if observer.snapshot.Meta.Locked == nil || observer.snapshot.Meta.Locked.ProviderContract.SupportsProviderVerbosity == nil {
		t.Fatalf("observer snapshot locked contract = %+v", observer.snapshot.Meta.Locked)
	}
	observer.snapshot.Meta.Locked.SystemPrompt = "observer mutation"
	*observer.snapshot.Meta.Locked.ProviderContract.SupportsProviderVerbosity = false

	locked := store.Metadata().Locked
	if locked == nil || locked.SystemPrompt != "original prompt" {
		t.Fatalf("store locked contract = %+v, want original prompt", locked)
	}
	if locked.ProviderContract.SupportsProviderVerbosity == nil || !*locked.ProviderContract.SupportsProviderVerbosity {
		t.Fatalf("store provider verbosity = %+v, want true", locked.ProviderContract.SupportsProviderVerbosity)
	}
}

func TestCommittedObservationFailurePrecedesLaterMutation(t *testing.T) {
	observer := newBlockingFailingPersistenceObserver(sessionTestPersistence)
	root := t.TempDir()
	store, err := Create(
		root,
		"workspace-x",
		"/tmp/work",
		testSessionCategory,
		WithPersistenceObserver(observer),
		WithPersistedSessionResolver(sessionTestPersistence),
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	observer.Arm()
	type mutationOutcome struct {
		result LockedContractMutationResult
		err    error
	}
	firstDone := make(chan mutationOutcome, 1)
	go func() {
		role := "reviewer"
		result, err := store.SetContinuationContextAndMarkLockedPromptFacingContractStale(
			ContinuationContext{AgentRole: &role},
		)
		firstDone <- mutationOutcome{result: result, err: err}
	}()
	select {
	case <-observer.blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("first persistence did not reach the observer")
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- store.SetName("later mutation")
	}()
	close(observer.release)

	first := <-firstDone
	if first.err == nil || !first.result.Committed {
		t.Fatalf("first mutation result = %+v, error = %v, want committed observer failure", first.result, first.err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if store.Metadata().Name != "later mutation" {
		t.Fatalf("session name = %q, want later mutation", store.Metadata().Name)
	}
	if continuation := store.Metadata().Continuation; continuation == nil || continuation.AgentRole == nil || *continuation.AgentRole != "reviewer" {
		t.Fatalf("committed continuation mutation = %+v, want reviewer", continuation)
	}

	reopened, err := OpenByID(root, store.Metadata().SessionID, sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("OpenByID: %v", err)
	}
	if reopened.Metadata().Name != "later mutation" {
		t.Fatalf("reopened name = %q, want later mutation", reopened.Metadata().Name)
	}
	if continuation := reopened.Metadata().Continuation; continuation == nil || continuation.AgentRole == nil || *continuation.AgentRole != "reviewer" {
		t.Fatalf("reopened continuation = %+v, want reviewer", continuation)
	}
}
