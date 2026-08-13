package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestQuestionHistoryCursorRejectsMissingAndZeroByteLogs(t *testing.T) {
	for _, test := range []struct {
		name   string
		create bool
	}{
		{name: "missing"},
		{name: "zero byte", create: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, eventsFile)
			if test.create {
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatalf("write empty event log: %v", err)
				}
			}
			if _, err := OpenQuestionHistoryCursor(dir, 1); err == nil {
				t.Fatal("open Question-history cursor unexpectedly succeeded")
			}
		})
	}
}

func TestQuestionHistoryCursorAcceptsVersionedHeaderOnlyLogs(t *testing.T) {
	for _, version := range []int{EventLogVersionV1, EventLogVersionV2} {
		t.Run(eventLogVersionTestName(version), func(t *testing.T) {
			dir := t.TempDir()
			writeVersionedEventLog(t, filepath.Join(dir, eventsFile), version, nil)
			cursor, err := OpenQuestionHistoryCursor(dir, 1)
			if err != nil {
				t.Fatalf("open v%d cursor: %v", version, err)
			}
			defer cursor.Close()
			if cursor.Version() != version || cursor.InitialSize() <= 0 {
				t.Fatalf("cursor facts = version %d size %d", cursor.Version(), cursor.InitialSize())
			}
			record, err := cursor.Next()
			if err != nil || record != nil {
				t.Fatalf("header-only Next = record %#v error %v", record, err)
			}
			if cursor.HistoryOmitted() {
				t.Fatal("header-only cursor reported omitted history")
			}
		})
	}
}

func TestQuestionHistoryCursorReadsNewestToOldestAcrossRetainedWindows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	writeVersionedEventLog(t, path, EventLogVersionV2, []EventRecord{
		mustVersionMatrixRecord(t, 1, MessageRoleUser, "oldest"),
		mustHistoryReplacementRecord(t, 2),
		mustVersionMatrixRecord(t, 3, MessageRoleAssistant, "middle"),
		mustHistoryReplacementRecord(t, 4),
		mustVersionMatrixRecord(t, 5, MessageRoleAssistant, "newest"),
	})
	cursor, err := OpenQuestionHistoryCursor(dir, 3)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer cursor.Close()
	var sequences []int64
	for {
		record, err := cursor.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if record == nil {
			break
		}
		sequences = append(sequences, record.Seq())
	}
	if !equalInt64s(sequences, []int64{5, 4, 3, 2, 1}) {
		t.Fatalf("sequences = %v", sequences)
	}
	if cursor.HistoryOmitted() {
		t.Fatal("complete cursor reported omitted history")
	}
}

func TestQuestionHistoryCursorMaxHandoffsOneDetectsOmissionWithoutDecodingOlderContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	header, err := encodeEventLogHeader(EventLogVersionV2)
	if err != nil {
		t.Fatalf("encode header: %v", err)
	}
	replacement := mustHistoryReplacementRecord(t, 2)
	replacementLine, err := encodeEventRecordV2(replacement)
	if err != nil {
		t.Fatalf("encode replacement: %v", err)
	}
	newest := mustVersionMatrixRecord(t, 3, MessageRoleAssistant, "newest")
	newestLine, err := encodeEventRecordV2(newest)
	if err != nil {
		t.Fatalf("encode newest: %v", err)
	}
	content := append(header, '\n')
	content = append(content, []byte(`{"seq":1,"kind":"message","payload":BROKEN}`)...)
	content = append(content, '\n')
	content = append(content, replacementLine...)
	content = append(content, '\n')
	content = append(content, newestLine...)
	content = append(content, '\n')
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write cursor fixture: %v", err)
	}

	cursor, err := OpenQuestionHistoryCursor(dir, 1)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer cursor.Close()
	record, err := cursor.Next()
	if err != nil || record == nil || record.Seq() != 3 {
		t.Fatalf("first Next = %#v, %v", record, err)
	}
	record, err = cursor.Next()
	if err != nil || record != nil {
		t.Fatalf("terminal Next = %#v, %v", record, err)
	}
	if !cursor.HistoryOmitted() {
		t.Fatal("cursor did not report omitted older window")
	}
}

func TestQuestionHistoryCursorIgnoresIncompleteConcurrentTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	writeVersionedEventLog(t, path, EventLogVersionV1, []EventRecord{
		mustVersionMatrixRecord(t, 1, MessageRoleUser, "complete"),
	})
	fp, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open fixture append: %v", err)
	}
	if _, err := fp.WriteString(`{"seq":2,"kind":"message"`); err != nil {
		t.Fatalf("append incomplete tail: %v", err)
	}
	if err := fp.Close(); err != nil {
		t.Fatalf("close fixture append: %v", err)
	}
	cursor, err := OpenQuestionHistoryCursor(dir, 1)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer cursor.Close()
	record, err := cursor.Next()
	if err != nil || record == nil || record.Seq() != 1 {
		t.Fatalf("Next = %#v, %v", record, err)
	}
}

func TestQuestionHistoryCursorSurfacesCompleteDecodeFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	header, err := encodeEventLogHeader(EventLogVersionV2)
	if err != nil {
		t.Fatalf("encode header: %v", err)
	}
	content := append(header, '\n')
	content = append(content, []byte(`{"seq":1,"kind":"message","payload":BROKEN}`)...)
	content = append(content, '\n')
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write malformed event log: %v", err)
	}
	cursor, err := OpenQuestionHistoryCursor(dir, 1)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer cursor.Close()
	if _, err := cursor.Next(); err == nil {
		t.Fatal("complete malformed record did not fail")
	}
}

func TestQuestionHistoryCursorDoesNotWaitForSessionMutationLock(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	if _, _, err := log.AppendRecord(nil, sessionTestMessage(MessageRoleUser, "visible")); err != nil {
		t.Fatalf("append fixture: %v", err)
	}
	store.mutationMu.Lock()
	defer store.mutationMu.Unlock()
	cursor, err := OpenQuestionHistoryCursor(store.Dir(), 1)
	if err != nil {
		t.Fatalf("open cursor while mutation lock held: %v", err)
	}
	defer cursor.Close()
	record, err := cursor.Next()
	if err != nil || record == nil {
		t.Fatalf("Next while mutation lock held = %#v, %v", record, err)
	}
}

func mustHistoryReplacementRecord(t *testing.T, sequence int64) EventRecord {
	t.Helper()
	record, err := NewEventRecord(sequence, nil, HistoryReplacementRecord{
		Engine: "local",
		Mode:   CompactionModeHandoff,
	})
	if err != nil {
		t.Fatalf("create history replacement: %v", err)
	}
	return record
}

func equalInt64s(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
