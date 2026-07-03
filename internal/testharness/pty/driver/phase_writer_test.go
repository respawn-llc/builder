package driver_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/driver"
)

func TestPhaseWriterFixtureProducesResolvableWindow(t *testing.T) {
	t.Parallel()

	output := filepath.Join(t.TempDir(), "phase-writer")
	if err := driver.BuildPackage(context.Background(), "core/internal/testharness/pty/testdata/cmd/phase-writer", output); err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	capture, err := driver.RunCommand(context.Background(), driver.CommandSpec{
		Path:       output,
		Dimensions: pty.MustDimensions(3, 20),
		Resizes: []driver.ResizeEvent{{
			After:      time.Millisecond,
			Dimensions: pty.MustDimensions(4, 20),
		}},
		Timeout: commandTestTimeout,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	windows, err := pty.ResolveOperationWindows(analysis)
	if err != nil {
		t.Fatalf("ResolveOperationWindows: %v", err)
	}
	if len(windows) != 1 {
		t.Fatalf("window count = %d, want 1: %#v", len(windows), windows)
	}
	if len(analysis.PhaseEvents) != 4 {
		t.Fatalf("phase event count = %d, want 4: %#v", len(analysis.PhaseEvents), analysis.PhaseEvents)
	}
}
