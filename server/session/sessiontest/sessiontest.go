// Package sessiontest provides test-only session fixtures and inspection
// helpers. Production code must never materialize the full event log into
// memory (histories can reach gigabytes); these helpers exist so tests can
// assert against complete histories and optional continuation roles without
// reintroducing production support for either behavior. The repo-wide
// architecture guard fails if any production package imports this package.
package sessiontest

import (
	"context"
	"core/server/session"

	"core/internal/testharness/recordstore"
)

type Persistence struct {
	records *recordstore.Store[session.PersistedSessionRecord]
}

func NewPersistence() *Persistence {
	return &Persistence{records: recordstore.New(clonePersistedSessionRecord)}
}

func (p *Persistence) Options() []session.StoreOption {
	return []session.StoreOption{
		session.WithPersistenceObserver(p),
		session.WithPersistedSessionResolver(p),
	}
}

func (p *Persistence) ObservePersistedStore(_ context.Context, snapshot session.PersistedStoreSnapshot) error {
	p.records.Put(snapshot.Meta.SessionID, session.PersistedSessionRecord{
		SessionDir: snapshot.SessionDir,
		Meta:       cloneMeta(&snapshot.Meta),
	})
	return nil
}

func (p *Persistence) ObserveEventLogReconciliation(_ context.Context, reconciliation session.PersistedEventLogReconciliation) error {
	record, ok := p.records.Get(reconciliation.SessionID)
	if !ok {
		return session.ErrSessionNotFound
	}
	meta := cloneMeta(record.Meta)
	meta.LastSequence = reconciliation.LastSequence
	meta.ConversationEstablished = reconciliation.ConversationEstablished
	meta.UpdatedAt = reconciliation.UpdatedAt
	record.Meta = meta
	p.records.Put(reconciliation.SessionID, record)
	return nil
}

func (p *Persistence) ResolvePersistedSession(_ context.Context, sessionID string) (session.PersistedSessionRecord, error) {
	record, ok := p.records.Get(sessionID)
	if !ok {
		return session.PersistedSessionRecord{}, session.ErrSessionNotFound
	}
	return record, nil
}

func clonePersistedSessionRecord(record session.PersistedSessionRecord) session.PersistedSessionRecord {
	record.Meta = cloneMeta(record.Meta)
	return record
}

func cloneMeta(meta *session.Meta) *session.Meta {
	if meta == nil {
		return nil
	}
	cloned := *meta
	return &cloned
}

func (p *Persistence) Open(dir string, options ...session.StoreOption) (*session.Store, error) {
	return session.Open(dir, append(p.Options(), options...)...)
}

// CollectEvents streams the store's event log via the production streaming
// reader and accumulates every event. Test-only.
func CollectEvents(store *session.Store) ([]session.Event, error) {
	if store == nil {
		return nil, nil
	}
	events := make([]session.Event, 0)
	if err := store.WalkEvents(func(evt session.Event) error {
		events = append(events, evt)
		return nil
	}); err != nil {
		return nil, err
	}
	return events, nil
}

// AgentRole constructs a present continuation agent-role fixture. Test-only.
func AgentRole(value string) *string {
	return &value
}

// SameAgentRole compares optional continuation agent-role fixtures. Test-only.
func SameAgentRole(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// Snapshot mirrors the durable session state a test commonly asserts against:
// metadata, the full event history, and conversation freshness.
type Snapshot struct {
	Meta                  session.Meta
	Events                []session.Event
	ConversationFreshness session.ConversationFreshness
}

// SnapshotFromDir opens the persisted session at dir and projects its durable
// state into a Snapshot using the production streaming readers. It surfaces the
// same symlink/integrity rejections as session.Open. Test-only.
func SnapshotFromDir(dir string, options ...session.StoreOption) (Snapshot, error) {
	store, err := session.Open(dir, options...)
	if err != nil {
		return Snapshot{}, err
	}
	events, err := CollectEvents(store)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		Meta:                  store.Meta(),
		Events:                events,
		ConversationFreshness: store.ConversationFreshness(),
	}, nil
}
