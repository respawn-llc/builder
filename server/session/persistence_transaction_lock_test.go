package session

import (
	"context"
	"sync"
	"testing"
	"time"
)

type blockingAfterPersistenceObserver struct {
	downstream PersistenceObserver
	mu         sync.Mutex
	blockNext  bool
	blocked    chan struct{}
	release    chan struct{}
}

func newBlockingAfterPersistenceObserver(
	downstream PersistenceObserver,
) *blockingAfterPersistenceObserver {
	return &blockingAfterPersistenceObserver{
		downstream: downstream,
		blocked:    make(chan struct{}),
		release:    make(chan struct{}),
	}
}

func (o *blockingAfterPersistenceObserver) Arm() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.blockNext = true
}

func (o *blockingAfterPersistenceObserver) ObservePersistedStore(
	ctx context.Context,
	snapshot PersistedStoreSnapshot,
) error {
	if err := o.downstream.ObservePersistedStore(ctx, snapshot); err != nil {
		return err
	}
	o.mu.Lock()
	block := o.blockNext
	o.blockNext = false
	o.mu.Unlock()
	if !block {
		return nil
	}
	close(o.blocked)
	select {
	case <-o.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *blockingAfterPersistenceObserver) ObserveEventLogReconciliation(
	ctx context.Context,
	reconciliation PersistedEventLogReconciliation,
) error {
	downstream, ok := o.downstream.(EventLogReconciliationObserver)
	if !ok {
		return errEventLogReconcilerRequired
	}
	return downstream.ObserveEventLogReconciliation(ctx, reconciliation)
}

func TestConcurrentOpenCannotRecoverActiveAppendTransaction(t *testing.T) {
	persistence := &testSessionMetadata{records: map[string]PersistedSessionRecord{}}
	observer := newBlockingAfterPersistenceObserver(persistence)
	root := t.TempDir()
	store, err := Create(
		root,
		"workspace-x",
		"/tmp/work",
		testSessionCategory,
		WithPersistenceObserver(observer),
		WithPersistedSessionResolver(persistence),
	)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	log := mustMaterializeSessionTestEventLog(t, store)

	type appendOutcome struct {
		record EventRecord
		err    error
	}
	observer.Arm()
	appendDone := make(chan appendOutcome, 1)
	go func() {
		record, _, err := log.AppendRecord(
			sessionTestStringPointer("step-1"),
			sessionTestMessage(MessageRoleAssistant, "first"),
		)
		appendDone <- appendOutcome{record: record, err: err}
	}()
	select {
	case <-observer.blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("append did not reach metadata persistence")
	}

	recovery, err := store.readAppendRecoveryRecord()
	if err != nil {
		close(observer.release)
		t.Fatalf("read active append recovery: %v", err)
	}
	if recovery == nil || recovery.Events == nil {
		close(observer.release)
		t.Fatalf("active append recovery = %+v, want event transaction", recovery)
	}
	// The observer gate gives the test a deterministic active transaction.
	// Restoring its prepared phase models the state a concurrent opener could
	// observe before the writer reaches the recovery commit point.
	recovery.Phase = appendRecoveryPrepared
	if err := store.writeAppendRecoveryRecord(*recovery); err != nil {
		close(observer.release)
		t.Fatalf("stage active append recovery: %v", err)
	}

	type openOutcome struct {
		store *Store
		err   error
	}
	openDone := make(chan openOutcome, 1)
	go func() {
		opened, err := OpenByID(root, store.Meta().SessionID,
			WithPersistenceObserver(observer),
			WithPersistedSessionResolver(persistence),
		)
		openDone <- openOutcome{store: opened, err: err}
	}()

	var (
		openedEarly bool
		opened      openOutcome
	)
	select {
	case opened = <-openDone:
		openedEarly = true
	case <-time.After(100 * time.Millisecond):
	}
	close(observer.release)

	appended := <-appendDone
	if !openedEarly {
		select {
		case opened = <-openDone:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent open did not resume after append commit")
		}
	}
	if openedEarly {
		t.Fatal("concurrent open recovered an append transaction that was still active")
	}
	if appended.err != nil {
		t.Fatalf("first append: %v", appended.err)
	}
	if appended.record.Seq() != 1 {
		t.Fatalf("first append sequence = %d, want 1", appended.record.Seq())
	}
	if opened.err != nil {
		t.Fatalf("concurrent open: %v", opened.err)
	}
	if got := opened.store.Meta().LastSequence; got != 1 {
		t.Fatalf("concurrently opened metadata sequence = %d, want 1", got)
	}

	second, _, err := log.AppendRecord(
		sessionTestStringPointer("step-2"),
		sessionTestMessage(MessageRoleAssistant, "second"),
	)
	if err != nil {
		t.Fatalf("append after concurrent open: %v", err)
	}
	if second.Seq() != 2 {
		t.Fatalf("second append sequence = %d, want 2", second.Seq())
	}
}
