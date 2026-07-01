// Package sessiontest provides test-only helpers for inspecting a session
// store's full event history. Production code must never materialize the full
// event log into memory (histories can reach gigabytes); these helpers exist so
// tests can assert against the complete history without reintroducing a
// production full-history materializer. The repo-wide architecture guard fails
// if any production package imports this package.
package sessiontest

import (
	"core/server/session"
)

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

// Snapshot mirrors the durable session state a test commonly asserts against:
// metadata, the full event history, derived run records, and conversation
// freshness.
type Snapshot struct {
	Meta                  session.Meta
	Events                []session.Event
	Runs                  []session.RunRecord
	ConversationFreshness session.ConversationFreshness
}

// SnapshotFromDir opens the persisted session at dir and projects its durable
// state into a Snapshot using the production streaming readers. It surfaces the
// same symlink/integrity rejections as session.Open. Test-only.
func SnapshotFromDir(dir string) (Snapshot, error) {
	store, err := session.Open(dir)
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
		Runs:                  session.ProjectRuns(events),
		ConversationFreshness: store.ConversationFreshness(),
	}, nil
}
