package app

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"core/cli/app/internal/runner"
	xansi "github.com/charmbracelet/x/ansi"
)

var errTerminalPhaseMarkerEncoderRequired = errors.New("terminal phase marker encoder is required")

type uiTerminalOutput struct {
	mu               sync.Mutex
	out              io.Writer
	markerEncoder    runner.TerminalPhaseMarkerEncoder
	started          bool
	readinessPending bool
	pendingMarkers   []runner.TerminalPhaseMarker
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
	if w.readinessPending {
		if _, err := w.out.Write([]byte(xansi.ShowCursor)); err != nil {
			return 0, fmt.Errorf("announce terminal input readiness: %w", err)
		}
		w.readinessPending = false
	}
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

// AnnounceInputReady queues the standard native-cursor readiness signal for
// the first renderer write, which occurs after Bubble Tea owns terminal mode.
// It must not itself drain fixture phase markers before the terminal is raw.
func (w *uiTerminalOutput) AnnounceInputReady() error {
	if w == nil || w.out == nil {
		return io.ErrClosedPipe
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return errors.New("terminal output already started before input readiness")
	}
	w.readinessPending = true
	return nil
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
