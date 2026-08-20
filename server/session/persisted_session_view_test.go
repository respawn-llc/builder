package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/shared/sessioncontract"
)

const persistedViewTestSessionID = "11111111-1111-4111-8111-111111111111"

func mustPersistedView[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func writePersistedViewRawLog(path, line string, trailingNewline bool) []byte {
	contents := append(append(mustPersistedView(encodeEventLogHeader(EventLogVersionV2)), '\n'), line...)
	if trailingNewline {
		contents = append(contents, '\n')
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		panic(err)
	}
	return contents
}

func assertPersistedViewLogUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	if after := mustPersistedView(os.ReadFile(path)); string(after) != string(before) {
		t.Fatalf("event log changed\nbefore=%q\nafter=%q", before, after)
	}
}

func TestPersistedSessionViewOpensCanonicalEmptyEventLogsWithoutMaterializing(t *testing.T) {
	for _, test := range []struct {
		name       string
		headerOnly bool
	}{{"zero byte", false}, {"header only", true}} {
		t.Run(test.name, func(t *testing.T) {
			persistence := &testSessionMetadata{records: map[string]PersistedSessionRecord{}}
			store := mustPersistedView(Create(t.TempDir(), "workspace", t.TempDir(), sessioncontract.SessionCategoryMain, persistence.options()...))
			if err := store.EnsureDurable(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.Dir(), eventsFile)
			if test.headerOnly {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				mustPersistedView(createCurrentEventLog(path))
			}
			before := mustPersistedView(os.ReadFile(path))
			record := mustPersistedView(persistence.ResolvePersistedSession(t.Context(), store.Meta().SessionID))
			count := 3
			record.ContextFacts.CompletedCompactionCount = &count
			view := mustPersistedView(OpenPersistedSessionView(record.Meta.SessionID, record))
			window := mustPersistedView(view.ReadSegmentForward(0, nil))
			if len(window.Records) != 0 || !window.ReachedStart || !window.ReachedEnd ||
				window.StartOffset != int64(len(before)) || window.EndOffset != int64(len(before)) {
				t.Fatalf("empty window = %+v", window)
			}
			meta, facts := view.Meta(), view.ContextFacts()
			meta.Name, *facts.CompletedCompactionCount = "aliased", 99
			if view.Meta().Name == "aliased" || *view.ContextFacts().CompletedCompactionCount != count {
				t.Fatal("persisted Session metadata or Context facts were aliased")
			}
			assertPersistedViewLogUnchanged(t, path, before)
		})
	}
}

func TestPersistedSessionViewFreezesExactNonzeroBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	writeVersionedEventLog(t, path, EventLogVersionV2, []EventRecord{mustVersionMatrixRecord(t, 1, MessageRoleUser, "first")})
	meta := Meta{SessionID: persistedViewTestSessionID, LastSequence: 1}
	view := mustPersistedView(OpenPersistedSessionView(meta.SessionID, PersistedSessionRecord{SessionDir: dir, Meta: &meta}))
	before := mustPersistedView(view.ReadSegmentForward(0, nil))
	writer := mustPersistedView(openCurrentEventLog(path, currentEventLogAuthoritative))
	mustPersistedView(writer.appendRecords([]EventRecord{mustVersionMatrixRecord(t, 2, MessageRoleAssistant, "later")}))
	after := mustPersistedView(view.ReadSegmentForward(0, nil))
	if len(after.Records) != 1 || after.Records[0].Seq() != 1 || after.EndOffset != before.EndOffset || !after.ReachedEnd {
		t.Fatalf("frozen window = %+v", after)
	}
}

func TestPersistedSessionViewRejectsNonPositiveBackwardSegmentCursor(t *testing.T) {
	dir := t.TempDir()
	writeVersionedEventLog(t, filepath.Join(dir, eventsFile), EventLogVersionV2, []EventRecord{
		mustVersionMatrixRecord(t, 1, MessageRoleUser, "first"),
	})
	meta := Meta{SessionID: persistedViewTestSessionID, LastSequence: 1}
	view := mustPersistedView(OpenPersistedSessionView(meta.SessionID, PersistedSessionRecord{SessionDir: dir, Meta: &meta}))

	for _, cursor := range []int64{0, -1} {
		if _, err := view.ReadSegmentBackward(cursor, nil); err == nil {
			t.Fatalf("ReadSegmentBackward(%d) accepted a non-positive cursor", cursor)
		}
	}
}

func TestPersistedSessionViewRejectsEveryMetadataLogMismatchWithoutReverseSearch(t *testing.T) {
	for _, test := range []struct {
		name        string
		metadataSeq int64
		recordCount int
	}{
		{"no records with nonzero metadata", 1, 0},
		{"log behind metadata", 2, 1},
		{"log ahead by one", 1, 2},
		{"log ahead arbitrarily far", 1, 128},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			records := make([]EventRecord, test.recordCount)
			for i := range records {
				records[i] = mustVersionMatrixRecord(t, int64(i+1), MessageRoleUser, "event")
			}
			writeVersionedEventLog(t, filepath.Join(dir, eventsFile), EventLogVersionV2, records)
			meta := Meta{SessionID: persistedViewTestSessionID, LastSequence: test.metadataSeq}
			_, err := OpenPersistedSessionView(meta.SessionID, PersistedSessionRecord{SessionDir: dir, Meta: &meta})
			var conflict EventLogReconciliationConflictError
			if !errors.As(err, &conflict) || conflict.CurrentLastSequence != test.metadataSeq ||
				conflict.ObservedLastSequence != int64(test.recordCount) {
				t.Fatalf("conflict = %+v, err=%v", conflict, err)
			}
		})
	}
}

func TestPersistedSessionViewRejectsTornTailWithoutRepair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	before := writePersistedViewRawLog(path, `{"sequence":1`, false)
	meta := Meta{SessionID: persistedViewTestSessionID, LastSequence: 1}
	_, err := OpenPersistedSessionView(meta.SessionID, PersistedSessionRecord{SessionDir: dir, Meta: &meta})
	var conflict EventLogReconciliationConflictError
	if !errors.As(err, &conflict) || !conflict.BoundaryIncomplete || conflict.ObservedLastSequence != 0 {
		t.Fatalf("conflict = %+v, err=%v", conflict, err)
	}
	assertPersistedViewLogUnchanged(t, path, before)
}

func TestPersistedSessionViewPreservesTypedMalformedEventLogFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	before := writePersistedViewRawLog(path, `{"sequence":1,"kind":"unsupported","payload":{}}`, true)
	meta := Meta{SessionID: persistedViewTestSessionID, LastSequence: 1}
	_, err := OpenPersistedSessionView(meta.SessionID, PersistedSessionRecord{SessionDir: dir, Meta: &meta})
	var typed *EventLogMaterializationError
	if !errors.As(err, &typed) || typed.Stage != EventLogMaterializationStageReconciliation ||
		typed.Committed || typed.PendingRepair {
		t.Fatalf("materialization error = %+v, err=%v", typed, err)
	}
	assertPersistedViewLogUnchanged(t, path, before)
}

func TestPersistedSessionViewOpenDoesNotWalkWholeHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	last := mustPersistedView(encodeEventRecordForVersion(EventLogVersionV2, mustVersionMatrixRecord(t, 10, MessageRoleAssistant, "tail")))
	contents := append(append(append(mustPersistedView(encodeEventLogHeader(EventLogVersionV2)), '\n'), "invalid historical record\n"...), last...)
	if err := os.WriteFile(path, append(contents, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	meta := Meta{SessionID: persistedViewTestSessionID, LastSequence: 10}
	view := mustPersistedView(OpenPersistedSessionView(meta.SessionID, PersistedSessionRecord{SessionDir: dir, Meta: &meta}))
	if revision := mustPersistedView(view.Revision()); revision != 10 {
		t.Fatalf("revision = %d", revision)
	}
	if _, err := view.ReadSegmentBackward(0, nil); err == nil {
		t.Fatal("segment read accepted malformed historical data")
	}
}
