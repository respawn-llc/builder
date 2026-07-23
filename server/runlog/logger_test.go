package runlog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
