//go:build darwin || linux

package session

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"core/shared/transcript"
	"golang.org/x/sys/unix"
)

const (
	eventLogShortWriteHelperEnv = "KENT_EVENT_LOG_SHORT_WRITE_HELPER"
	eventLogShortWriteDirEnv    = "KENT_EVENT_LOG_SHORT_WRITE_DIR"
	eventLogShortWriteMetaEnv   = "KENT_EVENT_LOG_SHORT_WRITE_META"
	eventLogShortWriteLimitEnv  = "KENT_EVENT_LOG_SHORT_WRITE_LIMIT"
	eventLogShortWriteCountEnv  = "KENT_EVENT_LOG_SHORT_WRITE_COUNT"
)

func TestEventLogShortWritesPreserveEveryCompleteMultiRecordPrefix(t *testing.T) {
	for _, recordCount := range []int{2, 3} {
		inputs, encoded := uncertainCommitAppendFixture(t, recordCount)
		terminators := eventRecordTerminatorOffsets(t, encoded)
		if len(terminators) != recordCount {
			t.Fatalf("record terminators = %v, want %d", terminators, recordCount)
		}
		for boundary := 0; boundary < recordCount-1; boundary++ {
			for _, delta := range []int{-1, 0, 1} {
				name := fmt.Sprintf(
					"%d_records/boundary_%d/delta_%+d",
					recordCount,
					boundary+1,
					delta,
				)
				t.Run(name, func(t *testing.T) {
					store := newSessionTestStore(t)
					mustMaterializeSessionTestEventLog(t, store)
					before, err := os.Stat(filepath.Join(store.Dir(), eventsFile))
					if err != nil {
						t.Fatalf("stat event log before short write: %v", err)
					}
					cutoff := terminators[boundary] + delta
					runEventLogShortWriteHelper(t, store, recordCount, before.Size()+int64(cutoff))

					reopened := mustOpenSessionTestStore(t, store)
					reopenedLog := mustMaterializeSessionTestEventLog(t, reopened)
					if revision, err := reopenedLog.Revision(); err != nil {
						t.Fatalf("read reopened revision: %v", err)
					} else if revision != int64(boundary+1) {
						t.Fatalf(
							"reopened revision = %d, want complete prefix %d",
							revision,
							boundary+1,
						)
					}
					records, err := collectEvents(reopened)
					if err != nil {
						t.Fatalf("collect reopened records: %v", err)
					}
					if len(records) != boundary+1 {
						t.Fatalf(
							"reopened records = %d, want complete prefix %d",
							len(records),
							boundary+1,
						)
					}
					for index, record := range records {
						if record.Seq() != int64(index+1) {
							t.Fatalf(
								"reopened record %d sequence = %d, want %d",
								index,
								record.Seq(),
								index+1,
							)
						}
						got, err := record.Payload()
						if err != nil {
							t.Fatalf("read reopened record %d: %v", index, err)
						}
						if !reflect.DeepEqual(got, inputs[index].Payload) {
							t.Fatalf(
								"reopened record %d payload = %#v, want %#v",
								index,
								got,
								inputs[index].Payload,
							)
						}
					}
				})
			}
		}
	}
}

func TestEventLogFsyncFailureAfterCompleteWriteLatchesUnknownCommit(t *testing.T) {
	store := newSessionTestStore(t)
	log := mustMaterializeSessionTestEventLog(t, store)
	log.log.path = os.DevNull

	records, receipt, appendErr := log.AppendRecordsAtomic(nil, []EventRecordPayload{
		sessionTestMessage(MessageRoleAssistant, "complete write before fsync failure"),
	})
	if appendErr == nil {
		t.Fatal("append to an unsyncable file unexpectedly succeeded")
	}
	if receipt.Committed {
		t.Fatalf("fsync failure receipt = %+v, want uncertain failure", receipt)
	}
	if len(records) != 1 {
		t.Fatalf("fsync failure built records = %d, want 1", len(records))
	}
	var persistenceErr *EventLogPersistenceError
	if !errors.As(appendErr, &persistenceErr) ||
		persistenceErr.Certainty != EventLogCommitUnknown {
		t.Fatalf("fsync failure = %v, want unknown commit certainty", appendErr)
	}
	if _, _, laterErr := log.AppendRecord(
		nil,
		sessionTestMessage(MessageRoleUser, "must not retry after fsync failure"),
	); !errors.Is(laterErr, persistenceErr) {
		t.Fatalf("later append error = %v, want latched %v", laterErr, persistenceErr)
	}

	reopened := mustOpenSessionTestStore(t, store)
	reopenedLog := mustMaterializeSessionTestEventLog(t, reopened)
	if revision, err := reopenedLog.Revision(); err != nil {
		t.Fatalf("read reopened revision: %v", err)
	} else if revision != 0 {
		t.Fatalf("reopened revision = %d, want allowed absent append", revision)
	}
}

func TestEventLogShortWriteHelperProcess(t *testing.T) {
	if os.Getenv(eventLogShortWriteHelperEnv) != "1" {
		return
	}
	dir := os.Getenv(eventLogShortWriteDirEnv)
	metaJSON, err := base64.StdEncoding.DecodeString(os.Getenv(eventLogShortWriteMetaEnv))
	if err != nil {
		t.Fatalf("decode helper metadata: %v", err)
	}
	var meta Meta
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		t.Fatalf("unmarshal helper metadata: %v", err)
	}
	recordCount, err := strconv.Atoi(os.Getenv(eventLogShortWriteCountEnv))
	if err != nil {
		t.Fatalf("parse helper record count: %v", err)
	}
	limit, err := strconv.ParseUint(os.Getenv(eventLogShortWriteLimitEnv), 10, 64)
	if err != nil {
		t.Fatalf("parse helper file-size limit: %v", err)
	}
	store, err := OpenResolved(PersistedSessionRecord{
		SessionDir: dir,
		Meta:       &meta,
	})
	if err != nil {
		t.Fatalf("open helper store: %v", err)
	}
	log, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize helper event log: %v", err)
	}
	inputs, _ := uncertainCommitAppendFixture(t, recordCount)

	var original unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_FSIZE, &original); err != nil {
		t.Fatalf("read helper file-size limit: %v", err)
	}
	limited := original
	limited.Cur = limit
	signal.Ignore(unix.SIGXFSZ)
	defer signal.Reset(unix.SIGXFSZ)
	if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatalf("set helper file-size limit: %v", err)
	}
	defer func() {
		if err := unix.Setrlimit(unix.RLIMIT_FSIZE, &original); err != nil {
			t.Errorf("restore helper file-size limit: %v", err)
		}
	}()

	records, receipt, appendErr := log.AppendRecordBatchAtomic(inputs)
	if appendErr == nil {
		t.Fatal("short append unexpectedly succeeded")
	}
	if receipt.Committed {
		t.Fatalf("short append receipt = %+v, want uncertain failure", receipt)
	}
	if len(records) != recordCount {
		t.Fatalf("short append built %d records, want %d", len(records), recordCount)
	}
	var persistenceErr *EventLogPersistenceError
	if !errors.As(appendErr, &persistenceErr) ||
		persistenceErr.Certainty != EventLogCommitUnknown {
		t.Fatalf("short append error = %v, want unknown commit certainty", appendErr)
	}
	if _, _, laterErr := log.AppendRecord(
		nil,
		sessionTestMessage(MessageRoleUser, "must not retry after uncertain commit"),
	); !errors.Is(laterErr, persistenceErr) {
		t.Fatalf("later append error = %v, want latched %v", laterErr, persistenceErr)
	}
}

func uncertainCommitAppendFixture(
	t testing.TB,
	recordCount int,
) ([]EventRecordAppendInput, []byte) {
	t.Helper()
	committedAt, err := transcript.NewCommittedAtUnixMs(1_700_000_000_000)
	if err != nil {
		t.Fatalf("create fixed committed time: %v", err)
	}
	stepID := "11111111-1111-4111-8111-111111111111"
	inputs := make([]EventRecordAppendInput, recordCount)
	records := make([]EventRecord, recordCount)
	for index := range inputs {
		payload := sessionTestMessage(
			MessageRoleAssistant,
			fmt.Sprintf("uncertain commit record %d", index+1),
		)
		inputs[index] = EventRecordAppendInput{
			StepID:              &stepID,
			Payload:             payload,
			committedAtUnixMs:   &committedAt,
			preserveCommittedAt: true,
		}
		record, err := newEventRecord(
			int64(index+1),
			&stepID,
			payload,
			&committedAt,
		)
		if err != nil {
			t.Fatalf("create encoded fixture record %d: %v", index, err)
		}
		records[index] = record
	}
	encoded, err := encodeCurrentEventRecordLines(records, false)
	if err != nil {
		t.Fatalf("encode fixture records: %v", err)
	}
	return inputs, encoded
}

func eventRecordTerminatorOffsets(t testing.TB, encoded []byte) []int {
	t.Helper()
	offsets := make([]int, 0)
	for index, value := range encoded {
		if value == '\n' {
			offsets = append(offsets, index+1)
		}
	}
	return offsets
}

func runEventLogShortWriteHelper(
	t testing.TB,
	store *Store,
	recordCount int,
	limit int64,
) {
	t.Helper()
	metaJSON, err := json.Marshal(store.Meta())
	if err != nil {
		t.Fatalf("marshal helper metadata: %v", err)
	}
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestEventLogShortWriteHelperProcess$",
	)
	command.Env = append(os.Environ(),
		eventLogShortWriteHelperEnv+"=1",
		eventLogShortWriteDirEnv+"="+store.Dir(),
		eventLogShortWriteMetaEnv+"="+base64.StdEncoding.EncodeToString(metaJSON),
		eventLogShortWriteLimitEnv+"="+strconv.FormatInt(limit, 10),
		eventLogShortWriteCountEnv+"="+strconv.Itoa(recordCount),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run short-write helper: %v\n%s", err, output)
	}
}
