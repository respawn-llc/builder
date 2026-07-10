package driver_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
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

func TestRunCommandDelaysInputRelativeToPhase(t *testing.T) {
	output := filepath.Join(t.TempDir(), "phase-input-writer")
	if err := driver.BuildPackage(context.Background(), "core/internal/testharness/pty/testdata/cmd/phase-input-writer", output); err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	delay := 100 * time.Millisecond
	capture, err := driver.RunCommand(context.Background(), driver.CommandSpec{
		Path:       output,
		Dimensions: pty.MustDimensions(2, 16),
		PhaseInputs: []driver.PhaseInputEvent{{
			Phase: pty.PhaseScenarioStart,
			After: delay,
			Bytes: []byte("x\n"),
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
	if len(analysis.PhaseEvents) != 1 {
		t.Fatalf("phase events = %#v, want one scenario start", analysis.PhaseEvents)
	}
	if len(capture.PhaseInputDispatches) != 1 {
		t.Fatalf("phase input dispatches = %#v, want one", capture.PhaseInputDispatches)
	}
	dispatch := capture.PhaseInputDispatches[0]
	if dispatch.Phase != pty.PhaseScenarioStart || dispatch.ScheduledAfter != delay {
		t.Fatalf("phase input dispatch = %#v, want scenario-start after %s", dispatch, delay)
	}
	if elapsed := dispatch.StartedAt - analysis.PhaseEvents[0].CapturedAt; elapsed < delay {
		t.Fatalf("recorded phase input elapsed = %s, want at least %s", elapsed, delay)
	}
	var inputWriteAt *time.Duration
	for _, operation := range analysis.Operations {
		if operation.Write != nil && operation.Write.Text == "input:x" {
			capturedAt := operation.CapturedAt
			inputWriteAt = &capturedAt
			break
		}
	}
	if inputWriteAt == nil {
		t.Fatalf("delayed input output missing: operations=%#v", analysis.Operations)
	}
	if elapsed := *inputWriteAt - analysis.PhaseEvents[0].CapturedAt; elapsed < delay {
		t.Fatalf("phase-relative input elapsed = %s, want at least %s", elapsed, delay)
	}
}

func TestRunCommandDispatchesFrameInputSequenceAfterCompletedFrames(t *testing.T) {
	output := filepath.Join(t.TempDir(), "phase-input-writer")
	if err := driver.BuildPackage(context.Background(), "core/internal/testharness/pty/testdata/cmd/phase-input-writer", output); err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	capture, err := driver.RunCommand(context.Background(), driver.CommandSpec{
		Path:       output,
		Args:       []string{"frame-sequence"},
		Dimensions: pty.MustDimensions(2, 16),
		FrameInputSequences: []driver.FrameInputSequence{{
			Phase: pty.PhaseScenarioStart,
			Inputs: []driver.FrameInput{
				{Readiness: pty.ReadinessRendererFrame, Bytes: []byte("x\n")},
				{Readiness: pty.ReadinessRendererFrame, Bytes: []byte("y\n")},
				{Readiness: pty.ReadinessRendererFrame, Bytes: []byte("z\n")},
			},
		}},
		Timeout: commandTestTimeout,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if len(capture.FrameInputDispatches) != 3 {
		t.Fatalf("frame input dispatches = %#v, want three", capture.FrameInputDispatches)
	}
	for index, dispatch := range capture.FrameInputDispatches {
		if dispatch.Phase != pty.PhaseScenarioStart || dispatch.InputIndex != index {
			t.Fatalf("frame input dispatch %d = %#v", index, dispatch)
		}
		if dispatch.ReadyBoundary != pty.ReadinessRendererFrame {
			t.Fatalf("frame input dispatch %d boundary = %d, want renderer frame", index, dispatch.ReadyBoundary)
		}
		if dispatch.ReadyBoundaryEndByteOffset <= 0 {
			t.Fatalf("frame input dispatch %d ready boundary end = %d, want positive", index, dispatch.ReadyBoundaryEndByteOffset)
		}
		if index > 0 && dispatch.ReadyBoundaryEndByteOffset <= capture.FrameInputDispatches[index-1].ReadyBoundaryEndByteOffset {
			t.Fatalf("frame input ready offsets are not increasing: %#v", capture.FrameInputDispatches)
		}
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	var inputWrites []string
	for _, operation := range analysis.Operations {
		if operation.Write == nil {
			continue
		}
		switch operation.Write.Text {
		case "input:x", "input:y", "input:z":
			inputWrites = append(inputWrites, operation.Write.Text)
		}
	}
	if want := []string{"input:x", "input:y", "input:z"}; !slices.Equal(inputWrites, want) {
		t.Fatalf("input writes = %#v, want %#v", inputWrites, want)
	}
}

func TestRunCommandDispatchesFrameInputsAcrossTypedReadinessBoundaries(t *testing.T) {
	output := filepath.Join(t.TempDir(), "phase-input-writer")
	if err := driver.BuildPackage(context.Background(), "core/internal/testharness/pty/testdata/cmd/phase-input-writer", output); err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	capture, err := driver.RunCommand(context.Background(), driver.CommandSpec{
		Path:       output,
		Args:       []string{"typed-readiness-sequence"},
		Dimensions: pty.MustDimensions(2, 16),
		FrameInputSequences: []driver.FrameInputSequence{{
			Phase: pty.PhaseScenarioStart,
			Inputs: []driver.FrameInput{
				{Readiness: pty.ReadinessRendererFrame, Bytes: []byte("x\n")},
				{Readiness: pty.ReadinessInputApplied, Bytes: []byte("y\n")},
				{Readiness: pty.ReadinessRendererFrame, Bytes: []byte("z\n")},
				{Readiness: pty.ReadinessNormalBufferRestored, Bytes: []byte("q\n")},
			},
		}},
		Timeout: commandTestTimeout,
	})
	if err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	if len(capture.FrameInputDispatches) != 4 {
		t.Fatalf("frame input dispatches = %#v, want four", capture.FrameInputDispatches)
	}
	wantBoundaries := []pty.ReadinessBoundaryKind{
		pty.ReadinessRendererFrame,
		pty.ReadinessInputApplied,
		pty.ReadinessRendererFrame,
		pty.ReadinessNormalBufferRestored,
	}
	for index, want := range wantBoundaries {
		if got := capture.FrameInputDispatches[index].ReadyBoundary; got != want {
			t.Fatalf("frame input dispatch %d boundary = %d, want %d", index, got, want)
		}
	}
}

func TestRunCommandRejectsFirstInputAppliedReadiness(t *testing.T) {
	binary := buildHelper(t)
	_, err := driver.RunCommand(context.Background(), driver.CommandSpec{
		Path:       binary,
		Args:       []string{"write"},
		Dimensions: pty.MustDimensions(2, 16),
		FrameInputSequences: []driver.FrameInputSequence{{
			Phase: pty.PhaseScenarioStart,
			Inputs: []driver.FrameInput{{
				Readiness: pty.ReadinessInputApplied,
				Bytes:     []byte("x\n"),
			}},
		}},
		Timeout: commandTestTimeout,
	})
	if err == nil {
		t.Fatal("RunCommand accepted input-applied readiness for the first sequence input")
	}
}

func TestRunCommandSurfacesPhaseRelativeInputWriteFailure(t *testing.T) {
	output := filepath.Join(t.TempDir(), "phase-input-writer")
	if err := driver.BuildPackage(context.Background(), "core/internal/testharness/pty/testdata/cmd/phase-input-writer", output); err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	_, err := driver.RunCommand(context.Background(), driver.CommandSpec{
		Path:       output,
		Args:       []string{"close-stdio"},
		Dimensions: pty.MustDimensions(2, 16),
		PhaseInputs: []driver.PhaseInputEvent{{
			Phase: pty.PhaseScenarioStart,
			After: 100 * time.Millisecond,
			Bytes: []byte("x\n"),
		}},
		Timeout: commandTestTimeout,
	})
	if err == nil {
		t.Fatal("RunCommand returned nil after required phase-relative input could not be written")
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
		if operation.Kind == pty.OperationWrite && operation.Write != nil && operation.Write.Text == "after" {
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
