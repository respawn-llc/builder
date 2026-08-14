package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNilIrreconcilableRecoveryDetailStillIdentifiesRecoveryRequirement(t *testing.T) {
	var detail *IrreconcilableRecoveryDetail
	if !errors.Is(detail, ErrStoreRecoveryRequired) {
		t.Fatal("typed-nil irreconcilable recovery detail did not identify ErrStoreRecoveryRequired")
	}
}

func TestOpenResolvedReportsIrreconcilableMetadataRecovery(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	eventsPath := filepath.Join(sessionDir, eventsFile)
	if _, err := createCurrentEventLog(eventsPath); err != nil {
		t.Fatalf("create current event log: %v", err)
	}

	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	preMeta := recoveryDetailTestMeta(now)
	postMeta := cloneMeta(preMeta)
	postMeta.InputDraft = "planned"
	postMeta.UpdatedAt = now.Add(time.Second)
	currentMeta := cloneMeta(preMeta)
	currentMeta.InputDraft = "runtime"
	currentMeta.UpdatedAt = now.Add(2 * time.Second)

	writer := &Store{sessionDir: sessionDir, meta: preMeta}
	recovery, err := writer.newAppendRecoveryRecord(
		preMeta,
		postMeta,
		appendRecoveryCommitted,
		nil,
	)
	if err != nil {
		t.Fatalf("create recovery record: %v", err)
	}
	if err := writer.writeAppendRecoveryRecord(recovery); err != nil {
		t.Fatalf("write recovery record: %v", err)
	}

	recoveryPath := filepath.Join(sessionDir, appendRecoveryFile)
	beforeArtifacts := captureRecoveryDetailArtifacts(t, recoveryPath, eventsPath, currentMeta)

	_, err = OpenResolved(PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta:       &currentMeta,
	})
	if err == nil {
		t.Fatal("OpenResolved succeeded with irreconcilable metadata recovery")
	}
	if !errors.Is(err, ErrStoreRecoveryRequired) {
		t.Fatalf("OpenResolved error = %v, want ErrStoreRecoveryRequired", err)
	}
	var detail *IrreconcilableRecoveryDetail
	if !errors.As(err, &detail) {
		t.Fatalf("OpenResolved error = %T %v, want IrreconcilableRecoveryDetail", err, err)
	}
	currentDigest, err := digestMeta(currentMeta)
	if err != nil {
		t.Fatalf("digest current metadata: %v", err)
	}
	if detail.SessionID != currentMeta.SessionID ||
		detail.Operation != "validate metadata state" ||
		detail.RecoveryPath != recoveryPath ||
		detail.EventsPath != eventsPath ||
		detail.CurrentMetadataSHA256 != currentDigest ||
		detail.PreMetadataSHA256 != recovery.Pre.SHA256 ||
		detail.PostMetadataSHA256 != recovery.Post.SHA256 ||
		detail.Phase != string(appendRecoveryCommitted) ||
		detail.Conflict != IrreconcilableRecoveryConflictMetadataState ||
		detail.Suffix != nil {
		t.Fatalf("irreconcilable recovery detail = %+v", detail)
	}

	beforeArtifacts.assertUnchanged(t, recoveryPath, eventsPath, currentMeta)
}

func TestOpenResolvedReportsIrreconcilableCommittedSuffixRecovery(t *testing.T) {
	sessionDir := filepath.Join(t.TempDir(), "session-1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("create session directory: %v", err)
	}
	eventsPath := filepath.Join(sessionDir, eventsFile)
	log, err := createCurrentEventLog(eventsPath)
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	endOffset, err := log.appendRecords([]EventRecord{
		currentTestMessageRecord(t, 1, "committed"),
	})
	if err != nil {
		t.Fatalf("append current event record: %v", err)
	}

	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	preMeta := recoveryDetailTestMeta(now)
	postMeta := cloneMeta(preMeta)
	postMeta.LastSequence = 1
	postMeta.ConversationEstablished = true
	postMeta.UpdatedAt = now.Add(time.Second)
	suffix := &appendRecoveryEvents{
		StartOffset:   log.firstEventOffset,
		EndOffset:     endOffset,
		EventCount:    1,
		FirstSequence: 1,
		LastSequence:  1,
		SHA256:        digestBytes([]byte("different committed suffix")),
	}
	writer := &Store{sessionDir: sessionDir, meta: preMeta}
	recovery, err := writer.newAppendRecoveryRecord(
		preMeta,
		postMeta,
		appendRecoveryCommitted,
		suffix,
	)
	if err != nil {
		t.Fatalf("create recovery record: %v", err)
	}
	if err := writer.writeAppendRecoveryRecord(recovery); err != nil {
		t.Fatalf("write recovery record: %v", err)
	}

	recoveryPath := filepath.Join(sessionDir, appendRecoveryFile)
	beforeArtifacts := captureRecoveryDetailArtifacts(t, recoveryPath, eventsPath, postMeta)

	_, err = OpenResolved(PersistedSessionRecord{
		SessionDir: sessionDir,
		Meta:       &postMeta,
	})
	if err == nil {
		t.Fatal("OpenResolved succeeded with irreconcilable committed suffix recovery")
	}
	if !errors.Is(err, ErrStoreRecoveryRequired) {
		t.Fatalf("OpenResolved error = %v, want ErrStoreRecoveryRequired", err)
	}
	var detail *IrreconcilableRecoveryDetail
	if !errors.As(err, &detail) {
		t.Fatalf("OpenResolved error = %T %v, want IrreconcilableRecoveryDetail", err, err)
	}
	currentDigest, err := digestMeta(postMeta)
	if err != nil {
		t.Fatalf("digest current metadata: %v", err)
	}
	if detail.SessionID != postMeta.SessionID ||
		detail.Operation != "inspect event suffix" ||
		detail.RecoveryPath != recoveryPath ||
		detail.EventsPath != eventsPath ||
		detail.CurrentMetadataSHA256 != currentDigest ||
		detail.PreMetadataSHA256 != recovery.Pre.SHA256 ||
		detail.PostMetadataSHA256 != recovery.Post.SHA256 ||
		detail.Phase != string(appendRecoveryCommitted) ||
		detail.Conflict != IrreconcilableRecoveryConflictCommittedSuffix ||
		detail.Suffix == nil ||
		detail.Suffix.StartOffset != suffix.StartOffset ||
		detail.Suffix.EndOffset != suffix.EndOffset ||
		detail.Suffix.EventCount != suffix.EventCount ||
		detail.Suffix.FirstSequence != suffix.FirstSequence ||
		detail.Suffix.LastSequence != suffix.LastSequence ||
		detail.Suffix.SHA256 != suffix.SHA256 {
		t.Fatalf("irreconcilable recovery detail = %+v", detail)
	}

	beforeArtifacts.assertUnchanged(t, recoveryPath, eventsPath, postMeta)
}

func TestOpenResolvedRecoversCommittedLegacyAppendBeforeMigration(t *testing.T) {
	store := newSessionTestStore(t)
	preMeta := store.Meta()
	postMeta := cloneMeta(preMeta)
	postMeta.LastSequence = 1
	postMeta.ConversationEstablished = true
	postMeta.UpdatedAt = preMeta.UpdatedAt.Add(time.Second)
	writeSessionFixtureEvents(t, store.Dir(), []legacyTestEvent{{
		Seq:       1,
		Timestamp: time.Now().UTC(),
		Kind:      string(EventKindMessage),
		Payload: mustFixtureJSON(t, map[string]any{
			"role":    string(MessageRoleUser),
			"content": "committed before migration",
		}),
	}})
	eventsPath := filepath.Join(store.Dir(), eventsFile)
	events, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read legacy event log: %v", err)
	}
	writer := &Store{sessionDir: store.Dir(), meta: preMeta}
	recovery, err := writer.newAppendRecoveryRecord(
		preMeta,
		postMeta,
		appendRecoveryCommitted,
		&appendRecoveryEvents{
			StartOffset:   0,
			EndOffset:     int64(len(events)),
			EventCount:    1,
			FirstSequence: 1,
			LastSequence:  1,
			SHA256:        digestBytes(events),
		},
	)
	if err != nil {
		t.Fatalf("create legacy append recovery record: %v", err)
	}
	if err := writer.writeAppendRecoveryRecord(recovery); err != nil {
		t.Fatalf("write legacy append recovery record: %v", err)
	}

	opened, err := OpenResolved(
		PersistedSessionRecord{SessionDir: store.Dir(), Meta: &preMeta},
		sessionTestPersistence.options()...,
	)
	if err != nil {
		t.Fatalf("recover legacy append transaction: %v", err)
	}
	if opened.Meta().LastSequence != 1 {
		t.Fatalf("recovered legacy sequence = %d, want 1", opened.Meta().LastSequence)
	}
	log, err := opened.MaterializeEventLog()
	if err != nil {
		t.Fatalf("migrate recovered legacy event log: %v", err)
	}
	window, err := log.ReadRecentRecords(1)
	if err != nil {
		t.Fatalf("read migrated legacy event: %v", err)
	}
	if len(window.Records) != 1 || window.Records[0].Seq() != 1 {
		t.Fatalf("migrated legacy records = %#v", window.Records)
	}
}

type recoveryDetailArtifacts struct {
	recovery []byte
	events   eventLogFileFingerprint
	metadata []byte
}

func captureRecoveryDetailArtifacts(
	t *testing.T,
	recoveryPath string,
	eventsPath string,
	meta Meta,
) recoveryDetailArtifacts {
	t.Helper()
	recovery, err := os.ReadFile(recoveryPath)
	if err != nil {
		t.Fatalf("read recovery artifact: %v", err)
	}
	return recoveryDetailArtifacts{
		recovery: recovery,
		events:   eventLogFingerprint(t, eventsPath),
		metadata: marshalRecoveryDetailMetadata(t, meta),
	}
}

func (before recoveryDetailArtifacts) assertUnchanged(
	t *testing.T,
	recoveryPath string,
	eventsPath string,
	meta Meta,
) {
	t.Helper()
	after := captureRecoveryDetailArtifacts(t, recoveryPath, eventsPath, meta)
	if !bytes.Equal(before.recovery, after.recovery) {
		t.Fatalf("recovery record changed: before=%q after=%q", before.recovery, after.recovery)
	}
	if !before.events.equal(after.events) {
		t.Fatalf("events file changed: before=%+v after=%+v", before.events, after.events)
	}
	if !bytes.Equal(before.metadata, after.metadata) {
		t.Fatalf("authoritative metadata changed: before=%q after=%q", before.metadata, after.metadata)
	}
}

func marshalRecoveryDetailMetadata(t *testing.T, meta Meta) []byte {
	t.Helper()
	encoded, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal authoritative metadata: %v", err)
	}
	return encoded
}

func recoveryDetailTestMeta(now time.Time) Meta {
	return Meta{
		SessionID:          "session-1",
		WorkspaceRoot:      "/workspace",
		WorkspaceContainer: "/workspace",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}
