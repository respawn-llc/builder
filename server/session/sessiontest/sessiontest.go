// Package sessiontest provides test-only session fixtures and inspection
// helpers. Production code must never materialize the full event log into
// memory (histories can reach gigabytes); these helpers exist so tests can
// assert against complete histories and optional continuation roles without
// reintroducing production support for either behavior. The repo-wide
// architecture guard fails if any production package imports this package.
package sessiontest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"core/server/session"

	"core/internal/testharness/recordstore"
	"core/shared/textutil"
)

// WriteEventLogHeaderFixture replaces a test session's event artifact with a
// self-describing header fixture.
func WriteEventLogHeaderFixture(
	t testing.TB,
	store *session.Store,
	header session.EventLogHeader,
) {
	t.Helper()
	if store == nil {
		t.Fatal("session store is required")
	}
	encoded, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal event log header fixture: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(store.Dir(), "events.jsonl"),
		append(encoded, '\n'),
		0o600,
	); err != nil {
		t.Fatalf("write event log header fixture: %v", err)
	}
}

// WriteUnsupportedEventLogVersion replaces a test session's event artifact
// with a newer self-describing header.
func WriteUnsupportedEventLogVersion(
	t testing.TB,
	store *session.Store,
	version int,
) {
	t.Helper()
	if version <= session.EventLogVersionV1 {
		t.Fatalf(
			"unsupported event log fixture version = %d, want > %d",
			version,
			session.EventLogVersionV1,
		)
	}
	WriteEventLogHeaderFixture(t, store, session.EventLogHeader{
		Contract: session.EventLogContract,
		Version:  version,
	})
}

type Persistence struct {
	records         *recordstore.Store[session.PersistedSessionRecord]
	contextWriteErr error
}

func NewPersistence() *Persistence {
	return &Persistence{records: recordstore.New(clonePersistedSessionRecord)}
}

func (p *Persistence) Options() []session.StoreOption {
	return []session.StoreOption{
		session.WithPersistenceObserver(p),
		session.WithPersistedSessionResolver(p),
		session.WithSessionContextFactWriter(p),
	}
}

func (p *Persistence) ObservePersistedStore(_ context.Context, snapshot session.PersistedStoreSnapshot) error {
	contextFacts := snapshot.ContextFacts.Clone()
	if existing, ok := p.records.Get(snapshot.Meta.SessionID); ok {
		contextFacts = existing.ContextFacts.Clone()
	}
	p.records.Put(snapshot.Meta.SessionID, session.PersistedSessionRecord{
		SessionDir:   snapshot.SessionDir,
		Meta:         cloneMeta(&snapshot.Meta),
		ContextFacts: contextFacts,
	})
	return nil
}

func (p *Persistence) WriteSessionContextFacts(
	_ context.Context,
	sessionID string,
	facts session.SessionContextFacts,
) error {
	if p.contextWriteErr != nil {
		return p.contextWriteErr
	}
	record, ok := p.records.Get(sessionID)
	if !ok {
		return session.ErrSessionNotFound
	}
	record.ContextFacts = facts.Clone()
	p.records.Put(sessionID, record)
	return nil
}

func (p *Persistence) WriteManualCompactEligibility(
	_ context.Context,
	sessionID string,
	eligible bool,
) error {
	if p.contextWriteErr != nil {
		return p.contextWriteErr
	}
	record, ok := p.records.Get(sessionID)
	if !ok {
		return session.ErrSessionNotFound
	}
	record.ContextFacts.ManualCompactEligible = &eligible
	p.records.Put(sessionID, record)
	return nil
}

func (p *Persistence) ObserveEventLogReconciliation(_ context.Context, reconciliation session.PersistedEventLogReconciliation) error {
	record, ok := p.records.Get(reconciliation.SessionID)
	if !ok {
		return session.ErrSessionNotFound
	}
	invalidateUsageState, err := reconciliation.UsageState.InvalidatesUsageState()
	if err != nil {
		return err
	}
	meta := cloneMeta(record.Meta)
	if meta.LastSequence != reconciliation.ObservedLastSequence {
		return session.EventLogReconciliationConflictError{
			SessionID:            reconciliation.SessionID,
			ObservedLastSequence: reconciliation.ObservedLastSequence,
			CurrentLastSequence:  meta.LastSequence,
		}
	}
	meta.LastSequence = reconciliation.LastSequence
	meta.ConversationEstablished = reconciliation.ConversationEstablished
	meta.UpdatedAt = reconciliation.UpdatedAt
	if invalidateUsageState {
		meta.UsageState = nil
	}
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
	record.ContextFacts = record.ContextFacts.Clone()
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

// CollectRecords streams the store's materialized event log and accumulates
// every typed record. Test-only.
func CollectRecords(store *session.Store) ([]session.EventRecord, error) {
	if store == nil {
		return nil, nil
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		return nil, err
	}
	records := make([]session.EventRecord, 0)
	if err := eventLog.WalkRecords(func(record session.EventRecord) error {
		records = append(records, record)
		return nil
	}); err != nil {
		return nil, err
	}
	return records, nil
}

// AgentRole constructs a present continuation agent-role fixture. Test-only.
func AgentRole(value string) *string {
	return &value
}

func CompleteChatSettingsState(t testing.TB, agent, supervisor, thinking string, fast, questions, autoCompaction bool) session.ChatSettingsState {
	state, err := session.ChatSettingsStateFromCompleteSettings(agent, session.ChatSettings{Supervisor: supervisor, Thinking: thinking, Fast: fast, Questions: questions, AutoCompaction: autoCompaction})
	if err != nil {
		t.Fatalf("build Chat settings: %v", err)
	}
	return state
}

func CommitChatSettingsTestState(t testing.TB, store *session.Store, update func(*session.ChatSettingsOverrides)) {
	state, err := session.ChatSettingsStateFromMeta(store.Meta())
	if err != nil {
		t.Fatalf("read Chat settings: %v", err)
	}
	settings, err := session.ResolveEffectiveChatSettings(nil, state.Settings, session.ChatSettings{Supervisor: "off", Thinking: "medium", Fast: false, Questions: true, AutoCompaction: true})
	if err != nil {
		t.Fatalf("complete Chat settings: %v", err)
	}
	state = CompleteChatSettingsState(t, state.Agent, settings.Supervisor, settings.Thinking, settings.Fast, settings.Questions, settings.AutoCompaction)
	update(state.Settings)
	if _, err := store.CommitChatSettingsState(state); err != nil {
		t.Fatalf("commit Chat settings: %v", err)
	}
}

// Snapshot mirrors the durable session state a test commonly asserts against:
// metadata, the full event history, and conversation freshness.
type Snapshot struct {
	Meta                  session.Meta
	Records               []session.EventRecord
	ConversationFreshness session.ConversationFreshness
}
