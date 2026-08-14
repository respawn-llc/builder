package session

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"core/shared/transcript"
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
			defer func() { _ = cursor.Close() }()
			if cursor.Version() != version || cursor.InitialSize() <= 0 {
				t.Fatalf("cursor facts = version %d size %d", cursor.Version(), cursor.InitialSize())
			}
			record, err := cursor.Next(t.Context())
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
	writeQuestionHistoryCursorLog(t, path, EventLogVersionV2, []EventRecord{
		mustQuestionHistoryCursorRecord(t, 1, "oldest"),
		mustHistoryReplacementRecord(t, 2),
		mustQuestionHistoryCursorRecord(t, 3, "middle"),
		mustHistoryReplacementRecord(t, 4),
		mustQuestionHistoryCursorRecord(t, 5, "newest"),
	})
	cursor, err := OpenQuestionHistoryCursor(dir, 3)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer func() { _ = cursor.Close() }()
	var sequences []int64
	for {
		record, err := cursor.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if record == nil {
			break
		}
		sequences = append(sequences, record.Seq())
	}
	if !equalInt64s(sequences, []int64{5, 3, 1}) {
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
	newest := mustQuestionHistoryCursorRecord(t, 3, "newest")
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
	defer func() { _ = cursor.Close() }()
	record, err := cursor.Next(t.Context())
	if err != nil || record == nil || record.Seq() != 3 {
		t.Fatalf("first Next = %#v, %v", record, err)
	}
	record, err = cursor.Next(t.Context())
	if err != nil || record != nil {
		t.Fatalf("terminal Next = %#v, %v", record, err)
	}
	if !cursor.HistoryOmitted() {
		t.Fatal("cursor did not report omitted older window")
	}
}

func TestQuestionHistoryCursorRejectsCorruptReplacementAtBoundary(t *testing.T) {
	tests := []struct {
		name string
		line []byte
	}{
		{
			name: "missing payload",
			line: []byte(`{"seq":2,"kind":"history_replaced"}`),
		},
		{
			name: "invalid mode",
			line: []byte(`{"seq":2,"kind":"history_replaced","payload":{"engine":"local","mode":"future","items":[]}}`),
		},
		{
			name: "duplicate payload",
			line: []byte(`{"seq":2,"kind":"history_replaced","payload":{"engine":"local"},"payload":{"mode":"handoff","items":[]}}`),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			olderLine, err := encodeEventRecordV2(
				mustQuestionHistoryCursorRecord(t, 1, "older"),
			)
			if err != nil {
				t.Fatalf("encode older: %v", err)
			}
			newest := mustQuestionHistoryCursorRecord(t, 3, "newest")
			newestLine, err := encodeEventRecordV2(newest)
			if err != nil {
				t.Fatalf("encode newest: %v", err)
			}
			writeRawVersionedEventLog(
				t,
				filepath.Join(dir, eventsFile),
				EventLogVersionV2,
				[][]byte{
					olderLine,
					test.line,
					newestLine,
				},
			)
			cursor, err := OpenQuestionHistoryCursor(dir, 1)
			if err != nil {
				t.Fatalf("open cursor: %v", err)
			}
			defer func() { _ = cursor.Close() }()
			record, err := cursor.Next(t.Context())
			if err != nil || record == nil || record.Seq() != 3 {
				t.Fatalf("newest Next = %#v, %v", record, err)
			}
			if _, err := cursor.Next(t.Context()); err == nil {
				t.Fatal("corrupt replacement completed history scan")
			}
		})
	}
}

func TestQuestionHistoryCursorStreamsLargeReplacementBoundaryWithoutMaterializingItems(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	header, err := encodeEventLogHeader(EventLogVersionV2)
	if err != nil {
		t.Fatalf("encode header: %v", err)
	}
	newest := mustQuestionHistoryCursorRecord(t, 2, "newest")
	newestLine, err := encodeEventRecordV2(newest)
	if err != nil {
		t.Fatalf("encode newest: %v", err)
	}
	fp, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("create cursor fixture: %v", err)
	}
	if _, err := fp.Write(append(header, '\n')); err != nil {
		t.Fatalf("write header: %v", err)
	}
	if _, err := fp.WriteString(`{"seq":1,"kind":"history_replaced","payload":{"engine":"local","mode":"handoff","items":[{"type":"other","raw":"`); err != nil {
		t.Fatalf("write replacement prefix: %v", err)
	}
	chunk := bytes.Repeat([]byte{'x'}, int(eventLogScanChunkSize))
	for range 2048 {
		if _, err := fp.Write(chunk); err != nil {
			t.Fatalf("write replacement item: %v", err)
		}
	}
	if _, err := fp.WriteString(`"}]}}` + "\n"); err != nil {
		t.Fatalf("write replacement suffix: %v", err)
	}
	if _, err := fp.Write(append(newestLine, '\n')); err != nil {
		t.Fatalf("write newest: %v", err)
	}
	if err := fp.Close(); err != nil {
		t.Fatalf("close cursor fixture: %v", err)
	}

	cursor, err := OpenQuestionHistoryCursor(dir, 2)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer func() { _ = cursor.Close() }()
	record, err := cursor.Next(t.Context())
	if err != nil || record == nil || record.Seq() != 2 {
		t.Fatalf("newest Next = %#v, %v", record, err)
	}
	record, err = cursor.Next(t.Context())
	if err != nil || record != nil {
		t.Fatalf("terminal Next = %#v, %v", record, err)
	}
}

func TestQuestionHistoryCursorLargeIgnoredScalarAllocationIsSizeIndependent(t *testing.T) {
	smallDir := writeQuestionHistoryCursorIgnoredRecord(t, 1<<10)
	largeDir := writeQuestionHistoryCursorIgnoredRecord(t, 16<<20)
	allocated := func(sessionDir string) uint64 {
		runtime.GC()
		var before runtime.MemStats
		var after runtime.MemStats
		runtime.ReadMemStats(&before)
		cursor, err := OpenQuestionHistoryCursor(sessionDir, 2)
		if err != nil {
			t.Fatalf("open ignored-record cursor: %v", err)
		}
		record, nextErr := cursor.Next(t.Context())
		closeErr := cursor.Close()
		if nextErr != nil || closeErr != nil || record != nil {
			t.Fatalf(
				"consume ignored record: record=%#v next error=%v close error=%v",
				record,
				nextErr,
				closeErr,
			)
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}
	smallAllocated := allocated(smallDir)
	largeAllocated := allocated(largeDir)
	if largeAllocated > smallAllocated+(1<<20) {
		t.Fatalf(
			"large ignored record allocated %d bytes vs small %d; want size-independent bounded allocation",
			largeAllocated,
			smallAllocated,
		)
	}
}

func TestEventRecordV2FieldValidationLargePayloadAllocationIsSizeIndependent(t *testing.T) {
	smallLine := questionHistoryCursorIgnoredRecord(t, 1<<10)
	largeLine := questionHistoryCursorIgnoredRecord(t, 16<<20)
	allocated := func(line []byte) uint64 {
		runtime.GC()
		var before runtime.MemStats
		var after runtime.MemStats
		runtime.ReadMemStats(&before)
		if err := validateEventRecordV2FieldNames(line); err != nil {
			t.Fatalf("validate v2 field names: %v", err)
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}
	smallAllocated := allocated(smallLine)
	largeAllocated := allocated(largeLine)
	if largeAllocated > smallAllocated+(1<<20) {
		t.Fatalf(
			"large v2 field validation allocated %d bytes vs small %d; want size-independent bounded allocation",
			largeAllocated,
			smallAllocated,
		)
	}
}

func TestQuestionHistoryCursorSurfacesOversizedLegacyDiscriminatorsWithSizeIndependentAllocation(t *testing.T) {
	tests := []struct {
		name  string
		write func(t *testing.T, tokenBytes int) string
	}{
		{
			name:  "event field name",
			write: writeQuestionHistoryCursorOversizedFieldName,
		},
		{
			name:  "tool name",
			write: writeQuestionHistoryCursorOversizedToolName,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			smallDir := test.write(t, 1<<20)
			largeDir := test.write(t, 8<<20)
			allocated := func(sessionDir string) uint64 {
				runtime.GC()
				var before runtime.MemStats
				var after runtime.MemStats
				runtime.ReadMemStats(&before)
				cursor, err := OpenQuestionHistoryCursor(sessionDir, 2)
				if err != nil {
					t.Fatalf("open oversized-token cursor: %v", err)
				}
				_, nextErr := cursor.Next(t.Context())
				closeErr := cursor.Close()
				if nextErr == nil || closeErr != nil {
					t.Fatalf(
						"consume oversized token: next error=%v close error=%v",
						nextErr,
						closeErr,
					)
				}
				runtime.ReadMemStats(&after)
				return after.TotalAlloc - before.TotalAlloc
			}
			smallAllocated := allocated(smallDir)
			largeAllocated := allocated(largeDir)
			if largeAllocated > smallAllocated+(1<<20) {
				t.Fatalf(
					"large ignored token allocated %d bytes vs small %d; want size-independent bounded allocation",
					largeAllocated,
					smallAllocated,
				)
			}
		})
	}
}

func TestQuestionHistoryCursorRejectsSkippedV2TypedAnswerCorruption(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		isError  bool
	}{
		{name: "failed Question", toolName: askQuestionToolName, isError: true},
		{name: "non-Question tool", toolName: "exec_command"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			line := v2CompletionFixtureLine(
				t,
				test.toolName,
				test.isError,
				[]byte(`{"ToolName":"ask_question","Question":"Choose"}`),
				[]byte(`{"freeform":"answer"}`),
				int64Pointer(1),
			)
			writeRawVersionedEventLog(t, filepath.Join(dir, eventsFile), EventLogVersionV2, [][]byte{line})
			cursor, err := OpenQuestionHistoryCursor(dir, 1)
			if err != nil {
				t.Fatalf("open cursor: %v", err)
			}
			defer func() { _ = cursor.Close() }()
			if _, err := cursor.Next(t.Context()); err == nil {
				t.Fatal("corrupt skipped v2 completion did not terminate cursor")
			}
		})
	}
}

func TestQuestionHistoryCursorRejectsCandidateTrailingGarbage(t *testing.T) {
	dir := t.TempDir()
	record := mustQuestionHistoryCursorRecord(t, 1, "question")
	line, err := encodeEventRecordV2(record)
	if err != nil {
		t.Fatalf("encode Question completion: %v", err)
	}
	line = append(line, []byte(` garbage`)...)
	writeRawVersionedEventLog(t, filepath.Join(dir, eventsFile), EventLogVersionV2, [][]byte{line})
	cursor, err := OpenQuestionHistoryCursor(dir, 1)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer func() { _ = cursor.Close() }()
	if _, err := cursor.Next(t.Context()); err == nil {
		t.Fatal("candidate with trailing garbage did not fail")
	}
}

func TestQuestionHistoryCursorDecodesEscapedQuestionToolName(t *testing.T) {
	dir := t.TempDir()
	line := []byte(`{"seq":1,"kind":"tool_completed","payload":{"call_id":"call","name":"ask_\u0071uestion","output_kind":"function","is_error":false,"output":"answer","presentation":{"ToolName":"ask_question","Question":"Choose"},"question_answer":{"freeform":"answer"}},"committed_at_unix_ms":1}`)
	writeRawVersionedEventLog(t, filepath.Join(dir, eventsFile), EventLogVersionV2, [][]byte{line})
	cursor, err := OpenQuestionHistoryCursor(dir, 1)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer func() { _ = cursor.Close() }()
	record, err := cursor.Next(t.Context())
	if err != nil || record == nil || record.Seq() != 1 {
		t.Fatalf("escaped Question completion = %#v, %v", record, err)
	}
}

func TestQuestionHistoryCursorAcceptsLongIgnoredNumber(t *testing.T) {
	dir := t.TempDir()
	number := bytes.Repeat([]byte{'7'}, 16<<10)
	line := append([]byte(`{"seq":1,"kind":"history_replaced","payload":{"engine":"local","mode":"handoff","items":[],"ignored":`), number...)
	line = append(line, []byte(`}}`)...)
	writeRawVersionedEventLog(t, filepath.Join(dir, eventsFile), EventLogVersionV2, [][]byte{line})
	cursor, err := OpenQuestionHistoryCursor(dir, 2)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer func() { _ = cursor.Close() }()
	record, err := cursor.Next(t.Context())
	if err != nil || record != nil {
		t.Fatalf("long ignored number = %#v, %v", record, err)
	}
}

func TestQuestionHistoryCursorRejectsExcessiveIgnoredNesting(t *testing.T) {
	dir := t.TempDir()
	const depth = 10_001
	var line bytes.Buffer
	line.WriteString(`{"seq":1,"kind":"history_replaced","payload":`)
	line.Write(bytes.Repeat([]byte{'['}, depth))
	line.WriteString(`null`)
	line.Write(bytes.Repeat([]byte{']'}, depth))
	line.WriteByte('}')
	writeRawVersionedEventLog(t, filepath.Join(dir, eventsFile), EventLogVersionV2, [][]byte{line.Bytes()})
	cursor, err := OpenQuestionHistoryCursor(dir, 2)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer func() { _ = cursor.Close() }()
	if _, err := cursor.Next(t.Context()); err == nil {
		t.Fatal("excessively nested ignored payload did not fail")
	}
}

func TestQuestionHistoryCursorIgnoresIncompleteConcurrentTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	writeVersionedEventLog(t, path, EventLogVersionV1, []EventRecord{
		mustQuestionHistoryCursorRecordV1(t, 1, "complete"),
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
	defer func() { _ = cursor.Close() }()
	record, err := cursor.Next(t.Context())
	if err != nil || record == nil || record.Seq() != 1 {
		t.Fatalf("Next = %#v, %v", record, err)
	}
}

func TestQuestionHistoryCursorReadsCompleteUnterminatedTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	header, err := encodeEventLogHeader(EventLogVersionV2)
	if err != nil {
		t.Fatalf("encode header: %v", err)
	}
	line, err := encodeEventRecordV2(
		mustQuestionHistoryCursorRecord(t, 1, "unterminated"),
	)
	if err != nil {
		t.Fatalf("encode unterminated Question: %v", err)
	}
	content := append(header, '\n')
	content = append(content, line...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write unterminated event log: %v", err)
	}
	cursor, err := OpenQuestionHistoryCursor(dir, 1)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer func() { _ = cursor.Close() }()
	record, err := cursor.Next(t.Context())
	if err != nil || record == nil || record.Seq() != 1 {
		t.Fatalf("unterminated Question = %#v, %v", record, err)
	}
}

func TestQuestionHistoryCursorCancellationInterruptsLargeRecordScan(t *testing.T) {
	dir := writeQuestionHistoryCursorIgnoredRecord(t, 4<<20)
	cursor, err := OpenQuestionHistoryCursor(dir, 2)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer func() { _ = cursor.Close() }()
	ctx := &cancelDuringQuestionHistoryReadContext{Context: t.Context()}
	if _, err := cursor.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("large-record scan error = %v, want context canceled", err)
	}
}

func questionHistoryCursorIgnoredRecord(t *testing.T, scalarBytes int) []byte {
	t.Helper()
	var buffer bytes.Buffer
	buffer.WriteString(`{"seq":1,"kind":"history_replaced","payload":{"engine":"local","mode":"handoff","items":[{"type":"other","raw":"`)
	buffer.Write(bytes.Repeat([]byte{'x'}, scalarBytes))
	buffer.WriteString(`"}]}}`)
	return buffer.Bytes()
}

func writeQuestionHistoryCursorIgnoredRecord(t *testing.T, scalarBytes int) string {
	t.Helper()
	dir := t.TempDir()
	writeRawVersionedEventLog(
		t,
		filepath.Join(dir, eventsFile),
		EventLogVersionV2,
		[][]byte{questionHistoryCursorIgnoredRecord(t, scalarBytes)},
	)
	return dir
}

func writeQuestionHistoryCursorOversizedFieldName(t *testing.T, tokenBytes int) string {
	t.Helper()
	dir := t.TempDir()
	var line bytes.Buffer
	line.WriteString(`{"seq":1,"kind":"message","`)
	line.Write(bytes.Repeat([]byte{'x'}, tokenBytes))
	line.WriteString(`":null,"payload":{"role":"user","content":[]}}`)
	writeRawVersionedEventLog(
		t,
		filepath.Join(dir, eventsFile),
		EventLogVersionV2,
		[][]byte{line.Bytes()},
	)
	return dir
}

func writeQuestionHistoryCursorOversizedToolName(t *testing.T, tokenBytes int) string {
	t.Helper()
	dir := t.TempDir()
	var line bytes.Buffer
	line.WriteString(`{"seq":1,"kind":"tool_completed","payload":{"name":"`)
	line.Write(bytes.Repeat([]byte{'x'}, tokenBytes))
	line.WriteString(`","is_error":false}}`)
	writeRawVersionedEventLog(
		t,
		filepath.Join(dir, eventsFile),
		EventLogVersionV2,
		[][]byte{line.Bytes()},
	)
	return dir
}

func TestQuestionHistoryCursorSurfacesCompleteDecodeFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsFile)
	header, err := encodeEventLogHeader(EventLogVersionV2)
	if err != nil {
		t.Fatalf("encode header: %v", err)
	}
	content := append(header, '\n')
	content = append(content, []byte(`{"seq":1,"kind":"tool_completed","payload":BROKEN}`)...)
	content = append(content, '\n')
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write malformed event log: %v", err)
	}
	cursor, err := OpenQuestionHistoryCursor(dir, 1)
	if err != nil {
		t.Fatalf("open cursor: %v", err)
	}
	defer func() { _ = cursor.Close() }()
	if _, err := cursor.Next(t.Context()); err == nil {
		t.Fatal("complete malformed record did not fail")
	}
}

func TestQuestionHistoryCursorDoesNotWaitForSessionMutationLock(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	if _, _, err := log.AppendRecord(nil, mustQuestionHistoryCursorPayload(t, "visible")); err != nil {
		t.Fatalf("append fixture: %v", err)
	}
	store.mutationMu.Lock()
	defer store.mutationMu.Unlock()
	cursor, err := OpenQuestionHistoryCursor(store.Dir(), 1)
	if err != nil {
		t.Fatalf("open cursor while mutation lock held: %v", err)
	}
	defer func() { _ = cursor.Close() }()
	record, err := cursor.Next(t.Context())
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

func mustQuestionHistoryCursorRecord(t *testing.T, sequence int64, question string) EventRecord {
	t.Helper()
	committedAt := transcript.CommittedAtUnixMs(sequence)
	record, err := newEventRecord(
		sequence,
		nil,
		mustQuestionHistoryCursorPayload(t, question),
		&committedAt,
	)
	if err != nil {
		t.Fatalf("create Question-history cursor record: %v", err)
	}
	return record
}

func mustQuestionHistoryCursorRecordV1(t *testing.T, sequence int64, question string) EventRecord {
	t.Helper()
	record, err := NewEventRecord(sequence, nil, mustQuestionHistoryCursorPayload(t, question))
	if err != nil {
		t.Fatalf("create v1 Question-history cursor record: %v", err)
	}
	return record
}

func mustQuestionHistoryCursorPayload(t *testing.T, question string) ToolCompletionRecord {
	t.Helper()
	return ToolCompletionRecord{
		CallID:     "call-" + question,
		Name:       askQuestionToolName,
		OutputKind: ToolOutputKindFunction,
		Output:     []byte(`"answer"`),
		Presentation: transcript.EncodeToolCallMeta(transcript.ToolCallMeta{
			ToolName: askQuestionToolName,
			Question: question,
		}),
		QuestionAnswer: &QuestionAnswerRecord{Freeform: stringPointer("answer")},
	}
}

func writeQuestionHistoryCursorLog(
	t *testing.T,
	path string,
	version int,
	records []EventRecord,
) {
	t.Helper()
	lines := make([][]byte, 0, len(records))
	for _, record := range records {
		line, err := encodeEventRecordForVersion(version, record)
		if err != nil {
			t.Fatalf("encode v%d Question-history cursor record: %v", version, err)
		}
		lines = append(lines, line)
	}
	writeRawVersionedEventLog(t, path, version, lines)
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

type cancelDuringQuestionHistoryReadContext struct {
	context.Context
	errCalls int
}

func (c *cancelDuringQuestionHistoryReadContext) Err() error {
	c.errCalls++
	if c.errCalls >= 7 {
		return context.Canceled
	}
	return nil
}
