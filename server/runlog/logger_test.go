package runlog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"core/server/runtime"
	"core/server/session"
	"core/server/session/sessiontest"
	"core/shared/sessioncontract"
)

func TestRunLoggerWritesStepsFile(t *testing.T) {
	dir := t.TempDir()
	logger, err := NewRunLogger(dir, nil)
	if err != nil {
		t.Fatalf("NewRunLogger: %v", err)
	}
	logger.Logf("step.start user_chars=%d", 10)
	if err := logger.Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, RunLogFileName))
	if err != nil {
		t.Fatalf("stat run log: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("run log is empty")
	}
}

func TestRunLoggerNoopsWhenSessionDirDoesNotExist(t *testing.T) {
	missingDir := filepath.Join(t.TempDir(), "missing-session")
	logger, err := NewRunLogger(missingDir, nil)
	if err != nil {
		t.Fatalf("NewRunLogger: %v", err)
	}
	logger.Logf("hello")
	if err := logger.Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}
	if _, err := os.Stat(filepath.Join(missingDir, RunLogFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat missing run log: %v", err)
	}
}

type failingWriteCloser struct{}

func (failingWriteCloser) WriteString(string) (int, error) {
	return 0, errors.New("disk full")
}

func (failingWriteCloser) Close() error {
	return nil
}

func TestRunLoggerReportsWriteFailureOnce(t *testing.T) {
	var diagnostics []RunLoggerDiagnostic
	logger := &RunLogger{
		fp: failingWriteCloser{},
		onDiagnostic: func(diag RunLoggerDiagnostic) {
			diagnostics = append(diagnostics, diag)
		},
	}
	logger.Logf("first")
	logger.Logf("second")

	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}
	if diagnostics[0].Kind != "write_failed" {
		t.Fatalf("diagnostic = %+v, want write_failed", diagnostics[0])
	}
	if diagnostics[0].Err == nil || !strings.Contains(diagnostics[0].Err.Error(), "disk full") {
		t.Fatalf("diagnostic error = %v, want disk full", diagnostics[0].Err)
	}
}

func TestDurabilityObserverAggregatesTypedSessionAndFlushObservations(t *testing.T) {
	observer := NewDurabilityObserver()
	observer.ObserveEventLogAppend(session.EventLogAppendObservation{
		RecordCount: 3,
		Latency:     4 * time.Millisecond,
		Succeeded:   true,
	})
	observer.ObserveEventLogSync(session.EventLogSyncObservation{
		Latency:   2 * time.Millisecond,
		Succeeded: true,
	})
	observer.ObserveResultGroupFlush(runtime.ResultGroupFlushObservation{
		Reason:      runtime.ResultGroupFlushStepBoundary,
		ResultCount: 2,
		RecordCount: 3,
		Latency:     6 * time.Millisecond,
		Succeeded:   true,
	})

	snapshot := observer.Snapshot()
	if snapshot.AppendTransactions != 1 || snapshot.PhysicalSyncs != 1 {
		t.Fatalf("durability counts = %+v, want one append and one sync", snapshot)
	}
	if len(snapshot.AppendRecordCounts) != 1 || snapshot.AppendRecordCounts[0] != 3 {
		t.Fatalf("append record counts = %v, want [3]", snapshot.AppendRecordCounts)
	}
	if len(snapshot.AppendLatencies) != 1 || snapshot.AppendLatencies[0] != 4*time.Millisecond {
		t.Fatalf("append latencies = %v, want [4ms]", snapshot.AppendLatencies)
	}
	if len(snapshot.SyncLatencies) != 1 || snapshot.SyncLatencies[0] != 2*time.Millisecond {
		t.Fatalf("sync latencies = %v, want [2ms]", snapshot.SyncLatencies)
	}
	if len(snapshot.Flushes) != 1 {
		t.Fatalf("flush observations = %d, want 1", len(snapshot.Flushes))
	}
	if snapshot.Flushes[0].Reason != runtime.ResultGroupFlushStepBoundary {
		t.Fatalf("flush reason = %v, want step boundary", snapshot.Flushes[0].Reason)
	}
}

func TestDurabilityLoggerFailureDoesNotAlterAppendReceipt(t *testing.T) {
	observer := NewDurabilityObserver()
	logger := &RunLogger{fp: failingWriteCloser{}}
	observer.Attach(logger)
	persistence := sessiontest.NewPersistence()
	options := append(
		persistence.Options(),
		session.WithDurabilityObserver(observer),
	)
	store, err := session.Create(
		t.TempDir(),
		"workspace",
		t.TempDir(),
		sessioncontract.SessionCategoryMain,
		options...,
	)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	eventLog, err := store.MaterializeEventLog()
	if err != nil {
		t.Fatalf("materialize event log: %v", err)
	}
	content := "durable"

	_, receipt, err := eventLog.AppendRecord(nil, session.MessageRecord{
		Role:    session.MessageRoleAssistant,
		Content: &content,
	})
	if err != nil {
		t.Fatalf("append record with failed durability logger: %v", err)
	}
	if !receipt.Committed {
		t.Fatal("append receipt is uncommitted after durability logger failure")
	}
	snapshot := observer.Snapshot()
	if snapshot.AppendTransactions != 1 || snapshot.PhysicalSyncs != 1 {
		t.Fatalf("durability snapshot = %+v, want one committed append and sync", snapshot)
	}
}
