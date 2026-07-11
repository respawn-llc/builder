//go:build !windows

package driver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"core/internal/testharness/pty"
	"core/internal/testharness/pty/driver"

	"github.com/google/uuid"
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
		for _, record := range pty.OperationRecords(operation) {
			if record.Write != nil && record.Write.Text() == "input:x" {
				capturedAt := record.CapturedAt
				inputWriteAt = &capturedAt
				break
			}
		}
		if inputWriteAt != nil {
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
		for _, record := range pty.OperationRecords(operation) {
			if record.Write == nil {
				continue
			}
			switch record.Write.Text() {
			case "input:x", "input:y", "input:z":
				inputWrites = append(inputWrites, record.Write.Text())
			}
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
		if operation.Kind == pty.OperationWrite {
			for _, segment := range operation.WriteSegments {
				if segment.Write.Text() == "after" {
					index := i
					afterWriteIndex = &index
				}
			}
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

func TestSessionCommandRequiresUUIDv4AndClosedPayloadShape(t *testing.T) {
	t.Parallel()

	dimensions := pty.MustDimensions(2, 8)
	for _, command := range []driver.SessionCommand{
		{ID: uuid.Nil, Kind: driver.SessionCommandWrite, Bytes: []byte("x")},
		{ID: uuid.Must(uuid.NewV7()), Kind: driver.SessionCommandWrite, Bytes: []byte("x")},
		{ID: uuid.New(), Kind: driver.SessionCommandWrite},
		{ID: uuid.New(), Kind: driver.SessionCommandResize},
		{ID: uuid.New(), Kind: driver.SessionCommandRuntimeControlByte},
		{ID: uuid.New(), Kind: driver.SessionCommandKind(99)},
		{ID: uuid.New(), Kind: driver.SessionCommandWrite, Bytes: []byte("x"), Dimensions: &dimensions},
		{ID: uuid.New(), Kind: driver.SessionCommandRuntimeControlByte, Bytes: []byte("x"), Dimensions: &dimensions},
		{ID: uuid.New(), Kind: driver.SessionCommandResize, Bytes: []byte("x"), Dimensions: &dimensions},
		{ID: uuid.New(), Kind: driver.SessionCommandResize, Bytes: []byte{}, Dimensions: &dimensions},
		{ID: uuid.New(), Kind: driver.SessionCommandTerminateProcess, Bytes: []byte("x")},
		{ID: uuid.New(), Kind: driver.SessionCommandTerminateProcess, Bytes: []byte{}},
		{ID: uuid.New(), Kind: driver.SessionCommandTerminateProcess, Dimensions: &dimensions},
	} {
		if err := command.Validate(); err == nil {
			t.Fatalf("Validate succeeded for %#v", command)
		}
	}
	if err := (driver.SessionCommand{ID: uuid.New(), Kind: driver.SessionCommandResize, Dimensions: &dimensions}).Validate(); err != nil {
		t.Fatalf("Validate resize: %v", err)
	}
}

func TestSessionPublishesTerminalAndProcessEvents(t *testing.T) {
	t.Parallel()

	session, err := driver.StartSession(driver.SessionSpec{
		Path:       buildHelper(t),
		Args:       []string{"write"},
		Env:        []string{"TERM=xterm-256color", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"},
		Dimensions: pty.MustDimensions(2, 8),
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	var terminal bool
	var exited bool
	for event := range session.Events() {
		if event.Kind == driver.SessionEventTerminalAnalysis && event.Analysis != nil {
			terminal = true
		}
		if event.Kind == driver.SessionEventProcessExit {
			exited = true
		}
	}
	if !terminal || !exited {
		t.Fatalf("events terminal=%v exited=%v", terminal, exited)
	}
}

func TestSessionChildReceivesOnlyDeclaredEnvironment(t *testing.T) {
	t.Setenv("KENT_OPENAI_BASE_URL", "http://poisoned.invalid/v1")
	t.Setenv("OPENAI_API_KEY", "poisoned")
	t.Setenv("HTTP_PROXY", "http://poisoned.invalid")
	t.Setenv("COLORTERM", "truecolor")

	session, err := driver.StartSession(driver.SessionSpec{
		Path:       buildHelper(t),
		Args:       []string{"env-json"},
		Env:        []string{"TERM=xterm-256color", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"},
		Dimensions: pty.MustDimensions(2, 8),
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	for range session.Events() {
	}
	capture, err := session.Capture()
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	var entries []string
	if err := json.Unmarshal(capture.Raw, &entries); err != nil {
		t.Fatalf("decode child environment: %v; raw=%q", err, capture.Raw)
	}
	environment := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, found := strings.Cut(entry, "=")
		if !found || key == "" {
			t.Fatalf("invalid child environment entry %q", entry)
		}
		environment[key] = value
	}
	if len(environment) != 3 || environment["TERM"] != "xterm-256color" || environment["LANG"] != "C.UTF-8" || environment["LC_ALL"] != "C.UTF-8" {
		t.Fatalf("child environment = %#v", environment)
	}
	for _, poisoned := range []string{"KENT_OPENAI_BASE_URL", "OPENAI_API_KEY", "HTTP_PROXY", "COLORTERM"} {
		if _, exists := environment[poisoned]; exists {
			t.Fatalf("child inherited poisoned environment key %s", poisoned)
		}
	}
}

func TestSessionWriteCommandCompletesOnlyAfterLargePTYInputIsAccepted(t *testing.T) {
	t.Parallel()

	session, err := driver.StartSession(driver.SessionSpec{
		Path:       buildHelper(t),
		Args:       []string{"read-large"},
		Env:        []string{"TERM=xterm-256color", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"},
		Dimensions: pty.MustDimensions(2, 32),
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		_ = session.ForceKill()
		select {
		case <-session.Done():
		case <-time.After(time.Second):
			t.Error("session did not exit during cleanup")
		}
	})
	ready := false
	for !ready {
		event, ok := <-session.Events()
		if !ok {
			t.Fatal("session exited before terminal raw-mode readiness")
		}
		if event.Analysis == nil {
			continue
		}
		for _, mode := range event.Analysis.PrivateModeChanges {
			if mode.Mode == 25 && mode.Enabled {
				ready = true
				break
			}
		}
	}
	commandID := uuid.New()
	payload := bytes.Repeat([]byte("x"), 256*1024)
	if err := session.Enqueue(driver.SessionCommand{ID: commandID, Kind: driver.SessionCommandWrite, Bytes: payload}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	completed := false
	var capture pty.Capture
	for event := range session.Events() {
		if event.Kind == driver.SessionEventCommandCompleted && event.CommandID == commandID {
			completed = true
		}
		if event.Kind == driver.SessionEventCommandFailed && event.CommandID == commandID {
			t.Fatalf("large write command failed: %v", event.Err)
		}
	}
	capture, err = session.Capture()
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if !completed {
		t.Fatal("large write command never completed")
	}
	analysis, err := pty.Analyze(capture)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := analysis.Screen.TextInRegion(pty.Region{Top: 0, Bottom: 1, Left: 0, Right: 15}); got != "received:262144" {
		t.Fatalf("child receipt = %q, want complete payload acknowledgement", got)
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
