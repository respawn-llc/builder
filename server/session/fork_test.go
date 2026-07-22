package session

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func forkUserMessageRecord(content string) MessageRecord {
	return MessageRecord{
		Role:    MessageRoleUser,
		Content: forkStringPointer(content),
	}
}

func userMessagePayload(t *testing.T, content string) MessageRecord {
	t.Helper()
	return forkUserMessageRecord(content)
}

func forkStringPointer(value string) *string {
	return &value
}

func forkMessageTypePointer(value MessageType) *MessageType {
	return &value
}

func materializedForkEventLog(t *testing.T, store *Store) MaterializedEventLog {
	t.Helper()
	log, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	return log
}

func appendForkTestRecords(
	t *testing.T,
	log MaterializedEventLog,
	userMessages int,
	perMessageFiller int,
) {
	t.Helper()
	for i := 0; i < userMessages; i++ {
		if _, _, err := log.AppendRecord(
			forkStringPointer("step"),
			forkUserMessageRecord("prompt"),
		); err != nil {
			t.Fatalf("append user message %d: %v", i, err)
		}
		for f := 0; f < perMessageFiller; f++ {
			if _, _, err := log.AppendRecord(forkStringPointer("step"), LocalEntryRecord{
				Visibility: EntryVisibilityHidden,
				Role:       "test",
				Text:       "filler",
			}); err != nil {
				t.Fatalf("append filler %d/%d: %v", i, f, err)
			}
		}
	}
}

func collectForkRecords(t *testing.T, log MaterializedEventLog) []EventRecord {
	t.Helper()
	var records []EventRecord
	if err := log.WalkRecords(func(record EventRecord) error {
		records = append(records, record)
		return nil
	}); err != nil {
		t.Fatalf("walk typed records: %v", err)
	}
	return records
}

type forkReplayCountingPersistence struct {
	base     *testSessionMetadata
	mu       sync.Mutex
	parentID string
	childSeq []int64
}

func newForkReplayCountingPersistence() *forkReplayCountingPersistence {
	return &forkReplayCountingPersistence{
		base: &testSessionMetadata{records: map[string]PersistedSessionRecord{}},
	}
}

func (p *forkReplayCountingPersistence) ObservePersistedStore(
	ctx context.Context,
	snapshot PersistedStoreSnapshot,
) error {
	if err := p.base.ObservePersistedStore(ctx, snapshot); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.parentID != "" &&
		snapshot.Meta.SessionID != p.parentID &&
		snapshot.Meta.LastSequence > 0 {
		if len(p.childSeq) == 0 ||
			p.childSeq[len(p.childSeq)-1] != snapshot.Meta.LastSequence {
			p.childSeq = append(p.childSeq, snapshot.Meta.LastSequence)
		}
	}
	return nil
}

func (p *forkReplayCountingPersistence) ObserveEventLogReconciliation(
	ctx context.Context,
	reconciliation PersistedEventLogReconciliation,
) error {
	return p.base.ObserveEventLogReconciliation(ctx, reconciliation)
}

func (p *forkReplayCountingPersistence) ResolvePersistedSession(
	ctx context.Context,
	sessionID string,
) (PersistedSessionRecord, error) {
	return p.base.ResolvePersistedSession(ctx, sessionID)
}

func (p *forkReplayCountingPersistence) options() []StoreOption {
	return []StoreOption{
		WithPersistenceObserver(p),
		WithPersistedSessionResolver(p),
	}
}

func (p *forkReplayCountingPersistence) startChildCapture(parentID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.parentID = parentID
	p.childSeq = nil
}

func (p *forkReplayCountingPersistence) childSequences() []int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int64(nil), p.childSeq...)
}

func TestForkAtUserMessageStreamsPrefixAcrossChunks(t *testing.T) {
	parent := newSessionTestStore(t)
	parentLog := materializedForkEventLog(t, parent)
	appendForkTestRecords(t, parentLog, 4, 3)

	parentRecords := collectForkRecords(t, parentLog)
	const forkIndex = 3
	expected := make([]EventRecord, 0)
	visible := 0
	var forkSeq int64
	for _, record := range parentRecords {
		isVisible, err := isForkVisibleUserMessage(record)
		if err != nil {
			t.Fatalf("inspect fork-visible user record: %v", err)
		}
		if isVisible {
			visible++
			if visible == forkIndex {
				forkSeq = record.Seq()
				break
			}
		}
		expected = append(expected, record)
	}
	child, ordinal, err := ForkAtUserMessage(parentLog, forkSeq, "fork", testSessionCategory)
	if err != nil {
		t.Fatalf("fork at user message: %v", err)
	}
	if ordinal != forkIndex {
		t.Fatalf("fork ordinal = %d, want %d", ordinal, forkIndex)
	}
	childLog := materializedForkEventLog(t, child)
	childRecords := collectForkRecords(t, childLog)
	if !reflect.DeepEqual(expected, childRecords) {
		t.Fatalf("fork child must replay the prefix before user message %d: want %d records, got %d", forkIndex, len(expected), len(childRecords))
	}
}

func mustForkRecord(
	t *testing.T,
	sequence int64,
	payload EventRecordPayload,
) EventRecord {
	t.Helper()
	record, err := NewEventRecord(sequence, forkStringPointer("step"), payload)
	if err != nil {
		t.Fatalf("build fork fixture record %d: %v", sequence, err)
	}
	return record
}

func largeForkLocalEntry() LocalEntryRecord {
	return LocalEntryRecord{
		Visibility: EntryVisibilityHidden,
		Role:       "test",
		Text:       strings.Repeat("x", forkReplayFlushByteBudget*3/5),
	}
}

func assertNoForkTemporaryArtifacts(t *testing.T, sessionDir string) {
	t.Helper()
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatalf("read fork session directory %q: %v", sessionDir, err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case eventsFile, eventLogMigrationLockFile:
		default:
			t.Fatalf(
				"fork replay left unexpected temporary disk artifact %q in %q",
				entry.Name(),
				sessionDir,
			)
		}
	}
}

func forkTypedMessage(messageType MessageType, content string) MessageRecord {
	return MessageRecord{
		Role:        MessageRoleDeveloper,
		MessageType: forkMessageTypePointer(messageType),
		Content:     forkStringPointer(content),
	}
}

func assertOnlyForkParentSessionDir(t *testing.T, root string, parent *Store) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read container dir: %v", err)
	}
	sessionDirs := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			sessionDirs = append(sessionDirs, entry.Name())
		}
	}
	if len(sessionDirs) != 1 || sessionDirs[0] != parent.Meta().SessionID {
		t.Fatalf("expected only the parent session dir to remain, got %v", sessionDirs)
	}
	if _, err := os.Stat(filepath.Join(root, parent.Meta().SessionID)); err != nil {
		t.Fatalf("parent session dir must survive a failed fork: %v", err)
	}
}
