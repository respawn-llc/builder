package session

import (
	"context"
	"os"
	"sync"
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
