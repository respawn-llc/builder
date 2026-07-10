package driver_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/driver"
)

const commandTestTimeout = 5 * time.Second

func TestRunCommandCapturesOutputAndAnalyzes(t *testing.T) {
	t.Parallel()

	binary := buildHelper(t)
	capture, err := driver.RunCommand(context.Background(), driver.CommandSpec{
		Path:       binary,
		Args:       []string{"write"},
		Dimensions: pty.MustDimensions(3, 16),
		Timeout:    commandTestTimeout,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if len(capture.Chunks) == 0 {
		t.Fatalf("chunk count = 0, want captured output")
	}
	if capture.ProcessExit == nil || capture.ProcessExit.Code != 0 {
		t.Fatalf("process exit = %#v, want code 0", capture.ProcessExit)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := analysis.Screen.TextInRegion(pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 5}); got != "hello" {
		t.Fatalf("screen text = %q, want %q", got, "hello")
	}
}

func TestRunCommandPreservesEmptyCapture(t *testing.T) {
	t.Parallel()

	binary := buildHelper(t)
	capture, err := driver.RunCommand(context.Background(), driver.CommandSpec{
		Path:       binary,
		Args:       []string{"no-output"},
		Dimensions: pty.MustDimensions(2, 8),
		Timeout:    commandTestTimeout,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if len(capture.Chunks) != 0 || len(capture.Raw) != 0 {
		t.Fatalf("capture should stay empty, got chunks=%#v raw=%#v", capture.Chunks, capture.Raw)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !analysis.Screen.IsBlank() {
		t.Fatalf("screen should be blank: %#v", analysis.Screen)
	}
}

func TestRunCommandFeedsInputAndAppliesResize(t *testing.T) {
	t.Parallel()

	binary := buildHelper(t)
	capture, err := driver.RunCommand(context.Background(), driver.CommandSpec{
		Path:       binary,
		Args:       []string{"echo-byte"},
		Dimensions: pty.MustDimensions(2, 12),
		Inputs: []driver.InputEvent{{
			After: 10 * time.Millisecond,
			Bytes: []byte("x\n"),
		}},
		Resizes: []driver.ResizeEvent{{
			After:      5 * time.Millisecond,
			Dimensions: pty.MustDimensions(3, 14),
		}},
		Timeout: commandTestTimeout,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if len(capture.Resizes) != 1 {
		t.Fatalf("capture resize count = %d, want 1", len(capture.Resizes))
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	foundResize := false
	for _, operation := range analysis.Operations {
		if operation.Kind == pty.OperationResize && operation.Region == (pty.Region{Top: 0, Bottom: 3, Left: 0, Right: 14}) {
			foundResize = true
		}
	}
	if !foundResize {
		t.Fatalf("resize operation not found in %#v", analysis.Operations)
	}
}

func TestRunCommandRecordsResizeAtActualCapturePosition(t *testing.T) {
	t.Parallel()

	binary := buildHelper(t)
	capture, err := driver.RunCommand(context.Background(), driver.CommandSpec{
		Path:       binary,
		Args:       []string{"resize-order"},
		Dimensions: pty.MustDimensions(2, 12),
		Resizes: []driver.ResizeEvent{{
			After:      200 * time.Millisecond,
			Dimensions: pty.MustDimensions(3, 14),
		}},
		Timeout: commandTestTimeout,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if len(capture.Resizes) != 1 {
		t.Fatalf("resize count = %d, want 1", len(capture.Resizes))
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	var resizeIndex *int
	var afterWriteIndex *int
	for i, operation := range analysis.Operations {
		if operation.Kind == pty.OperationResize {
			index := i
			resizeIndex = &index
		}
		if operation.Kind == pty.OperationWrite && operation.Write != nil && operation.Write.Text() == "after" {
			index := i
			afterWriteIndex = &index
		}
	}
	if resizeIndex == nil || afterWriteIndex == nil || *resizeIndex > *afterWriteIndex {
		t.Fatalf("operation order resize=%v after_write=%v operations=%#v", resizeIndex, afterWriteIndex, analysis.Operations)
	}
}

func TestRunCommandRecordsResizeBeforeFirstChunk(t *testing.T) {
	t.Parallel()

	binary := buildHelper(t)
	capture, err := driver.RunCommand(context.Background(), driver.CommandSpec{
		Path:       binary,
		Args:       []string{"resize-before-write"},
		Dimensions: pty.MustDimensions(2, 12),
		Resizes: []driver.ResizeEvent{{
			After:      10 * time.Millisecond,
			Dimensions: pty.MustDimensions(3, 14),
		}},
		Timeout: commandTestTimeout,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if len(capture.Resizes) != 1 || capture.Resizes[0].Placement.Kind != pty.ResizeBeforeFirstChunk {
		t.Fatalf("resizes = %#v, want one pre-first-chunk resize", capture.Resizes)
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(analysis.Operations) == 0 || analysis.Operations[0].Kind != pty.OperationResize {
		t.Fatalf("first operation = %#v, want resize before output", analysis.Operations)
	}
}

func TestRunCommandTimeoutTerminatesProcessAndReader(t *testing.T) {
	t.Parallel()

	binary := buildHelper(t)
	capture, err := driver.RunCommand(context.Background(), driver.CommandSpec{
		Path:       binary,
		Args:       []string{"hang"},
		Dimensions: pty.MustDimensions(2, 8),
		Timeout:    100 * time.Millisecond,
	})
	if !errors.As(err, new(*driver.TimeoutError)) {
		t.Fatalf("RunCommand error = %v, want TimeoutError", err)
	}
	if capture.ProcessExit == nil {
		t.Fatalf("process exit not recorded after timeout cleanup")
	}
	if !capture.ReadLoopDone {
		t.Fatalf("read loop completion not recorded after timeout cleanup")
	}
}

func buildHelper(t *testing.T) string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "ansi-writer")
	if err := driver.BuildPackage(context.Background(), "core/internal/testharness/pty/testdata/cmd/ansi-writer", output); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("BuildPackage: %v\n%s", err, string(exitErr.Stderr))
		}
		t.Fatalf("BuildPackage: %v", err)
	}
	return output
}
