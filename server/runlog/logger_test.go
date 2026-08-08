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

type countingWriteCloser struct {
	writes int
}

func (w *countingWriteCloser) WriteString(string) (int, error) {
	w.writes++
	return 1, nil
}

func (*countingWriteCloser) Close() error {
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

func TestDurabilityObserverStreamsTypedSessionAndFlushObservations(t *testing.T) {
	observer := NewDurabilityObserver()
	writer := &countingWriteCloser{}
	observer.Attach(&RunLogger{fp: writer})
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

	if writer.writes != 3 {
		t.Fatalf("durability log writes = %d, want one per observation", writer.writes)
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
}
