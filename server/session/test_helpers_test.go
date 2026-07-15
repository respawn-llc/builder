package session

import (
	"context"
	"sync"
	"testing"

	"core/internal/testharness/recordstore"
)

type testSessionMetadata struct {
	records map[string]PersistedSessionRecord
	store   *recordstore.Store[PersistedSessionRecord]
	once    sync.Once
}

var sessionTestPersistence = &testSessionMetadata{records: map[string]PersistedSessionRecord{}}

func (p *testSessionMetadata) ObservePersistedStore(_ context.Context, snapshot PersistedStoreSnapshot) error {
	p.sharedStore().Put(snapshot.Meta.SessionID, PersistedSessionRecord{
		SessionDir: snapshot.SessionDir,
		Meta:       cloneTestMeta(&snapshot.Meta),
	})
	return nil
}

func (p *testSessionMetadata) ObserveEventLogReconciliation(_ context.Context, reconciliation PersistedEventLogReconciliation) error {
	record, ok := p.sharedStore().Get(reconciliation.SessionID)
	if !ok {
		return ErrSessionNotFound
	}
	meta := cloneTestMeta(record.Meta)
	meta.LastSequence = reconciliation.LastSequence
	meta.ConversationEstablished = reconciliation.ConversationEstablished
	meta.UpdatedAt = reconciliation.UpdatedAt
	record.Meta = meta
	p.sharedStore().Put(reconciliation.SessionID, record)
	return nil
}

func (p *testSessionMetadata) ResolvePersistedSession(_ context.Context, sessionID string) (PersistedSessionRecord, error) {
	record, ok := p.sharedStore().Get(sessionID)
	if !ok {
		return PersistedSessionRecord{}, ErrSessionNotFound
	}
	return record, nil
}

func (p *testSessionMetadata) sharedStore() *recordstore.Store[PersistedSessionRecord] {
	p.once.Do(func() {
		p.store = recordstore.NewWithRecords(p.records, cloneTestPersistedSessionRecord)
	})
	return p.store
}

func cloneTestPersistedSessionRecord(record PersistedSessionRecord) PersistedSessionRecord {
	record.Meta = cloneTestMeta(record.Meta)
	return record
}

func cloneTestMeta(meta *Meta) *Meta {
	if meta == nil {
		return nil
	}
	cloned := *meta
	return &cloned
}

func (p *testSessionMetadata) options() []StoreOption {
	return []StoreOption{
		WithPersistenceObserver(p),
		WithPersistedSessionResolver(p),
	}
}

func newSessionTestStore(t *testing.T) *Store {
	t.Helper()
	return newSessionTestStoreAt(t, t.TempDir())
}

func newSessionTestStoreAt(t *testing.T, root string) *Store {
	t.Helper()
	store, err := Create(root, "workspace-x", "/tmp/work", sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	if err := store.EnsureDurable(); err != nil {
		t.Fatalf("persist store: %v", err)
	}
	return store
}

func newSessionTestLazyStore(t *testing.T) *Store {
	t.Helper()
	return newSessionTestLazyStoreAt(t, t.TempDir())
}

func newSessionTestLazyStoreAt(t *testing.T, root string) *Store {
	t.Helper()
	store, err := NewLazy(root, "workspace-x", "/tmp/work", sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("new lazy store: %v", err)
	}
	return store
}

func reopenSessionTestStore(t *testing.T, store *Store) *Store {
	t.Helper()
	reopened, err := Open(store.Dir(), sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return reopened
}

func openSessionTestStore(store *Store) (*Store, error) {
	return Open(store.Dir(), sessionTestPersistence.options()...)
}
