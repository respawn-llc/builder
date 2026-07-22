package session

import "context"

type stubPersistedSessionResolver struct {
	record PersistedSessionRecord
	err    error
}

func (resolver stubPersistedSessionResolver) ResolvePersistedSession(context.Context, string) (PersistedSessionRecord, error) {
	if resolver.err != nil {
		return PersistedSessionRecord{}, resolver.err
	}
	return resolver.record, nil
}

func collectEvents(store *Store) ([]Event, error) {
	events := make([]Event, 0)
	if err := store.WalkEvents(func(event Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		return nil, err
	}
	return events, nil
}

type recordingPersistenceObserver struct {
	snapshot       PersistedStoreSnapshot
	reconciliation PersistedEventLogReconciliation
	called         bool
	reconciled     bool
	err            error
	afterObserve   func() error
}

func (observer *recordingPersistenceObserver) ObservePersistedStore(_ context.Context, snapshot PersistedStoreSnapshot) error {
	observer.called = true
	observer.snapshot = snapshot
	if observer.afterObserve != nil {
		return observer.afterObserve()
	}
	return observer.err
}

func (observer *recordingPersistenceObserver) ObserveEventLogReconciliation(_ context.Context, reconciliation PersistedEventLogReconciliation) error {
	observer.reconciled = true
	observer.reconciliation = reconciliation
	return observer.err
}
