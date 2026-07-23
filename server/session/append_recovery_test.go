package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverEventIntentCommitsDurablyAppendedSuffix(t *testing.T) {
	store, event := appendRecoveryFixture(t, appendRecoveryEventIntent)
	reopened := mustOpenSessionTestStore(t, store)
	if got := storeTestMeta(reopened).LastSequence; got != event.Seq() {
		t.Fatalf("recovered last sequence = %d, want %d", got, event.Seq())
	}
	if got := mustMaterializedRevision(mustMaterializeSessionTestEventLog(t, reopened)); got != event.Seq() {
		t.Fatalf("recovered event-log revision = %d, want %d", got, event.Seq())
	}
	assertNoActiveAppendRecovery(t, reopened)
}

func TestRecoverLegacyPreparedSuffixRollsBack(t *testing.T) {
	store, _ := appendRecoveryFixture(t, appendRecoveryPrepared)
	reopened := mustOpenSessionTestStore(t, store)
	if got := storeTestMeta(reopened).LastSequence; got != 0 {
		t.Fatalf("recovered last sequence = %d, want 0", got)
	}
	if got := mustMaterializedRevision(mustMaterializeSessionTestEventLog(t, reopened)); got != 0 {
		t.Fatalf("recovered event-log revision = %d, want 0", got)
	}
	assertNoActiveAppendRecovery(t, reopened)
}

func TestAcknowledgedRecoveryWitnessDoesNotReplayBeforeNextMutation(t *testing.T) {
	observer := &recordingPersistenceObserver{}
	store, err := Create(
		t.TempDir(),
		"workspace",
		t.TempDir(),
		testSessionCategory,
		WithPersistenceObserver(observer),
	)
	if err != nil {
		t.Fatalf("create observed store: %v", err)
	}
	log := mustMaterializeSessionTestEventLog(t, store)
	if _, _, err := log.AppendRecord(nil, sessionTestMessage(MessageRoleAssistant, "persisted")); err != nil {
		t.Fatalf("append event: %v", err)
	}
	afterAppend := observer.callCount
	assertNoActiveAppendRecovery(t, store)

	if err := store.SetName("renamed"); err != nil {
		t.Fatalf("mutate after acknowledged append: %v", err)
	}
	if got := observer.callCount; got != afterAppend+1 {
		t.Fatalf("observer calls after next mutation = %d, want %d without replay", got, afterAppend+1)
	}
	if _, _, err := log.AppendRecord(nil, sessionTestMessage(MessageRoleAssistant, "reused")); err != nil {
		t.Fatalf("append through acknowledged recovery file: %v", err)
	}
	assertNoActiveAppendRecovery(t, store)
}

func appendRecoveryFixture(t *testing.T, phase appendRecoveryPhase) (*Store, EventRecord) {
	t.Helper()
	store := newSessionTestStore(t)
	eventLog := mustMaterializeSessionTestEventLog(t, store)
	event, err := NewEventRecord(1, nil, sessionTestMessage(MessageRoleAssistant, "durable"))
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	payload, err := encodeCurrentEventRecordLines([]EventRecord{event}, false)
	if err != nil {
		t.Fatalf("encode event: %v", err)
	}
	startOffset := eventLog.log.firstEventOffset
	if _, err := eventLog.log.appendRecords([]EventRecord{event}); err != nil {
		t.Fatalf("append durable event without observer: %v", err)
	}

	preMeta := cloneMeta(store.meta)
	postMeta := cloneMeta(preMeta)
	postMeta.LastSequence = event.Seq()
	postMeta.UpdatedAt = store.options.now()
	recovery, err := store.newAppendRecoveryRecord(
		preMeta,
		postMeta,
		phase,
		&appendRecoveryEvents{
			StartOffset:   startOffset,
			EndOffset:     startOffset + int64(len(payload)),
			EventCount:    1,
			FirstSequence: event.Seq(),
			LastSequence:  event.Seq(),
			SHA256:        digestBytes(payload),
		},
	)
	if err != nil {
		t.Fatalf("create event intent recovery: %v", err)
	}
	if err := store.writeAppendRecoveryRecord(recovery); err != nil {
		t.Fatalf("write recovery: %v", err)
	}
	return store, event
}

func assertNoActiveAppendRecovery(t *testing.T, store *Store) {
	t.Helper()
	record, err := store.readAppendRecoveryRecord()
	if err != nil {
		t.Fatalf("read acknowledged recovery: %v", err)
	}
	if record != nil {
		t.Fatalf("acknowledged recovery = %+v, want absent", record)
	}
	info, err := os.Stat(filepath.Join(store.Dir(), appendRecoveryFile))
	if err != nil {
		t.Fatalf("acknowledged recovery artifact: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("acknowledged recovery artifact size = %d, want 0", info.Size())
	}
}
