package session

import (
	"context"
	"sync"
	"testing"

	"core/internal/testharness/recordstore"
	"core/shared/sessioncontract"
)

const testSessionCategory = sessioncontract.SessionCategoryMain

type testSessionMetadata struct {
	records map[string]PersistedSessionRecord
	store   *recordstore.Store[PersistedSessionRecord]
	once    sync.Once
}

var sessionTestPersistence = &testSessionMetadata{records: map[string]PersistedSessionRecord{}}

func (p *testSessionMetadata) ObservePersistedStore(_ context.Context, snapshot PersistedStoreSnapshot) error {
	contextFacts := snapshot.ContextFacts.Clone()
	if existing, ok := p.sharedStore().Get(snapshot.Meta.SessionID); ok {
		contextFacts = existing.ContextFacts.Clone()
	}
	p.sharedStore().Put(snapshot.Meta.SessionID, PersistedSessionRecord{
		SessionDir:   snapshot.SessionDir,
		Meta:         cloneTestMeta(&snapshot.Meta),
		ContextFacts: contextFacts,
	})
	return nil
}

func (p *testSessionMetadata) WriteSessionContextFacts(
	_ context.Context,
	sessionID string,
	facts SessionContextFacts,
) error {
	record, ok := p.sharedStore().Get(sessionID)
	if !ok {
		return ErrSessionNotFound
	}
	record.ContextFacts = facts.Clone()
	p.sharedStore().Put(sessionID, record)
	return nil
}

func (p *testSessionMetadata) WriteManualCompactEligibility(
	_ context.Context,
	sessionID string,
	eligible bool,
) error {
	record, ok := p.sharedStore().Get(sessionID)
	if !ok {
		return ErrSessionNotFound
	}
	record.ContextFacts.ManualCompactEligible = &eligible
	p.sharedStore().Put(sessionID, record)
	return nil
}

func (p *testSessionMetadata) ResolvePersistedSession(_ context.Context, sessionID string) (PersistedSessionRecord, error) {
	record, ok := p.sharedStore().Get(sessionID)
	if !ok {
		return PersistedSessionRecord{}, ErrSessionNotFound
	}
	return record, nil
}

func (p *testSessionMetadata) ProjectAppend(_ context.Context, projection AppendProjection) error {
	record, ok := p.sharedStore().Get(projection.SessionID.String())
	if !ok {
		return ErrSessionNotFound
	}
	meta := cloneTestMeta(record.Meta)
	applyAppendProjectionToMeta(meta, projection)
	record.Meta = meta
	p.sharedStore().Put(projection.SessionID.String(), record)
	return nil
}

func (p *testSessionMetadata) sharedStore() *recordstore.Store[PersistedSessionRecord] {
	p.once.Do(func() {
		p.store = recordstore.NewWithRecords(p.records, cloneTestPersistedSessionRecord)
	})
	return p.store
}

func cloneTestPersistedSessionRecord(record PersistedSessionRecord) PersistedSessionRecord {
	record.Meta = cloneTestMeta(record.Meta)
	record.ContextFacts = record.ContextFacts.Clone()
	return record
}

func cloneTestMeta(meta *Meta) *Meta {
	if meta == nil {
		return nil
	}
	cloned := *meta
	if meta.UsageState != nil {
		usage := *meta.UsageState
		if meta.UsageState.HistoryReplacementEventSequence != nil {
			sequence := *meta.UsageState.HistoryReplacementEventSequence
			usage.HistoryReplacementEventSequence = &sequence
		}
		cloned.UsageState = &usage
	}
	return &cloned
}

func storeTestMeta(store *Store) Meta {
	return store.metaSnapshot().meta
}

func (p *testSessionMetadata) options() []StoreOption {
	return []StoreOption{
		WithPersistenceObserver(p),
		WithAppendProjector(p.ProjectAppend),
		WithPersistedSessionResolver(p),
		WithSessionContextFactWriter(p),
	}
}

func newSessionTestStore(t *testing.T) *Store {
	t.Helper()
	return newSessionTestStoreAt(t, t.TempDir())
}

func newSessionTestStoreAt(t *testing.T, root string) *Store {
	t.Helper()
	store, err := Create(root, "workspace-x", "/tmp/work", testSessionCategory, sessionTestPersistence.options()...)
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
	store, err := NewLazy(root, "workspace-x", "/tmp/work", testSessionCategory, sessionTestPersistence.options()...)
	if err != nil {
		t.Fatalf("new lazy store: %v", err)
	}
	return store
}

func openSessionTestStore(store *Store) (*Store, error) {
	return Open(store.Dir(), sessionTestPersistence.options()...)
}

func mustOpenSessionTestStore(t *testing.T, store *Store) *Store {
	t.Helper()
	opened, err := openSessionTestStore(store)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return opened
}

func mustMaterializeSessionTestEventLog(t *testing.T, store *Store) MaterializedEventLog {
	t.Helper()
	log, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	return log
}

func sessionTestMessage(role MessageRole, content string) MessageRecord {
	return MessageRecord{Role: role, Content: &content}
}

func sessionTestStringPointer(value string) *string {
	return &value
}
