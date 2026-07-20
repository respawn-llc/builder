package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMaterializeEventLogReturnsCurrentCapability(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	path := filepath.Join(sessionDir, eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{
		currentTestMessageRecord(t, 1, "hello"),
	}); err != nil {
		t.Fatalf("append current event record: %v", err)
	}
	before := eventLogFingerprint(t, path)
	now := time.Now().UTC()
	observer := &eventLogReconciliationTestObserver{}
	store, err := OpenResolved(PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta: &Meta{
			SessionID:               "session-1",
			CreatedAt:               now,
			UpdatedAt:               now,
			LastSequence:            1,
			ConversationEstablished: true,
		},
	}, WithPersistenceObserver(eventLogReconciliationObserverAdapter{observer}))
	if err != nil {
		t.Fatalf("open metadata-bound store: %v", err)
	}

	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize current event log: %v", err)
	}
	if mustMaterializedRevision(capability) != 1 {
		t.Fatalf("capability revision = %d, want 1", mustMaterializedRevision(capability))
	}
	if mustConversationFreshness(capability) != ConversationFreshnessEstablished {
		t.Fatalf(
			"capability freshness = %v, want established",
			mustConversationFreshness(capability),
		)
	}
	if observer.calls != 1 {
		t.Fatalf("current event-log reconciliation calls = %d, want 1", observer.calls)
	}
	after := eventLogFingerprint(t, path)
	if string(after.contents) != string(before.contents) ||
		after.size != before.size ||
		!after.modTime.Equal(before.modTime) {
		t.Fatalf("current event log changed during materialization: before=%+v after=%+v", before, after)
	}
	if _, err := os.Stat(filepath.Join(sessionDir, eventLogMigrationWorkspaceDir)); !os.IsNotExist(err) {
		t.Fatalf("current materialization created migration workspace: %v", err)
	}
	window, err := capability.ReadRecentRecords(1)
	if err != nil {
		t.Fatalf("read current capability: %v", err)
	}
	assertCurrentRecordSequences(t, window.Records, 1)
}

func TestMaterializedEventLogAppendsTypedRecord(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	path := filepath.Join(sessionDir, eventsFile)
	if _, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	}); err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	now := time.Now().UTC()
	observer := &eventLogReconciliationTestObserver{}
	store, err := OpenResolved(PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta: &Meta{
			SessionID: "session-1",
			CreatedAt: now,
			UpdatedAt: now,
		},
	}, WithPersistenceObserver(eventLogReconciliationObserverAdapter{observer}))
	if err != nil {
		t.Fatalf("open metadata-bound store: %v", err)
	}
	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize current event log: %v", err)
	}
	content := "typed append"
	record, receipt, err := capability.AppendRecord(
		stringPointer("step-1"),
		MessageRecord{Role: MessageRoleUser, Content: &content},
	)
	if err != nil {
		t.Fatalf("append typed record: %v", err)
	}
	if !receipt.Committed || record.Seq() != 1 || mustEventRecordKind(record) != EventKindMessage {
		t.Fatalf("typed append result = record %#v receipt %+v", record, receipt)
	}
	if mustMaterializedRevision(capability) != 1 {
		t.Fatalf("capability revision = %d, want 1", mustMaterializedRevision(capability))
	}
	window, err := capability.ReadRecentRecords(1)
	if err != nil {
		t.Fatalf("read appended typed record: %v", err)
	}
	assertCurrentRecordSequences(t, window.Records, 1)
}

func TestMaterializedEventLogAppendsTypedBatchAtomically(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	path := filepath.Join(sessionDir, eventsFile)
	if _, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	}); err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	now := time.Now().UTC()
	store, err := OpenResolved(PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta: &Meta{
			SessionID: "session-1",
			CreatedAt: now,
			UpdatedAt: now,
		},
	}, WithPersistenceObserver(eventLogReconciliationObserverAdapter{
		&eventLogReconciliationTestObserver{},
	}))
	if err != nil {
		t.Fatalf("open metadata-bound store: %v", err)
	}
	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize current event log: %v", err)
	}
	userContent := "first"
	assistantContent := "second"
	records, receipt, err := capability.AppendRecordsAtomic(
		stringPointer("step-1"),
		[]EventRecordPayload{
			MessageRecord{Role: MessageRoleUser, Content: &userContent},
			MessageRecord{Role: MessageRoleAssistant, Content: &assistantContent},
		},
	)
	if err != nil {
		t.Fatalf("append typed batch: %v", err)
	}
	if !receipt.Committed || len(records) != 2 ||
		records[0].Seq() != 1 || records[1].Seq() != 2 {
		t.Fatalf("typed batch result = records %#v receipt %+v", records, receipt)
	}
	window, err := capability.ReadRecentRecords(2)
	if err != nil {
		t.Fatalf("read typed batch: %v", err)
	}
	assertCurrentRecordSequences(t, window.Records, 1, 2)
}

func TestMaterializedEventLogAppendReturnsTypedEndByteCursor(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	path := filepath.Join(sessionDir, eventsFile)
	if _, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	}); err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	now := time.Now().UTC()
	store, err := OpenResolved(PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta: &Meta{
			SessionID: "session-1",
			CreatedAt: now,
			UpdatedAt: now,
		},
	}, WithPersistenceObserver(eventLogReconciliationObserverAdapter{
		&eventLogReconciliationTestObserver{},
	}))
	if err != nil {
		t.Fatalf("open metadata-bound store: %v", err)
	}
	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize current event log: %v", err)
	}
	content := "cursor append"
	result, err := capability.AppendRecordWithEndByteCursor(
		stringPointer("step-1"),
		MessageRecord{Role: MessageRoleUser, Content: &content},
	)
	if err != nil {
		t.Fatalf("append typed record with cursor: %v", err)
	}
	if !result.Committed ||
		result.Record.Seq() != 1 ||
		result.EndByteCursor == nil ||
		*result.EndByteCursor <= 0 {
		t.Fatalf("typed cursor append result = %+v", result)
	}
	window, err := capability.ReadSegmentBackward(*result.EndByteCursor, nil)
	if err != nil {
		t.Fatalf("read typed cursor append: %v", err)
	}
	assertCurrentRecordSequences(t, window.Records, 1)
}

func TestMaterializedEventLogReplaysTypedRecordsWithChildSequences(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	path := filepath.Join(sessionDir, eventsFile)
	if _, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	}); err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	now := time.Now().UTC()
	store, err := OpenResolved(PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta: &Meta{
			SessionID: "session-1",
			CreatedAt: now,
			UpdatedAt: now,
		},
	}, WithPersistenceObserver(eventLogReconciliationObserverAdapter{
		&eventLogReconciliationTestObserver{},
	}))
	if err != nil {
		t.Fatalf("open metadata-bound store: %v", err)
	}
	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize current event log: %v", err)
	}
	firstContent := "first"
	secondContent := "second"
	first, err := NewEventRecord(7, stringPointer("step-1"), MessageRecord{
		Role: MessageRoleUser, Content: &firstContent,
	})
	if err != nil {
		t.Fatalf("create first replay record: %v", err)
	}
	second, err := NewEventRecord(11, stringPointer("step-2"), MessageRecord{
		Role: MessageRoleAssistant, Content: &secondContent,
	})
	if err != nil {
		t.Fatalf("create second replay record: %v", err)
	}

	replayed, err := capability.AppendReplayRecords([]EventRecord{first, second})
	if err != nil {
		t.Fatalf("append typed replay records: %v", err)
	}
	if len(replayed) != 2 || replayed[0].Seq() != 1 || replayed[1].Seq() != 2 ||
		replayed[0].StepID() == nil || *replayed[0].StepID() != "step-1" ||
		replayed[1].StepID() == nil || *replayed[1].StepID() != "step-2" {
		t.Fatalf("typed replay result = %#v", replayed)
	}
}

func TestMaterializedEventLogWalksTypedRecords(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	path := filepath.Join(sessionDir, eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{
		currentTestMessageRecord(t, 1, "first"),
		currentTestMessageRecord(t, 2, "second"),
	}); err != nil {
		t.Fatalf("append current records: %v", err)
	}
	now := time.Now().UTC()
	store, err := OpenResolved(PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta: &Meta{
			SessionID:               "session-1",
			CreatedAt:               now,
			UpdatedAt:               now,
			LastSequence:            2,
			ConversationEstablished: true,
		},
	}, WithPersistenceObserver(eventLogReconciliationObserverAdapter{
		&eventLogReconciliationTestObserver{},
	}))
	if err != nil {
		t.Fatalf("open metadata-bound store: %v", err)
	}
	capability, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize current event log: %v", err)
	}

	var records []EventRecord
	if err := capability.WalkRecords(func(record EventRecord) error {
		records = append(records, record)
		return nil
	}); err != nil {
		t.Fatalf("walk typed records: %v", err)
	}
	assertCurrentRecordSequences(t, records, 1, 2)
}

func TestMaterializedEventLogFindsTerminalAssistantForPendingRecoveryStep(
	t *testing.T,
) {
	store := newSessionTestStore(t)
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	stepID := "step-recovery"
	content := "done"
	finalPhase := MessagePhaseFinal
	if _, _, err := eventLog.AppendRecord(&stepID, MessageRecord{
		Role:    MessageRoleAssistant,
		Content: &content,
		Phase:   &finalPhase,
	}); err != nil {
		t.Fatalf("append terminal assistant: %v", err)
	}

	found, err := eventLog.PendingRecoveryStepHasTerminalAssistant(stepID)
	if err != nil {
		t.Fatalf("find terminal assistant: %v", err)
	}
	if !found {
		t.Fatal("terminal assistant was not found")
	}
}

func TestRemoveDurableInvalidatesMaterializedEventLogCapability(t *testing.T) {
	store := newSessionTestStore(t)
	eventLog := mustMaterializeSessionTestEventLog(t, store)

	if err := store.RemoveDurable(); err != nil {
		t.Fatalf("RemoveDurable: %v", err)
	}
	if err := eventLog.ValidateOwner(store); err == nil {
		t.Fatal("removed Store retained a valid materialized event-log capability")
	}
}

func TestFilelessCurrentMaterializationIsReadOnlyAndLeaseOwnsNoArtifact(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	path := filepath.Join(sessionDir, eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{
		currentTestMessageRecord(t, 1, "hello"),
	}); err != nil {
		t.Fatalf("append current event record: %v", err)
	}
	appendCurrentTestBytes(t, path, []byte(`{"seq":2,"kind":"message"`))
	before := eventLogFingerprint(t, path)
	now := time.Now().UTC()
	store, err := OpenResolved(
		PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta: &Meta{
				SessionID:               "session-1",
				CreatedAt:               now,
				UpdatedAt:               now,
				LastSequence:            1,
				ConversationEstablished: true,
			},
		},
		WithFilelessEventPersistence(),
	)
	if err != nil {
		t.Fatalf("open fileless metadata-bound store: %v", err)
	}

	capability, lease, err := store.MaterializeFilelessEventLog(context.Background())
	if err != nil {
		t.Fatalf("materialize fileless current event log: %v", err)
	}
	if mustMaterializedRevision(capability) != 1 {
		t.Fatalf("fileless capability revision = %d, want 1", mustMaterializedRevision(capability))
	}
	window, err := capability.ReadRecentRecords(1)
	if err != nil {
		t.Fatalf("read fileless current capability: %v", err)
	}
	assertCurrentRecordSequences(t, window.Records, 1)
	if err := lease.Close(); err != nil {
		t.Fatalf("close no-artifact lease: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("close no-artifact lease twice: %v", err)
	}
	after := eventLogFingerprint(t, path)
	if !before.equal(after) {
		t.Fatalf("fileless current materialization changed source: before=%+v after=%+v", before, after)
	}
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatalf("read session directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != eventsFile {
		t.Fatalf("fileless current materialization created artifacts: %+v", entries)
	}
}

func TestFilelessLegacyMaterializationTransformsWithoutMutatingSource(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	path := filepath.Join(sessionDir, eventsFile)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-01-01T00:00:00Z","kind":"message","payload":{"role":"user","content":"legacy"}}` + "\n",
	)
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatalf("write legacy event log: %v", err)
	}
	before := eventLogFingerprint(t, path)
	now := time.Now().UTC()
	store, err := OpenResolved(
		PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta: &Meta{
				SessionID:               "session-1",
				CreatedAt:               now,
				UpdatedAt:               now,
				LastSequence:            1,
				ConversationEstablished: true,
			},
		},
		WithFilelessEventPersistence(),
	)
	if err != nil {
		t.Fatalf("open fileless metadata-bound store: %v", err)
	}

	capability, lease, err := store.MaterializeFilelessEventLog(context.Background())
	if err != nil {
		t.Fatalf("materialize fileless legacy event log: %v", err)
	}
	window, err := capability.ReadRecentRecords(1)
	if err != nil {
		t.Fatalf("read transformed legacy capability: %v", err)
	}
	assertCurrentRecordSequences(t, window.Records, 1)
	artifactInfo, err := os.Stat(lease.log.path)
	if err != nil {
		t.Fatalf("stat transformed event-log artifact: %v", err)
	}
	if got := artifactInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("transformed event-log permissions = %o, want 600", got)
	}
	rootInfo, err := os.Stat(lease.artifactRoot)
	if err != nil {
		t.Fatalf("stat transformed event-log artifact root: %v", err)
	}
	if got := rootInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("transformed artifact-root permissions = %o, want 700", got)
	}
	_, repeatedLease, err := store.MaterializeFilelessEventLog(context.Background())
	if err != nil {
		t.Fatalf("repeat fileless legacy materialization: %v", err)
	}
	if repeatedLease != lease {
		t.Fatal("repeat fileless legacy materialization returned a second artifact owner")
	}
	after := eventLogFingerprint(t, path)
	if !before.equal(after) {
		t.Fatalf("fileless legacy materialization changed source: before=%+v after=%+v", before, after)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("close legacy artifact lease: %v", err)
	}
	if _, err := os.Stat(lease.artifactRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy artifact remains after lease close: %v", err)
	}
	if err := capability.ValidateOwner(store); err == nil {
		t.Fatal("closed legacy artifact lease retained a valid capability")
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("close legacy artifact lease twice: %v", err)
	}
}

func TestFilelessLegacyMaterializationUsesTransformedRevisionWithoutMutatingSource(t *testing.T) {
	tests := []struct {
		name         string
		legacy       string
		metaRevision int64
		wantRevision int64
	}{
		{
			name: "sequence regression is normalized",
			legacy: strings.Join([]string{
				`{"seq":2,"timestamp":"2026-01-01T00:00:00Z","kind":"message","payload":{"role":"user","content":"first"}}`,
				`{"seq":1,"timestamp":"2026-01-01T00:00:01Z","kind":"message","payload":{"role":"assistant","content":"second"}}`,
				"",
			}, "\n"),
			metaRevision: 1,
			wantRevision: 3,
		},
		{
			name: "trailing readerless record is dropped",
			legacy: strings.Join([]string{
				`{"seq":1,"timestamp":"2026-01-01T00:00:00Z","kind":"message","payload":{"role":"user","content":"kept"}}`,
				`{"seq":2,"timestamp":"2026-01-01T00:00:01Z","kind":"run_finished","payload":{}}`,
				"",
			}, "\n"),
			metaRevision: 2,
			wantRevision: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sessionDir := filepath.Join(t.TempDir(), "session-1")
			if err := os.MkdirAll(sessionDir, 0o755); err != nil {
				t.Fatalf("create session directory: %v", err)
			}
			path := filepath.Join(sessionDir, eventsFile)
			if err := os.WriteFile(path, []byte(test.legacy), 0o644); err != nil {
				t.Fatalf("write legacy event log: %v", err)
			}
			before := eventLogFingerprint(t, path)
			now := time.Now().UTC()
			store, err := OpenResolved(
				PersistedSessionRecord{
					SessionDir: sessionDir,
					Meta: &Meta{
						SessionID:    "session-1",
						CreatedAt:    now,
						UpdatedAt:    now,
						LastSequence: test.metaRevision,
					},
				},
				WithFilelessEventPersistence(),
			)
			if err != nil {
				t.Fatalf("open fileless metadata-bound store: %v", err)
			}

			capability, lease, err := store.MaterializeFilelessEventLog(context.Background())
			if err != nil {
				t.Fatalf("materialize fileless legacy event log: %v", err)
			}
			artifactRoot := lease.artifactRoot
			if got := mustMaterializedRevision(capability); got != test.wantRevision {
				t.Fatalf("transformed revision = %d, want %d", got, test.wantRevision)
			}
			if err := lease.Close(); err != nil {
				t.Fatalf("close fileless artifact lease: %v", err)
			}
			if _, err := os.Stat(artifactRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("fileless artifact remains after close: %v", err)
			}
			after := eventLogFingerprint(t, path)
			if !before.equal(after) {
				t.Fatalf("fileless legacy materialization changed source: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestFilelessLegacyMaterializationHonorsCancellationWithoutMutatingSource(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	path := filepath.Join(sessionDir, eventsFile)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-01-01T00:00:00Z","kind":"message","payload":{"role":"user","content":"legacy"}}` + "\n",
	)
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatalf("write legacy event log: %v", err)
	}
	before := eventLogFingerprint(t, path)
	now := time.Now().UTC()
	store, err := OpenResolved(
		PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta: &Meta{
				SessionID:    "session-1",
				CreatedAt:    now,
				UpdatedAt:    now,
				LastSequence: 1,
			},
		},
		WithFilelessEventPersistence(),
	)
	if err != nil {
		t.Fatalf("open fileless metadata-bound store: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, lease, err := store.MaterializeFilelessEventLog(ctx); !errors.Is(err, context.Canceled) {
		if lease != nil {
			_ = lease.Close()
		}
		t.Fatalf("materialization error = %v, want context canceled", err)
	}
	after := eventLogFingerprint(t, path)
	if !before.equal(after) {
		t.Fatalf("canceled fileless materialization changed source: before=%+v after=%+v", before, after)
	}
}

func TestFilelessLegacyMaterializationCleansArtifactsAfterFailure(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	path := filepath.Join(sessionDir, eventsFile)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-01-01T00:00:00Z","kind":"message","payload":not-json}` + "\n",
	)
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatalf("write malformed legacy event log: %v", err)
	}
	before := eventLogFingerprint(t, path)
	now := time.Now().UTC()
	store, err := OpenResolved(
		PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta: &Meta{
				SessionID:    "session-1",
				CreatedAt:    now,
				UpdatedAt:    now,
				LastSequence: 1,
			},
		},
		WithFilelessEventPersistence(),
	)
	if err != nil {
		t.Fatalf("open fileless metadata-bound store: %v", err)
	}

	if _, lease, err := store.MaterializeFilelessEventLog(context.Background()); err == nil {
		if lease != nil {
			_ = lease.Close()
		}
		t.Fatal("malformed legacy materialization succeeded")
	}
	after := eventLogFingerprint(t, path)
	if !before.equal(after) {
		t.Fatalf("failed fileless materialization changed source: before=%+v after=%+v", before, after)
	}
	if err := (MaterializedEventLog{store: store}).ValidateOwner(store); err == nil {
		t.Fatal("failed fileless materialization retained a capability")
	}
}

func TestFilelessLegacyArtifactLeaseRetriesCleanupFailure(t *testing.T) {
	store := newSessionTestStore(t)
	log := &currentEventLog{path: filepath.Join(t.TempDir(), eventsFile)}
	store.mu.Lock()
	store.materializedEventLog = log
	store.mu.Unlock()
	cleanupErr := errors.New("cleanup failed")
	calls := 0
	lease := &EventLogArtifactLease{
		store:        store,
		log:          log,
		artifactRoot: t.TempDir(),
		remove: func(string) error {
			calls++
			if calls == 1 {
				return cleanupErr
			}
			return nil
		},
	}

	if err := lease.Close(); !errors.Is(err, cleanupErr) {
		t.Fatalf("first lease close error = %v, want %v", err, cleanupErr)
	}
	if err := (MaterializedEventLog{store: store, log: log}).ValidateOwner(store); err != nil {
		t.Fatalf("failed cleanup invalidated capability: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("retry lease close: %v", err)
	}
	if calls != 2 {
		t.Fatalf("cleanup calls = %d, want 2", calls)
	}
	if err := (MaterializedEventLog{store: store, log: log}).ValidateOwner(store); err == nil {
		t.Fatal("successful cleanup retained capability")
	}
}

func TestFilelessLegacyArtifactLeaseSerializesCleanupAndRematerialization(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	path := filepath.Join(sessionDir, eventsFile)
	legacy := []byte(
		`{"seq":1,"timestamp":"2026-01-01T00:00:00Z","kind":"message","payload":{"role":"user","content":"legacy"}}` + "\n",
	)
	if err := os.WriteFile(path, legacy, 0o644); err != nil {
		t.Fatalf("write legacy event log: %v", err)
	}
	now := time.Now().UTC()
	store, err := OpenResolved(
		PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta: &Meta{
				SessionID:    "session-1",
				CreatedAt:    now,
				UpdatedAt:    now,
				LastSequence: 1,
			},
		},
		WithFilelessEventPersistence(),
	)
	if err != nil {
		t.Fatalf("open fileless metadata-bound store: %v", err)
	}
	capability, lease, err := store.MaterializeFilelessEventLog(context.Background())
	if err != nil {
		t.Fatalf("materialize fileless legacy event log: %v", err)
	}
	originalRemove := lease.remove
	removeStarted := make(chan struct{})
	allowRemove := make(chan struct{})
	lease.remove = func(root string) error {
		close(removeStarted)
		<-allowRemove
		return originalRemove(root)
	}
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- lease.Close()
	}()
	<-removeStarted

	type materializationResult struct {
		lease *EventLogArtifactLease
		err   error
	}
	materializeStarted := make(chan struct{})
	materializeDone := make(chan materializationResult, 1)
	go func() {
		close(materializeStarted)
		_, nextLease, materializeErr := store.MaterializeFilelessEventLog(
			context.Background(),
		)
		materializeDone <- materializationResult{
			lease: nextLease,
			err:   materializeErr,
		}
	}()
	<-materializeStarted
	validateStarted := make(chan struct{})
	validateDone := make(chan error, 1)
	go func() {
		close(validateStarted)
		validateDone <- capability.ValidateOwner(store)
	}()
	<-validateStarted
	select {
	case result := <-materializeDone:
		t.Fatalf(
			"rematerialization completed while prior artifact cleanup was active: %+v",
			result,
		)
	case <-time.After(100 * time.Millisecond):
	}
	select {
	case validateErr := <-validateDone:
		t.Fatalf(
			"capability validation completed while artifact cleanup was active: %v",
			validateErr,
		)
	case <-time.After(100 * time.Millisecond):
	}

	close(allowRemove)
	if err := <-closeDone; err != nil {
		t.Fatalf("close legacy artifact lease: %v", err)
	}
	result := <-materializeDone
	if result.err != nil {
		t.Fatalf("rematerialize after cleanup: %v", result.err)
	}
	if result.lease == nil || result.lease == lease {
		t.Fatal("rematerialization did not issue a new artifact lease")
	}
	if validateErr := <-validateDone; validateErr == nil {
		t.Fatal("capability validation succeeded after its artifact lease closed")
	}
	if err := result.lease.Close(); err != nil {
		t.Fatalf("close rematerialized artifact lease: %v", err)
	}
}
