package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"core/shared/rollbacktarget"
)

func TestCurrentEventLogCreatesHeaderOnlyLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current event log: %v", err)
	}
	reader := bufio.NewReader(bytes.NewReader(contents))
	headerLine, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read current event log header line: %v", err)
	}
	if _, err := decodeEventLogHeader(headerLine); err != nil {
		t.Fatalf("decode current event log header: %v", err)
	}
	eventBytes, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read current event log body: %v", err)
	}
	if len(eventBytes) != 0 {
		t.Fatalf("header-only log contains event bytes: %q", eventBytes)
	}

	window, err := log.readSegmentForward(0, 32, nil)
	if err != nil {
		t.Fatalf("read header-only log: %v", err)
	}
	if len(window.Records) != 0 || !window.ReachedStart || !window.ReachedEnd {
		t.Fatalf("header-only window = %#v", window)
	}
	if window.StartOffset != log.firstEventOffset || window.EndOffset != log.firstEventOffset {
		t.Fatalf(
			"header-only cursors = [%d,%d], want first-event offset %d",
			window.StartOffset,
			window.EndOffset,
			log.firstEventOffset,
		)
	}
}

func TestCurrentEventLogAppendAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	options := eventLogOptions{fsyncPolicy: EventLogFSyncAlways}
	log, err := createCurrentEventLog(path, options)
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	firstContent := "first"
	secondContent := "second"
	first, err := NewEventRecord(1, stringPointer("step-1"), MessageRecord{
		Role:    MessageRoleUser,
		Content: &firstContent,
	})
	if err != nil {
		t.Fatalf("create first record: %v", err)
	}
	second, err := NewEventRecord(2, nil, MessageRecord{
		Role:    MessageRoleAssistant,
		Content: &secondContent,
	})
	if err != nil {
		t.Fatalf("create second record: %v", err)
	}

	endOffset, err := log.appendRecords([]EventRecord{first, second})
	if err != nil {
		t.Fatalf("append current records: %v", err)
	}
	if endOffset <= log.firstEventOffset {
		t.Fatalf("append end offset = %d, want after header offset %d", endOffset, log.firstEventOffset)
	}

	reopened, err := openCurrentEventLog(path, currentEventLogAuthoritative, options)
	if err != nil {
		t.Fatalf("reopen current event log: %v", err)
	}
	if reopened.lastSequence != 2 {
		t.Fatalf("last sequence = %d, want 2", reopened.lastSequence)
	}
	window, err := reopened.readSegmentForward(0, 7, nil)
	if err != nil {
		t.Fatalf("read reopened current event log: %v", err)
	}
	if len(window.Records) != 2 || window.Records[0].Seq() != 1 || window.Records[1].Seq() != 2 {
		t.Fatalf("reopened records = %#v", window.Records)
	}
	if window.StartOffset != reopened.firstEventOffset || window.EndOffset != endOffset {
		t.Fatalf(
			"reopened cursors = [%d,%d], want [%d,%d]",
			window.StartOffset,
			window.EndOffset,
			reopened.firstEventOffset,
			endOffset,
		)
	}
	if !window.ReachedStart || !window.ReachedEnd {
		t.Fatalf("reopened window boundaries = %#v", window)
	}
}

func TestCurrentEventLogToolCompletionProviderPathsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	options := eventLogOptions{fsyncPolicy: EventLogFSyncAlways}
	log, err := createCurrentEventLog(path, options)
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	functionRaw := json.RawMessage(`{ "type" : "function_call_output", "call_id" : "call-function", "output" : "done" }`)
	customRaw := json.RawMessage(`{ "type" : "custom_tool_call_output", "call_id" : "call-custom", "output" : "patched" }`)
	errorRaw := json.RawMessage(`{ "type" : "function_call_output", "call_id" : "call-error", "output" : "{\"error\":\"failed\"}" }`)
	imageOutputRaw := json.RawMessage(`{ "type" : "function_call_output", "call_id" : "call-image", "output" : "attached file content" }`)
	imageAttachmentRaw := json.RawMessage(`{ "type" : "message", "role" : "user", "content" : [ { "type" : "input_file", "file_id" : "file-1" } ] }`)
	inputs := []ToolCompletionRecord{
		{
			CallID:     "call-function",
			Name:       "exec_command",
			OutputKind: ToolOutputKindFunction,
			Output:     json.RawMessage(`"done"`),
			ProviderItems: []ToolCompletionProviderItem{{
				Type: ProviderInputItemTypeFunctionCallOutput, CallID: stringPointer("call-function"), Raw: functionRaw,
			}},
		},
		{
			CallID:     "call-custom",
			Name:       "patch",
			OutputKind: ToolOutputKindCustom,
			Output:     json.RawMessage(`"patched"`),
			ProviderItems: []ToolCompletionProviderItem{{
				Type: ProviderInputItemTypeCustomToolOutput, CallID: stringPointer("call-custom"), Raw: customRaw,
			}},
		},
		{
			CallID:     "call-error",
			Name:       "exec_command",
			OutputKind: ToolOutputKindFunction,
			IsError:    true,
			Output:     json.RawMessage(`{"error":"failed"}`),
			ProviderItems: []ToolCompletionProviderItem{{
				Type: ProviderInputItemTypeFunctionCallOutput, CallID: stringPointer("call-error"), Raw: errorRaw,
			}},
		},
		{
			CallID:     "call-image",
			Name:       "view_image",
			OutputKind: ToolOutputKindFunction,
			Output:     json.RawMessage(`[{"type":"input_file","file_id":"file-1"}]`),
			ProviderItems: []ToolCompletionProviderItem{
				{Type: ProviderInputItemTypeFunctionCallOutput, CallID: stringPointer("call-image"), Raw: imageOutputRaw},
				{
					Type: ProviderInputItemTypeOther, Name: stringPointer("view_image"),
					CallID: stringPointer("call-image"), Raw: imageAttachmentRaw,
					LinkedCallID: stringPointer("call-image"),
					LinkKind:     providerItemLinkKindPointer(ProviderItemLinkToolOutputAttachment),
				},
			},
		},
	}
	records := make([]EventRecord, 0, len(inputs))
	for index, input := range inputs {
		record, recordErr := NewEventRecord(int64(index+1), nil, input)
		if recordErr != nil {
			t.Fatalf("create tool completion record %d: %v", index, recordErr)
		}
		records = append(records, record)
	}
	if _, err := log.appendRecords(records); err != nil {
		t.Fatalf("append tool completion records: %v", err)
	}

	reopened, err := openCurrentEventLog(path, currentEventLogAuthoritative, options)
	if err != nil {
		t.Fatalf("reopen current event log: %v", err)
	}
	window, err := reopened.readRecentRecords(len(inputs), 1024)
	if err != nil {
		t.Fatalf("read reopened completions: %v", err)
	}
	if len(window.Records) != len(inputs) {
		t.Fatalf("reopened records = %d, want %d", len(window.Records), len(inputs))
	}
	for index, event := range window.Records {
		completion, ok := mustEventRecordPayload(event).(ToolCompletionRecord)
		if !ok {
			t.Fatalf("payload %d type = %T, want ToolCompletionRecord", index, mustEventRecordPayload(event))
		}
		if !reflect.DeepEqual(completion, mustEventRecordPayload(records[index])) {
			t.Fatalf("reopened completion %d = %#v, want %#v", index, completion, mustEventRecordPayload(records[index]))
		}
	}
}

func TestCurrentEventLogHistoryReplacementProviderRawSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	options := eventLogOptions{fsyncPolicy: EventLogFSyncAlways}
	log, err := createCurrentEventLog(path, options)
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	raw := json.RawMessage(`{ "type" : "other", "escaped" : "\u0061", "number" : 1.2300 }`)
	record, err := NewEventRecord(1, nil, HistoryReplacementRecord{
		Engine:                            "local",
		Mode:                              CompactionModeHandoff,
		WorkflowRunID:                     stringPointer("workflow-run-1"),
		CompactionNumber:                  intPointer(2),
		CommittedEntryStart:               intPointer(8),
		PendingHandoffFutureMessage:       stringPointer("continue"),
		LastCommittedAssistantFinalAnswer: stringPointer("answer"),
		LatestRollbackCandidate: &rollbacktarget.CandidateLocator{
			UserMessageSeq:       5,
			CandidatePageEndByte: 2048,
		},
		Items: []ProviderHistoryItem{{
			Type: ProviderHistoryItemTypeOther,
			Raw:  raw,
		}},
	})
	if err != nil {
		t.Fatalf("create history replacement: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{record}); err != nil {
		t.Fatalf("append history replacement: %v", err)
	}

	reopened, err := openCurrentEventLog(path, currentEventLogAuthoritative, options)
	if err != nil {
		t.Fatalf("reopen current event log: %v", err)
	}
	window, err := reopened.readRecentRecords(1, 1024)
	if err != nil {
		t.Fatalf("read reopened history replacement: %v", err)
	}
	if len(window.Records) != 1 {
		t.Fatalf("reopened record count = %d, want 1", len(window.Records))
	}
	replacement, ok := mustEventRecordPayload(window.Records[0]).(HistoryReplacementRecord)
	if !ok {
		t.Fatalf("payload type = %T, want HistoryReplacementRecord", mustEventRecordPayload(window.Records[0]))
	}
	if !reflect.DeepEqual(replacement, mustEventRecordPayload(record)) {
		t.Fatalf("reopened replacement = %#v, want %#v", replacement, mustEventRecordPayload(record))
	}
	if !bytes.Equal(replacement.Items[0].Raw, raw) {
		t.Fatalf("reopened Raw = %s, want exact %s", replacement.Items[0].Raw, raw)
	}
}

func TestCurrentEventLogAppendRejectsSequenceGapWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat current event log before rejected append: %v", err)
	}
	content := "out of sequence"
	record, err := NewEventRecord(2, nil, MessageRecord{
		Role:    MessageRoleUser,
		Content: &content,
	})
	if err != nil {
		t.Fatalf("create out-of-sequence record: %v", err)
	}

	if _, err := log.appendRecords([]EventRecord{record}); err == nil {
		t.Fatal("expected sequence validation error")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat current event log after rejected append: %v", err)
	}
	if after.Size() != before.Size() || log.lastSequence != 0 {
		t.Fatalf(
			"rejected append mutated log: size %d -> %d, last sequence %d",
			before.Size(),
			after.Size(),
			log.lastSequence,
		)
	}
}

func TestCurrentEventLogTornTailIsReadOnlyUntilAuthoritativeOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	options := eventLogOptions{fsyncPolicy: EventLogFSyncAlways}
	log, err := createCurrentEventLog(path, options)
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	content := "committed"
	record, err := NewEventRecord(1, nil, MessageRecord{
		Role:    MessageRoleUser,
		Content: &content,
	})
	if err != nil {
		t.Fatalf("create committed record: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{record}); err != nil {
		t.Fatalf("append committed record: %v", err)
	}
	committedContents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed current event log: %v", err)
	}
	fp, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open current event log for torn append: %v", err)
	}
	if _, err := fp.Write([]byte(`{"seq":2,"kind":"message","payload":{"role":"assistant"`)); err != nil {
		_ = fp.Close()
		t.Fatalf("write torn event tail: %v", err)
	}
	if err := fp.Close(); err != nil {
		t.Fatalf("close torn current event log: %v", err)
	}
	tornContents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read torn current event log: %v", err)
	}

	readOnly, err := openCurrentEventLog(path, currentEventLogReadOnly, options)
	if err != nil {
		t.Fatalf("open torn current event log read-only: %v", err)
	}
	if readOnly.lastSequence != 1 {
		t.Fatalf("read-only last sequence = %d, want 1", readOnly.lastSequence)
	}
	afterReadOnly, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current event log after read-only open: %v", err)
	}
	if !bytes.Equal(afterReadOnly, tornContents) {
		t.Fatal("read-only open mutated torn current event log")
	}

	authoritative, err := openCurrentEventLog(path, currentEventLogAuthoritative, options)
	if err != nil {
		t.Fatalf("open torn current event log authoritatively: %v", err)
	}
	if authoritative.lastSequence != 1 {
		t.Fatalf("authoritative last sequence = %d, want 1", authoritative.lastSequence)
	}
	afterAuthoritative, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read repaired current event log: %v", err)
	}
	if !bytes.Equal(afterAuthoritative, committedContents) {
		t.Fatalf("authoritative repair = %q, want committed bytes %q", afterAuthoritative, committedContents)
	}
}

func TestCurrentEventLogRejectsCompleteInvalidFinalRecordWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	options := eventLogOptions{fsyncPolicy: EventLogFSyncAlways}
	if _, err := createCurrentEventLog(path, options); err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	appendCurrentTestBytes(t, path, []byte(`{"seq":1,"kind":"unsupported","payload":{}}`))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read invalid current event log: %v", err)
	}

	if _, err := openCurrentEventLog(path, currentEventLogReadOnly, options); err == nil {
		t.Fatal("expected read-only strict contract error")
	}
	afterReadOnly, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current event log after read-only rejection: %v", err)
	}
	if !bytes.Equal(afterReadOnly, before) {
		t.Fatal("read-only strict contract rejection mutated current event log")
	}

	if _, err := openCurrentEventLog(path, currentEventLogAuthoritative, options); err == nil {
		t.Fatal("expected authoritative strict contract error")
	}
	afterAuthoritative, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current event log after authoritative rejection: %v", err)
	}
	if !bytes.Equal(afterAuthoritative, before) {
		t.Fatal("authoritative strict contract rejection mutated current event log")
	}
}

func TestCurrentEventLogPreservesValidFinalRecordWithoutNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	options := eventLogOptions{fsyncPolicy: EventLogFSyncAlways}
	_, err := createCurrentEventLog(path, options)
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	content := "valid without terminator"
	record, err := NewEventRecord(1, nil, MessageRecord{
		Role:    MessageRoleUser,
		Content: &content,
	})
	if err != nil {
		t.Fatalf("create record: %v", err)
	}
	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	fp, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open current event log for unterminated append: %v", err)
	}
	if _, err := fp.Write(line); err != nil {
		_ = fp.Close()
		t.Fatalf("write unterminated record: %v", err)
	}
	if err := fp.Close(); err != nil {
		t.Fatalf("close unterminated current event log: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read unterminated current event log: %v", err)
	}

	opened, err := openCurrentEventLog(path, currentEventLogAuthoritative, options)
	if err != nil {
		t.Fatalf("open valid unterminated current event log: %v", err)
	}
	afterOpen, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current event log after open: %v", err)
	}
	if !bytes.Equal(afterOpen, before) {
		t.Fatal("authoritative open changed a valid unterminated record")
	}

	nextContent := "next"
	next, err := NewEventRecord(2, nil, MessageRecord{
		Role:    MessageRoleAssistant,
		Content: &nextContent,
	})
	if err != nil {
		t.Fatalf("create next record: %v", err)
	}
	if _, err := opened.appendRecords([]EventRecord{next}); err != nil {
		t.Fatalf("append after valid unterminated record: %v", err)
	}
	window, err := opened.readSegmentForward(0, 5, nil)
	if err != nil {
		t.Fatalf("read records after separator repair: %v", err)
	}
	if len(window.Records) != 2 || window.Records[0].Seq() != 1 || window.Records[1].Seq() != 2 {
		t.Fatalf("records after separator repair = %#v", window.Records)
	}
}

func TestCurrentEventLogPaginatesWithPhysicalHeaderInvisibleCursors(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	records := []EventRecord{
		currentTestMessageRecord(t, 1, "one"),
		currentTestHistoryReplacementRecord(t, 2),
		currentTestMessageRecord(t, 3, "three"),
		currentTestMessageRecord(t, 4, "four"),
		currentTestHistoryReplacementRecord(t, 5),
		currentTestMessageRecord(t, 6, "six"),
	}
	endOffset, err := log.appendRecords(records)
	if err != nil {
		t.Fatalf("append current event records: %v", err)
	}
	matchReplacement := func(record EventRecord) bool {
		return mustEventRecordKind(record) == EventKindHistoryReplace
	}

	newest, err := log.readNewestSegmentBackward(5, matchReplacement)
	if err != nil {
		t.Fatalf("read newest current segment: %v", err)
	}
	assertCurrentRecordSequences(t, newest.Records, 5, 6)
	if newest.ReachedStart || !newest.ReachedEnd || newest.EndOffset != endOffset {
		t.Fatalf("newest current segment boundaries = %#v", newest)
	}

	older, err := log.readSegmentBackward(newest.StartOffset, 5, matchReplacement)
	if err != nil {
		t.Fatalf("read older current segment: %v", err)
	}
	assertCurrentRecordSequences(t, older.Records, 2, 3, 4)
	if older.ReachedStart || older.ReachedEnd || older.EndOffset != newest.StartOffset {
		t.Fatalf("older current segment boundaries = %#v", older)
	}

	oldest, err := log.readSegmentBackward(older.StartOffset, 5, matchReplacement)
	if err != nil {
		t.Fatalf("read oldest current segment: %v", err)
	}
	assertCurrentRecordSequences(t, oldest.Records, 1)
	if !oldest.ReachedStart || oldest.ReachedEnd ||
		oldest.StartOffset != log.firstEventOffset || oldest.EndOffset != older.StartOffset {
		t.Fatalf("oldest current segment boundaries = %#v", oldest)
	}

	forward, err := log.readSegmentForward(0, 5, matchReplacement)
	if err != nil {
		t.Fatalf("read current segment forward from zero: %v", err)
	}
	assertCurrentRecordSequences(t, forward.Records, 1)
	if !forward.ReachedStart || forward.ReachedEnd ||
		forward.StartOffset != log.firstEventOffset || forward.EndOffset != older.StartOffset {
		t.Fatalf("first forward current segment boundaries = %#v", forward)
	}
}

func TestCurrentEventLogForwardReadsOnlyOneRecordPastPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	if _, err := log.appendRecords([]EventRecord{
		currentTestMessageRecord(t, 1, "one"),
		currentTestHistoryReplacementRecord(t, 2),
	}); err != nil {
		t.Fatalf("append current event records: %v", err)
	}
	appendCurrentTestLine(t, path, []byte(`{"seq":3,"kind":"unsupported","payload":{}}`))

	window, err := log.readSegmentForward(0, 1<<20, func(record EventRecord) bool {
		return mustEventRecordKind(record) == EventKindHistoryReplace
	})
	if err != nil {
		t.Fatalf("read first current page: %v", err)
	}
	assertCurrentRecordSequences(t, window.Records, 1)
}

func TestCurrentEventLogBackwardValidatesImmediateNewerRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	appendCurrentTestRecord(t, path, currentTestHistoryReplacementRecord(t, 1))
	appendCurrentTestRecord(t, path, currentTestMessageRecord(t, 3, "three"))
	newerOffset := appendCurrentTestRecord(t, path, currentTestMessageRecord(t, 2, "two"))

	if _, err := log.readSegmentBackward(newerOffset, 1, func(record EventRecord) bool {
		return mustEventRecordKind(record) == EventKindHistoryReplace
	}); err == nil {
		t.Fatal("expected backward seam sequence validation error")
	}
}

func TestCurrentEventLogBackwardReadsOnlyImmediateNewerRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	appendCurrentTestRecord(t, path, currentTestHistoryReplacementRecord(t, 1))
	appendCurrentTestRecord(t, path, currentTestMessageRecord(t, 2, "two"))
	newerOffset := appendCurrentTestRecord(t, path, currentTestMessageRecord(t, 3, "three"))
	appendCurrentTestLine(t, path, []byte(`{"seq":4,"kind":"unsupported","payload":{}}`))

	window, err := log.readSegmentBackward(newerOffset, 1<<20, func(record EventRecord) bool {
		return mustEventRecordKind(record) == EventKindHistoryReplace
	})
	if err != nil {
		t.Fatalf("read current page with malformed second newer record: %v", err)
	}
	assertCurrentRecordSequences(t, window.Records, 1, 2)
}

func TestCurrentEventLogRejectsLocalSequenceRegression(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	appendCurrentTestRecord(t, path, currentTestMessageRecord(t, 1, "one"))
	appendCurrentTestRecord(t, path, currentTestMessageRecord(t, 3, "three"))
	appendCurrentTestRecord(t, path, currentTestMessageRecord(t, 2, "two"))

	if _, err := log.readSegmentForward(0, 1, nil); err == nil {
		t.Fatal("expected forward local sequence regression error")
	}
	if _, err := log.readNewestSegmentBackward(1, nil); err == nil {
		t.Fatal("expected backward local sequence regression error")
	}
}

func TestCurrentEventLogForwardValidatesImmediateBoundaryRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	appendCurrentTestRecord(t, path, currentTestMessageRecord(t, 1, "one"))
	appendCurrentTestRecord(t, path, currentTestMessageRecord(t, 3, "three"))
	appendCurrentTestRecord(t, path, currentTestHistoryReplacementRecord(t, 2))

	if _, err := log.readSegmentForward(0, 1, func(record EventRecord) bool {
		return mustEventRecordKind(record) == EventKindHistoryReplace
	}); err == nil {
		t.Fatal("expected forward seam sequence validation error")
	}
}

func TestCurrentEventLogReadsRecentRecordsWithoutExposingHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	records := make([]EventRecord, 0, 6)
	for sequence := int64(1); sequence <= 6; sequence++ {
		records = append(records, currentTestMessageRecord(t, sequence, fmt.Sprintf("message-%d", sequence)))
	}
	endOffset, err := log.appendRecords(records)
	if err != nil {
		t.Fatalf("append current event records: %v", err)
	}

	window, err := log.readRecentRecords(3, 1)
	if err != nil {
		t.Fatalf("read recent current event records: %v", err)
	}
	assertCurrentRecordSequences(t, window.Records, 4, 5, 6)
	if window.ReachedStart || !window.ReachedEnd || window.EndOffset != endOffset ||
		window.StartOffset <= log.firstEventOffset {
		t.Fatalf("recent current event window = %#v", window)
	}

	all, err := log.readRecentRecords(10, 1)
	if err != nil {
		t.Fatalf("read all current event records: %v", err)
	}
	assertCurrentRecordSequences(t, all.Records, 1, 2, 3, 4, 5, 6)
	if !all.ReachedStart || !all.ReachedEnd || all.StartOffset != log.firstEventOffset {
		t.Fatalf("all current event window = %#v", all)
	}
}

func TestCurrentEventLogRecentReadInspectsOnlyOneOlderRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), eventsFile)
	log, err := createCurrentEventLog(path, eventLogOptions{
		fsyncPolicy: EventLogFSyncAlways,
	})
	if err != nil {
		t.Fatalf("create current event log: %v", err)
	}
	appendCurrentTestLine(t, path, []byte(`{"seq":1,"kind":"unsupported","payload":{}}`))
	appendCurrentTestRecord(t, path, currentTestMessageRecord(t, 2, "two"))
	appendCurrentTestRecord(t, path, currentTestMessageRecord(t, 3, "three"))
	appendCurrentTestRecord(t, path, currentTestMessageRecord(t, 4, "four"))

	window, err := log.readRecentRecords(2, 1)
	if err != nil {
		t.Fatalf("read recent records with malformed second older record: %v", err)
	}
	assertCurrentRecordSequences(t, window.Records, 3, 4)
	if window.ReachedStart {
		t.Fatalf("recent current event window unexpectedly reached start: %#v", window)
	}
}

func currentTestMessageRecord(t *testing.T, sequence int64, content string) EventRecord {
	t.Helper()
	record, err := NewEventRecord(sequence, nil, MessageRecord{
		Role:    MessageRoleUser,
		Content: &content,
	})
	if err != nil {
		t.Fatalf("create message record %d: %v", sequence, err)
	}
	return record
}

func currentTestHistoryReplacementRecord(t *testing.T, sequence int64) EventRecord {
	t.Helper()
	record, err := NewEventRecord(sequence, nil, HistoryReplacementRecord{
		Engine: "local",
		Mode:   CompactionModeAuto,
	})
	if err != nil {
		t.Fatalf("create history replacement record %d: %v", sequence, err)
	}
	return record
}

func assertCurrentRecordSequences(t *testing.T, records []EventRecord, want ...int64) {
	t.Helper()
	if len(records) != len(want) {
		t.Fatalf("record count = %d, want %d", len(records), len(want))
	}
	for index := range want {
		if records[index].Seq() != want[index] {
			t.Fatalf("record %d sequence = %d, want %d", index, records[index].Seq(), want[index])
		}
	}
}

func appendCurrentTestLine(t *testing.T, path string, line []byte) int64 {
	t.Helper()
	payload := append(append([]byte(nil), line...), '\n')
	return appendCurrentTestBytes(t, path, payload)
}

func appendCurrentTestBytes(t *testing.T, path string, payload []byte) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat current event log before raw append: %v", err)
	}
	fp, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open current event log for raw append: %v", err)
	}
	if _, err := fp.Write(payload); err != nil {
		_ = fp.Close()
		t.Fatalf("append raw current event line: %v", err)
	}
	if err := fp.Close(); err != nil {
		t.Fatalf("close raw current event append: %v", err)
	}
	return info.Size()
}

func appendCurrentTestRecord(t *testing.T, path string, record EventRecord) int64 {
	t.Helper()
	line, err := encodeEventRecordV1(record)
	if err != nil {
		t.Fatalf("encode raw current event record: %v", err)
	}
	return appendCurrentTestLine(t, path, line)
}
