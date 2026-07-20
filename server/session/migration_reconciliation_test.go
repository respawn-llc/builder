package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEventLogPreparationClassifiesPreCommitFailureWithoutChangingSource(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	source := []byte(`{"contract":"kent.session.events","version":1`)
	writeEventLogPreparationSource(t, eventsPath, source)

	store := newEventLogPreparationStore(t, sessionDir, Meta{})
	_, err := store.prepareEventLogMaterialization()
	var materializationErr *EventLogMaterializationError
	if !errors.As(err, &materializationErr) {
		t.Fatalf("preparation error is not typed: %v", err)
	}
	if materializationErr.Committed || materializationErr.PendingRepair {
		t.Fatalf("pre-commit materialization facts = %+v", materializationErr)
	}
	if materializationErr.Stage != EventLogMaterializationStagePreparation {
		t.Fatalf("pre-commit stage = %v", materializationErr.Stage)
	}
	if got, readErr := os.ReadFile(eventsPath); readErr != nil || string(got) != string(source) {
		t.Fatalf("pre-commit source changed: bytes=%q err=%v", got, readErr)
	}
}

func TestEventLogPreparationClassifiesCleanupFailureWithoutChangingSource(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	workspace := eventLogMigrationWorkspacePath(sessionDir)
	if err := ensureOwnedEventLogMigrationWorkspace(workspace); err != nil {
		t.Fatalf("create owned workspace: %v", err)
	}
	unknownPath := filepath.Join(workspace, "operator-note")
	writeEventLogPreparationSource(t, unknownPath, []byte("preserve"))
	eventsPath := filepath.Join(sessionDir, eventsFile)
	source := []byte("{not-json}\n")
	writeEventLogPreparationSource(t, eventsPath, source)

	_, err := newEventLogPreparationStore(t, sessionDir, Meta{}).prepareEventLogMaterialization()
	assertPreCommitMaterializationError(t, err)
	if got, readErr := os.ReadFile(eventsPath); readErr != nil || string(got) != string(source) {
		t.Fatalf("cleanup failure changed source: bytes=%q err=%v", got, readErr)
	}
}

func TestHeaderInstallCleanupFailureReportsCommittedPendingFacts(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	workspace := eventLogMigrationWorkspacePath(sessionDir)
	store := newEventLogPreparationStore(t, sessionDir, Meta{})
	var release func()
	err := installHeaderOnlyCurrentEventLog(eventsPath, workspace, func() {
		store.setEventLogMaterializationState(
			eventLogCurrentReconciliationPending,
			eventLogSourceMissing,
			nil,
		)
		release = armEventLogWorkspaceCleanupFailure(t, workspace)
	})
	defer func() {
		if release != nil {
			release()
		}
	}()
	var materializationErr *EventLogMaterializationError
	if !errors.As(err, &materializationErr) {
		t.Fatalf("post-rename cleanup error is not typed: %v", err)
	}
	if !materializationErr.Committed || !materializationErr.PendingRepair ||
		materializationErr.Stage != EventLogMaterializationStagePreparation {
		t.Fatalf("post-rename cleanup facts = %+v", materializationErr)
	}
	if _, err := openCurrentEventLog(eventsPath, currentEventLogReadOnly, eventLogOptions{}); err != nil {
		t.Fatalf("post-rename current source unavailable: %v", err)
	}
}

func TestEventLogPreparationDoesNotReusePriorPendingCommitFacts(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	store := newEventLogPreparationStore(t, sessionDir, Meta{})
	store.setEventLogMaterializationState(
		eventLogCurrentReconciliationPending,
		eventLogSourceCurrent,
		eventLogSourceVersion(EventLogVersionV1),
	)
	if err := os.RemoveAll(sessionDir); err != nil {
		t.Fatalf("remove session directory: %v", err)
	}
	if err := os.WriteFile(sessionDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("replace session directory with file: %v", err)
	}

	_, err := store.prepareEventLogMaterialization()
	assertPreCommitMaterializationError(t, err)
}

func TestCurrentReconciliationObserverFailureKeepsCommittedLogPending(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	observer := &eventLogReconciliationTestObserver{err: context.DeadlineExceeded}
	store := newEventLogReconciliationStore(t, sessionDir, Meta{}, observer, nil)

	result, err := store.prepareEventLogMaterialization()
	if err != nil {
		t.Fatalf("prepare header-only current event log: %v", err)
	}
	assertEventLogPreparationResult(t, result, eventLogCurrentReconciliationPending, eventLogSourceMissing)
	err = store.reconcileCurrentEventLog()
	var materializationErr *EventLogMaterializationError
	if !errors.As(err, &materializationErr) {
		t.Fatalf("reconciliation error is not typed: %v", err)
	}
	if !materializationErr.Committed || !materializationErr.PendingRepair {
		t.Fatalf("post-rename materialization facts = %+v", materializationErr)
	}
	if materializationErr.Stage != EventLogMaterializationStageReconciliation {
		t.Fatalf("post-rename stage = %v", materializationErr.Stage)
	}
	if snapshot := eventLogPreparationStoreSnapshot(t, store); snapshot.state != eventLogCurrentReconciliationPending {
		t.Fatalf("state after observer failure = %v, want pending", snapshot.state)
	}
	if _, err := openCurrentEventLog(filepath.Join(sessionDir, eventsFile), currentEventLogReadOnly, eventLogOptions{}); err != nil {
		t.Fatalf("current file was not retained after observer failure: %v", err)
	}
}

func TestCurrentReconciliationRecoversEstablishedConversationFromVisibleUserMessage(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	log, err := createCurrentEventLog(eventsPath, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{
		currentTestMessageRecord(t, 1, "visible user message"),
	}); err != nil {
		t.Fatalf("append visible user message: %v", err)
	}
	meta := reconciliationTestMeta()
	meta.LastSequence = 1
	observer := &eventLogReconciliationTestObserver{}
	store := newEventLogReconciliationStore(t, sessionDir, meta, observer, nil)
	if _, err := store.prepareEventLogMaterialization(); err != nil {
		t.Fatalf("prepare current event log: %v", err)
	}

	if err := store.reconcileCurrentEventLog(); err != nil {
		t.Fatalf("reconcile current event log: %v", err)
	}
	if len(observer.observations) != 1 ||
		!observer.observations[0].ConversationEstablished {
		t.Fatalf("reconciliation observation = %+v, want established", observer.observations)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize reconciled event log: %v", err)
	}
	if mustConversationFreshness(eventLog) != ConversationFreshnessEstablished {
		t.Fatal("reconciled metadata did not establish conversation")
	}
}

func TestCurrentReconciliationRecoversEstablishedConversationFromCompactedUserHistory(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	log, err := createCurrentEventLog(eventsPath, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	record, err := NewEventRecord(1, nil, HistoryReplacementRecord{
		Engine: "local",
		Mode:   CompactionModeAuto,
		Items: []ProviderHistoryItem{{
			Type:    ProviderHistoryItemTypeMessage,
			Role:    pointerTo(MessageRoleUser),
			Content: stringPointer("compacted visible user message"),
			Raw:     []byte(`{"type":"message","role":"user"}`),
		}},
	})
	if err != nil {
		t.Fatalf("create compacted user history: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{record}); err != nil {
		t.Fatalf("append compacted user history: %v", err)
	}
	meta := reconciliationTestMeta()
	meta.LastSequence = 1
	observer := &eventLogReconciliationTestObserver{}
	store := newEventLogReconciliationStore(t, sessionDir, meta, observer, nil)
	if _, err := store.prepareEventLogMaterialization(); err != nil {
		t.Fatalf("prepare current event log: %v", err)
	}

	if err := store.reconcileCurrentEventLog(); err != nil {
		t.Fatalf("reconcile current event log: %v", err)
	}
	if len(observer.observations) != 1 ||
		!observer.observations[0].ConversationEstablished {
		t.Fatalf("reconciliation observation = %+v, want established", observer.observations)
	}
}

func TestCurrentReconciliationPreservesEstablishedConversationAcrossRepeatedCompaction(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	log, err := createCurrentEventLog(eventsPath, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{
		currentTestMessageRecord(t, 1, "visible user message"),
		currentTestHistoryReplacementRecord(t, 2),
		currentTestHistoryReplacementRecord(t, 3),
	}); err != nil {
		t.Fatalf("append repeatedly compacted history: %v", err)
	}
	meta := reconciliationTestMeta()
	meta.LastSequence = 3
	observer := &eventLogReconciliationTestObserver{}
	store := newEventLogReconciliationStore(t, sessionDir, meta, observer, nil)
	if _, err := store.prepareEventLogMaterialization(); err != nil {
		t.Fatalf("prepare current event log: %v", err)
	}

	if err := store.reconcileCurrentEventLog(); err != nil {
		t.Fatalf("reconcile current event log: %v", err)
	}
	if len(observer.observations) != 1 ||
		!observer.observations[0].ConversationEstablished {
		t.Fatalf("reconciliation observation = %+v, want established", observer.observations)
	}
}

func TestCurrentReconciliationClearsStaleEstablishedConversationForHeaderOnlyLog(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	createCurrentEventLogPreparationSource(t, filepath.Join(sessionDir, eventsFile))
	meta := reconciliationTestMeta()
	meta.ConversationEstablished = true
	observer := &eventLogReconciliationTestObserver{}
	store := newEventLogReconciliationStore(t, sessionDir, meta, observer, nil)
	if _, err := store.prepareEventLogMaterialization(); err != nil {
		t.Fatalf("prepare current event log: %v", err)
	}

	if err := store.reconcileCurrentEventLog(); err != nil {
		t.Fatalf("reconcile current event log: %v", err)
	}
	if len(observer.observations) != 1 ||
		observer.observations[0].ConversationEstablished {
		t.Fatalf("reconciliation observation = %+v, want fresh", observer.observations)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize reconciled event log: %v", err)
	}
	if mustConversationFreshness(eventLog) == ConversationFreshnessEstablished {
		t.Fatal("reconciled metadata retained stale established conversation")
	}
	if mustConversationFreshness(eventLog) != ConversationFreshnessFresh {
		t.Fatalf(
			"reconciled freshness = %v, want fresh",
			mustConversationFreshness(eventLog),
		)
	}
}

func TestCurrentReconciliationInvalidatesUsageAfterNewCompactionBoundary(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	log, err := createCurrentEventLog(eventsPath, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{
		currentTestMessageRecord(t, 1, "visible user message"),
		currentTestHistoryReplacementRecord(t, 2),
	}); err != nil {
		t.Fatalf("append current records: %v", err)
	}
	meta := reconciliationTestMeta()
	meta.LastSequence = 1
	meta.UsageState = &UsageState{InputTokens: 42}
	observer := &eventLogReconciliationTestObserver{}
	store := newEventLogReconciliationStore(t, sessionDir, meta, observer, nil)
	if _, err := store.prepareEventLogMaterialization(); err != nil {
		t.Fatalf("prepare current event log: %v", err)
	}

	if err := store.reconcileCurrentEventLog(); err != nil {
		t.Fatalf("reconcile current event log: %v", err)
	}
	if len(observer.observations) != 1 ||
		observer.observations[0].UsageState != UsageStateReconciliationInvalidate {
		t.Fatalf("reconciliation observation = %+v, want usage invalidation", observer.observations)
	}
	if store.Metadata().UsageState != nil {
		t.Fatalf("reconciled metadata retained stale usage state: %+v", store.Metadata().UsageState)
	}
}

func TestCurrentReconciliationRefreshesNewerMetadataAndRetries(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	meta := reconciliationTestMeta()
	meta.LastSequence = 7
	resolver := &eventLogReconciliationTestResolver{record: PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta:       &meta,
	}}
	observer := &eventLogReconciliationTestObserver{
		conflictFirst: true,
		onConflict: func() {
			refreshed := meta
			refreshed.LastSequence = 9
			resolver.record.Meta = &refreshed
		},
	}
	store := newEventLogReconciliationStore(t, sessionDir, meta, observer, resolver)
	createCurrentEventLogPreparationSource(t, filepath.Join(sessionDir, eventsFile))
	if _, err := store.prepareEventLogMaterialization(); err != nil {
		t.Fatalf("prepare current event log: %v", err)
	}

	if err := store.reconcileCurrentEventLog(); err != nil {
		t.Fatalf("reconcile after metadata refresh: %v", err)
	}
	if observer.calls != 2 || observer.observations[0].ObservedLastSequence != 7 ||
		observer.observations[1].ObservedLastSequence != 9 {
		t.Fatalf("reconciliation observations = %+v", observer.observations)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize reconciled event log: %v", err)
	}
	if got := mustMaterializedRevision(eventLog); got != 0 {
		t.Fatalf("reconciled metadata last sequence = %d, want current log sequence 0", got)
	}
	if snapshot := eventLogPreparationStoreSnapshot(t, store); snapshot.state != eventLogCurrent {
		t.Fatalf("state after durable retry = %v, want current", snapshot.state)
	}
}

func TestCurrentReconciliationRecomputesLogFactsAfterConflict(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	meta := reconciliationTestMeta()
	resolver := &eventLogReconciliationTestResolver{record: PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta:       &meta,
	}}
	observer := &eventLogReconciliationTestObserver{
		conflictFirst: true,
		onConflict: func() {
			log, err := openCurrentEventLog(
				filepath.Join(sessionDir, eventsFile),
				currentEventLogAuthoritative,
				eventLogOptions{fsyncPolicy: EventLogFSyncAlways},
			)
			if err != nil {
				t.Fatalf("open current event log during conflict: %v", err)
			}
			if _, err := log.appendRecords([]EventRecord{currentTestMessageRecord(t, 1, "new")}); err != nil {
				t.Fatalf("append current event log during conflict: %v", err)
			}
			refreshed := meta
			refreshed.LastSequence = 1
			resolver.record.Meta = &refreshed
		},
	}
	store := newEventLogReconciliationStore(t, sessionDir, meta, observer, resolver)
	createCurrentEventLogPreparationSource(t, filepath.Join(sessionDir, eventsFile))
	if _, err := store.prepareEventLogMaterialization(); err != nil {
		t.Fatalf("prepare current event log: %v", err)
	}

	if err := store.reconcileCurrentEventLog(); err != nil {
		t.Fatalf("reconcile after append conflict: %v", err)
	}
	if observer.calls != 2 || observer.observations[1].LastSequence != 1 {
		t.Fatalf("reconciliation observations = %+v", observer.observations)
	}
}

func TestCurrentReconciliationStopsAfterOneRefreshedConflict(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	meta := reconciliationTestMeta()
	resolver := &eventLogReconciliationTestResolver{record: PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta:       &meta,
	}}
	observer := &eventLogReconciliationTestObserver{conflictFirst: true}
	observer.alwaysConflict = true
	store := newEventLogReconciliationStore(t, sessionDir, meta, observer, resolver)
	createCurrentEventLogPreparationSource(t, filepath.Join(sessionDir, eventsFile))
	if _, err := store.prepareEventLogMaterialization(); err != nil {
		t.Fatalf("prepare current event log: %v", err)
	}

	err := store.reconcileCurrentEventLog()
	var materializationErr *EventLogMaterializationError
	if !errors.As(err, &materializationErr) || !materializationErr.Committed ||
		!materializationErr.PendingRepair {
		t.Fatalf("bounded conflict error facts = %+v / %v", materializationErr, err)
	}
	if observer.calls != 2 {
		t.Fatalf("always-conflicting observer calls = %d, want 2", observer.calls)
	}
}

func TestCurrentReconciliationRetryFromPendingSucceedsWithoutPreparation(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	observer := &eventLogReconciliationTestObserver{err: context.DeadlineExceeded}
	store := newEventLogReconciliationStore(t, sessionDir, Meta{}, observer, nil)
	if _, err := store.prepareEventLogMaterialization(); err != nil {
		t.Fatalf("prepare current event log: %v", err)
	}
	if err := store.reconcileCurrentEventLog(); err == nil {
		t.Fatal("expected first reconciliation failure")
	}
	observer.err = nil
	if err := store.reconcileCurrentEventLog(); err != nil {
		t.Fatalf("retry pending reconciliation: %v", err)
	}
	if observer.calls != 2 {
		t.Fatalf("observer calls = %d, want 2", observer.calls)
	}
	if snapshot := eventLogPreparationStoreSnapshot(t, store); snapshot.state != eventLogCurrent ||
		snapshot.source != eventLogSourceCurrent {
		t.Fatalf("state after retry = %v, want current", snapshot.state)
	}
}

func TestCurrentReconciliationHoldsStableLockAcrossObservation(t *testing.T) {
	sessionDir := newEventLogPreparationSessionDir(t)
	eventsPath := filepath.Join(sessionDir, eventsFile)
	log, err := createCurrentEventLog(eventsPath, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{
		currentTestMessageRecord(t, 1, "first"),
	}); err != nil {
		t.Fatalf("append initial current record: %v", err)
	}
	meta := reconciliationTestMeta()
	meta.LastSequence = 1

	firstObservationStarted := make(chan struct{})
	releaseFirstObservation := make(chan struct{})
	firstObserver := eventLogReconciliationObserverFunc(func(
		_ context.Context,
		_ PersistedEventLogReconciliation,
	) error {
		close(firstObservationStarted)
		<-releaseFirstObservation
		return nil
	})
	firstStore := newEventLogReconciliationStore(t, sessionDir, meta, firstObserver, nil)
	if _, err := firstStore.prepareEventLogMaterialization(); err != nil {
		t.Fatalf("prepare first current event log: %v", err)
	}

	secondAppendCommitted := make(chan struct{})
	secondObserver := eventLogReconciliationObserverFunc(func(
		_ context.Context,
		_ PersistedEventLogReconciliation,
	) error {
		current, openErr := openCurrentEventLog(
			eventsPath,
			currentEventLogAuthoritative,
			eventLogOptions{fsyncPolicy: EventLogFSyncAlways},
		)
		if openErr != nil {
			return openErr
		}
		if _, appendErr := current.appendRecords([]EventRecord{
			currentTestMessageRecord(t, 2, "second"),
		}); appendErr != nil {
			return appendErr
		}
		close(secondAppendCommitted)
		return context.DeadlineExceeded
	})
	secondStore := newEventLogReconciliationStore(t, sessionDir, meta, secondObserver, nil)
	if _, err := secondStore.prepareEventLogMaterialization(); err != nil {
		t.Fatalf("prepare second current event log: %v", err)
	}

	firstDone := make(chan error, 1)
	go func() {
		firstDone <- firstStore.reconcileCurrentEventLog()
	}()
	<-firstObservationStarted

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- secondStore.reconcileCurrentEventLog()
	}()

	appendRaced := false
	select {
	case <-secondAppendCommitted:
		appendRaced = true
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirstObservation)

	if err := <-firstDone; err != nil {
		t.Fatalf("first reconciliation: %v", err)
	}
	secondErr := <-secondDone
	var materializationErr *EventLogMaterializationError
	if !errors.As(secondErr, &materializationErr) ||
		!materializationErr.Committed ||
		!materializationErr.PendingRepair {
		t.Fatalf("second committed observation failure = %+v / %v", materializationErr, secondErr)
	}
	if appendRaced {
		t.Fatal("second Store committed an append while stale reconciliation was observing metadata")
	}
	current, err := openCurrentEventLog(
		eventsPath,
		currentEventLogReadOnly,
		eventLogOptions{},
	)
	if err != nil {
		t.Fatalf("reopen current event log: %v", err)
	}
	if current.lastSequence != 2 {
		t.Fatalf("current event log sequence = %d, want committed append at 2", current.lastSequence)
	}
}

type eventLogReconciliationObserverFunc func(
	context.Context,
	PersistedEventLogReconciliation,
) error

func (f eventLogReconciliationObserverFunc) ObserveEventLogReconciliation(
	ctx context.Context,
	observation PersistedEventLogReconciliation,
) error {
	return f(ctx, observation)
}

type eventLogReconciliationTestObserver struct {
	err            error
	conflictFirst  bool
	alwaysConflict bool
	onConflict     func()
	calls          int
	observations   []PersistedEventLogReconciliation
}

func (o *eventLogReconciliationTestObserver) ObserveEventLogReconciliation(
	_ context.Context,
	observation PersistedEventLogReconciliation,
) error {
	o.calls++
	o.observations = append(o.observations, observation)
	if o.conflictFirst && (o.calls == 1 || o.alwaysConflict) {
		if o.onConflict != nil {
			o.onConflict()
		}
		return EventLogReconciliationConflictError{
			SessionID:            observation.SessionID,
			ObservedLastSequence: observation.ObservedLastSequence,
			CurrentLastSequence:  observation.ObservedLastSequence + 1,
		}
	}
	return o.err
}

func eventLogSourceVersion(value int) *int {
	return &value
}

type eventLogReconciliationTestResolver struct {
	record PersistedSessionRecord
}

func (r *eventLogReconciliationTestResolver) ResolvePersistedSession(
	context.Context,
	string,
) (PersistedSessionRecord, error) {
	return r.record, nil
}

func reconciliationTestMeta() Meta {
	now := time.Now().UTC()
	return Meta{
		SessionID: "session",
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func newEventLogReconciliationStore(
	t *testing.T,
	sessionDir string,
	meta Meta,
	observer EventLogReconciliationObserver,
	resolver PersistedSessionResolver,
) *Store {
	t.Helper()
	if meta.SessionID == "" {
		meta = reconciliationTestMeta()
	}
	options := []StoreOption{
		WithPersistenceObserver(eventLogReconciliationObserverAdapter{observer}),
	}
	if resolver != nil {
		options = append(options, WithPersistedSessionResolver(resolver))
	}
	store, err := OpenResolved(PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta:       &meta,
	}, options...)
	if err != nil {
		t.Fatalf("open reconciliation store: %v", err)
	}
	return store
}

func assertPreCommitMaterializationError(t *testing.T, err error) {
	t.Helper()
	var materializationErr *EventLogMaterializationError
	if !errors.As(err, &materializationErr) {
		t.Fatalf("pre-commit error is not typed: %v", err)
	}
	if materializationErr.Committed || materializationErr.PendingRepair ||
		materializationErr.Stage != EventLogMaterializationStagePreparation {
		t.Fatalf("pre-commit materialization facts = %+v", materializationErr)
	}
}

type eventLogReconciliationObserverAdapter struct {
	EventLogReconciliationObserver
}

func (eventLogReconciliationObserverAdapter) ObservePersistedStore(
	context.Context,
	PersistedStoreSnapshot,
) error {
	return nil
}
