package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenResolvedDoesNotReadOrMutateEventLog(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	eventsPath := filepath.Join(sessionDir, eventsFile)
	if err := os.WriteFile(eventsPath, []byte("legacy bytes without a valid event\n"), 0o644); err != nil {
		t.Fatalf("write opaque legacy event log: %v", err)
	}
	fixedTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(eventsPath, fixedTime, fixedTime); err != nil {
		t.Fatalf("set event log timestamps: %v", err)
	}
	before := eventLogFingerprint(t, eventsPath)

	now := time.Now().UTC()
	store, err := OpenResolved(PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta: &Meta{
			SessionID: "session-1",
			CreatedAt: now,
			UpdatedAt: now,
		},
	})
	if err != nil {
		t.Fatalf("open metadata-bound store: %v", err)
	}
	if store.Metadata().SessionID != "session-1" {
		t.Fatalf("opened session identity = %q", store.Metadata().SessionID)
	}
	after := eventLogFingerprint(t, eventsPath)
	if !before.equal(after) {
		t.Fatalf("metadata-only open changed event log: before=%+v after=%+v", before, after)
	}
}

func TestMetadataMutationsLeaveOpaqueEventLogUnchanged(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	eventsPath := filepath.Join(sessionDir, eventsFile)
	if err := os.WriteFile(eventsPath, []byte("opaque legacy event bytes\n"), 0o644); err != nil {
		t.Fatalf("write opaque legacy event log: %v", err)
	}
	fixedTime := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(eventsPath, fixedTime, fixedTime); err != nil {
		t.Fatalf("set event log timestamps: %v", err)
	}
	before := eventLogFingerprint(t, eventsPath)
	now := time.Now().UTC()
	observer := &recordingPersistenceObserver{}
	store, err := OpenResolved(
		PersistedSessionRecord{
			SessionDir: sessionDir,
			Meta: &Meta{
				SessionID: "session-1",
				CreatedAt: now,
				UpdatedAt: now,
			},
		},
		WithPersistenceObserver(observer),
	)
	if err != nil {
		t.Fatalf("open metadata-bound store: %v", err)
	}

	if err := store.SetInputDraft("draft"); err != nil {
		t.Fatalf("set input draft: %v", err)
	}
	if _, err := store.SetGoal("ship metadata boundary", GoalActorUser); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if err := store.SetPendingModelRecovery(PendingModelRecovery{
		RecoveryID: "recovery-1",
		Reason:     "provider_disconnect",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}

	after := eventLogFingerprint(t, eventsPath)
	if !before.equal(after) {
		t.Fatalf("metadata mutations changed event log: before=%+v after=%+v", before, after)
	}
}

func TestMetadataProjectionOmitsEventRevisionAndFreshness(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	content := "establish conversation"
	if _, _, err := log.AppendRecord(stringPointer("step-1"), MessageRecord{
		Role: MessageRoleUser, Content: &content,
	}); err != nil {
		t.Fatalf("append event: %v", err)
	}

	metadata := store.Metadata()
	if metadata.SessionID != store.Metadata().SessionID {
		t.Fatalf("metadata session identity = %q, want %q", metadata.SessionID, store.Metadata().SessionID)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("encode metadata projection: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("decode metadata projection: %v", err)
	}
	if _, ok := fields["last_sequence"]; ok {
		t.Fatalf("metadata projection exposed event revision: %s", encoded)
	}
	if _, ok := fields["conversation_established"]; ok {
		t.Fatalf("metadata projection exposed conversation freshness: %s", encoded)
	}
}

func TestCurrentMetadataOperationsLeaveEventLogUnchanged(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	eventsPath := filepath.Join(sessionDir, eventsFile)
	log, err := createCurrentEventLog(eventsPath, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{
		currentTestMessageRecord(t, 1, "established"),
	}); err != nil {
		t.Fatalf("append current event record: %v", err)
	}
	before := eventLogFingerprint(t, eventsPath)
	now := time.Now().UTC()
	observer := &recordingPersistenceObserver{}
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
		WithPersistenceObserver(observer),
	)
	if err != nil {
		t.Fatalf("open current metadata-bound store: %v", err)
	}
	beforeRevision := storeTestMeta(store).LastSequence
	if store.materializedEventLog != nil {
		t.Fatal("metadata-bound store materialized its event log during open")
	}

	_ = store.Metadata()
	if err := store.SetInputDraft("draft"); err != nil {
		t.Fatalf("set input draft: %v", err)
	}
	role := "fast"
	if err := store.SetContinuationContext(ContinuationContext{AgentRole: &role}); err != nil {
		t.Fatalf("set continuation role: %v", err)
	}
	if err := store.MarkModelDispatchLocked(sessionTestLockedContract()); err != nil {
		t.Fatalf("set locked contract: %v", err)
	}
	if _, err := store.SetGoal("metadata-only goal", GoalActorUser); err != nil {
		t.Fatalf("set metadata-only goal: %v", err)
	}
	if _, err := store.SetGoalStatus(GoalStatusPaused, GoalActorUser); err != nil {
		t.Fatalf("pause metadata-only goal: %v", err)
	}
	if _, err := store.ClearGoal(GoalActorUser); err != nil {
		t.Fatalf("clear metadata-only goal: %v", err)
	}
	if err := store.SetPendingModelRecovery(PendingModelRecovery{
		RecoveryID: "recovery-1",
		Reason:     "provider_disconnect",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("set pending recovery: %v", err)
	}
	if err := store.ClearPendingModelRecovery(); err != nil {
		t.Fatalf("clear pending recovery: %v", err)
	}

	after := eventLogFingerprint(t, eventsPath)
	if !before.equal(after) {
		t.Fatalf("current metadata operations changed event log: before=%+v after=%+v", before, after)
	}
	if store.materializedEventLog != nil {
		t.Fatal("metadata-only goal/recovery mutations materialized the event log")
	}
	if got := storeTestMeta(store).LastSequence; got != beforeRevision {
		t.Fatalf(
			"metadata-only goal/recovery mutations changed event revision: got %d want %d",
			got,
			beforeRevision,
		)
	}
}

type eventLogFileFingerprint struct {
	contents []byte
	size     int64
	modTime  time.Time
}

func eventLogFingerprint(t *testing.T, path string) eventLogFileFingerprint {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read event log fingerprint: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat event log fingerprint: %v", err)
	}
	return eventLogFileFingerprint{
		contents: contents,
		size:     info.Size(),
		modTime:  info.ModTime(),
	}
}

func (f eventLogFileFingerprint) equal(other eventLogFileFingerprint) bool {
	return bytes.Equal(f.contents, other.contents) &&
		f.size == other.size &&
		f.modTime.Equal(other.modTime)
}
