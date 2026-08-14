package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"core/shared/sessioncontract"
)

func TestPersistedSessionViewOpensCanonicalEmptyEventLogsWithoutMaterializing(t *testing.T) {
	for _, test := range []struct {
		name          string
		prepare       func(*testing.T, string)
		wantEndOffset func(*testing.T, string) int64
	}{
		{
			name:    "zero byte durable log",
			prepare: func(*testing.T, string) {},
			wantEndOffset: func(t *testing.T, path string) int64 {
				t.Helper()
				info, err := os.Stat(path)
				if err != nil {
					t.Fatalf("stat zero-byte event log: %v", err)
				}
				if info.Size() != 0 {
					t.Fatalf("zero-byte event log size = %d, want 0", info.Size())
				}
				return 0
			},
		},
		{
			name: "current header only",
			prepare: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove zero-byte event log: %v", err)
				}
				if _, err := createCurrentEventLog(path); err != nil {
					t.Fatalf("create header-only event log: %v", err)
				}
			},
			wantEndOffset: func(t *testing.T, path string) int64 {
				t.Helper()
				log, err := openCurrentEventLog(path, currentEventLogReadOnly)
				if err != nil {
					t.Fatalf("open header-only event log: %v", err)
				}
				return log.firstEventOffset
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			persistence := &testSessionMetadata{records: map[string]PersistedSessionRecord{}}
			store, err := Create(
				t.TempDir(),
				"workspace",
				t.TempDir(),
				sessioncontract.SessionCategoryMain,
				persistence.options()...,
			)
			if err != nil {
				t.Fatalf("create Session: %v", err)
			}
			if err := store.EnsureDurable(); err != nil {
				t.Fatalf("EnsureDurable: %v", err)
			}
			path := filepath.Join(store.Dir(), eventsFile)
			test.prepare(t, path)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read event log before open: %v", err)
			}
			record, err := persistence.ResolvePersistedSession(t.Context(), store.Meta().SessionID)
			if err != nil {
				t.Fatalf("resolve persisted Session: %v", err)
			}
			count := 3
			eligible := true
			record.ContextFacts = SessionContextFacts{
				CompletedCompactionCount: &count,
				ManualCompactEligible:    &eligible,
			}

			view, err := OpenPersistedSessionView(record.Meta.SessionID, record)
			if err != nil {
				t.Fatalf("open persisted Session view: %v", err)
			}
			revision, err := view.Revision()
			if err != nil || revision != 0 {
				t.Fatalf("revision = %d, %v; want 0", revision, err)
			}
			freshness, err := view.ConversationFreshness()
			if err != nil || freshness != ConversationFreshnessFresh {
				t.Fatalf("conversation freshness = %v, %v; want fresh", freshness, err)
			}
			wantOffset := test.wantEndOffset(t, path)
			for name, read := range map[string]func() (EventRecordWindow, error){
				"forward": func() (EventRecordWindow, error) {
					return view.ReadSegmentForward(0, nil)
				},
				"backward": func() (EventRecordWindow, error) {
					return view.ReadSegmentBackward(0, nil)
				},
				"newest backward": func() (EventRecordWindow, error) {
					return view.ReadNewestSegmentBackward(nil)
				},
				"recent": func() (EventRecordWindow, error) {
					return view.ReadRecentRecords(8)
				},
			} {
				window, err := read()
				if err != nil {
					t.Fatalf("%s empty window: %v", name, err)
				}
				if len(window.Records) != 0 ||
					!window.ReachedStart ||
					!window.ReachedEnd ||
					window.StartOffset != wantOffset ||
					window.EndOffset != wantOffset {
					t.Fatalf("%s empty window = %+v, want frozen offset %d", name, window, wantOffset)
				}
			}
			meta := view.Meta()
			facts := view.ContextFacts()
			meta.Name = "aliased"
			*facts.CompletedCompactionCount = 99
			if view.Meta().Name == "aliased" ||
				view.ContextFacts().CompletedCompactionCount == nil ||
				*view.ContextFacts().CompletedCompactionCount != count {
				t.Fatal("persisted Session metadata or Context facts were aliased")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read event log after open: %v", err)
			}
			if string(after) != string(before) {
				t.Fatalf("read-only open mutated event log\nbefore=%q\nafter=%q", before, after)
			}
		})
	}
}

func TestPersistedSessionViewFreezesExactNonzeroBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	first := mustVersionMatrixRecord(t, 1, MessageRoleUser, "first")
	writeVersionedEventLog(t, path, EventLogVersionV2, []EventRecord{first})
	meta := Meta{SessionID: "11111111-1111-4111-8111-111111111111", LastSequence: 1, ConversationEstablished: true}
	view, err := OpenPersistedSessionView(meta.SessionID, PersistedSessionRecord{SessionDir: dir, Meta: &meta})
	if err != nil {
		t.Fatalf("open persisted Session view: %v", err)
	}
	before, err := view.ReadSegmentForward(0, nil)
	if err != nil {
		t.Fatalf("read initial frozen window: %v", err)
	}
	if len(before.Records) != 1 || before.Records[0].Seq() != 1 || !before.ReachedStart || !before.ReachedEnd {
		t.Fatalf("initial frozen window = %+v", before)
	}
	freshness, err := view.ConversationFreshness()
	if err != nil || freshness != ConversationFreshnessEstablished {
		t.Fatalf("conversation freshness = %v, %v; want established", freshness, err)
	}

	writer, err := openCurrentEventLog(path, currentEventLogAuthoritative)
	if err != nil {
		t.Fatalf("open event-log writer: %v", err)
	}
	second := mustVersionMatrixRecord(t, 2, MessageRoleAssistant, "later")
	if _, err := writer.appendRecords([]EventRecord{second}); err != nil {
		t.Fatalf("append later record: %v", err)
	}

	for name, read := range map[string]func() (EventRecordWindow, error){
		"forward": func() (EventRecordWindow, error) {
			return view.ReadSegmentForward(0, nil)
		},
		"backward": func() (EventRecordWindow, error) {
			return view.ReadSegmentBackward(0, nil)
		},
		"newest backward": func() (EventRecordWindow, error) {
			return view.ReadNewestSegmentBackward(nil)
		},
		"recent": func() (EventRecordWindow, error) {
			return view.ReadRecentRecords(8)
		},
	} {
		window, err := read()
		if err != nil {
			t.Fatalf("%s frozen window: %v", name, err)
		}
		if len(window.Records) != 1 ||
			window.Records[0].Seq() != 1 ||
			window.EndOffset != before.EndOffset ||
			!window.ReachedEnd {
			t.Fatalf("%s frozen window after append = %+v, want original end %d", name, window, before.EndOffset)
		}
	}
	for name, cursor := range map[string]int64{
		"captured end": before.EndOffset,
		"later end":    before.EndOffset + 1,
	} {
		window, err := view.ReadSegmentForward(cursor, nil)
		if err != nil {
			t.Fatalf("read forward from %s cursor: %v", name, err)
		}
		if len(window.Records) != 0 ||
			window.StartOffset != before.EndOffset ||
			window.EndOffset != before.EndOffset ||
			!window.ReachedEnd {
			t.Fatalf("forward window from %s cursor = %+v", name, window)
		}
	}
}

func TestPersistedSessionViewRejectsEveryMetadataLogMismatchWithoutReverseSearch(t *testing.T) {
	tests := []struct {
		name             string
		metadataSequence int64
		recordCount      int
		wantObserved     int64
	}{
		{name: "no records with nonzero metadata", metadataSequence: 1, recordCount: 0, wantObserved: 0},
		{name: "log behind metadata", metadataSequence: 2, recordCount: 1, wantObserved: 1},
		{name: "log ahead by one", metadataSequence: 1, recordCount: 2, wantObserved: 2},
		{name: "log ahead arbitrarily far", metadataSequence: 1, recordCount: 128, wantObserved: 128},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, eventsFile)
			records := make([]EventRecord, 0, test.recordCount)
			for sequence := 1; sequence <= test.recordCount; sequence++ {
				records = append(records, mustVersionMatrixRecord(t, int64(sequence), MessageRoleUser, "event"))
			}
			writeVersionedEventLog(t, path, EventLogVersionV2, records)
			meta := Meta{SessionID: "22222222-2222-4222-8222-222222222222", LastSequence: test.metadataSequence}

			_, err := OpenPersistedSessionView(meta.SessionID, PersistedSessionRecord{SessionDir: dir, Meta: &meta})
			var conflict EventLogReconciliationConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("error = %v, want EventLogReconciliationConflictError", err)
			}
			if conflict.SessionID != meta.SessionID ||
				conflict.ObservedLastSequence != test.wantObserved ||
				conflict.CurrentLastSequence != test.metadataSequence {
				t.Fatalf("conflict = %+v", conflict)
			}
		})
	}
}

func TestPersistedSessionViewRejectsTornTailWithoutRepair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	header, err := encodeEventLogHeader(EventLogVersionV2)
	if err != nil {
		t.Fatalf("encode event-log header: %v", err)
	}
	contents := append(append(header, '\n'), []byte(`{"sequence":1`)...)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write torn event log: %v", err)
	}
	meta := Meta{SessionID: "33333333-3333-4333-8333-333333333333", LastSequence: 1}

	_, err = OpenPersistedSessionView(meta.SessionID, PersistedSessionRecord{SessionDir: dir, Meta: &meta})
	var conflict EventLogReconciliationConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("torn event-log error = %v, want EventLogReconciliationConflictError", err)
	}
	if conflict.ObservedLastSequence != 0 ||
		conflict.CurrentLastSequence != 1 ||
		!conflict.BoundaryIncomplete {
		t.Fatalf("torn event-log conflict = %+v", conflict)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read torn event log after open: %v", err)
	}
	if string(after) != string(contents) {
		t.Fatalf("read-only open repaired torn event log\nbefore=%q\nafter=%q", contents, after)
	}
}

func TestPersistedSessionViewPreservesTypedMalformedEventLogFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	header, err := encodeEventLogHeader(EventLogVersionV2)
	if err != nil {
		t.Fatalf("encode event-log header: %v", err)
	}
	contents := append(append(header, '\n'), []byte(`{"sequence":1,"kind":"unsupported","payload":{}}`)...)
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write malformed event log: %v", err)
	}
	meta := Meta{SessionID: "55555555-5555-4555-8555-555555555555", LastSequence: 1}

	_, err = OpenPersistedSessionView(meta.SessionID, PersistedSessionRecord{SessionDir: dir, Meta: &meta})
	var materializationErr *EventLogMaterializationError
	if !errors.As(err, &materializationErr) {
		t.Fatalf("malformed event-log error = %v, want EventLogMaterializationError", err)
	}
	if materializationErr.Stage != EventLogMaterializationStageReconciliation ||
		materializationErr.Committed ||
		materializationErr.PendingRepair {
		t.Fatalf("malformed event-log error = %+v", materializationErr)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read malformed event log after open: %v", err)
	}
	if string(after) != string(contents) {
		t.Fatalf("read-only open mutated malformed event log\nbefore=%q\nafter=%q", contents, after)
	}
}

func TestPersistedSessionViewOpenDoesNotWalkWholeHistory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	header, err := encodeEventLogHeader(EventLogVersionV2)
	if err != nil {
		t.Fatalf("encode event-log header: %v", err)
	}
	last := mustVersionMatrixRecord(t, 10, MessageRoleAssistant, "bounded tail")
	lastLine, err := encodeEventRecordForVersion(EventLogVersionV2, last)
	if err != nil {
		t.Fatalf("encode last record: %v", err)
	}
	contents := append(append(header, '\n'), []byte("invalid historical record\n")...)
	contents = append(contents, lastLine...)
	contents = append(contents, '\n')
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write bounded-tail fixture: %v", err)
	}
	meta := Meta{SessionID: "44444444-4444-4444-8444-444444444444", LastSequence: 10}

	view, err := OpenPersistedSessionView(meta.SessionID, PersistedSessionRecord{SessionDir: dir, Meta: &meta})
	if err != nil {
		t.Fatalf("open inspected more than the bounded tail: %v", err)
	}
	revision, err := view.Revision()
	if err != nil || revision != 10 {
		t.Fatalf("revision = %d, %v; want 10", revision, err)
	}
	if _, err := view.ReadSegmentBackward(0, nil); err == nil {
		t.Fatal("segment read accepted malformed historical data")
	}
}
