package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRollingTUILoggerWritesUnderPersistenceRoot(t *testing.T) {
	root := t.TempDir()
	logger, err := newRollingTUILogger(root)
	if err != nil {
		t.Fatalf("newRollingTUILogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })

	logger.Logf("diagnostic=%d", 1)

	info, err := os.Stat(filepath.Join(root, tuiLogDirName, tuiLogFileName))
	if err != nil {
		t.Fatalf("stat tui log: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("expected tui log to contain a diagnostic entry")
	}
}

func TestRollingTUILoggerRotatesBySize(t *testing.T) {
	root := t.TempDir()
	logger, err := newRollingTUILogger(root)
	if err != nil {
		t.Fatalf("newRollingTUILogger: %v", err)
	}
	logger.maxBytes = 64
	logger.maxFiles = 3
	t.Cleanup(func() { _ = logger.Close() })

	logger.Logf("%080d", 1)
	logger.Logf("%080d", 2)

	if _, err := os.Stat(filepath.Join(root, tuiLogDirName, tuiLogFileName)); err != nil {
		t.Fatalf("current tui log missing after rotation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, tuiLogDirName, tuiLogFileName+".1")); err != nil {
		t.Fatalf("rotated tui log missing: %v", err)
	}
}

func TestMultiUILoggerFansOut(t *testing.T) {
	first := &testUILogger{}
	second := &testUILogger{}
	logger := newMultiUILogger(first, nil, second)

	logger.Logf("diagnostic=%d", 1)

	if len(first.lines) != 1 || len(second.lines) != 1 {
		t.Fatalf("expected fanout logs, first=%#v second=%#v", first.lines, second.lines)
	}
}
