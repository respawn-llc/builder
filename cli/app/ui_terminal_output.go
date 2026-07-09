package app

import (
	"errors"
	"io"
	"sync"

	"core/cli/app/internal/runner"
)

var errTerminalPhaseMarkerEncoderRequired = errors.New("terminal phase marker encoder is required")

type uiTerminalOutput struct {
	mu             sync.Mutex
	out            io.Writer
	markerEncoder  runner.TerminalPhaseMarkerEncoder
	started        bool
	pendingMarkers []runner.TerminalPhaseMarker
}

func newUITerminalOutput(out io.Writer, markerEncoder runner.TerminalPhaseMarkerEncoder) *uiTerminalOutput {
	return &uiTerminalOutput{out: out, markerEncoder: markerEncoder}
}

func (w *uiTerminalOutput) Write(payload []byte) (int, error) {
	if len(payload) == 0 {
		return 0, nil
	}
	if w == nil || w.out == nil {
		return 0, io.ErrClosedPipe
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.out.Write(payload)
	if err != nil || n != len(payload) {
		return n, err
	}
	w.started = true
	if err := w.drainPendingMarkersLocked(); err != nil {
		return n, err
	}
	return n, nil
}

func (w *uiTerminalOutput) RequestTerminalPhaseMarker(marker runner.TerminalPhaseMarker) error {
	if w == nil || w.out == nil {
		return io.ErrClosedPipe
	}
	if w.markerEncoder == nil {
		return errTerminalPhaseMarkerEncoderRequired
	}
	if err := marker.Validate(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		w.pendingMarkers = append(w.pendingMarkers, marker)
		return nil
	}
	return w.writeMarkerLocked(marker)
}

func (w *uiTerminalOutput) drainPendingMarkersLocked() error {
	for _, marker := range w.pendingMarkers {
		if err := w.writeMarkerLocked(marker); err != nil {
			return err
		}
	}
	w.pendingMarkers = nil
	return nil
}

func (w *uiTerminalOutput) writeMarkerLocked(marker runner.TerminalPhaseMarker) error {
	payload, err := w.markerEncoder.EncodeTerminalPhaseMarker(marker)
	if err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err = w.out.Write(payload)
	return err
}

// uiTerminalOutputFile forwards the terminal-cursor-file interface (Read/Close/
// Fd) through to the underlying file so the cursor/gate writer wrappers and the
// Bubble Tea program can still detect the real terminal file descriptor. This
// keeps resize detection (term.IsTerminal on the output writer's Fd) working
// even though uiTerminalOutput intercepts Write for phase-marker injection.
// When the underlying writer is not a file, it degrades to the plain marker
// sink and satisfies io.Writer + TerminalPhaseMarkerSink only.
type uiTerminalOutputFile struct {
	*uiTerminalOutput
	file terminalCursorFile
}

func newUITerminalOutputFile(out io.Writer, markerEncoder runner.TerminalPhaseMarkerEncoder) uiTerminalOutputFile {
	base := newUITerminalOutput(out, markerEncoder)
	if file, ok := out.(terminalCursorFile); ok {
		return uiTerminalOutputFile{uiTerminalOutput: base, file: file}
	}
	return uiTerminalOutputFile{uiTerminalOutput: base}
}

func (w uiTerminalOutputFile) Read(p []byte) (int, error) {
	if w.file == nil {
		return 0, io.ErrClosedPipe
	}
	return w.file.Read(p)
}

func (w uiTerminalOutputFile) Close() error {
	if w.file == nil {
		return nil
	}
	return w.file.Close()
}

func (w uiTerminalOutputFile) Fd() uintptr {
	if w.file == nil {
		return 0
	}
	return w.file.Fd()
}
